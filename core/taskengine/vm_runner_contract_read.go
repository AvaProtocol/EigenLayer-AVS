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

// buildStructuredDataWithDecimalFormatting converts result interface{} array to StructuredField array with decimal formatting
func (r *ContractReadProcessor) buildStructuredDataWithDecimalFormatting(method *abi.Method, result []interface{}, decimalsValue *big.Int, fieldsToFormat []string) ([]*avsproto.ContractReadNode_MethodResult_StructuredField, map[string]interface{}) {
	var structuredFields []*avsproto.ContractReadNode_MethodResult_StructuredField
	rawFieldsMetadata := make(map[string]interface{})

	// Helper function to check if a field should be formatted
	shouldFormatField := func(fieldName string) bool {
		if decimalsValue == nil || len(fieldsToFormat) == 0 {
			return false
		}
		for _, field := range fieldsToFormat {
			if field == fieldName {
				return true
			}
		}
		return false
	}

	// Helper function to format a big.Int value with decimals
	formatWithDecimals := func(value *big.Int, decimals *big.Int) string {
		if decimals == nil || decimals.Cmp(big.NewInt(0)) == 0 {
			return value.String()
		}

		// Create divisor: 10^decimals
		divisor := new(big.Int).Exp(big.NewInt(10), decimals, nil)

		// Calculate quotient and remainder
		quotient := new(big.Int).Div(value, divisor)
		remainder := new(big.Int).Mod(value, divisor)

		// Format remainder with leading zeros
		remainderStr := remainder.String()
		decimalsInt := int(decimals.Int64())

		// Pad with leading zeros if necessary
		for len(remainderStr) < decimalsInt {
			remainderStr = "0" + remainderStr
		}

		// Remove trailing zeros from remainder
		remainderStr = strings.TrimRight(remainderStr, "0")
		if remainderStr == "" {
			remainderStr = "0"
		}

		return fmt.Sprintf("%s.%s", quotient.String(), remainderStr)
	}

	// Handle the case where method has outputs but result is empty
	if len(result) == 0 && len(method.Outputs) > 0 {
		return structuredFields, rawFieldsMetadata
	}

	// If method has no defined outputs, create a generic field
	if len(method.Outputs) == 0 && len(result) > 0 {
		for i, item := range result {
			fieldName := fmt.Sprintf("output_%d", i)
			fieldType := "unknown"
			value := fmt.Sprintf("%v", item)

			// Check if this field should be formatted with decimals
			if bigIntValue, ok := item.(*big.Int); ok && shouldFormatField(fieldName) {
				rawValue := bigIntValue.String()
				formattedValue := formatWithDecimals(bigIntValue, decimalsValue)

				// Store formatted value in field
				structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
					Name:  fieldName,
					Type:  fieldType,
					Value: formattedValue,
				})

				// Store raw value in metadata
				rawFieldsMetadata[fieldName+"Raw"] = rawValue
			} else {
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

		// Check if this field should be formatted with decimals
		if bigIntValue, ok := item.(*big.Int); ok && shouldFormatField(fieldName) {
			rawValue := bigIntValue.String()
			formattedValue := formatWithDecimals(bigIntValue, decimalsValue)

			// Store formatted value in field
			structuredFields = append(structuredFields, &avsproto.ContractReadNode_MethodResult_StructuredField{
				Name:  fieldName,
				Type:  fieldType,
				Value: formattedValue,
			})

			// Store raw value in metadata
			rawFieldsMetadata[fieldName+"Raw"] = rawValue

			// Also rename "answer" to match expected variable names
			if fieldName == "answer" {
				// The formatted "answer" becomes the main "answer" field
				// The raw "answer" becomes "answerRaw" in metadata
				// This maintains backward compatibility while using the new naming convention
			}
		} else {
			// Convert value to string representation
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

		// Skip decimals() calls that are only used for formatting
		if methodCall.GetMethodName() == "decimals" && len(methodCall.GetApplyToFields()) > 0 {
			log.WriteString(fmt.Sprintf("  ⏭️  Skipping decimals() call (used for formatting only)\n"))
			continue
		}

		result := r.executeMethodCallWithDecimalFormatting(ctx, &parsedABI, contractAddr, methodCall, decimalsValue, fieldsToFormat)
		results = append(results, result)

		// Collect raw fields metadata from this method call
		if result.Success && len(result.Data) > 0 {
			// Extract raw fields metadata (this would be set by executeMethodCallWithDecimalFormatting)
			for _, field := range result.Data {
				// Check if there's a corresponding raw field
				rawFieldName := field.Name + "Raw"
				if rawValue, exists := allRawFieldsMetadata[rawFieldName]; exists {
					// Store in metadata for later use
					allRawFieldsMetadata[rawFieldName] = rawValue
				}
			}
		}

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

	// Convert results to Go maps for JSON conversion
	var resultsArray []interface{}
	for _, methodResult := range results {
		resultMap := map[string]interface{}{
			"methodName": methodResult.MethodName,
			"success":    methodResult.Success,
			"error":      methodResult.Error,
		}

		// Convert structured fields to a map
		dataMap := make(map[string]interface{})
		for _, field := range methodResult.Data {
			dataMap[field.Name] = field.Value
		}
		resultMap["data"] = dataMap

		resultsArray = append(resultsArray, resultMap)
	}

	// Convert results to JSON for the new protobuf structure
	resultsValue, err := structpb.NewValue(resultsArray)
	if err != nil {
		log.WriteString(fmt.Sprintf("Failed to convert results to protobuf Value: %v\n", err))
		resultsValue = structpb.NewNullValue()
	}

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
	var rawFieldsMetadata map[string]interface{}

	if decimalsValue != nil && len(fieldsToFormat) > 0 {
		// Use decimal formatting
		structuredData, rawFieldsMetadata = r.buildStructuredDataWithDecimalFormatting(method, result, decimalsValue, fieldsToFormat)

		// Store raw fields metadata for later use (TODO: add to response metadata)
		for key, value := range rawFieldsMetadata {
			// This would be added to the response metadata when supported
			_ = key
			_ = value
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

	return &avsproto.ContractReadNode_MethodResult{
		Success:    true,
		Error:      "",
		MethodName: method.Name,
		Data:       structuredData,
	}
}
