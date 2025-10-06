package taskengine

import (
	"encoding/json"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Input size limits for DoS prevention
// These limits are designed to prevent malicious users from overwhelming the system
// with extremely large inputs while allowing legitimate use cases.
const (
	// MaxManualTriggerDataSize is the maximum size for manual trigger data field (1MB)
	// This allows for reasonable JSON payloads while preventing memory exhaustion
	MaxManualTriggerDataSize = 1024 * 1024 // 1MB

	// MaxRestAPIBodySize is the maximum size for REST API request bodies (10MB)
	// This accommodates larger payloads that might be needed for API integrations
	// while still preventing abuse
	MaxRestAPIBodySize = 10 * 1024 * 1024 // 10MB

	// MaxCustomCodeSourceSize is the maximum size for custom JavaScript code (100KB)
	// Most legitimate JavaScript code should be well under this limit
	MaxCustomCodeSourceSize = 100 * 1024 // 100KB

	// MaxContractABISize is the maximum total size for contract ABI arrays (1MB)
	// This accommodates large contracts with many functions while preventing abuse
	MaxContractABISize = 1024 * 1024 // 1MB

	// MaxEventTriggerABIItemSize is the maximum size for a single ABI item (100KB)
	// Individual ABI function definitions should be much smaller than this
	MaxEventTriggerABIItemSize = 100 * 1024 // 100KB
)

// ValidationErrorMessages provides consistent error messages for size limit violations
var ValidationErrorMessages = struct {
	ManualTriggerDataTooLarge   string
	RestAPIBodyTooLarge         string
	CustomCodeSourceTooLarge    string
	ContractABITooLarge         string
	EventTriggerABIItemTooLarge string
	RestAPIBodyInvalidJSON      string
}{
	ManualTriggerDataTooLarge:   "ManualTrigger data exceeds maximum size limit",
	RestAPIBodyTooLarge:         "REST API request body exceeds maximum size limit",
	CustomCodeSourceTooLarge:    "Custom code source exceeds maximum size limit",
	ContractABITooLarge:         "Contract ABI exceeds maximum total size limit",
	EventTriggerABIItemTooLarge: "Contract ABI item exceeds maximum size limit",
	RestAPIBodyInvalidJSON:      "REST API request body contains invalid JSON",
}

// ParseLanguageFromConfig extracts and validates the language field from a config map.
// It supports multiple input types (int32, float64, string, Lang enum) for flexibility.
// Returns error if lang is missing or invalid - NO DEFAULT (strict requirement).
func ParseLanguageFromConfig(config map[string]interface{}) (avsproto.Lang, error) {
	langInterface, ok := config["lang"]
	if !ok {
		return 0, NewStructuredError(
			avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			"language field (lang) is required",
			map[string]interface{}{
				"field": "lang",
				"issue": "missing required field",
			},
		)
	}

	switch v := langInterface.(type) {
	case int32:
		return avsproto.Lang(v), nil
	case float64:
		// JSON unmarshaling often produces float64 for numbers
		return avsproto.Lang(int32(v)), nil
	case avsproto.Lang:
		return v, nil
	case string:
		// Support string enum names (e.g., "JSON", "JavaScript")
		if enumVal, exists := avsproto.Lang_value[v]; exists {
			return avsproto.Lang(enumVal), nil
		}
		return 0, NewStructuredError(
			avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			fmt.Sprintf("invalid language string value: %q", v),
			map[string]interface{}{
				"field":        "lang",
				"issue":        "invalid string value",
				"received":     v,
				"valid_values": []string{"LANG_JAVASCRIPT", "LANG_JSON", "LANG_GRAPHQL", "LANG_HANDLEBARS"},
			},
		)
	default:
		return 0, NewStructuredError(
			avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			fmt.Sprintf("invalid language field type: %T", v),
			map[string]interface{}{
				"field":         "lang",
				"issue":         "invalid type",
				"received_type": fmt.Sprintf("%T", v),
			},
		)
	}
}

// ConvertLangStringToEnum converts lowercase language strings (from SDK) to protobuf enum constants.
// This is used by both RunNodeImmediatelyRPC (for CustomCode) and RunTriggerRPC (for ManualTrigger).
// Returns the converted enum value, or the original value if it's not a convertible string.
func ConvertLangStringToEnum(langValue interface{}) interface{} {
	langStr, ok := langValue.(string)
	if !ok {
		// Not a string, return as-is (might already be an enum)
		return langValue
	}

	// Convert lowercase language string to protobuf enum constant
	switch langStr {
	case "javascript":
		return avsproto.Lang_LANG_JAVASCRIPT
	case "json":
		return avsproto.Lang_LANG_JSON
	case "graphql":
		return avsproto.Lang_LANG_GRAPHQL
	case "handlebars":
		return avsproto.Lang_LANG_HANDLEBARS
	default:
		// Unknown/invalid string - return as-is and let ParseLanguageFromConfig handle the error
		return langValue
	}
}

// ValidateManualTriggerFromProtobuf extracts and validates manual trigger data from protobuf output.
// Used by TriggerTask path when working with protobuf ManualTrigger_Output.
func ValidateManualTriggerFromProtobuf(manualOutput *avsproto.ManualTrigger_Output, task *model.Task) error {
	if manualOutput == nil || manualOutput.Data == nil {
		return nil // No data to validate
	}

	// Extract data from protobuf Value
	var data interface{}
	if stringVal := manualOutput.Data.GetStringValue(); stringVal != "" {
		data = stringVal
	} else {
		data = manualOutput.Data.AsInterface()
	}

	// Get language from trigger config
	if task.Trigger.GetManual() == nil || task.Trigger.GetManual().Config == nil {
		return NewStructuredError(
			avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			"manual trigger config is required",
			map[string]interface{}{
				"field": "config",
				"issue": "missing trigger config",
			},
		)
	}
	lang := task.Trigger.GetManual().Config.Lang

	// Validate based on language using universal validator (which checks for LANG_UNSPECIFIED)
	return ValidateInputByLanguage(data, lang)
}

// ValidateInputByLanguage is the UNIVERSAL validation function for all nodes.
// Instead of each node (ManualTrigger, FilterNode, BranchNode, etc.) implementing
// their own validation logic, they ALL call this single function with their lang field.
//
// Benefits:
// - DRY: No duplicate validation code across nodes
// - Extensible: Add new languages in ONE place
// - Consistent: All nodes validate the same way
// - Testable: Test validation logic once, not per-node
//
// Usage:
//
//	lang := avsproto.Lang_LANG_JSON // or from config
//	if err := ValidateInputByLanguage(data, lang); err != nil {
//	    return nil, err
//	}
func ValidateInputByLanguage(data interface{}, lang avsproto.Lang) error {
	// First validate that lang is not UNSPECIFIED (zero value)
	// Use INVALID_NODE_CONFIG instead of INVALID_TRIGGER_CONFIG because this validator
	// is called from both trigger contexts (ManualTrigger) and non-trigger contexts
	// (CustomCodeNode, FilterNode, BranchNode). Using a generic error code provides
	// consistency and avoids coupling this utility to trigger-specific semantics.
	if lang == avsproto.Lang_LANG_UNSPECIFIED {
		return NewStructuredError(
			avsproto.ErrorCode_INVALID_NODE_CONFIG,
			"language field (lang) is required and cannot be LANG_UNSPECIFIED",
			map[string]interface{}{
				"field": "lang",
				"issue": "LANG_UNSPECIFIED not allowed",
			},
		)
	}

	switch lang {
	case avsproto.Lang_LANG_JSON:
		return ValidateJSONFormat(data)
	case avsproto.Lang_LANG_JAVASCRIPT:
		return ValidateJavaScriptFormat(data)
	case avsproto.Lang_LANG_GRAPHQL:
		return ValidateGraphQLFormat(data)
	case avsproto.Lang_LANG_HANDLEBARS:
		return ValidateHandlebarsFormat(data)
	default:
		// For unknown/unspecified languages, no validation
		// This allows flexibility for future formats
		return nil
	}
}

// ValidateJSONFormat validates JSON string format and size.
// This is extracted from ValidateManualTriggerData to be reusable
// by any node that uses JSON format (not just ManualTrigger).
func ValidateJSONFormat(data interface{}) error {
	// Allow nil or missing data to pass through - handled by caller
	if data == nil {
		return nil
	}

	// Only validate if data is a string
	if dataStr, ok := data.(string); ok {
		// Check size limit first (before parsing to avoid wasting resources)
		if len(dataStr) > MaxManualTriggerDataSize {
			return NewStructuredError(
				avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
				fmt.Sprintf("%s: %d bytes (max: %d bytes)", ValidationErrorMessages.ManualTriggerDataTooLarge, len(dataStr), MaxManualTriggerDataSize),
				map[string]interface{}{
					"field":   "data",
					"issue":   "size limit exceeded",
					"size":    len(dataStr),
					"maxSize": MaxManualTriggerDataSize,
				},
			)
		}

		// Try to parse as JSON to validate format
		var jsonTest interface{}
		if err := json.Unmarshal([]byte(dataStr), &jsonTest); err != nil {
			return NewStructuredError(
				avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
				fmt.Sprintf("Data must be valid JSON: %s", err.Error()),
				map[string]interface{}{
					"field": "data",
					"issue": "invalid JSON format",
					"error": err.Error(),
					"data":  dataStr,
				},
			)
		}
	}

	return nil
}

// ValidateJavaScriptFormat validates JavaScript code size.
// This ensures ALL JavaScript code (CustomCodeNode, FilterNode, BranchNode) is checked
// for size limits, providing consistent DoS protection across all nodes.
//
// Coverage:
// - CustomCodeNode.Config.Source (full JavaScript programs)
// - FilterNode.Config.Expression (JavaScript filter expressions)
// - BranchNode.Condition.Expression (JavaScript condition expressions)
func ValidateJavaScriptFormat(data interface{}) error {
	// Allow nil or missing data to pass through - handled by caller
	if data == nil {
		return nil
	}

	// Only validate if data is a string
	if dataStr, ok := data.(string); ok {
		// Check size limit (same limit for all JavaScript code)
		if len(dataStr) > MaxCustomCodeSourceSize {
			return NewStructuredError(
				avsproto.ErrorCode_INVALID_NODE_CONFIG,
				fmt.Sprintf("JavaScript code exceeds maximum size limit: %d bytes (max: %d bytes)", len(dataStr), MaxCustomCodeSourceSize),
				map[string]interface{}{
					"field":    "source/expression",
					"issue":    "size limit exceeded",
					"size":     len(dataStr),
					"maxSize":  MaxCustomCodeSourceSize,
					"language": "JavaScript",
				},
			)
		}

		// TODO: Add JavaScript syntax validation
		// For now, size check is sufficient (syntax errors caught at runtime)
	}

	return nil
}

// ValidateGraphQLFormat validates GraphQL query size.
// This ensures ALL GraphQL queries have size limits for DoS protection.
func ValidateGraphQLFormat(data interface{}) error {
	if data == nil {
		return nil
	}

	if dataStr, ok := data.(string); ok {
		if len(dataStr) > MaxCustomCodeSourceSize {
			return NewStructuredError(
				avsproto.ErrorCode_INVALID_NODE_CONFIG,
				fmt.Sprintf("GraphQL query exceeds maximum size limit: %d bytes (max: %d bytes)", len(dataStr), MaxCustomCodeSourceSize),
				map[string]interface{}{
					"field":    "query",
					"issue":    "size limit exceeded",
					"size":     len(dataStr),
					"maxSize":  MaxCustomCodeSourceSize,
					"language": "GraphQL",
				},
			)
		}
	}

	return nil
}

// ValidateHandlebarsFormat validates Handlebars template size.
// This ensures ALL Handlebars templates have size limits for DoS protection.
func ValidateHandlebarsFormat(data interface{}) error {
	if data == nil {
		return nil
	}

	if dataStr, ok := data.(string); ok {
		if len(dataStr) > MaxCustomCodeSourceSize {
			return NewStructuredError(
				avsproto.ErrorCode_INVALID_NODE_CONFIG,
				fmt.Sprintf("Handlebars template exceeds maximum size limit: %d bytes (max: %d bytes)", len(dataStr), MaxCustomCodeSourceSize),
				map[string]interface{}{
					"field":    "template",
					"issue":    "size limit exceeded",
					"size":     len(dataStr),
					"maxSize":  MaxCustomCodeSourceSize,
					"language": "Handlebars",
				},
			)
		}
	}

	return nil
}
