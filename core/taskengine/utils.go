package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/modules"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/dop251/goja"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// Metadata holds token and block metadata
type Metadata struct {
	Name           string
	Symbol         string
	Decimals       uint8
	FormattedValue string
	BlockNumber    uint64
}

// GetMetadataForTransfer retrieves and caches token metadata and block data
func GetMetadataForTransfer(log *types.Log) (*Metadata, error) {
	tokenAddress := log.Address.Hex()
	cacheKey := fmt.Sprintf("erc20:%s", tokenAddress)

	m := Metadata{}

	// Check if metadata is cached
	if data, err := cache.Get(cacheKey); err == nil {
		var metadata Metadata
		if err := json.Unmarshal(data, &metadata); err == nil {
			m = metadata
		}
	}

	if m.Name == "" {
		// Retrieve token data
		token, err := erc20.NewErc20(log.Address, rpcConn)
		if err != nil {
			return nil, fmt.Errorf("error creating ERC20 instance: %w", err)
		}

		m.Symbol, err = token.Symbol(nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching token symbol: %w", err)
		}

		m.Name, err = token.Name(nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching token name: %w", err)
		}

		m.Decimals, err = token.Decimals(nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching token decimals: %w", err)
		}
		data, err := json.Marshal(m)
		if err == nil {
			// Ignore cache errors - caching is not critical
			cache.Set(cacheKey, data)
		}
	}

	return &m, nil
}

func GetBlock(blockNumber uint64) (*types.Header, error) {
	cacheKey := fmt.Sprintf("blkt:%d", blockNumber)

	var blockHeader *types.Header
	// Check if metadata is cached
	if data, err := cache.Get(cacheKey); err == nil {
		if err = json.Unmarshal(data, blockHeader); err == nil {
			return blockHeader, nil
		}
	}

	blockHeader, err := rpcConn.HeaderByNumber(context.Background(), big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, fmt.Errorf("error fetching block header: %w", err)
	}

	data, err := json.Marshal(blockHeader)
	if err == nil {
		// Ignore cache errors - caching is not critical
		cache.Set(cacheKey, data)
	}

	return blockHeader, nil
}

// ToDecimal converts a big.Int value to a human-readable format
func FormatUnit(value *big.Int, decimals int) *big.Float {
	f := new(big.Float).SetInt(value)
	decimalFactor := new(big.Float).SetFloat64(1)
	ten := big.NewFloat(10)
	for i := 0; i < decimals; i++ {
		decimalFactor.Mul(decimalFactor, ten)
	}
	return new(big.Float).Quo(f, decimalFactor)
}

func ToDecimal(ivalue interface{}, decimals int) decimal.Decimal {
	value := new(big.Int)
	switch v := ivalue.(type) {
	case string:
		value.SetString(v, 10)
	case *big.Int:
		value = v
	}

	mul := decimal.NewFromFloat(float64(10)).Pow(decimal.NewFromFloat(float64(decimals)))
	num, _ := decimal.NewFromString(value.String())
	result := num.Div(mul)

	return result
}

// NewGojaVM creates a new Goja runtime and applies standard configurations.
func NewGojaVM() *goja.Runtime {
	vm := goja.New()
	macros.ConfigureGojaRuntime(vm)

	// Add console.log support for debugging
	console := vm.NewObject()
	console.Set("log", func(args ...interface{}) {
		fmt.Printf("🔍 JS CONSOLE: %v\n", args)
	})
	vm.Set("console", console)

	return vm
}

