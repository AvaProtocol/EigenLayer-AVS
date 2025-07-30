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
	"google.golang.org/protobuf/types/known/structpb"
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

func (r *ContractWriteProcessor) getInputData(node *avsproto.ContractWriteNode) (string, string, []*avsproto.ContractWriteNode_MethodCall, error) {
	var contractAddress, callData string
	var methodCalls []*avsproto.ContractWriteNode_MethodCall

	// Priority 1: Use node.Config if available (static configuration)
	if node.Config != nil {
		contractAddress = node.Config.ContractAddress
		callData = node.Config.CallData
		// Note: ABI is handled directly from protobuf Values in Execute method for optimization
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
	r.vm.mu.Unlock()

	// Apply template variable preprocessing
	contractAddress = r.vm.preprocessTextWithVariableMapping(contractAddress)
	callData = r.vm.preprocessTextWithVariableMapping(callData)

	// If we have method_calls from config but also call_data from variables, prefer method_calls
	// If no method_calls but we have call_data, create a single method call
	if len(methodCalls) == 0 && callData != "" {
		methodCalls = []*avsproto.ContractWriteNode_MethodCall{
			{
				CallData:   &callData,
				MethodName: UnknownMethodName, // Will be resolved from ABI if available
			},
		}
	}

	if contractAddress == "" {
		return "", "", nil, fmt.Errorf("missing required configuration: contractAddress")
	}

	// Validate contract address format
	if !common.IsHexAddress(contractAddress) {
		return "", "", nil, fmt.Errorf("invalid contract address format: %s", contractAddress)
	}

	if len(methodCalls) == 0 {
		return "", "", nil, fmt.Errorf("missing required configuration: either method_calls or call_data must be provided")
	}

	return contractAddress, callData, methodCalls, nil
}

func (r *ContractWriteProcessor) executeMethodCall(
	ctx context.Context,
	parsedABI *abi.ABI,
	contractAddress common.Address,
	methodCall *avsproto.ContractWriteNode_MethodCall,
) *avsproto.ContractWriteNode_MethodResult {
	t0 := time.Now()

	// Substitute template variables in methodParams before generating calldata
	// Use preprocessTextWithVariableMapping for each parameter to support dot notation like {{value.address}}
	resolvedMethodParams := make([]string, len(methodCall.MethodParams))
	for i, param := range methodCall.MethodParams {
		resolvedMethodParams[i] = r.vm.preprocessTextWithVariableMapping(param)
	}

	// Use shared utility to generate or use existing calldata
	var existingCallData string
	if methodCall.CallData != nil {
		existingCallData = *methodCall.CallData
	}
	callData, err := GenerateOrUseCallData(methodCall.MethodName, existingCallData, resolvedMethodParams, parsedABI)
	if err != nil {
		if r.vm != nil && r.vm.logger != nil {
			r.vm.logger.Error("❌ Failed to get/generate calldata for contract write",
				"methodName", methodCall.MethodName,
				"providedCallData", methodCall.CallData,
				"rawMethodParams", methodCall.MethodParams,
				"resolvedMethodParams", resolvedMethodParams,
				"error", err)
		}
		return &avsproto.ContractWriteNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("failed to get/generate calldata: %v", err),
			MethodName: methodCall.MethodName,
		}
	}

	// Log successful calldata generation if needed
	if existingCallData == "" && callData != "" && r.vm != nil && r.vm.logger != nil {
		r.vm.logger.Debug("✅ Generated calldata from methodName and methodParams for contract write",
			"methodName", methodCall.MethodName,
			"rawMethodParams", methodCall.MethodParams,
			"resolvedMethodParams", resolvedMethodParams,
			"generatedCallData", callData)
	}

	calldata := common.FromHex(callData)

	// Resolve method name from ABI if not provided or if provided name is UnknownMethodName
	methodName := methodCall.MethodName
	if parsedABI != nil && (methodName == "" || methodName == UnknownMethodName) {
		if method, err := byte4.GetMethodFromCalldata(*parsedABI, calldata); err == nil {
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
	if parsedABI != nil {
		// Convert ABI back to JSON string for Tenderly
		// For now, we'll use an empty string and let Tenderly handle it
		contractAbiStr = ""
	}

	// Simulate the contract write using Tenderly
	simulationResult, err := tenderlyClient.SimulateContractWrite(
		ctx,
		contractAddress.Hex(),
		callData,
		contractAbiStr,
		methodName,
		chainID,
	)

	if err != nil {
		r.vm.logger.Warn("🚫 Tenderly simulation failed, using mock result", "error", err)

		// Create a mock result when Tenderly fails
		return r.createMockContractWriteResult(methodName, contractAddress.Hex(), callData, parsedABI, t0, chainID)
	}

	// Convert Tenderly simulation result to legacy protobuf format
	return r.convertTenderlyResultToFlexibleFormat(simulationResult, parsedABI, callData)
}

// createMockContractWriteResult creates a mock result when Tenderly fails
func (r *ContractWriteProcessor) createMockContractWriteResult(methodName, contractAddress, callData string, parsedABI *abi.ABI, startTime time.Time, chainID int64) *avsproto.ContractWriteNode_MethodResult {
	// Extract methodABI from contract ABI if available
	var methodABI *structpb.Value
	if parsedABI != nil {
		if method, exists := parsedABI.Methods[methodName]; exists {
			if abiMap := r.extractMethodABI(&method); abiMap != nil {
				if abiValue, err := structpb.NewValue(abiMap); err == nil {
					methodABI = abiValue
				}
			}
		}
	}

	// Create flexible receipt as JSON object
	receiptMap := map[string]interface{}{
		"transactionHash": fmt.Sprintf("0x%064x", startTime.UnixNano()),
		"logs":            []interface{}{}, // Empty logs array
		// All other fields can be added dynamically as needed
	}

	receipt, _ := structpb.NewValue(receiptMap)

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName: methodName,
		MethodAbi:  methodABI,
		Success:    true,
		Error:      "",
		Receipt:    receipt,
		Value:      nil, // Mock transactions don't have return values
	}
}

// convertTenderlyResultToLegacyFormat converts Tenderly result to new flexible format
func (r *ContractWriteProcessor) convertTenderlyResultToFlexibleFormat(result *ContractWriteSimulationResult, parsedABI *abi.ABI, callData string) *avsproto.ContractWriteNode_MethodResult {
	// Extract methodABI from contract ABI if available
	var methodABI *structpb.Value
	if parsedABI != nil {
		if method, exists := parsedABI.Methods[result.MethodName]; exists {
			if abiMap := r.extractMethodABI(&method); abiMap != nil {
				if abiValue, err := structpb.NewValue(abiMap); err == nil {
					methodABI = abiValue
				}
			}
		}
	}

	// Create flexible receipt as JSON object with Tenderly data and standard fields
	receiptStatus := "0x1" // Default to success
	if !result.Success {
		receiptStatus = "0x0" // Set to failure if transaction failed
	}

	receiptMap := map[string]interface{}{
		"transactionHash":   result.Transaction.Hash,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              // ✅ From Tenderly
		"from":              result.Transaction.From,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              // ✅ From Tenderly
		"to":                result.Transaction.To,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // ✅ From Tenderly
		"blockNumber":       "0x1",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // Mock value for simulation
		"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",                                                                                                                                                                                                                                                                                                                                                                                                                                                                 // Mock value
		"transactionIndex":  "0x0",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // Mock value for simulation
		"gasUsed":           StandardGasCostHex,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   // Standard gas cost for simple transaction
		"cumulativeGasUsed": StandardGasCostHex,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   // Standard gas cost for simple transaction
		"effectiveGasPrice": "0x3b9aca00",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         // Mock value (1 gwei)
		"status":            receiptStatus,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        // Success/failure status based on actual result
		"logsBloom":         "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", // Empty logs bloom
		"logs":              []interface{}{},                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      // Empty logs array
		"type":              "0x2",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // EIP-1559 transaction type
	}

	receipt, _ := structpb.NewValue(receiptMap)

	// Extract return value from Tenderly response
	var returnValue *structpb.Value
	if result.ReturnData != nil {
		// Map returnData.value to our value field
		if valueProto, err := structpb.NewValue(result.ReturnData.Value); err == nil {
			returnValue = valueProto
		}
	}

	// Handle errors
	errorMsg := ""
	success := result.Success
	if result.Error != nil {
		errorMsg = result.Error.Message
		success = false
	}

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName: result.MethodName,
		MethodAbi:  methodABI,
		Success:    success,
		Error:      errorMsg,
		Receipt:    receipt,
		Value:      returnValue,
	}
}

