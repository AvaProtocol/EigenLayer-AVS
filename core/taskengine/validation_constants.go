package taskengine

import (
	"encoding/json"
	"fmt"

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
//	lang := avsproto.Lang_JSON // or from config
//	if err := ValidateInputByLanguage(data, lang); err != nil {
//	    return nil, err
//	}
func ValidateInputByLanguage(data interface{}, lang avsproto.Lang) error {
	switch lang {
	case avsproto.Lang_JSON:
		return ValidateJSONFormat(data)
	case avsproto.Lang_JavaScript:
		// TODO: Implement JavaScript syntax validation
		// For now, no validation (JavaScript is validated at runtime)
		return nil
	case avsproto.Lang_GraphQL:
		// TODO: Implement GraphQL syntax validation
		// For now, no validation (GraphQL is validated at runtime)
		return nil
	case avsproto.Lang_Handlebars:
		// TODO: Implement Handlebars template validation
		// For now, no validation (Handlebars is validated at runtime)
		return nil
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