// NewGojaVMWithModules creates a new Goja runtime with module support.
func NewGojaVMWithModules() (*goja.Runtime, *modules.Registry, error) {
	vm := goja.New()
	macros.ConfigureGojaRuntime(vm)

	// Add console.log support for debugging
	console := vm.NewObject()
	console.Set("log", func(args ...interface{}) {
		fmt.Printf("🔍 JS CONSOLE: %v\n", args)
	})
	vm.Set("console", console)

	registry := modules.NewRegistry()

	builtinLoader := modules.NewBuiltinLoader()
	if err := builtinLoader.RegisterBuiltinLibraries(); err != nil {
		return nil, nil, err
	}

	registry.RegisterLoader("lodash", builtinLoader)
	registry.RegisterLoader("dayjs", builtinLoader)
	registry.RegisterLoader("uuid", builtinLoader)

	return vm, registry, nil
}

// ConvertResultsArrayToProtobufValue converts a results array to protobuf Value
// with consistent handling for single vs multiple method calls.
// This consolidates the repeated conversion logic used across contract processors.
func ConvertResultsArrayToProtobufValue(resultsArray []interface{}, log *strings.Builder) *structpb.Value {
	// Always return as array for consistency, regardless of single or multiple method calls
	var resultsValue *structpb.Value
	var structErr error

	// Always return as array to maintain consistent data structure
	resultsValue, structErr = structpb.NewValue(resultsArray)

	if structErr != nil {
		if log != nil {
			log.WriteString(fmt.Sprintf("Failed to convert results to protobuf Value: %v\n", structErr))
		}
		resultsValue = structpb.NewNullValue()
	}

	return resultsValue
}

// ExtractResultsFromProtobufValue extracts results from protobuf Value, handling both single objects and arrays
func ExtractResultsFromProtobufValue(data *structpb.Value) []interface{} {
	var results []interface{}

	if data.GetListValue() != nil {
		// Data is an array
		for _, item := range data.GetListValue().GetValues() {
			results = append(results, item.AsInterface())
		}
	} else {
		// Data might be a single object, wrap it in an array for consistency
		results = append(results, data.AsInterface())
	}

	return results
}

// ProcessContractReadResults handles the complex logic for contract read result processing
// including single vs multiple method handling and raw metadata extraction
func ProcessContractReadResults(results []interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// For single method calls, return the data directly (flattened)
	if len(results) == 1 {
		return processSingleContractReadResult(results[0], result)
	} else {
		// Multiple method results - return as array (keep existing behavior)
		result["results"] = results

		// Create metadata._raw array with all method results
		var rawMethodResults []interface{}
		for _, methodResult := range results {
			if methodMap, ok := methodResult.(map[string]interface{}); ok {
				rawResult := map[string]interface{}{
					"methodName": methodMap["methodName"],
					"success":    methodMap["success"],
					"error":      methodMap["error"],
				}

				// Use the data field from the method result
				if data, ok := methodMap["data"]; ok {
					rawResult["data"] = data
				}

				rawMethodResults = append(rawMethodResults, rawResult)
			}
		}

		// Store raw method results for metadata
		result["_rawMethodResults"] = rawMethodResults
	}

	return result
}

// processSingleContractReadResult handles the logic for single method call results
func processSingleContractReadResult(methodResult interface{}, result map[string]interface{}) map[string]interface{} {
	if methodResultMap, ok := methodResult.(map[string]interface{}); ok {
		if success, ok := methodResultMap["success"].(bool); ok && success {
			// Use "value" field from new format
			if value, ok := methodResultMap["value"]; ok {
				if valueMap, ok := value.(map[string]interface{}); ok {
					// Return data directly without wrapping
					for key, val := range valueMap {
						result[key] = val
					}
				} else {
					// Single value result (e.g., for balanceOf)
					result["data"] = value
				}
				// Also include method metadata for debugging
				result["method_name"] = methodResultMap["methodName"]
			}
		} else {
			// For failed calls, include error information
			result["error"] = methodResultMap["error"]
			result["method_name"] = methodResultMap["methodName"]
			result["success"] = false
		}
	}

	return result
}

