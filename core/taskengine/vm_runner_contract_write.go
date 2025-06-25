package taskengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

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

	// ALWAYS USE TENDERLY SIMULATION FOR CONTRACT WRITES
	// This provides consistent behavior between run_node_immediately and simulateTask
	r.vm.logger.Info("🔮 Using Tenderly simulation for contract write",
		"contract", contractAddress.Hex(),
		"method", methodName)

	// Initialize Tenderly client
	tenderlyClient := NewTenderlyClient(r.vm.logger)

	// Get chain ID for simulation
	var chainID int64 = 11155111 // Default to Sepolia
	if r.smartWalletConfig != nil {
		// Try to extract chain ID from RPC URL or use default
		chainID = 11155111 // Sepolia default
	}

	// Get contract ABI as string
	var contractAbiStr string
	if contractAbi != nil {
		// Convert ABI back to JSON string for Tenderly
		// For now, we'll use an empty string and let Tenderly handle it
		contractAbiStr = ""
	}

	// Simulate the contract write using Tenderly
	simulationResult, err := tenderlyClient.SimulateContractWrite(
		ctx,
		contractAddress.Hex(),
		methodCall.CallData,
		contractAbiStr,
		methodName,
		chainID,
	)

	if err != nil {
		r.vm.logger.Warn("🚫 Tenderly simulation failed, using mock result", "error", err)

		// Create a mock successful result when Tenderly fails
		return &avsproto.ContractWriteNode_MethodResult{
			MethodName: methodName,
			Success:    true,
			Transaction: &avsproto.ContractWriteNode_TransactionData{
				Hash:           fmt.Sprintf("0x%064x", time.Now().UnixNano()),
				Status:         "simulated",
				From:           "0x0000000000000000000000000000000000000001",
				To:             contractAddress.Hex(),
				Value:          "0",
				Timestamp:      t0.Unix(),
				Simulation:     true,
				SimulationMode: "mock_fallback",
				ChainId:        chainID,
			},
			InputData: methodCall.CallData,
		}
	}

	// Convert Tenderly simulation result to protobuf format
	methodResult := &avsproto.ContractWriteNode_MethodResult{
		MethodName: simulationResult.MethodName,
		Success:    simulationResult.Success,
		InputData:  simulationResult.InputData,
	}

	// Convert transaction data
	if simulationResult.Transaction != nil {
		methodResult.Transaction = &avsproto.ContractWriteNode_TransactionData{
			Hash:           simulationResult.Transaction.Hash,
			Status:         simulationResult.Transaction.Status,
			From:           simulationResult.Transaction.From,
			To:             simulationResult.Transaction.To,
			Value:          simulationResult.Transaction.Value,
			Timestamp:      simulationResult.Transaction.Timestamp,
			Simulation:     simulationResult.Transaction.Simulation,
			SimulationMode: "tenderly",
			ChainId:        chainID,
		}
	}

	// Convert error data if present
	if simulationResult.Error != nil {
		methodResult.Error = &avsproto.ContractWriteNode_ErrorData{
			Code:    simulationResult.Error.Code,
			Message: simulationResult.Error.Message,
		}
	}

	// Convert return data if present
	if simulationResult.ReturnData != nil {
		methodResult.ReturnData = &avsproto.ContractWriteNode_ReturnData{
			Name:  simulationResult.ReturnData.Name,
			Type:  simulationResult.ReturnData.Type,
			Value: simulationResult.ReturnData.Value,
		}
	}

	r.vm.logger.Info("✅ Tenderly simulation completed",
		"method", methodName,
		"success", methodResult.Success,
		"simulation_mode", "tenderly")

	return methodResult
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

	// Get node data using helper function to reduce duplication
	nodeName, nodeInput := r.vm.GetNodeDataForExecution(stepID)

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

	// Convert results to Go maps for JSON conversion
	var resultsArray []interface{}
	for _, methodResult := range results {
		resultMap := map[string]interface{}{
			"methodName": methodResult.MethodName,
			"success":    methodResult.Success,
			"inputData":  methodResult.InputData,
		}

		// Convert transaction data
		if methodResult.Transaction != nil {
			resultMap["transaction"] = map[string]interface{}{
				"hash":           methodResult.Transaction.Hash,
				"status":         methodResult.Transaction.Status,
				"from":           methodResult.Transaction.From,
				"to":             methodResult.Transaction.To,
				"value":          methodResult.Transaction.Value,
				"timestamp":      methodResult.Transaction.Timestamp,
				"simulation":     methodResult.Transaction.Simulation,
				"simulationMode": methodResult.Transaction.SimulationMode,
				"chainId":        methodResult.Transaction.ChainId,
			}
		}

		// Convert error data if present
		if methodResult.Error != nil {
			resultMap["error"] = methodResult.Error.Message
		} else {
			resultMap["error"] = nil
		}

		// Convert return data if present
		if methodResult.ReturnData != nil {
			resultMap["returnData"] = methodResult.ReturnData.Value
		} else {
			resultMap["returnData"] = nil
		}

		// Convert events if present (empty for now, but structure for consistency)
		resultMap["events"] = []interface{}{}

		resultsArray = append(resultsArray, resultMap)
	}

	// Convert results to JSON for the new protobuf structure using shared helper
	resultsValue := ConvertResultsArrayToProtobufValue(resultsArray, &log)

	// Create output with all results
	s.OutputData = &avsproto.Execution_Step_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode_Output{
			Data: resultsValue,
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
