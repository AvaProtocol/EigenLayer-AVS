package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"bytes"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/modules"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	"github.com/dop251/goja"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/shopspring/decimal"
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
	return vm
}

// NewGojaVMWithModules creates a new Goja runtime with module support.
func NewGojaVMWithModules() (*goja.Runtime, *modules.Registry, error) {
	vm := goja.New()
	macros.ConfigureGojaRuntime(vm)

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

// ConvertContractAbiToReader converts a []*structpb.Value array (from protobuf) directly to bytes.Reader
// for efficient ABI parsing without unnecessary string conversion.
func ConvertContractAbiToReader(abiValues []*structpb.Value) (*bytes.Reader, error) {
	if len(abiValues) == 0 {
		return bytes.NewReader([]byte("[]")), nil
	}

	// Convert each protobuf Value back to Go interface{}
	abiArray := make([]interface{}, len(abiValues))
	for i, value := range abiValues {
		abiArray[i] = value.AsInterface()
	}

	// Marshal directly to JSON bytes
	abiBytes, err := json.Marshal(abiArray)
	if err != nil {
		return nil, fmt.Errorf("failed to convert contractAbi array to JSON: %v", err)
	}

	// Return bytes.Reader directly - no string conversion!
	return bytes.NewReader(abiBytes), nil
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

// ConvertJSONABIToProtobufValues converts a JSON ABI string to protobuf Values
// This is primarily used by test files to convert user-input format ABIs
func ConvertJSONABIToProtobufValues(jsonABI string) []*structpb.Value {
	var abiArray []interface{}
	if err := json.Unmarshal([]byte(jsonABI), &abiArray); err != nil {
		return nil
	}
	abiValues, err := ConvertInterfaceArrayToProtobufValues(abiArray)
	if err != nil {
		return nil
	}
	return abiValues
}
