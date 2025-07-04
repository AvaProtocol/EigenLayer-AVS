package taskengine

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// ABIValueConverter provides utilities for converting ABI values to appropriate types
type ABIValueConverter struct {
	decimalsValue     *big.Int
	fieldsToFormat    []string
	rawFieldsMetadata map[string]interface{}
}

// NewABIValueConverter creates a new ABI value converter
func NewABIValueConverter(decimalsValue *big.Int, fieldsToFormat []string) *ABIValueConverter {
	return &ABIValueConverter{
		decimalsValue:     decimalsValue,
		fieldsToFormat:    fieldsToFormat,
		rawFieldsMetadata: make(map[string]interface{}),
	}
}

// ShouldFormatField checks if a field should be formatted with decimals
func (c *ABIValueConverter) ShouldFormatField(fieldName string) bool {
	if c.decimalsValue == nil || len(c.fieldsToFormat) == 0 {
		return false
	}
	for _, field := range c.fieldsToFormat {
		if field == fieldName {
			return true
		}
	}
	return false
}

// FormatWithDecimals formats a big.Int value with decimals
func (c *ABIValueConverter) FormatWithDecimals(value *big.Int, decimals *big.Int) string {
	if decimals == nil || decimals.Cmp(big.NewInt(0)) <= 0 {
		return value.String()
	}

	// Create divisor: 10^decimals
	divisor := new(big.Int).Exp(big.NewInt(10), decimals, nil)

	// Perform division: quotient and remainder
	quotient := new(big.Int).Div(value, divisor)
	remainder := new(big.Int).Mod(value, divisor)

	// Format the result as a decimal string
	if remainder.Cmp(big.NewInt(0)) == 0 {
		return quotient.String()
	}

	// Pad remainder with leading zeros to match decimal places
	remainderStr := remainder.String()
	decimalsInt := int(decimals.Int64())
	for len(remainderStr) < decimalsInt {
		remainderStr = "0" + remainderStr
	}

	// Remove trailing zeros from remainder
	remainderStr = strings.TrimRight(remainderStr, "0")
	if remainderStr == "" {
		return quotient.String()
	}

	return fmt.Sprintf("%s.%s", quotient.String(), remainderStr)
}

// ConvertABIValueToInterface converts an ABI value to the appropriate Go interface{} type
// This is used for event parsing where we return interface{} values
func (c *ABIValueConverter) ConvertABIValueToInterface(value interface{}, abiType abi.Type, fieldName string) interface{} {
	switch abiType.T {
	case abi.UintTy, abi.IntTy:
		// Handle numeric types - return proper numeric values, not strings
		if bigInt, ok := value.(*big.Int); ok {
			// Check if this field should be formatted with decimals
			shouldFormat := c.ShouldFormatField(fieldName)

			// Always store raw value for the "value" field (for Transfer events)
			if fieldName == "value" {
				c.rawFieldsMetadata[fieldName+"Raw"] = bigInt.String()
			}

			if shouldFormat {
				formattedValue := c.FormatWithDecimals(bigInt, c.decimalsValue)
				return formattedValue
			}

			// Return appropriate numeric type based on ABI size
			switch abiType.Size {
			case 8: // uint8, int8
				if abiType.T == abi.UintTy {
					return bigInt.Uint64() // Will fit in uint64 for uint8
				} else {
					return bigInt.Int64() // Will fit in int64 for int8
				}
			case 16: // uint16, int16
				if abiType.T == abi.UintTy {
					return bigInt.Uint64()
				} else {
					return bigInt.Int64()
				}
			case 32: // uint32, int32
				if abiType.T == abi.UintTy {
					return bigInt.Uint64()
				} else {
					return bigInt.Int64()
				}
			case 64: // uint64, int64
				if abiType.T == abi.UintTy {
					return bigInt.Uint64()
				} else {
					return bigInt.Int64()
				}
			default: // uint256, int256 - keep as string to avoid precision loss
				return bigInt.String()
			}
		}
		// If not a big.Int, convert to string as fallback
		return fmt.Sprintf("%v", value)

	case abi.BoolTy:
		// Return boolean values as actual booleans
		if boolVal, ok := value.(bool); ok {
			return boolVal
		}
		return fmt.Sprintf("%v", value)

	case abi.AddressTy:
		// Return addresses as hex strings
		if addr, ok := value.(common.Address); ok {
			return addr.Hex()
		}
		return fmt.Sprintf("%v", value)

	case abi.StringTy:
		// Return strings as-is
		if str, ok := value.(string); ok {
			return str
		}
		return fmt.Sprintf("%v", value)

	case abi.BytesTy, abi.FixedBytesTy:
		// Return bytes as hex strings
		if bytes, ok := value.([]byte); ok {
			return "0x" + common.Bytes2Hex(bytes)
		}
		if hash, ok := value.(common.Hash); ok {
			return hash.Hex()
		}
		return fmt.Sprintf("%v", value)

	default:
		// For any other types, convert to string
		return fmt.Sprintf("%v", value)
	}
}

