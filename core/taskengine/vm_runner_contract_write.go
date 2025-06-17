package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/byte4"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type SendUserOpFunc func(
	config *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *preset.VerifyingPaymasterRequest,
) (*userop.UserOperation, *types.Receipt, error)

type ContractWriteProcessor struct {
	*CommonProcessor
	client            *ethclient.Client
	smartWalletConfig *config.SmartWalletConfig
	owner             common.Address
	sendUserOpFunc    SendUserOpFunc
}

func NewContractWriteProcessor(vm *VM, client *ethclient.Client, smartWalletConfig *config.SmartWalletConfig, owner common.Address) *ContractWriteProcessor {
	r := &ContractWriteProcessor{
		client:            client,
		smartWalletConfig: smartWalletConfig,
		owner:             owner,
		sendUserOpFunc:    preset.SendUserOp, // Default to the real implementation
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}

	return r
}

func (r *ContractWriteProcessor) getInputData(node *avsproto.ContractWriteNode) (string, string, string, []*avsproto.ContractWriteNode_MethodCall, error) {
	var contractAddress, callData, contractAbi string
	var methodCalls []*avsproto.ContractWriteNode_MethodCall

	// Priority 1: Use node.Config if available (static configuration)
	if node.Config != nil {
		contractAddress = node.Config.ContractAddress
		callData = node.Config.CallData
		contractAbi = node.Config.ContractAbi
		methodCalls = node.Config.MethodCalls
	}

	// Priority 2: Override with VM variables if set (dynamic runtime values)
	r.vm.mu.Lock()
	if addr, exists := r.vm.vars["contract_address"]; exists {
		if addrStr, ok := addr.(string); ok {
			contractAddress = addrStr
		}
	}
	if data, exists := r.vm.vars["call_data"]; exists {
		if dataStr, ok := data.(string); ok {
			callData = dataStr
		}
	}
	if abi, exists := r.vm.vars["contract_abi"]; exists {
		if abiStr, ok := abi.(string); ok {
			contractAbi = abiStr
		}
	}
	r.vm.mu.Unlock()

	// Apply template variable preprocessing
	contractAddress = r.vm.preprocessTextWithVariableMapping(contractAddress)
	callData = r.vm.preprocessTextWithVariableMapping(callData)
	contractAbi = r.vm.preprocessTextWithVariableMapping(contractAbi)

	// If we have method_calls from config but also call_data from variables, prefer method_calls
	// If no method_calls but we have call_data, create a single method call
	if len(methodCalls) == 0 && callData != "" {
		methodCalls = []*avsproto.ContractWriteNode_MethodCall{
			{
				CallData:   callData,
				MethodName: "unknown", // Will be resolved from ABI if available
			},
		}
	}

	if contractAddress == "" {
		return "", "", "", nil, fmt.Errorf("missing required configuration: contract_address")
	}

	// Validate contract address format
	if !common.IsHexAddress(contractAddress) {
		return "", "", "", nil, fmt.Errorf("invalid contract address format: %s", contractAddress)
	}

	if len(methodCalls) == 0 {
		return "", "", "", nil, fmt.Errorf("missing required configuration: either method_calls or call_data must be provided")
	}

	return contractAddress, callData, contractAbi, methodCalls, nil
}

