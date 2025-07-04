package taskengine

import (
	"context"
	"fmt"
	"math/big"
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
	structuredFields, _ := r.buildStructuredDataWithDecimalFormatting(method, result, nil, nil)
	return structuredFields, nil
}

// buildStructuredDataWithDecimalFormatting converts result interface{} array to StructuredField array with decimal formatting
func (r *ContractReadProcessor) buildStructuredDataWithDecimalFormatting(method *abi.Method, result []interface{}, decimalsValue *big.Int, fieldsToFormat []string) ([]*avsproto.ContractReadNode_MethodResult_StructuredField, map[string]interface{}) {
	var structuredFields []*avsproto.ContractReadNode_MethodResult_StructuredField
	rawFieldsMetadata := make(map[string]interface{})

	// Create ABI value converter
	converter := NewABIValueConverter(decimalsValue, fieldsToFormat)

	// If method has no defined outputs, create a generic field
	if len(method.Outputs) == 0 && len(result) > 0 {
		for i, item := range result {
			fieldName := fmt.Sprintf("output_%d", i)
			fieldType := "unknown"

			// Check if this field should be formatted with decimals
			if bigIntValue, ok := item.(*big.Int); ok && converter.ShouldFormatField(fieldName) {
				rawValue := bigIntValue.String()
				formattedValue := converter.FormatWithDecimals(bigIntValue, decimalsValue)

				// Store formatted value in field
				structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
					Name:  fieldName,
					Type:  fieldType,
					Value: formattedValue,
				})

				// Store raw value in metadata
				rawFieldsMetadata[fieldName+"Raw"] = rawValue
			} else {
				// Use ABI-aware conversion even for unknown types
				value := fmt.Sprintf("%v", item)
				structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
					Name:  fieldName,
					Type:  fieldType,
					Value: value,
				})
			}
		}
		return structuredFields, rawFieldsMetadata
	}

	// Map results to named fields based on ABI
	for i, item := range result {
		var fieldName, fieldType string
		var abiType abi.Type

		if i < len(method.Outputs) {
			fieldName = method.Outputs[i].Name
			fieldType = method.Outputs[i].Type.String()
			abiType = method.Outputs[i].Type

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
			// Use a default string type for unknown fields
			abiType = abi.Type{T: abi.StringTy}
		}

		// Use ABI-aware conversion
		value := converter.ConvertABIValueToString(item, abiType, fieldName)

		structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
			Name:  fieldName,
			Type:  fieldType,
			Value: value,
		})
	}

	// Merge raw fields metadata from converter
	for key, value := range converter.GetRawFieldsMetadata() {
		rawFieldsMetadata[key] = value
	}

	return structuredFields, rawFieldsMetadata
}

// callContractMethod makes a contract method call to retrieve additional data (like decimals)
func (r *ContractReadProcessor) callContractMethod(contractAddress common.Address, callData string) (interface{}, error) {
	// Remove 0x prefix if present
	callDataHex := strings.TrimPrefix(callData, "0x")

	// Convert hex string to bytes
	callDataBytes := common.FromHex("0x" + callDataHex)

	// Create the call message
	msg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: callDataBytes,
	}

	// Make the contract call
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := r.client.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("contract call failed: %w", err)
	}

	// For decimals() method, we expect a uint8 return value
	// The result is 32 bytes, but we only need the last byte for uint8
	if len(result) >= 32 {
		// Convert the last byte to big.Int (decimals is typically uint8)
		decimals := new(big.Int).SetBytes(result[31:32])
		return decimals, nil
	}

	return nil, fmt.Errorf("unexpected result length: %d", len(result))
}