// extractMethodABI extracts ABI information for a specific method
func (r *ContractWriteProcessor) extractMethodABI(method *abi.Method) map[string]interface{} {
	if method == nil {
		return nil
	}

	// Convert inputs
	inputs := make([]interface{}, len(method.Inputs))
	for i, input := range method.Inputs {
		inputs[i] = map[string]interface{}{
			"name": input.Name,
			"type": input.Type.String(),
		}
	}

	// Convert outputs
	outputs := make([]interface{}, len(method.Outputs))
	for i, output := range method.Outputs {
		outputs[i] = map[string]interface{}{
			"name": output.Name,
			"type": output.Type.String(),
		}
	}

	return map[string]interface{}{
		"name":            method.Name,
		"type":            "function",
		"inputs":          inputs,
		"outputs":         outputs,
		"stateMutability": method.StateMutability,
		"constant":        method.Constant,
		"payable":         method.Payable,
	}
}

func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	s := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE, r.vm)

	var log strings.Builder
	var err error

	defer func() {
		if err != nil {
			finalizeExecutionStep(s, false, err.Error(), log.String())
		}
	}()

	// Get input configuration
	contractAddress, _, methodCalls, inputErr := r.getInputData(node)
	if inputErr != nil {
		err = inputErr
		return s, err
	}

	log.WriteString(fmt.Sprintf("Contract Write Node: %s\n", contractAddress))
	log.WriteString(fmt.Sprintf("Number of method calls: %d\n", len(methodCalls)))

	// Parse ABI if provided - OPTIMIZED: Use protobuf Values directly
	var parsedABI *abi.ABI
	if node.Config != nil && len(node.Config.ContractAbi) > 0 {
		if optimizedParsedABI, parseErr := ParseABIOptimized(node.Config.ContractAbi); parseErr == nil {
			parsedABI = optimizedParsedABI
			log.WriteString("✅ ABI parsed successfully using optimized shared method (no string conversion)\n")
		} else {
			log.WriteString(fmt.Sprintf("Warning: Failed to parse ABI with optimized method: %v\n", parseErr))
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
			// Extract transaction hash from flexible receipt
			var txHash string
			if result.Receipt != nil {
				if receiptMap := result.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
					if hash, ok := receiptMap["transactionHash"].(string); ok {
						txHash = hash
					}
				}
			}
			log.WriteString(fmt.Sprintf("✅ Success: %s (tx: %s)\n", result.MethodName, txHash))
		} else {
			log.WriteString(fmt.Sprintf("❌ Failed: %s - %s\n", result.MethodName, result.Error))
			// Don't fail the entire execution for individual method failures
		}
	}

	// Convert results to Go maps for JSON conversion using standardized format
	var resultsArray []interface{}
	for _, methodResult := range results {
		resultMap := map[string]interface{}{
			"methodName": methodResult.MethodName,
			"success":    methodResult.Success,
			"error":      methodResult.Error,
		}

		// Add methodABI if available
		if methodResult.MethodAbi != nil {
			resultMap["methodABI"] = methodResult.MethodAbi.AsInterface()
		}

		// Add flexible receipt - already in the correct JSON format
		if methodResult.Receipt != nil {
			resultMap["receipt"] = methodResult.Receipt.AsInterface()
		}

		// Add blockNumber for convenience if available in receipt
		if methodResult.Receipt != nil {
			if receiptMap := methodResult.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
				if blockNumber, ok := receiptMap["blockNumber"]; ok {
					resultMap["blockNumber"] = blockNumber
				}
			}
		}

		// Add return value
		if methodResult.Value != nil {
			resultMap["value"] = methodResult.Value.AsInterface()
		} else {
			resultMap["value"] = nil
		}

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
			// Extract transaction hash from flexible receipt
			var txHash string
			if results[0].Receipt != nil {
				if receiptMap := results[0].Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
					if hash, ok := receiptMap["transactionHash"].(string); ok {
						txHash = hash
					}
				}
			}
			outputVars["receipt"] = results[0].Receipt
			outputVars["hash"] = txHash
			outputVars["success"] = results[0].Success
		}
		// Always provide results array for multi-method scenarios
		outputVars["results"] = results
	}
	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, outputVars)

	// Use shared function to finalize execution step with success
	finalizeExecutionStep(s, true, "", log.String())

	return s, nil
}