func (r *ContractWriteProcessor) executeMethodCall(
	ctx context.Context,
	contractAbi *abi.ABI,
	contractAddress common.Address,
	methodCall *avsproto.ContractWriteNode_MethodCall,
) *avsproto.ContractWriteNode_MethodResult {
	t0 := time.Now()

	calldata := common.FromHex(methodCall.CallData)

	// Resolve method name from ABI if not provided or if provided name is "unknown"
	methodName := methodCall.MethodName
	if contractAbi != nil && (methodName == "" || methodName == "unknown") {
		if method, err := byte4.GetMethodFromCalldata(*contractAbi, calldata); err == nil {
			methodName = method.Name
		}
	}

	userOpCalldata, err := aa.PackExecute(
		contractAddress,
		big.NewInt(0), // TODO: load correct salt from the task
		calldata,
	)
	if err != nil {
		return &avsproto.ContractWriteNode_MethodResult{
			MethodName: methodName,
			Success:    false,
			Error: &avsproto.ContractWriteNode_ErrorData{
				Code:    "CALLDATA_ENCODING_ERROR",
				Message: fmt.Sprintf("failed to encode calldata: %v", err),
			},
			InputData: methodCall.CallData,
		}
	}

	// Execute the transaction via smart wallet + bundler
	total, _ := r.vm.db.GetCounter(ContractWriteCounterKey(r.owner), 0)

	var paymasterRequest *preset.VerifyingPaymasterRequest
	// Paymaster request logic (preserving existing logic)
	if total >= 10 && !isWhitelistedAddress(r.owner, r.smartWalletConfig.WhitelistAddresses) {
		// No paymaster request for non-whitelisted addresses after 10 transactions
	} else {
		paymasterRequest = preset.GetVerifyingPaymasterRequestForDuration(r.smartWalletConfig.PaymasterAddress, 15*time.Minute)
	}

	userOp, txReceipt, err := r.sendUserOpFunc(
		r.smartWalletConfig,
		r.owner,
		userOpCalldata,
		paymasterRequest,
	)

	if err != nil {
		return &avsproto.ContractWriteNode_MethodResult{
			MethodName: methodName,
			Success:    false,
			Error: &avsproto.ContractWriteNode_ErrorData{
				Code:    "TRANSACTION_FAILED",
				Message: fmt.Sprintf("failed to send transaction: %v", err),
			},
			InputData: methodCall.CallData,
		}
	}

	// Increment counter after successful execution
	_, err = r.vm.db.IncCounter(ContractWriteCounterKey(r.owner), 0)
	if err != nil && r.vm.logger != nil {
		r.vm.logger.Error("failed to increment counter", "error", err)
	}

	// Build transaction data
	transactionData := &avsproto.ContractWriteNode_TransactionData{
		Hash:      "",
		Status:    "pending",
		From:      userOp.Sender.Hex(),
		To:        contractAddress.Hex(),
		Value:     "0",
		Nonce:     userOp.Nonce.String(),
		GasLimit:  userOp.CallGasLimit.String(),
		Timestamp: t0.Unix(),
	}

	var events []*avsproto.ContractWriteNode_EventData
	var returnData *avsproto.ContractWriteNode_ReturnData

	// If we have a receipt, the transaction is confirmed
	if txReceipt != nil {
		transactionData.Hash = txReceipt.TxHash.Hex()
		transactionData.Status = "confirmed"
		if txReceipt.Status == 0 {
			transactionData.Status = "failed"
		}
		transactionData.BlockNumber = fmt.Sprintf("%d", txReceipt.BlockNumber.Uint64())
		transactionData.BlockHash = txReceipt.BlockHash.Hex()
		transactionData.GasUsed = fmt.Sprintf("%d", txReceipt.GasUsed)
		transactionData.GasPrice = fmt.Sprintf("%d", txReceipt.EffectiveGasPrice.Uint64())
		transactionData.EffectiveGasPrice = fmt.Sprintf("%d", txReceipt.EffectiveGasPrice.Uint64())
		transactionData.TransactionIndex = fmt.Sprintf("%d", txReceipt.TransactionIndex)

		// Decode events if ABI is available
		if contractAbi != nil {
			events = r.decodeEvents(txReceipt.Logs, contractAbi)
		}

		// For successful transactions, try to decode return data
		if txReceipt.Status == 1 && contractAbi != nil {
			returnData = r.decodeReturnData(methodName, contractAbi)
		}
	}

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName:  methodName,
		Success:     txReceipt == nil || txReceipt.Status == 1,
		Transaction: transactionData,
		Events:      events,
		ReturnData:  returnData,
		InputData:   methodCall.CallData,
	}
}

func (r *ContractWriteProcessor) decodeEvents(logs []*types.Log, contractABI *abi.ABI) []*avsproto.ContractWriteNode_EventData {
	var events []*avsproto.ContractWriteNode_EventData

	for _, log := range logs {
		// Try to find matching event in ABI
		for _, abiEvent := range contractABI.Events {
			if len(log.Topics) > 0 && log.Topics[0] == abiEvent.ID {
				// Found matching event
				eventData := &avsproto.ContractWriteNode_EventData{
					EventName: abiEvent.Name,
					Address:   log.Address.Hex(),
					Topics:    make([]string, len(log.Topics)),
					Data:      common.Bytes2Hex(log.Data),
					Decoded:   make(map[string]string),
				}

				// Convert topics to hex strings
				for i, topic := range log.Topics {
					eventData.Topics[i] = topic.Hex()
				}

				// Try to decode the event data
				if decoded, err := contractABI.Unpack(abiEvent.Name, log.Data); err == nil {
					// Build decoded map
					for i, input := range abiEvent.Inputs {
						if i < len(decoded) {
							eventData.Decoded[input.Name] = fmt.Sprintf("%v", decoded[i])
						}
					}
				}

				events = append(events, eventData)
				break
			}
		}
	}

	return events
}