// SubstituteTemplateVariablesArray is a shared utility function that replaces template variables
// in each element of a string array using the provided substitution function.
// Used by loop execution for processing method parameters with template substitution.
func SubstituteTemplateVariablesArray(arr []string, iterInputs map[string]interface{}, substituteFunc func(string, map[string]interface{}) string) []string {
	if len(arr) == 0 {
		return arr
	}

	result := make([]string, len(arr))
	for i, item := range arr {
		result[i] = substituteFunc(item, iterInputs)
	}
	return result
}

// convertToJSONCompatible converts complex types (like protobuf) to JSON-compatible structures.
// This function handles time.Time, pointers, protobuf types, and reflection-based conversion.
// Used by loop execution to ensure iteration results can be serialized to JSON.
func convertToJSONCompatible(data interface{}) interface{} {
	if data == nil {
		return nil
	}

	// Use reflection to handle time.Time and other specific types
	rv := reflect.ValueOf(data)
	rt := reflect.TypeOf(data)

	// Handle time.Time specifically
	if rt == reflect.TypeOf(time.Time{}) {
		if timeVal, ok := data.(time.Time); ok {
			return timeVal.Format(time.RFC3339)
		}
	}

	// Handle pointers
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		return convertToJSONCompatible(rv.Elem().Interface())
	}

	switch v := data.(type) {
	case map[string]interface{}:
		// Already JSON-compatible, but recursively convert nested values
		result := make(map[string]interface{})
		for key, value := range v {
			result[key] = convertToJSONCompatible(value)
		}
		return result
	case []interface{}:
		// Already JSON-compatible, but recursively convert nested values
		result := make([]interface{}, len(v))
		for i, value := range v {
			result[i] = convertToJSONCompatible(value)
		}
		return result
	case *structpb.Value:
		// Convert protobuf Value to native Go type
		return convertToJSONCompatible(v.AsInterface())
	case *structpb.Struct:
		// Convert protobuf Struct to map
		return convertToJSONCompatible(v.AsMap())
	case *structpb.ListValue:
		// Convert protobuf ListValue to slice
		values := v.GetValues()
		result := make([]interface{}, len(values))
		for i, value := range values {
			result[i] = convertToJSONCompatible(value)
		}
		return result
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		// Basic JSON-compatible types
		return data
	default:
		// For complex types that might not be JSON-compatible, try to convert them
		// Check if it implements json.Marshaler
		if marshaler, ok := data.(json.Marshaler); ok {
			if jsonBytes, err := marshaler.MarshalJSON(); err == nil {
				var result interface{}
				if err := json.Unmarshal(jsonBytes, &result); err == nil {
					return result
				}
			}
		}

		// For other types, try reflection-based conversion
		if rv.Kind() == reflect.Struct {
			result := make(map[string]interface{})
			for i := 0; i < rv.NumField(); i++ {
				field := rt.Field(i)
				if field.IsExported() {
					fieldValue := rv.Field(i)
					if fieldValue.CanInterface() {
						result[field.Name] = convertToJSONCompatible(fieldValue.Interface())
					}
				}
			}
			return result
		}

		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			result := make([]interface{}, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				result[i] = convertToJSONCompatible(rv.Index(i).Interface())
			}
			return result
		}

		if rv.Kind() == reflect.Map {
			result := make(map[string]interface{})
			for _, key := range rv.MapKeys() {
				keyStr := fmt.Sprintf("%v", key.Interface())
				result[keyStr] = convertToJSONCompatible(rv.MapIndex(key).Interface())
			}
			return result
		}

		// Fallback: convert to string
		return fmt.Sprintf("%v", data)
	}
}

// convertProtoFieldsToMap converts protobuf fields to a map.
// Used for contract read result processing in loop iterations.
func convertProtoFieldsToMap(fields []*avsproto.ContractReadNode_MethodResult_StructuredField) map[string]interface{} {
	result := make(map[string]interface{})
	for _, field := range fields {
		result[field.GetName()] = field.GetValue()
	}
	return result
}

