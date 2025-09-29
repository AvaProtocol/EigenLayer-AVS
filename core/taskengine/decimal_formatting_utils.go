package taskengine

import (
	"math/big"
	"strconv"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// DecimalFormattingContext holds information needed for consistent decimal formatting
type DecimalFormattingContext struct {
	DecimalsValue  *big.Int
	ApplyToFields  map[string]bool // Set of field names that should be formatted
	ProviderMethod string          // Name of method that provided the decimals (for logging)
}

// NewDecimalFormattingContext creates a new decimal formatting context
func NewDecimalFormattingContext(decimalsValue *big.Int, fieldsToFormat []string, providerMethod string) *DecimalFormattingContext {
	applyToFields := make(map[string]bool)
	for _, field := range fieldsToFormat {
		applyToFields[field] = true
	}
	return &DecimalFormattingContext{
		DecimalsValue:  decimalsValue,
		ApplyToFields:  applyToFields,
		ProviderMethod: providerMethod,
	}
}

// ApplyDecimalFormattingToStructuredFields applies decimal formatting to protobuf StructuredFields
// This is used by ContractRead/ContractWrite nodes
func (ctx *DecimalFormattingContext) ApplyDecimalFormattingToStructuredFields(originalData []*avsproto.ContractReadNode_MethodResult_StructuredField) []*avsproto.ContractReadNode_MethodResult_StructuredField {
	if len(originalData) == 0 || ctx.DecimalsValue == nil || len(ctx.ApplyToFields) == 0 {
		return originalData
	}

	var formattedData []*avsproto.ContractReadNode_MethodResult_StructuredField
	converter := &ABIValueConverter{}

	for _, field := range originalData {
		if ctx.ApplyToFields[field.Name] {
			// Apply decimal formatting to this field
			if rawValue, ok := new(big.Int).SetString(field.Value, 10); ok {
				formattedValue := converter.FormatWithDecimals(rawValue, ctx.DecimalsValue)
				formattedData = append(formattedData, &avsproto.ContractReadNode_MethodResult_StructuredField{
					Name:  field.Name,
					Value: formattedValue,
				})
			} else {
				// If we can't parse as big int, keep original
				formattedData = append(formattedData, field)
			}
		} else {
			// Keep original field as-is
			formattedData = append(formattedData, field)
		}
	}

	return formattedData
}

// ApplyDecimalFormattingToEventData applies decimal formatting to event data structure
// This is used by EventTrigger nodes
func (ctx *DecimalFormattingContext) ApplyDecimalFormattingToEventData(eventData map[string]interface{}, eventName string, logger Logger) {
	if ctx.DecimalsValue == nil || len(ctx.ApplyToFields) == 0 {
		return
	}

	converter := &ABIValueConverter{}

	// Apply formatting to each field that should be formatted
	for applyToField := range ctx.ApplyToFields {
		// Parse eventName.fieldName format
		parts := strings.Split(applyToField, ".")
		if len(parts) == 2 {
			targetEventName := parts[0]
			targetFieldName := parts[1]

			// Only apply formatting if this matches the current event
			if targetEventName == eventName {
				// Check if this field exists in the event data
				if rawValue, exists := eventData[targetFieldName]; exists {
					if rawValueStr, ok := rawValue.(string); ok {
						if rawBigInt, success := new(big.Int).SetString(rawValueStr, 10); success {
							formattedValue := converter.FormatWithDecimals(rawBigInt, ctx.DecimalsValue)
							eventData[targetFieldName] = formattedValue
							eventData[targetFieldName+"Raw"] = rawValueStr

							if logger != nil {
								logger.Info("✅ Applied decimal formatting to event field",
									"eventName", eventName,
									"fieldName", targetFieldName,
									"applyToField", applyToField,
									"rawValue", rawValueStr,
									"formattedValue", formattedValue,
									"decimals", ctx.DecimalsValue.String(),
									"providerMethod", ctx.ProviderMethod)
							}
						}
					}
				}
			}
		} else {
			// Handle direct field names (backwards compatibility)
			if rawValue, exists := eventData[applyToField]; exists {
				if rawValueStr, ok := rawValue.(string); ok {
					if rawBigInt, success := new(big.Int).SetString(rawValueStr, 10); success {
						formattedValue := converter.FormatWithDecimals(rawBigInt, ctx.DecimalsValue)
						eventData[applyToField] = formattedValue
						eventData[applyToField+"Raw"] = rawValueStr

						if logger != nil {
							logger.Info("✅ Applied decimal formatting to event field (direct)",
								"eventName", eventName,
								"fieldName", applyToField,
								"rawValue", rawValueStr,
								"formattedValue", formattedValue,
								"decimals", ctx.DecimalsValue.String(),
								"providerMethod", ctx.ProviderMethod)
						}
					}
				}
			}
		}
	}
}

// ShouldApplyDecimalFormatting checks if a condition should be evaluated with decimal formatting
// This helps determine if condition values need to be interpreted as decimal values
func (ctx *DecimalFormattingContext) ShouldApplyDecimalFormatting(fieldName string) bool {
	if ctx == nil || ctx.DecimalsValue == nil {
		return false
	}
	return ctx.ApplyToFields[fieldName]
}

// FormatConditionValueForComparison formats a condition value for comparison with a decimal-formatted field
// If the field should be decimal-formatted, this treats the condition value as a decimal number
// and formats it the same way for consistent comparison
func (ctx *DecimalFormattingContext) FormatConditionValueForComparison(fieldName, conditionValue string) string {
	// Check if this field should be decimal-formatted
	// Handle both direct field names and eventName.fieldName format
	shouldFormat := ctx.ShouldApplyDecimalFormatting(fieldName)
	if !shouldFormat {
		// Also check if any of our applyToFields matches this field name pattern
		for applyToField := range ctx.ApplyToFields {
			if strings.HasSuffix(applyToField, "."+fieldName) {
				shouldFormat = true
				break
			}
		}
	}

	if !shouldFormat {
		return conditionValue
	}

	// Parse the condition value as a decimal number
	if conditionFloat, err := strconv.ParseFloat(conditionValue, 64); err == nil {
		// Convert to raw format by multiplying by 10^decimals
		decimalsInt := ctx.DecimalsValue.Int64()
		multiplier := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(decimalsInt), nil))
		rawValue := new(big.Float).Mul(big.NewFloat(conditionFloat), multiplier)

		// Convert back to big.Int
		rawBigInt, _ := rawValue.Int(nil)

		// Format using the same decimal formatting logic
		converter := &ABIValueConverter{}
		return converter.FormatWithDecimals(rawBigInt, ctx.DecimalsValue)
	}

	// If we can't parse as float, return original value
	return conditionValue
}

// Logger interface for logging decimal formatting operations
type Logger interface {
	Info(msg string, keysAndValues ...interface{})
	Warn(msg string, keysAndValues ...interface{})
	Debug(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
}
