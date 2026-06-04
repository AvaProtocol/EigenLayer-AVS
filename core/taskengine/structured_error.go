package taskengine

import (
	"fmt"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// StructuredError provides consistent error handling with error codes
type StructuredError struct {
	Code    avsproto.ErrorCode
	Message string
	Details map[string]interface{}
}

// Error implements the error interface
func (e *StructuredError) Error() string {
	return e.Message
}

// GetCode returns the error code
func (e *StructuredError) GetCode() avsproto.ErrorCode {
	return e.Code
}

// GetDetails returns additional error details
func (e *StructuredError) GetDetails() map[string]interface{} {
	return e.Details
}

// NewStructuredError creates a new structured error
func NewStructuredError(code avsproto.ErrorCode, message string, details ...map[string]interface{}) *StructuredError {
	var detailsMap map[string]interface{}
	if len(details) > 0 {
		detailsMap = details[0]
	}

	return &StructuredError{
		Code:    code,
		Message: message,
		Details: detailsMap,
	}
}

// Common error constructors for validation errors

// NewMissingRequiredFieldError creates an error for missing required fields
func NewMissingRequiredFieldError(fieldName string) *StructuredError {
	return NewStructuredError(
		avsproto.ErrorCode_MISSING_REQUIRED_FIELD,
		fmt.Sprintf("%s is required", fieldName),
		map[string]interface{}{"field": fieldName},
	)
}

// NewInvalidNodeConfigError creates an error for invalid node configuration
func NewInvalidNodeConfigError(reason string) *StructuredError {
	return NewStructuredError(
		avsproto.ErrorCode_INVALID_NODE_CONFIG,
		fmt.Sprintf("invalid node configuration: %s", reason),
		map[string]interface{}{"reason": reason},
	)
}

// NewInvalidAddressError creates an error for invalid blockchain addresses
func NewInvalidAddressError(address string) *StructuredError {
	return NewStructuredError(
		avsproto.ErrorCode_INVALID_ADDRESS,
		fmt.Sprintf("invalid address: %s", address),
		map[string]interface{}{"address": address},
	)
}

// NewInvalidRequestError creates an error for general request validation failures
func NewInvalidRequestError(reason string) *StructuredError {
	return NewStructuredError(
		avsproto.ErrorCode_INVALID_REQUEST,
		fmt.Sprintf("invalid request: %s", reason),
		map[string]interface{}{"reason": reason},
	)
}

// NewMethodNotFoundError marks the "method name not present in the contract
// ABI" failure as a user-input error rather than a server fault. Used by
// GenerateCallData for ContractWrite / ContractRead nodes. Log sites that
// detect this via IsStructuredError should use Warn (not Error) so that
// SentryLogger doesn't capture every misnamed method as a Sentry alert
// (Sentry EIGENLAYER-AVS-1K: 12 events with methodName="nonexistent" that
// were almost certainly a CI test loop misusing a placeholder string).
func NewMethodNotFoundError(methodName string) *StructuredError {
	return NewStructuredError(
		avsproto.ErrorCode_INVALID_NODE_CONFIG,
		fmt.Sprintf("method '%s' not found in contract ABI", methodName),
		map[string]interface{}{"method_name": methodName},
	)
}

// NewMethodParamCountError marks a parameter-count mismatch as a user-input
// error. Same Sentry-noise rationale as NewMethodNotFoundError.
func NewMethodParamCountError(methodName string, expected, got int) *StructuredError {
	return NewStructuredError(
		avsproto.ErrorCode_INVALID_NODE_CONFIG,
		fmt.Sprintf("method '%s' expects %d parameters, got %d", methodName, expected, got),
		map[string]interface{}{"method_name": methodName, "expected": expected, "got": got},
	)
}

// IsStructuredError checks if an error is a structured error and returns it
func IsStructuredError(err error) (*StructuredError, bool) {
	if structErr, ok := err.(*StructuredError); ok {
		return structErr, true
	}
	return nil, false
}

// GetErrorCode extracts error code from an error, returns UNSPECIFIED if not a structured error
func GetErrorCode(err error) avsproto.ErrorCode {
	if structErr, ok := IsStructuredError(err); ok {
		return structErr.GetCode()
	}
	return avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED
}

// GetErrorCodeForProtobuf extracts error code for protobuf responses
func GetErrorCodeForProtobuf(err error) avsproto.ErrorCode {
	return GetErrorCode(err)
}