// parseGoMapString attempts to parse Go map string representation into a structured object.
// This is used as a fallback serialization method when JSON marshaling fails.
// Basic Go map string parsing for simple cases like "map[key:value]".
func parseGoMapString(s string) interface{} {
	// Handle empty cases
	if s == "" || s == "<nil>" || s == "null" {
		return nil
	}

	// Try to detect and parse simple map[key:value] patterns
	if strings.HasPrefix(s, "map[") && strings.HasSuffix(s, "]") {
		// Extract content between map[ and ]
		content := s[4 : len(s)-1]

		// Simple key:value parser
		result := make(map[string]interface{})

		// Split by spaces, but be careful with nested structures
		parts := strings.Fields(content)
		for _, part := range parts {
			if colonIdx := strings.Index(part, ":"); colonIdx > 0 {
				key := part[:colonIdx]
				value := part[colonIdx+1:]

				// Try to convert value to appropriate type
				if value == "true" {
					result[key] = true
				} else if value == "false" {
					result[key] = false
				} else if strings.Contains(value, ".") {
					// Try to parse as float
					if f, err := strconv.ParseFloat(value, 64); err == nil {
						result[key] = f
					} else {
						result[key] = value
					}
				} else {
					// Try to parse as int
					if i, err := strconv.Atoi(value); err == nil {
						result[key] = i
					} else {
						result[key] = value
					}
				}
			}
		}

		if len(result) > 0 {
			return result
		}
	}

	// If parsing fails, return nil so caller can use string representation
	return nil
}

// ConvertContractAbiToReader converts []*structpb.Value to *bytes.Reader
// for efficient ABI parsing without unnecessary string conversion.
func ConvertContractAbiToReader(abiValues []*structpb.Value) (*bytes.Reader, error) {
	if len(abiValues) == 0 {
		return bytes.NewReader([]byte("[]")), nil
	}

	// Convert protobuf Values to JSON more robustly
	abiElements := make([]json.RawMessage, len(abiValues))
	for i, value := range abiValues {
		// Get the struct value and convert it properly to JSON
		if structValue := value.GetStructValue(); structValue != nil {
			// Convert the struct to JSON bytes using protojson
			jsonBytes, err := protojson.Marshal(structValue)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal struct value %d: %v", i, err)
			}
			abiElements[i] = json.RawMessage(jsonBytes)
		} else {
			// Fallback to AsInterface for non-struct values
			interfaceValue := value.AsInterface()
			jsonBytes, err := json.Marshal(interfaceValue)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal interface value %d: %v", i, err)
			}
			abiElements[i] = json.RawMessage(jsonBytes)
		}
	}

	// Marshal the final array
	finalBytes, err := json.Marshal(abiElements)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal final ABI array: %v", err)
	}

	return bytes.NewReader(finalBytes), nil
}

// ParseABIOptimized efficiently parses ABI from protobuf Values without string conversion
// This function is shared between ContractReadProcessor and ContractWriteProcessor
func ParseABIOptimized(abiValues []*structpb.Value) (*abi.ABI, error) {
	if len(abiValues) == 0 {
		return nil, fmt.Errorf("empty ABI")
	}

	// Use the optimized helper to get bytes.Reader directly
	reader, err := ConvertContractAbiToReader(abiValues)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ABI to reader: %v", err)
	}

	// Parse ABI directly from bytes.Reader - no string conversion!
	parsedABI, err := abi.JSON(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}

	return &parsedABI, nil
}

// ConvertInterfaceArrayToProtobufValues converts a []interface{} array to []*structpb.Value array
// for protobuf contractAbi field storage.
func ConvertInterfaceArrayToProtobufValues(abiArray []interface{}) ([]*structpb.Value, error) {
	if len(abiArray) == 0 {
		return []*structpb.Value{}, nil
	}

	// Convert each interface{} to protobuf Value
	abiValues := make([]*structpb.Value, len(abiArray))
	for i, item := range abiArray {
		value, err := structpb.NewValue(item)
		if err != nil {
			return nil, fmt.Errorf("failed to convert contractAbi element %d to protobuf Value: %v", i, err)
		}
		abiValues[i] = value
	}

	return abiValues, nil
}

