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
	"google.golang.org/protobuf/types/known/structpb"
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
				formattedValue := converter.FormatWithDecimals(bigIntValue, decimalsValue)

				// Store formatted value in field
				structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
					Name:  fieldName,
					Type:  fieldType,
					Value: formattedValue,
				})
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

		// Debug logging for each field with concrete examples
		if r.vm.logger != nil {
			r.vm.logger.Debug("Processing field in buildStructuredDataWithDecimalFormatting",
				"fieldName", fieldName, // e.g., "roundId", "answer", "startedAt"
				"fieldType", fieldType, // e.g., "uint80", "int256", "uint256"
				"rawValue", fmt.Sprintf("%v", item), // e.g., "18446744073709572839", "2189300000"
				"shouldFormat", converter.ShouldFormatField(fieldName), // e.g., true for "answer", false for "roundId"
				"itemType", fmt.Sprintf("%T", item)) // e.g., "*big.Int", "string"
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

	// Debug logging for raw fields metadata with concrete examples
	if r.vm.logger != nil {
		r.vm.logger.Debug("buildStructuredDataWithDecimalFormatting completed",
			"rawFieldsMetadata", rawFieldsMetadata, // e.g., {"answerRaw": "2189300000", "decimals": "8"}
			"fieldCount", len(structuredFields)) // e.g., 5 (for getRoundData: roundId, answer, startedAt, updatedAt, answeredInRound)
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
func (r *ContractReadProcessor) executeMethodCallWithoutFormatting(ctx context.Context, contractAbi *abi.ABI, contractAddress common.Address, methodCall *avsproto.ContractReadNode_MethodCall) *avsproto.ContractReadNode_MethodResult {
	// Use callData as-is without template substitution (callData should be literal hex string)
	var preprocessedCallData string
	if methodCall.CallData != nil {
		preprocessedCallData = *methodCall.CallData
	}
	methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())

	// Validate methodName is provided when callData is not specified
	if preprocessedCallData == "" && methodName == "" {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      NewMissingRequiredFieldError("methodCalls[].methodName").Error(),
			MethodName: methodName,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Generate callData from methodName and methodParams if callData is empty
	var finalCallData string
	if preprocessedCallData == "" && methodName != "" {
		// Find the method in the ABI
		if contractAbi != nil {
			if method, exists := contractAbi.Methods[methodName]; exists {
				if len(method.Inputs) == 0 {
					// Method has no inputs, generate calldata from method signature
					calldata, err := contractAbi.Pack(methodName)
					if err != nil {
						return &avsproto.ContractReadNode_MethodResult{
							Success:    false,
							Error:      fmt.Sprintf("failed to generate calldata for method %s: %v", methodName, err),
							MethodName: methodName,
							Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
						}
					}
					finalCallData = fmt.Sprintf("0x%x", calldata)
				} else {
					// Method has inputs, generate calldata from methodParams
					// Parse method parameters from the protobuf and preprocess template variables
					var methodParams []string
					for _, param := range methodCall.GetMethodParams() {
						// Preprocess template variables in each parameter
						preprocessedParam := r.vm.preprocessTextWithVariableMapping(param)
						methodParams = append(methodParams, preprocessedParam)
					}

					// Use the shared utility to generate calldata with proper type conversion
					callDataHex, err := GenerateCallData(methodName, methodParams, contractAbi)
					if err != nil {
						return &avsproto.ContractReadNode_MethodResult{
							Success:    false,
							Error:      fmt.Sprintf("failed to generate calldata: %v", err),
							MethodName: methodName,
							Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
						}
					}
					finalCallData = callDataHex
				}
			} else {
				return &avsproto.ContractReadNode_MethodResult{
					Success:    false,
					Error:      fmt.Sprintf("method %s not found in contract ABI", methodName),
					MethodName: methodName,
					Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
				}
			}
		} else {
			return &avsproto.ContractReadNode_MethodResult{
				Success:    false,
				Error:      "contract ABI is required to generate calldata",
				MethodName: methodName,
				Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
			}
		}
	} else {
		finalCallData = preprocessedCallData
	}

	calldata := common.FromHex(finalCallData)
	// Use runner smart wallet as the caller when available
	callMsg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: calldata,
	}
	if r.vm != nil {
		if aaSenderVar, ok := r.vm.vars["aa_sender"]; ok {
			if aaSenderStr, ok2 := aaSenderVar.(string); ok2 && common.IsHexAddress(aaSenderStr) {
				callMsg.From = common.HexToAddress(aaSenderStr)
			}
		}
	}

	// Execute the contract call
	output, err := r.client.CallContract(ctx, callMsg, nil)
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

	var log strings.Builder
	log.WriteString(formatNodeExecutionLogHeader(s))

	var err error
	defer func() {
		finalizeStep(s, err == nil, err, "", log.String())
	}()

	// Get configuration from node config
	if err = validateNodeConfig(node.Config, "ContractReadNode"); err != nil {
		log.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return s, err
	}

	config := node.Config
	// Note: ABI is handled directly from protobuf Values using optimized parsing
	if config.ContractAddress == "" {
		err = NewMissingRequiredFieldError("contractAddress")
		return s, err
	}
	if len(config.ContractAbi) == 0 {
		err = NewMissingRequiredFieldError("contractAbi")
		return s, err
	}

	// Resolve template variables first before validation
	contractAddress := r.vm.preprocessTextWithVariableMapping(config.ContractAddress)

	// Validate contract address after template resolution
	if !common.IsHexAddress(contractAddress) {
		err = NewInvalidAddressError(contractAddress)
		return s, err
	}
	// Note: ABI is never subject to template variable substitution

	if len(config.MethodCalls) == 0 {
		err = NewMissingRequiredFieldError("methodCalls")
		return s, err
	}

	// Parse the ABI - OPTIMIZED: Use protobuf Values directly
	var parsedABI *abi.ABI
	if len(config.ContractAbi) > 0 {
		if optimizedParsedABI, parseErr := ParseABIOptimized(config.ContractAbi); parseErr == nil {
			parsedABI = optimizedParsedABI
		} else {
			err = fmt.Errorf("failed to parse ABI: %v", parseErr)
			return s, err
		}
	} else {
		err = NewMissingRequiredFieldError("contractAbi")
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

	for _, methodCall := range config.MethodCalls {
		methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())
		log.WriteString(fmt.Sprintf("Calling method %s on %s\n", methodName, contractAddress))

		if r.vm.logger != nil {
			r.vm.logger.Debug("Processing method with applyToFields",
				"methodName", methodName, // e.g., "decimals", "getRoundData"
				"applyToFields", methodCall.GetApplyToFields(), // e.g., ["getRoundData.answer"] (decimals applies to answer field)
				"callData", methodCall.GetCallData()) // e.g., "0x313ce567" (decimals()), "0xfeaf968c" (latestRoundData())
		}

		// Execute the method call using existing callData (backend will handle methodParams separately)
		result := r.executeMethodCallWithoutFormatting(ctx, parsedABI, contractAddr, methodCall)
		methodResults = append(methodResults, result)

		// If this method has applyToFields, it provides decimal formatting for other methods
		if len(methodCall.GetApplyToFields()) > 0 {
			if result.Success && len(result.Data) > 0 {
				if decimalsInt, err := strconv.ParseInt(result.Data[0].Value, 10, 64); err == nil {
					decimalValue := big.NewInt(decimalsInt)
					decimalProviders[methodName] = decimalValue
					if r.vm.logger != nil {
						r.vm.logger.Debug("Method provides decimal value",
							"methodName", methodName, // e.g., "decimals"
							"decimalValue", decimalValue.String(), // e.g., "8" (USDC has 8 decimals), "18" (ETH has 18 decimals)
							"applyToFields", methodCall.GetApplyToFields(), // e.g., ["getRoundData.answer"] - will format answer field
							"resultValue", result.Data[0].Value) // e.g., "8" - the actual decimals() return value
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

	// Store original raw results for metadata (before any decimal formatting)
	// methodResults contains the raw, unformatted results from the first pass
	var rawResultsForMetadata []*avsproto.ContractReadNode_MethodResult
	rawResultsForMetadata = append(rawResultsForMetadata, methodResults...)

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

						// Parse the method.field format or just method name for single values
						parts := strings.Split(applyToField, ".")
						var targetMethodName, targetFieldName string

						if len(parts) == 1 {
							// Simple format: just method name (for single value methods)
							targetMethodName = parts[0]
							targetFieldName = parts[0] // Use method name as field name for single outputs
						} else if len(parts) == 2 {
							// Dot notation format: methodName.fieldName
							targetMethodName = parts[0]
							targetFieldName = parts[1]
						} else {
							if r.vm.logger != nil {
								r.vm.logger.Debug("Invalid applyToFields format", "applyToField", applyToField, "expected", "methodName or methodName.fieldName", "parts", parts)
							}
							continue
						}

						if r.vm.logger != nil {
							r.vm.logger.Debug("Parsed applyToField",
								"targetMethodName", targetMethodName, // e.g., "getRoundData"
								"targetFieldName", targetFieldName, // e.g., "answer"
								"currentMethodName", methodName, // e.g., "getRoundData"
								"applyToField", applyToField) // e.g., "getRoundData.answer" (original format)
						}

						// Check if this is the target method and has the target field
						if targetMethodName == methodName {
							if r.vm.logger != nil {
								r.vm.logger.Debug("Found target method match", "targetMethodName", targetMethodName, "currentMethodName", methodName)
							}

							for _, field := range result.Data {
								if r.vm.logger != nil {
									r.vm.logger.Debug("Checking field in result",
										"fieldName", field.Name, // e.g., "roundId", "answer", "startedAt"
										"fieldValue", field.Value, // e.g., "18446744073709572839", "2189300000", "1733878404"
										"targetFieldName", targetFieldName, // e.g., "answer" (the field we want to format)
										"isMatch", field.Name == targetFieldName) // e.g., true when field.Name = "answer" and targetFieldName = "answer"
								}

								if field.Name == targetFieldName {
									needsDecimalFormatting = true
									decimalsValue = decimalValue
									fieldsToFormat = append(fieldsToFormat, targetFieldName)
									if r.vm.logger != nil {
										r.vm.logger.Debug("Method field will be formatted with decimals",
											"methodName", methodName, // e.g., "getRoundData"
											"fieldName", targetFieldName, // e.g., "answer"
											"rawValue", field.Value, // e.g., "2189300000" (raw 8-decimal value)
											"decimalValue", decimalValue.String(), // e.g., "8" (USDC decimals)
											"fromMethod", otherMethodName, // e.g., "decimals" (method that provided the decimal count)
											"willFormat", fmt.Sprintf("%s.%s", methodName, targetFieldName)) // e.g., "getRoundData.answer"
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

		// Apply decimal formatting to existing result if needed (avoid re-execution)
		if needsDecimalFormatting && decimalsValue != nil && result.Success {
			if r.vm.logger != nil {
				r.vm.logger.Debug("Applying decimal formatting to existing result", "methodName", methodName, "fieldsToFormat", fieldsToFormat)
			}

			// Apply decimal formatting to the existing successful result
			formattedData, rawFieldsMetadata := r.applyDecimalFormattingToResult(result.Data, decimalsValue, fieldsToFormat)

			// Create new result with formatted data
			formattedResult := &avsproto.ContractReadNode_MethodResult{
				Success:    true,
				Error:      "",
				MethodName: result.MethodName,
				Data:       formattedData,
			}
			results = append(results, formattedResult)

			// Collect raw fields metadata
			for fieldName, fieldValue := range rawFieldsMetadata {
				allRawFieldsMetadata[fieldName] = fieldValue
			}
		} else {
			if r.vm.logger != nil {
				r.vm.logger.Debug("Using original result for method (no decimal formatting needed)", "methodName", methodName)
			}
			results = append(results, result)
		}
	}

	// Convert results to standardized format
	var resultsArray []interface{}                                              // For metadata (raw backend responses)
	var combinedCleanData map[string]interface{} = make(map[string]interface{}) // For data (single flattened object)

	if r.vm.logger != nil {
		r.vm.logger.Debug("Processing results for data/metadata separation", "resultCount", len(results))
	}

	// Use rawResultsForMetadata for metadata (raw, unformatted data)
	for i, methodResult := range results {
		// Ensure method name is populated to avoid empty keys in outputs
		effectiveMethodName := methodResult.MethodName
		if effectiveMethodName == "" {
			if i < len(config.MethodCalls) && config.MethodCalls[i] != nil && config.MethodCalls[i].MethodName != "" {
				effectiveMethodName = config.MethodCalls[i].MethodName
			} else {
				effectiveMethodName = fmt.Sprintf("method_%d", i+1)
			}
			methodResult.MethodName = effectiveMethodName
		}
		// Get the corresponding raw result for metadata
		rawResult := rawResultsForMetadata[i]

		// Create metadata entry (full backend response)
		metadataEntry := map[string]interface{}{
			"methodName": effectiveMethodName,
			"success":    methodResult.Success,
			"error":      methodResult.Error,
		}
		// Add methodABI to metadata if available
		if len(parsedABI.Methods) > 0 {
			if method, exists := parsedABI.Methods[effectiveMethodName]; exists {
				if methodABI := r.extractMethodABI(&method); methodABI != nil {
					metadataEntry["methodABI"] = methodABI
				} else {
					metadataEntry["methodABI"] = nil
				}
			} else {
				metadataEntry["methodABI"] = nil
			}
		} else {
			metadataEntry["methodABI"] = nil
		}
		// Extract the primary return value for the metadata's "value" field (raw format - no decimal formatting applied)
		var metadataValue interface{}
		if len(rawResult.Data) > 0 {
			if len(rawResult.Data) == 1 {
				metadataValue = rawResult.Data[0].Value
			} else {
				valueMap := make(map[string]interface{})
				for _, field := range rawResult.Data {
					// Only include the original raw fields, exclude any "Raw" suffixed fields
					if !strings.HasSuffix(field.Name, "Raw") {
						valueMap[field.Name] = field.Value
					}
				}
				metadataValue = valueMap
			}
		}
		metadataEntry["value"] = metadataValue
		resultsArray = append(resultsArray, metadataEntry)

		if r.vm.logger != nil {
			r.vm.logger.Debug("Added metadata entry", "methodName", effectiveMethodName, "metadataEntry", metadataEntry)
		}

		// Add to combined clean data object (user-friendly format - with decimal formatting applied)
		if len(methodResult.Data) > 0 {
			if len(methodResult.Data) == 1 {
				// Single output: add directly as {methodName: value}
				combinedCleanData[effectiveMethodName] = methodResult.Data[0].Value
			} else {
				// Multiple outputs: create a map of field names to values
				valueMap := make(map[string]interface{})
				for _, field := range methodResult.Data {
					if field.Name != "_rawContractOutput" {
						valueMap[field.Name] = field.Value
					}
				}

				// Add as {methodName: valueMap}
				combinedCleanData[effectiveMethodName] = valueMap
			}
		} else {
			// No data, set to nil
			combinedCleanData[effectiveMethodName] = nil
		}

		if r.vm.logger != nil {
			r.vm.logger.Debug("Added to combined clean data", "methodName", effectiveMethodName, "currentCombinedData", combinedCleanData)
		}
	}

	if r.vm.logger != nil {
		r.vm.logger.Debug("Final data structures constructed", "resultsArrayLength", len(resultsArray), "combinedCleanDataKeys", len(combinedCleanData))
	}

	// Convert both to protobuf values
	resultsValue := ConvertResultsArrayToProtobufValue(resultsArray, &log)

	// Convert the single combined object to protobuf
	var cleanDataValue *structpb.Value
	var structErr error
	cleanDataValue, structErr = structpb.NewValue(combinedCleanData)
	if structErr != nil {
		log.WriteString(fmt.Sprintf("Failed to convert combined clean data to protobuf Value: %v\n", structErr))
		cleanDataValue = structpb.NewNullValue()
	}

	// Create output with data only; attach detailed results to step-level metadata
	s.OutputData = &avsproto.Execution_Step_ContractRead{
		ContractRead: &avsproto.ContractReadNode_Output{
			Data: cleanDataValue,
		},
	}
	s.Metadata = resultsValue

	// Set output variables for backward compatibility
	// Use shared function to set output variable for this step with proper object structure
	setNodeOutputData(r.CommonProcessor, stepID, combinedCleanData)

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

	// Determine step success from method results: any method with Success == false marks step as failed
	stepSuccess, stepErrorMsg := computeReadStepSuccess(results)
	finalizeStep(s, stepSuccess, nil, stepErrorMsg, log.String())

	return s, nil
}

// applyDecimalFormattingToResult applies decimal formatting to existing result data using shared utility
func (r *ContractReadProcessor) applyDecimalFormattingToResult(originalData []*avsproto.ContractReadNode_MethodResult_StructuredField, decimalsValue *big.Int, fieldsToFormat []string) ([]*avsproto.ContractReadNode_MethodResult_StructuredField, map[string]string) {
	if len(originalData) == 0 || decimalsValue == nil || len(fieldsToFormat) == 0 {
		return originalData, make(map[string]string)
	}

	// Create decimal formatting context
	formattingContext := NewDecimalFormattingContext(decimalsValue, fieldsToFormat, "contract_read")

	// Apply formatting using shared utility
	formattedData := formattingContext.ApplyDecimalFormattingToStructuredFields(originalData)

	// Log the formatting operations
	if r.vm.logger != nil {
		for _, field := range originalData {
			if formattingContext.ShouldApplyDecimalFormatting(field.Name) {
				r.vm.logger.Debug("Applied decimal formatting via shared utility",
					"fieldName", field.Name,
					"rawValue", field.Value,
					"decimals", decimalsValue.String())
			}
		}
	}

	return formattedData, make(map[string]string)
}

// extractMethodABI extracts ABI information for a specific method
func (r *ContractReadProcessor) extractMethodABI(method *abi.Method) map[string]interface{} {
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

// executeMethodCallWithDecimalFormatting executes a single method call with decimal formatting support
func (r *ContractReadProcessor) executeMethodCallWithDecimalFormatting(ctx context.Context, contractAbi *abi.ABI, contractAddress common.Address, methodCall *avsproto.ContractReadNode_MethodCall, decimalsValue *big.Int, fieldsToFormat []string) *avsproto.ContractReadNode_MethodResult {
	// Use callData as-is without template substitution (callData should be literal hex string)
	var preprocessedCallData string
	if methodCall.CallData != nil {
		preprocessedCallData = *methodCall.CallData
	}
	methodName := r.vm.preprocessTextWithVariableMapping(methodCall.GetMethodName())

	// Validate methodName is provided when callData is not specified
	if preprocessedCallData == "" && methodName == "" {
		return &avsproto.ContractReadNode_MethodResult{
			Success:    false,
			Error:      NewMissingRequiredFieldError("methodCalls[].methodName").Error(),
			MethodName: methodName,
			Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
		}
	}

	// Generate callData from methodName and methodParams if callData is empty (same logic as executeMethodCallWithoutFormatting)
	var finalCallData string
	if preprocessedCallData == "" && methodName != "" {
		// Find the method in the ABI
		method, exists := contractAbi.Methods[methodName]
		if !exists {
			return &avsproto.ContractReadNode_MethodResult{
				Success:    false,
				Error:      fmt.Sprintf("method %s not found in ABI", methodName),
				MethodName: methodName,
				Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
			}
		}

		// Parse method parameters from the protobuf and preprocess template variables
		var methodParams []interface{}
		for _, param := range methodCall.GetMethodParams() {
			// Preprocess template variables in each parameter
			preprocessedParam := r.vm.preprocessTextWithVariableMapping(param)
			methodParams = append(methodParams, preprocessedParam)
		}

		// Pack the method call
		packedData, err := method.Inputs.Pack(methodParams...)
		if err != nil {
			return &avsproto.ContractReadNode_MethodResult{
				Success:    false,
				Error:      fmt.Sprintf("failed to pack method parameters: %v", err),
				MethodName: methodName,
				Data:       []*avsproto.ContractReadNode_MethodResult_StructuredField{},
			}
		}

		// Combine method selector with packed parameters
		finalCallData = fmt.Sprintf("0x%x%x", method.ID, packedData)
	} else {
		finalCallData = preprocessedCallData
	}

	calldata := common.FromHex(finalCallData)
	// Use runner smart wallet as the caller when available
	callMsg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: calldata,
	}
	if r.vm != nil {
		if aaSenderVar, ok := r.vm.vars["aa_sender"]; ok {
			if aaSenderStr, ok2 := aaSenderVar.(string); ok2 && common.IsHexAddress(aaSenderStr) {
				callMsg.From = common.HexToAddress(aaSenderStr)
			}
		}
	}

	// Execute the contract call
	output, err := r.client.CallContract(ctx, callMsg, nil)
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
		// Use decimal formatting (no raw fields creation)
		structuredDataFields, _ := r.buildStructuredDataWithDecimalFormatting(method, result, decimalsValue, fieldsToFormat)
		structuredData = structuredDataFields
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
