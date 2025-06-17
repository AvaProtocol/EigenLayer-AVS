package taskengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/byte4"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type ContractReadProcessor struct {
	*CommonProcessor
	client *ethclient.Client
}

func NewContractReadProcessor(vm *VM, client *ethclient.Client) *ContractReadProcessor {
	return &ContractReadProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		client: client,
	}
}

// buildStructuredData converts result interface{} array to StructuredField array with named fields
func (r *ContractReadProcessor) buildStructuredData(method *abi.Method, result []interface{}) ([]*avsproto.ContractReadNode_MethodResult_StructuredField, error) {
	var structuredFields []*avsproto.ContractReadNode_MethodResult_StructuredField

	// Handle the case where method has outputs but result is empty
	if len(result) == 0 && len(method.Outputs) > 0 {
		return structuredFields, nil
	}

	// If method has no defined outputs, create a generic field
	if len(method.Outputs) == 0 && len(result) > 0 {
		for i, item := range result {
			fieldName := fmt.Sprintf("output_%d", i)
			fieldType := "unknown"
			value := fmt.Sprintf("%v", item)

			structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
				Name:  fieldName,
				Type:  fieldType,
				Value: value,
			})
		}
		return structuredFields, nil
	}

	// Map results to named fields based on ABI
	for i, item := range result {
		var fieldName, fieldType string
		if i < len(method.Outputs) {
			fieldName = method.Outputs[i].Name
			fieldType = method.Outputs[i].Type.String()

			// Handle empty field names (common in Chainlink contracts)
			if fieldName == "" {
				if len(method.Outputs) == 1 {
					// Single unnamed output - use the method name as field name
					fieldName = method.Name
				} else {
					// Multiple outputs - use positional naming
					fieldName = fmt.Sprintf("output_%d", i)
				}
			}
		} else {
			fieldName = fmt.Sprintf("output_%d", i)
			fieldType = "unknown"
		}

		// Convert value to string representation
		value := fmt.Sprintf("%v", item)

		structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
			Name:  fieldName,
			Type:  fieldType,
			Value: value,
		})
	}

	return structuredFields, nil
}