// executeMethodCall executes a single method call and returns the result
func (r *ContractReadProcessor) executeMethodCall(ctx context.Context, contractAbi *abi.ABI, contractAddress common.Address, methodCall *avsproto.ContractReadNode_MethodCall) *avsproto.ContractReadNode_MethodResult {
	// Preprocess template variables in method call data
	preprocessedCallData := r.vm.preprocessTextWithVariableMapping(methodCall.GetCallData())
	methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())

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
	var allRawFieldsMetadata = make(map[string]interface{})

	// Check if any method call needs decimal formatting
	var decimalsValue *big.Int
	var fieldsToFormat []string

	// First pass: look for decimals() method calls
	for _, methodCall := range config.MethodCalls {
		if methodCall.GetMethodName() == "decimals" && len(methodCall.GetApplyToFields()) > 0 {
			if r.vm.logger != nil {
				r.vm.logger.Info("🔍 Processing decimals method call for formatting",
					"methodName", methodCall.GetMethodName(),
					"callData", methodCall.GetCallData(),
					"applyToFields", methodCall.GetApplyToFields())
			}

			// Make the decimals() call to the contract
			if decimals, err := r.callContractMethod(contractAddr, methodCall.GetCallData()); err == nil {
				if decimalsInt, ok := decimals.(*big.Int); ok {
					decimalsValue = decimalsInt
					fieldsToFormat = methodCall.GetApplyToFields()
					if r.vm.logger != nil {
						r.vm.logger.Info("📞 Retrieved decimals from contract",
							"contract", contractAddr.Hex(),
							"decimals", decimalsValue.String(),
							"applyToFields", fieldsToFormat)
					}
				}
			} else {
				if r.vm.logger != nil {
					r.vm.logger.Warn("Failed to call decimals() method", "error", err)
				}
			}
			break
		}
	}

	// Execute each method call serially
	for i, methodCall := range config.MethodCalls {
		log.WriteString(fmt.Sprintf("Call %d: %s on %s\n", i+1, methodCall.GetMethodName(), config.ContractAddress))

		// Execute all method calls, including decimals() calls used for formatting
		result := r.executeMethodCallWithDecimalFormatting(ctx, &parsedABI, contractAddr, methodCall, decimalsValue, fieldsToFormat)
		results = append(results, result)

		// Collect raw fields metadata from this method call
		if result.Success && len(result.Data) > 0 {
			// Extract raw fields metadata from the structured data fields
			for _, field := range result.Data {
				// Check if this is a raw field (ends with "Raw")
				if strings.HasSuffix(field.Name, "Raw") {
					allRawFieldsMetadata[field.Name] = field.Value
				}
			}
		}

		// Log the result
		if result.Success {
			if methodCall.GetMethodName() == "decimals" && len(methodCall.GetApplyToFields()) > 0 {
				log.WriteString(fmt.Sprintf("  ✅ Success: %s (used for formatting)\n", result.MethodName))
			} else {
				log.WriteString(fmt.Sprintf("  ✅ Success: %s\n", result.MethodName))
			}
		} else {
			log.WriteString(fmt.Sprintf("  ❌ Failed: %s - %s\n", result.MethodName, result.Error))
			// If any method call fails, mark the overall execution as failed
			if err == nil {
				err = fmt.Errorf("method call failed: %s", result.Error)
			}
		}
	}

	s.Log = log.String()

	// Convert results to Go maps for JSON conversion
	var resultsArray []interface{}
	for _, methodResult := range results {
		resultMap := map[string]interface{}{
			"methodName": methodResult.MethodName,
			"success":    methodResult.Success,
			"error":      methodResult.Error,
		}

		// Convert structured fields to a map and extract raw structured data
		dataMap := make(map[string]interface{})
		var rawStructuredFields []interface{}
		for _, field := range methodResult.Data {
			if field.Name == "_rawContractOutput" {
				// Skip the raw hex output, we don't need it anymore
				continue
			} else {
				// Regular data fields for the main response
				dataMap[field.Name] = field.Value

				// Also build the raw structured fields array for metadata
				rawStructuredFields = append(rawStructuredFields, map[string]interface{}{
					"name":  field.Name,
					"type":  field.Type,
					"value": field.Value,
				})
			}
		}

		// Add raw fields from decimal formatting to the main data
		if len(allRawFieldsMetadata) > 0 {
			for key, value := range allRawFieldsMetadata {
				if key != "decimals" { // Skip the decimals metadata field
					dataMap[key] = value
				}
			}
		}

		// Check execution context: if VM has a task, it's simulation (SimulateTask)
		isSimulation := r.vm.task != nil

		// Include raw structured fields in the result for metadata (only for direct execution)
		if !isSimulation {
			resultMap["rawStructuredFields"] = rawStructuredFields
			dataMap["rawStructuredFields"] = rawStructuredFields
		}

		resultMap["data"] = dataMap

		resultsArray = append(resultsArray, resultMap)
	}

	// Convert results to JSON for the new protobuf structure using shared helper
	resultsValue := ConvertResultsArrayToProtobufValue(resultsArray, &log)

	// Create output with all results
	s.OutputData = &avsproto.Execution_Step_ContractRead{
		ContractRead: &avsproto.ContractReadNode_Output{
			Data: resultsValue,
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

	// Add decimals info to metadata if we retrieved it
	if decimalsValue != nil {
		allRawFieldsMetadata["decimals"] = decimalsValue.String()
	}

	// TODO: Add raw fields metadata to response metadata when the runNodeWithInputs response supports it
	// For now, log the metadata for debugging
	if len(allRawFieldsMetadata) > 0 && r.vm.logger != nil {
		r.vm.logger.Debug("Contract read raw fields metadata", "metadata", allRawFieldsMetadata)
	}

	return s, nil
}

// executeMethodCallWithDecimalFormatting executes a single method call with decimal formatting support
func (r *ContractReadProcessor) executeMethodCallWithDecimalFormatting(ctx context.Context, contractAbi *abi.ABI, contractAddress common.Address, methodCall *avsproto.ContractReadNode_MethodCall, decimalsValue *big.Int, fieldsToFormat []string) *avsproto.ContractReadNode_MethodResult {
	// Preprocess template variables in method call data
	preprocessedCallData := r.vm.preprocessTextWithVariableMapping(methodCall.GetCallData())
	methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())

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

	// Build structured data with decimal formatting if needed
	var structuredData []*avsproto.ContractReadNode_MethodResult_StructuredField

	if decimalsValue != nil && len(fieldsToFormat) > 0 {
		// Use decimal formatting and capture raw fields metadata
		structuredDataFields, rawFieldsMetadata := r.buildStructuredDataWithDecimalFormatting(method, result, decimalsValue, fieldsToFormat)
		structuredData = structuredDataFields

		// Add raw fields (like answerRaw) to the structured data
		for rawFieldName, rawValue := range rawFieldsMetadata {
			structuredData = append(structuredData, &avsproto.ContractReadNode_MethodResult_StructuredField{
				Name:  rawFieldName,
				Value: fmt.Sprintf("%v", rawValue),
			})
		}
	} else {
		// Use regular formatting
		var err error
		structuredData, err = r.buildStructuredData(method, result)
		if err != nil {
			return &avsproto.ContractReadNode_MethodResult{
				Success:    false,
				Error:      fmt.Sprintf("failed to build structured data: %v", err),
				MethodName: method.Name,
				Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
			}
		}
	}

	// Add the raw contract output to the structured data for metadata purposes
	if len(output) > 0 {
		// Convert raw bytes to hex string for JSON serialization
		rawHex := fmt.Sprintf("0x%x", output)
		structuredData = append(structuredData, &avsproto.ContractReadNode_MethodResult_StructuredField{
			Name:  "_rawContractOutput",
			Value: rawHex,
		})
	}

	return &avsproto.ContractReadNode_MethodResult{
		Success:    true,
		Error:      "",
		MethodName: method.Name,
		Data:       structuredData,
	}
}