// ConvertJSONABIToProtobufValues converts a JSON ABI string to protobuf Values.
func ConvertJSONABIToProtobufValues(jsonABI string) ([]*structpb.Value, error) {
	// Parse JSON ABI into interface{}
	var abiArray []interface{}
	if err := json.Unmarshal([]byte(jsonABI), &abiArray); err != nil {
		return nil, fmt.Errorf("failed to parse JSON ABI: %v", err)
	}

	// Convert to protobuf Values
	return ConvertInterfaceArrayToProtobufValues(abiArray)
}

// GenerateCallData generates calldata from methodName, methodParams, and ABI
func GenerateCallData(methodName string, methodParams []string, contractAbi *abi.ABI) (string, error) {
	if contractAbi == nil {
		return "", fmt.Errorf("contract ABI is required to generate calldata")
	}

	// Find the method in the ABI
	method, exists := contractAbi.Methods[methodName]
	if !exists {
		return "", fmt.Errorf("method '%s' not found in contract ABI", methodName)
	}

	// Validate parameter count
	if len(methodParams) != len(method.Inputs) {
		return "", fmt.Errorf("method '%s' expects %d parameters, got %d", methodName, len(method.Inputs), len(methodParams))
	}

	// Prepare arguments for ABI encoding
	var args []interface{}
	for i, param := range methodParams {
		input := method.Inputs[i]

		// Get parameter name for better error messages
		paramName := fmt.Sprintf("parameter[%d]", i)
		if input.Name != "" {
			paramName = input.Name
		}

		// Parse parameter based on its ABI type
		parsedArg, err := parseABIParameter(param, input.Type)
		if err != nil {
			// For tuple parameters, the error already includes field names, so return it directly
			// For simple parameters, we need to include the parameter name
			if input.Type.T == abi.TupleTy {
				return "", err
			}
			// For simple parameters, include parameter name in the error
			return "", fmt.Errorf("failed to parse %s (%s): %v", paramName, input.Type.String(), err)
		}

		args = append(args, parsedArg)
	}

	// Pack the method call using go-ethereum ABI
	// For struct parameters, we might need to handle them specially
	if len(method.Inputs) == 1 && method.Inputs[0].Type.T == abi.TupleTy {
		// Special handling for single struct parameter
		if slice, ok := args[0].([]interface{}); ok {
			// Create a struct type dynamically using reflection to match the ABI tuple definition
			tupleType := method.Inputs[0].Type

			// Create struct fields based on the tuple definition
			var fields []reflect.StructField
			for i := range tupleType.TupleElems {
				fieldName := tupleType.TupleRawNames[i]
				// Capitalize first letter for exported field (Go requirement)
				if len(fieldName) > 0 {
					fieldName = strings.ToUpper(fieldName[:1]) + fieldName[1:]
				}

				// Use the actual type of the slice element
				var fieldType reflect.Type
				if i < len(slice) {
					fieldType = reflect.TypeOf(slice[i])
				} else {
					fieldType = reflect.TypeOf((*interface{})(nil)).Elem()
				}

				fields = append(fields, reflect.StructField{
					Name: fieldName,
					Type: fieldType,
				})
			}

			// Create the struct type and instance
			structType := reflect.StructOf(fields)
			structValue := reflect.New(structType).Elem()

			// Set the field values from the slice
			for i, value := range slice {
				if i >= len(slice) || i >= structValue.NumField() {
					break // Prevent index out of range panics
				}
				field := structValue.Field(i)
				if field.CanSet() {
					field.Set(reflect.ValueOf(value))
				}
			}

			// Pack the struct using the ABI
			structInterface := structValue.Interface()
			packedArgs, err := method.Inputs.Pack(structInterface)
			if err != nil {
				return "", fmt.Errorf("failed to pack struct arguments: %v", err)
			}

			// Combine method selector with packed arguments
			selector := contractAbi.Methods[methodName].ID
			calldata := append(selector, packedArgs...)

			return fmt.Sprintf("0x%x", calldata), nil
		}
	}

	// Fallback to normal packing
	calldata, err := contractAbi.Pack(methodName, args...)
	if err != nil {
		return "", fmt.Errorf("failed to pack method call: %v", err)
	}

	return fmt.Sprintf("0x%x", calldata), nil
}