// executeMethodCall executes a single method call and returns the result
func (r *ContractReadProcessor) executeMethodCall(ctx context.Context, contractAbi *abi.ABI, contractAddress common.Address, methodCall *avsproto.ContractReadNode_MethodCall) *avsproto.ContractReadNode_MethodResult {
	// Preprocess template variables in method call data
	preprocessedCallData := r.vm.preprocessTextWithVariableMapping(methodCall.CallData)
	methodName := r.vm.preprocessTextWithVariableMapping(methodCall.MethodName)

	calldata := common.FromHex(preprocessedCallData)
	msg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: calldata,
	}

	// Execute the contract call
	output, err := r.client.CallContract(ctx, msg, nil)
	if err != nil {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("contract call failed: %v", err),
			MethodName: methodName,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Debug: Log the contract call details and response
	if r.vm != nil && r.vm.logger != nil {
		// Get chain ID for debugging
		chainID, _ := r.client.ChainID(ctx)

		// Check if contract has code (exists)
		code, _ := r.client.CodeAt(ctx, contractAddress, nil)

		r.vm.logger.Debug("Contract call executed",
			"contract_address", contractAddress.Hex(),
			"chain_id", chainID,
			"contract_exists", len(code) > 0,
			"contract_code_length", len(code),
			"calldata", fmt.Sprintf("0x%x", calldata),
			"output_length", len(output),
			"output_hex", fmt.Sprintf("0x%x", output),
			"method_name", methodName,
		)
	}

	// Get the method from calldata to decode the response
	method, err := byte4.GetMethodFromCalldata(*contractAbi, calldata)
	if err != nil {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("failed to detect method from ABI: %v", err),
			MethodName: methodName,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Validate that the provided methodName matches the actual method detected from callData
	if method.Name != methodName {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("method name mismatch: callData corresponds to '%s' but methodName is '%s'. Please verify the function selector matches the intended method", method.Name, methodName),
			MethodName: methodName,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Handle empty contract response
	if len(output) == 0 {
		// Check if contract exists to provide better error message
		code, _ := r.client.CodeAt(ctx, contractAddress, nil)
		chainID, _ := r.client.ChainID(ctx)

		var errorMsg string
		if len(code) == 0 {
			errorMsg = fmt.Sprintf("contract does not exist at address %s on chain ID %v - verify the contract address and network", contractAddress.Hex(), chainID)
		} else {
			errorMsg = fmt.Sprintf("contract call returned empty data - function may not be implemented or is reverting silently at address %s on chain ID %v", contractAddress.Hex(), chainID)
		}

		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      errorMsg,
			MethodName: method.Name,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Decode the result using the ABI
	result, err := contractAbi.Unpack(method.Name, output)
	if err != nil {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("failed to decode result: %v", err),
			MethodName: method.Name,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Build structured data with named fields
	structuredData, err := r.buildStructuredData(method, result)
	if err != nil {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("failed to build structured data: %v", err),
			MethodName: method.Name,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	return &avsproto.ContractReadNode_MethodResult{
		Success:    true,
		Error:      "",
		MethodName: method.Name,
		Data:       structuredData,
	}
}

func (r *ContractReadProcessor) Execute(stepID string, node *avsproto.ContractReadNode) (*avsproto.Execution_Step, error) {
	ctx := context.Background()
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
		Type:       avsproto.NodeType_NODE_TYPE_CONTRACT_READ.String(),
		Name:       nodeName,
		Input:      nodeInput, // Include node input data for debugging
	}

	var err error
	defer func() {
		s.EndAt = time.Now().UnixMilli()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder

	// Get configuration from node config
	if node.Config == nil {
		err = fmt.Errorf("missing contract read configuration")
		return s, err
	}

	config := node.Config
	if config.ContractAddress == "" || config.ContractAbi == "" {
		err = fmt.Errorf("missing required configuration: contract_address and contract_abi are required")
		return s, err
	}

	if len(config.MethodCalls) == 0 {
		err = fmt.Errorf("no method calls specified")
		return s, err
	}

	// Preprocess template variables in configuration
	contractAddress := r.vm.preprocessTextWithVariableMapping(config.ContractAddress)
	contractAbi := r.vm.preprocessTextWithVariableMapping(config.ContractAbi)

	// Parse the ABI
	parsedABI, err := abi.JSON(strings.NewReader(contractAbi))
	if err != nil {
		err = fmt.Errorf("failed to parse ABI: %w", err)
		return s, err
	}

	contractAddr := common.HexToAddress(contractAddress)
	var results []*avsproto.ContractReadNode_MethodResult

	// Execute each method call serially
	for i, methodCall := range config.MethodCalls {
		log.WriteString(fmt.Sprintf("Call %d: %s on %s\n", i+1, methodCall.MethodName, config.ContractAddress))

		result := r.executeMethodCall(ctx, &parsedABI, contractAddr, methodCall)
		results = append(results, result)

		// Log the result
		if result.Success {
			log.WriteString(fmt.Sprintf("  ✅ Success: %s\n", result.MethodName))
		} else {
			log.WriteString(fmt.Sprintf("  ❌ Failed: %s - %s\n", result.MethodName, result.Error))
			// If any method call fails, mark the overall execution as failed
			if err == nil {
				err = fmt.Errorf("method call failed: %s", result.Error)
			}
		}
	}

	s.Log = log.String()

	// Create output with all results
	s.OutputData = &avsproto.Execution_Step_ContractRead{
		ContractRead: &avsproto.ContractReadNode_Output{
			Results: results,
		},
	}

	// Set output variables for backward compatibility
	// For single method calls, set the first result as the main output
	if len(results) > 0 && results[0].Success {
		// Convert structured fields to interface{} for VM variable setting
		var resultInterfaces []interface{}
		for _, field := range results[0].Data {
			resultInterfaces = append(resultInterfaces, field.Value)
		}
		r.SetOutputVarForStep(stepID, resultInterfaces)
	}

	return s, nil
}