func (r *ContractWriteProcessor) decodeReturnData(methodName string, contractABI *abi.ABI) *avsproto.ContractWriteNode_ReturnData {
	// For write functions, return data is typically just success (bool)
	// This is a placeholder - in practice, most write functions don't return meaningful data
	if method, exists := contractABI.Methods[methodName]; exists && len(method.Outputs) > 0 {
		// Most write functions return bool success
		return &avsproto.ContractWriteNode_ReturnData{
			Name:  method.Outputs[0].Name,
			Type:  method.Outputs[0].Type.String(),
			Value: "true", // Default success value
		}
	}
	return nil
}

func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().UnixMilli()

	// Look up the task node to get the name
	var nodeName string = "unknown"
	r.vm.mu.Lock()
	if taskNode, exists := r.vm.TaskNodes[stepID]; exists {
		nodeName = taskNode.Name
	}
	r.vm.mu.Unlock()

	// Get the node's input data
	var nodeInput *structpb.Value
	r.vm.mu.Lock()
	if taskNode, exists := r.vm.TaskNodes[stepID]; exists {
		nodeInput = taskNode.Input
	}
	r.vm.mu.Unlock()

	s := &avsproto.Execution_Step{
		Id:         stepID,
		Log:        "",
		OutputData: nil,
		Success:    true,
		Error:      "",
		StartAt:    t0,
		Type:       avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
		Name:       nodeName,
		Input:      nodeInput, // Include node input data for debugging
	}

	var log strings.Builder
	var err error

	defer func() {
		s.Log = log.String()
		s.EndAt = time.Now().UnixMilli()
		s.Success = err == nil
	}()

	// Get input configuration
	contractAddress, _, contractAbi, methodCalls, inputErr := r.getInputData(node)
	if inputErr != nil {
		err = inputErr
		s.Error = err.Error()
		return s, err
	}

	log.WriteString(fmt.Sprintf("Contract Write Node: %s\n", contractAddress))
	log.WriteString(fmt.Sprintf("Number of method calls: %d\n", len(methodCalls)))

	// Parse ABI if provided
	var parsedABI *abi.ABI
	if contractAbi != "" {
		if parsed, parseErr := abi.JSON(strings.NewReader(contractAbi)); parseErr == nil {
			parsedABI = &parsed
			log.WriteString("ABI parsed successfully for event decoding\n")
		} else {
			log.WriteString(fmt.Sprintf("Warning: Failed to parse ABI: %v\n", parseErr))
		}
	}

	contractAddr := common.HexToAddress(contractAddress)
	var results []*avsproto.ContractWriteNode_MethodResult

	// Execute each method call
	ctx := context.Background()
	for i, methodCall := range methodCalls {
		log.WriteString(fmt.Sprintf("\nExecuting method %d: %s\n", i+1, methodCall.MethodName))

		result := r.executeMethodCall(ctx, parsedABI, contractAddr, methodCall)
		results = append(results, result)

		if result.Success {
			log.WriteString(fmt.Sprintf("✅ Success: %s (tx: %s)\n", result.MethodName, result.Transaction.Hash))
		} else {
			log.WriteString(fmt.Sprintf("❌ Failed: %s - %s\n", result.MethodName, result.Error.Message))
			// Don't fail the entire execution for individual method failures
		}
	}

	// Create output with all results
	s.OutputData = &avsproto.Execution_Step_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode_Output{
			Results: results,
		},
	}

	// Set output variables for backward compatibility and chaining
	outputVars := make(map[string]any)
	if len(results) > 0 {
		// For single method calls, set the result as the main output
		if len(results) == 1 && results[0].Success {
			outputVars["transaction"] = results[0].Transaction
			outputVars["hash"] = results[0].Transaction.Hash
			outputVars["success"] = results[0].Success
		}
		// Always provide results array for multi-method scenarios
		outputVars["results"] = results
	}
	r.SetOutputVarForStep(stepID, outputVars)

	return s, nil
}
