package taskengine

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