// GenerateOrUseCallData generates calldata from methodName and methodParams if callData is empty,
// otherwise returns the existing callData. This is shared by both ContractRead and ContractWrite processors.
func GenerateOrUseCallData(methodName string, callData string, methodParams []string, contractAbi *abi.ABI) (string, error) {
	// If callData is already provided, use it directly
	if callData != "" {
		return callData, nil
	}

	// If callData is empty but we have methodName, generate it from methodParams
	if methodName != "" {
		return GenerateCallData(methodName, methodParams, contractAbi)
	}

	// No callData and no methodName - this is an error
	return "", fmt.Errorf("either callData or methodName must be provided")
}

// parseABIParameter parses a string parameter into the appropriate Go type based on ABI type
func parseABIParameter(param string, abiType abi.Type) (interface{}, error) {
	// Handle empty parameters for methods with no inputs (only when type is truly unspecified)
	// Note: We check abiType.T == 0 instead of String() == "" because String() might return empty for incomplete types
	if param == "" && abiType.T == 0 {
		return nil, nil
	}

	switch abiType.T {
	case abi.AddressTy:
		if !common.IsHexAddress(param) {
			return nil, fmt.Errorf("invalid address format: %s", param)
		}
		return common.HexToAddress(param), nil

	case abi.UintTy, abi.IntTy:
		// Handle big integers
		// Validate that the parameter is a valid number
		paramTrimmed := strings.TrimSpace(param)
		if paramTrimmed == "" {
			return nil, fmt.Errorf("expected numeric value, got ''")
		}

		value := new(big.Int)
		var ok bool
		if strings.HasPrefix(paramTrimmed, "0x") {
			_, ok = value.SetString(paramTrimmed[2:], 16)
		} else {
			// Check if it's a valid decimal number (allows digits, optional negative sign)
			_, ok = value.SetString(paramTrimmed, 10)
		}

		if !ok {
			return nil, fmt.Errorf("expected numeric value, got '%s'", paramTrimmed)
		}

		return value, nil

	case abi.BoolTy:
		switch strings.ToLower(param) {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		default:
			return nil, fmt.Errorf("invalid boolean value: %s", param)
		}

	case abi.StringTy:
		return param, nil

	case abi.BytesTy, abi.FixedBytesTy:
		if strings.HasPrefix(param, "0x") {
			return common.FromHex(param), nil
		}
		return []byte(param), nil

	case abi.SliceTy, abi.ArrayTy:
		// For arrays, we'd need more complex parsing
		// For now, return error to indicate unsupported
		return nil, fmt.Errorf("array/slice parameters not yet supported in generateCallData")

	case abi.TupleTy:
		// Handle struct/tuple parameters
		// If param is a JSON array, parse it and convert elements to appropriate types
		if strings.HasPrefix(param, "[") && strings.HasSuffix(param, "]") {
			var arrayElements []interface{}
			if err := json.Unmarshal([]byte(param), &arrayElements); err != nil {
				return nil, fmt.Errorf("failed to parse JSON array for tuple: %v", err)
			}

			// Validate that we have the right number of elements
			if len(arrayElements) != len(abiType.TupleElems) {
				return nil, fmt.Errorf("tuple expects %d elements, got %d", len(abiType.TupleElems), len(arrayElements))
			}

			// Parse each element according to its type
			var tupleElements []interface{}
			for i, element := range arrayElements {
				// Convert element to string, handling different JSON types properly
				var elementStr string
				switch v := element.(type) {
				case string:
					elementStr = v
				case float64:
					// JSON numbers become float64, convert to string without decimal if it's a whole number
					if v == float64(int64(v)) {
						elementStr = fmt.Sprintf("%.0f", v)
					} else {
						elementStr = fmt.Sprintf("%g", v)
					}
				case bool:
					if v {
						elementStr = "true"
					} else {
						elementStr = "false"
					}
				default:
					elementStr = fmt.Sprintf("%v", element)
				}

				// Get field name for better error messages
				fieldName := fmt.Sprintf("element[%d]", i)
				if i < len(abiType.TupleRawNames) && abiType.TupleRawNames[i] != "" {
					fieldName = abiType.TupleRawNames[i]
				}

				parsedElement, err := parseABIParameter(elementStr, *abiType.TupleElems[i])
				if err != nil {
					return nil, fmt.Errorf("failed to parse tuple %s (%s): %v", fieldName, abiType.TupleElems[i].String(), err)
				}
				tupleElements = append(tupleElements, parsedElement)
			}

			// For Go ethereum ABI library, we need to return the tuple elements
			// in a way that can be unpacked. The library expects struct-like data.
			// Let's try returning a map or a different structure

			// Actually, let's try returning the slice but handle it differently in the caller
			return tupleElements, nil
		}
		return nil, fmt.Errorf("tuple parameter must be a JSON array")

	default:
		return nil, fmt.Errorf("unsupported ABI type: %s", abiType.String())
	}
}

