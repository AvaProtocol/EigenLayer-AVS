package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/modules"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	"github.com/dop251/goja"
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
	// For single method calls, return the data directly (flattened format)
	// For multiple method calls, return as array (backward compatibility)
	var resultsValue *structpb.Value
	var structErr error

	if len(resultsArray) == 1 {
		// Single method call - return the result directly for flattened format
		resultsValue, structErr = structpb.NewValue(resultsArray[0])
	} else {
		// Multiple method calls - return as array
		resultsValue, structErr = structpb.NewValue(resultsArray)
	}

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
					"data":       methodMap["rawStructuredFields"], // The raw structured fields from ABI decoding
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
			// Return the data directly for successful single method calls
			if data, ok := methodResultMap["data"].(map[string]interface{}); ok {
				// Return data directly without wrapping, but exclude rawStructuredFields
				for key, value := range data {
					if key != "rawStructuredFields" {
						result[key] = value
					}
				}
				// Also include method metadata for debugging
				result["method_name"] = methodResultMap["methodName"]
				// Store raw structured fields separately for metadata (don't add to main result)
				if rawStructuredFields, ok := methodResultMap["rawStructuredFields"].([]interface{}); ok {
					result["rawStructuredFields"] = rawStructuredFields // Keep for metadata generation
				}
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
