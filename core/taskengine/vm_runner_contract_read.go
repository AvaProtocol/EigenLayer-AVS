package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
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

	// Debug logging
	if r.vm.logger != nil {
		r.vm.logger.Debug("buildStructuredDataWithDecimalFormatting called",
			"method", method.Name,
			"decimalsValue", decimalsValue.String(),
			"fieldsToFormat", fieldsToFormat,
			"resultCount", len(result))
	}

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

		// Debug logging for each field
		if r.vm.logger != nil {
			r.vm.logger.Debug("Processing field in buildStructuredDataWithDecimalFormatting",
				"fieldName", fieldName,
				"fieldType", fieldType,
				"shouldFormat", converter.ShouldFormatField(fieldName),
				"itemType", fmt.Sprintf("%T", item))
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

	// Debug logging for raw fields metadata
	if r.vm.logger != nil {
		r.vm.logger.Debug("buildStructuredDataWithDecimalFormatting completed",
			"rawFieldsMetadata", rawFieldsMetadata)
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

// executeMethodCallWithoutFormatting executes a single method call without decimal formatting
func (r *ContractReadProcessor) executeMethodCallWithoutFormatting(ctx context.Context, contractAbi *abi.ABI, contractAddress common.Address, methodName string, callData string) *avsproto.ContractReadNode_MethodResult {
	// Preprocess template variables in method call data
	preprocessedCallData := r.vm.preprocessTextWithVariableMapping(callData)
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

	if r.vm.logger != nil {
		r.vm.logger.Debug("ContractReadProcessor.Execute called", "stepID", stepID)
	}

	// Use shared function to create execution step
	s := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_CONTRACT_READ, r.vm)

	var err error
	defer func() {
		if err != nil {
			finalizeExecutionStep(s, false, err.Error(), "")
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
		err = fmt.Errorf("missing required configuration: contractAddress and contractAbi are required")
		return s, err
	}

	if len(config.MethodCalls) == 0 {
		err = fmt.Errorf("no method calls specified")
		return s, err
	}

	// Preprocess template variables in configuration
	contractAddress := r.vm.preprocessTextWithVariableMapping(config.ContractAddress)
	contractAbi := r.vm.preprocessTextWithVariableMapping(config.ContractAbi)

	// Validate contract address
	if !common.IsHexAddress(contractAddress) {
		err = fmt.Errorf("invalid contract address: %s", contractAddress)
		return s, err
	}

	// Parse the ABI
	abiObj, err := abi.JSON(strings.NewReader(contractAbi))
	if err != nil {
		err = fmt.Errorf("failed to parse ABI: %v", err)
		return s, err
	}

	// Get the contract address
	contractAddr := common.HexToAddress(contractAddress)

	// Process method calls in order and collect decimal formatting information
	var results []*avsproto.ContractReadNode_MethodResult
	allRawFieldsMetadata := make(map[string]interface{})

	// First pass: Execute all methods and collect their results
	var methodResults []*avsproto.ContractReadNode_MethodResult
	var decimalProviders = make(map[string]*big.Int) // methodName -> decimal value

	if r.vm.logger != nil {
		r.vm.logger.Debug("Starting method call processing")
	}

	for i, methodCall := range config.MethodCalls {
		methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())
		log.WriteString(fmt.Sprintf("Call %d: %s on %s\n", i+1, methodName, contractAddress))

		if r.vm.logger != nil {
			r.vm.logger.Debug("Processing method with applyToFields", "methodName", methodName, "applyToFields", methodCall.GetApplyToFields())
		}

		// Execute the method call
		result := r.executeMethodCallWithoutFormatting(ctx, &abiObj, contractAddr, methodCall.GetMethodName(), methodCall.GetCallData())
		methodResults = append(methodResults, result)

		// If this method has applyToFields, it provides decimal formatting for other methods
		if len(methodCall.GetApplyToFields()) > 0 {
			if result.Success && len(result.Data) > 0 {
				if decimalsInt, err := strconv.ParseInt(result.Data[0].Value, 10, 64); err == nil {
					decimalValue := big.NewInt(decimalsInt)
					decimalProviders[methodName] = decimalValue
					if r.vm.logger != nil {
						r.vm.logger.Debug("Method provides decimal value", "methodName", methodName, "decimalValue", decimalValue.String(), "applyToFields", methodCall.GetApplyToFields())
					}
				}
			}
		}

		// Log the result
		if result.Success {
			log.WriteString(fmt.Sprintf("Method %s executed successfully\n", result.MethodName))
		} else {
			log.WriteString(fmt.Sprintf("Method %s failed: %s\n", result.MethodName, result.Error))
			if result.Error != "" {
				err = fmt.Errorf("method call failed: %s", result.Error)
			}
		}
	}

	// Second pass: Process results and apply decimal formatting where needed
	if r.vm.logger != nil {
		r.vm.logger.Debug("Starting second pass to apply decimal formatting")
		r.vm.logger.Debug("About to start second pass loop", "methodCallsCount", len(config.MethodCalls))
	}

	for i, methodCall := range config.MethodCalls {
		methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())
		result := methodResults[i]

		if r.vm.logger != nil {
			r.vm.logger.Debug("Processing method in second pass", "methodName", methodName, "hasResult", result != nil)
		}

		// Check if this method needs decimal formatting applied to any of its fields
		var needsDecimalFormatting bool
		var decimalsValue *big.Int
		var fieldsToFormat []string

		// Look through all methods to see if any of them want to apply formatting to this method's fields
		for _, otherMethodCall := range config.MethodCalls {
			if len(otherMethodCall.GetApplyToFields()) > 0 {
				otherMethodName := r.vm.preprocessTextWithVariableMapping(otherMethodCall.GetMethodName())
				if r.vm.logger != nil {
					r.vm.logger.Debug("Checking other method for applyToFields", "otherMethodName", otherMethodName, "applyToFields", otherMethodCall.GetApplyToFields())
				}

				if decimalValue, exists := decimalProviders[otherMethodName]; exists {
					if r.vm.logger != nil {
						r.vm.logger.Debug("Found decimal provider", "otherMethodName", otherMethodName, "decimalValue", decimalValue.String())
					}

					// This other method provides decimal formatting
					for _, applyToField := range otherMethodCall.GetApplyToFields() {
						if r.vm.logger != nil {
							r.vm.logger.Debug("Processing applyToField", "applyToField", applyToField)
						}

						// Parse the method.field format
						parts := strings.Split(applyToField, ".")
						if len(parts) != 2 {
							if r.vm.logger != nil {
								r.vm.logger.Debug("Invalid applyToFields format", "applyToField", applyToField, "expected", "methodName.fieldName", "parts", parts)
							}
							continue
						}

						targetMethodName := parts[0]
						targetFieldName := parts[1]

						if r.vm.logger != nil {
							r.vm.logger.Debug("Parsed applyToField", "targetMethodName", targetMethodName, "targetFieldName", targetFieldName, "currentMethodName", methodName)
						}

						// Check if this is the target method and has the target field
						if targetMethodName == methodName {
							if r.vm.logger != nil {
								r.vm.logger.Debug("Found target method match", "targetMethodName", targetMethodName, "currentMethodName", methodName)
							}

							for _, field := range result.Data {
								if r.vm.logger != nil {
									r.vm.logger.Debug("Checking field in result", "fieldName", field.Name, "targetFieldName", targetFieldName)
								}

								if field.Name == targetFieldName {
									needsDecimalFormatting = true
									decimalsValue = decimalValue
									fieldsToFormat = append(fieldsToFormat, targetFieldName)
									if r.vm.logger != nil {
										r.vm.logger.Debug("Method field will be formatted with decimals", "methodName", methodName, "fieldName", targetFieldName, "decimalValue", decimalValue.String(), "fromMethod", otherMethodName)
									}
									break
								}
							}
						}
					}
				} else {
					if r.vm.logger != nil {
						r.vm.logger.Debug("No decimal provider found for method", "methodName", otherMethodName)
					}
				}
			}
		}

		// Re-execute the method with decimal formatting if needed
		if needsDecimalFormatting && decimalsValue != nil {
			if r.vm.logger != nil {
				r.vm.logger.Debug("Re-executing method with decimal formatting", "methodName", methodName, "fieldsToFormat", fieldsToFormat)
			}
			formattedResult := r.executeMethodCallWithDecimalFormatting(ctx, &abiObj, contractAddr, methodCall, decimalsValue, fieldsToFormat)
			results = append(results, formattedResult)

			// Collect raw fields metadata
			for _, field := range formattedResult.Data {
				if strings.HasSuffix(field.Name, "Raw") {
					allRawFieldsMetadata[field.Name] = field.Value
				}
			}
		} else {
			if r.vm.logger != nil {
				r.vm.logger.Debug("Using original result for method (no decimal formatting needed)", "methodName", methodName)
			}
			results = append(results, result)
		}
	}

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
			} else if strings.HasSuffix(field.Name, "Raw") {
				// Raw fields (like answerRaw) should be added to the main data
				dataMap[field.Name] = field.Value

				// Also build the raw structured fields array for metadata
				rawStructuredFields = append(rawStructuredFields, map[string]interface{}{
					"name":  field.Name,
					"type":  field.Type,
					"value": field.Value,
				})
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
		// Use shared function to set output variable for this step
		setNodeOutputData(r.CommonProcessor, stepID, resultInterfaces)
	}

	// Add decimals info to metadata if we retrieved it
	if len(decimalProviders) > 0 {
		for methodName, decimals := range decimalProviders {
			allRawFieldsMetadata[methodName] = decimals.String()
		}
	}

	// TODO: Add raw fields metadata to response metadata when the runNodeWithInputs response supports it
	// For now, log the metadata for debugging
	if len(allRawFieldsMetadata) > 0 && r.vm.logger != nil {
		r.vm.logger.Debug("Contract read raw fields metadata", "metadata", allRawFieldsMetadata)
	}

	// Use shared function to finalize execution step with success
	finalizeExecutionStep(s, true, "", log.String())

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
