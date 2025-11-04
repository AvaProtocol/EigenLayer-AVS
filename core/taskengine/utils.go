package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"bytes"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/modules"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
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
// This eliminates code duplication between VM and LoopProcessor implementations.
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

		// Parse parameter based on its ABI type
		parsedArg, err := parseABIParameter(param, input.Type)
		if err != nil {
			return "", fmt.Errorf("failed to parse parameter %d (%s): %v", i, param, err)
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
	// Handle empty parameters for methods with no inputs
	if param == "" && abiType.String() == "" {
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
		value := new(big.Int)
		if strings.HasPrefix(param, "0x") {
			value.SetString(param[2:], 16)
		} else {
			value.SetString(param, 10)
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

				parsedElement, err := parseABIParameter(elementStr, *abiType.TupleElems[i])
				if err != nil {
					return nil, fmt.Errorf("failed to parse tuple element %d: %v", i, err)
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