// GetMapKeys returns the keys of a map[string]interface{} as a slice of strings
// This is a utility function used across multiple files for debugging purposes
func GetMapKeys(m map[string]interface{}) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// FormatAsJSON converts any data structure to a JSON string for logging purposes.
// This function disables HTML escaping to provide clean, readable output for developer logs.
// For complex structures (maps, slices, structs), it marshals to JSON.
// For simple types (strings, numbers, booleans), it returns a string representation.
func FormatAsJSON(data interface{}) string {
	if data == nil {
		return "null"
	}

	// Handle simple types directly
	switch v := data.(type) {
	case string:
		// If it's already a valid JSON string, return as-is
		// Otherwise, marshal it to preserve proper JSON string quoting
		var testJSON interface{}
		if err := json.Unmarshal([]byte(v), &testJSON); err == nil {
			// It's valid JSON, return as-is
			return v
		}
		// Not valid JSON, marshal as string
		result, _ := json.Marshal(v)
		return string(result)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		result, _ := json.Marshal(v)
		return string(result)
	}

	// For complex types, marshal to JSON with HTML escaping disabled
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false) // Disable HTML escaping for clean output
	encoder.SetIndent("", "")    // Compact output (no pretty printing)

	if err := encoder.Encode(data); err != nil {
		// If marshaling fails, fall back to fmt.Sprintf
		return fmt.Sprintf("%v", data)
	}

	// Remove trailing newline added by encoder.Encode
	result := buf.String()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	return result
}

// FormatStringListWithAnd formats a list of strings with natural language conjunctions.
// This is useful for generating human-readable lists in logs, emails, and UI messages.
// Examples:
//   - 1 item: "the node1 node"
//   - 2 items: "both node1 and node2 nodes"
//   - 3+ items: "node1, node2, and node3 nodes"
func FormatStringListWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("the %s node", items[0])
	case 2:
		return fmt.Sprintf("both %s and %s nodes", items[0], items[1])
	default:
		// Three or more: "X, Y, and Z nodes"
		lastItem := items[len(items)-1]
		otherItems := strings.Join(items[:len(items)-1], ", ")
		return fmt.Sprintf("%s, and %s nodes", otherItems, lastItem)
	}
}