// ConvertABIValueToString converts an ABI value to string representation
// This is used for contract reads and writes where we need string values
func (c *ABIValueConverter) ConvertABIValueToString(value interface{}, abiType abi.Type, fieldName string) string {
	switch abiType.T {
	case abi.UintTy, abi.IntTy:
		// Handle numeric types - return proper values based on size
		if bigInt, ok := value.(*big.Int); ok {
			// Always store raw value for the "value" field (for Transfer events)
			if fieldName == "value" {
				c.rawFieldsMetadata[fieldName+"Raw"] = bigInt.String()
			}

			// Check if this field should be formatted with decimals
			if c.ShouldFormatField(fieldName) {
				formattedValue := c.FormatWithDecimals(bigInt, c.decimalsValue)
				return formattedValue
			}

			// Return appropriate numeric type based on ABI size
			switch abiType.Size {
			case 8, 16, 32, 64: // uint8-64, int8-64
				if abiType.T == abi.UintTy {
					return fmt.Sprintf("%d", bigInt.Uint64())
				} else {
					return fmt.Sprintf("%d", bigInt.Int64())
				}
			default: // uint256, int256 - keep as string to avoid precision loss
				return bigInt.String()
			}
		}
		// If not a big.Int, convert to string as fallback
		return fmt.Sprintf("%v", value)

	case abi.BoolTy:
		// Return boolean values as string representation of bool
		if boolVal, ok := value.(bool); ok {
			return fmt.Sprintf("%t", boolVal)
		}
		return fmt.Sprintf("%v", value)

	case abi.AddressTy:
		// Return addresses as hex strings
		if addr, ok := value.(common.Address); ok {
			return addr.Hex()
		}
		return fmt.Sprintf("%v", value)

	case abi.StringTy:
		// Return strings as-is
		if str, ok := value.(string); ok {
			return str
		}
		return fmt.Sprintf("%v", value)

	case abi.BytesTy, abi.FixedBytesTy:
		// Return bytes as hex strings
		if bytes, ok := value.([]byte); ok {
			return "0x" + common.Bytes2Hex(bytes)
		}
		if hash, ok := value.(common.Hash); ok {
			return hash.Hex()
		}
		return fmt.Sprintf("%v", value)

	default:
		// For any other types, convert to string
		return fmt.Sprintf("%v", value)
	}
}

// GetRawFieldsMetadata returns the raw fields metadata collected during conversion
func (c *ABIValueConverter) GetRawFieldsMetadata() map[string]interface{} {
	return c.rawFieldsMetadata
}

// ConvertTopicValueByType converts a topic value based on its ABI type
func ConvertTopicValueByType(topicValue common.Hash, abiType abi.Type) interface{} {
	switch abiType.T {
	case abi.UintTy, abi.IntTy:
		if bigInt := new(big.Int).SetBytes(topicValue.Bytes()); bigInt != nil {
			// Return appropriate numeric type based on ABI size
			switch abiType.Size {
			case 8, 16, 32, 64:
				if abiType.T == abi.UintTy {
					return bigInt.Uint64()
				} else {
					return bigInt.Int64()
				}
			default: // uint256, int256
				return bigInt.String()
			}
		}
		return topicValue.Hex()
	case abi.BoolTy:
		return new(big.Int).SetBytes(topicValue.Bytes()).Cmp(big.NewInt(0)) != 0
	case abi.AddressTy:
		return common.HexToAddress(topicValue.Hex()).Hex()
	default:
		return topicValue.Hex()
	}
}

// ConvertTopicValueToString converts a topic value to string based on its ABI type
func ConvertTopicValueToString(topicValue common.Hash, abiType abi.Type) string {
	switch abiType.T {
	case abi.UintTy, abi.IntTy:
		if bigInt := new(big.Int).SetBytes(topicValue.Bytes()); bigInt != nil {
			// Return appropriate numeric type based on ABI size
			switch abiType.Size {
			case 8, 16, 32, 64:
				if abiType.T == abi.UintTy {
					return fmt.Sprintf("%d", bigInt.Uint64())
				} else {
					return fmt.Sprintf("%d", bigInt.Int64())
				}
			default: // uint256, int256
				return bigInt.String()
			}
		}
		return topicValue.Hex()
	case abi.BoolTy:
		boolVal := new(big.Int).SetBytes(topicValue.Bytes()).Cmp(big.NewInt(0)) != 0
		return fmt.Sprintf("%t", boolVal)
	case abi.AddressTy:
		return common.HexToAddress(topicValue.Hex()).Hex()
	default:
		return topicValue.Hex()
	}
}
