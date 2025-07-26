package taskengine

import (
	"encoding/json"
	"fmt"

	"github.com/go-resty/resty/v2"
)

// ProcessRestAPIResponse processes a REST API response and returns the appropriate data format
// For HTTP 2xx status codes: returns the body content directly
// For non-2xx status codes: returns error format with {success: false, error: string, statusCode: int}
func ProcessRestAPIResponse(response *resty.Response) map[string]interface{} {
	statusCode := response.StatusCode()

	// Parse response body
	var bodyData interface{}
	responseBody := response.Body()
	if len(responseBody) > 0 {
		// Try to parse as JSON first
		var jsonData interface{}
		if err := json.Unmarshal(responseBody, &jsonData); err == nil {
			bodyData = jsonData
		} else {
			// Fallback to string if not valid JSON
			bodyData = string(responseBody)
		}
	} else {
		bodyData = ""
	}

	// For HTTP 2xx status codes, return the body directly
	if statusCode >= 200 && statusCode < 300 {
		// If body is a map, return it directly; otherwise wrap it
		if bodyMap, ok := bodyData.(map[string]interface{}); ok {
			return bodyMap
		} else {
			return map[string]interface{}{"data": bodyData}
		}
	}

	// For non-2xx status codes, return error format
	return map[string]interface{}{
		"success":      false,
		"error":        fmt.Sprintf("HTTP %d error", statusCode),
		"statusCode":   statusCode,
		"responseBody": bodyData,
	}
}

// ProcessRestAPIResponseRaw processes a raw REST API response map (from protobuf)
// and returns the appropriate data format using the same logic as ProcessRestAPIResponse
func ProcessRestAPIResponseRaw(responseData map[string]interface{}) map[string]interface{} {
	// Check if this has statusCode field
	statusCodeValue, hasStatus := responseData["statusCode"]
	if !hasStatus {
		// No status code, return as-is (shouldn't happen with proper REST responses)
		return responseData
	}

	var statusCode int
	switch sc := statusCodeValue.(type) {
	case int:
		statusCode = sc
	case float64:
		statusCode = int(sc)
	default:
		// Invalid status code type, return as-is
		return responseData
	}

	// Get data from response
	bodyData, hasData := responseData["data"]
	if !hasData {
		bodyData = ""
	}

	// For HTTP 2xx status codes, return the body directly
	if statusCode >= 200 && statusCode < 300 {
		// If body is a map, return it directly; otherwise wrap it
		if bodyMap, ok := bodyData.(map[string]interface{}); ok {
			return bodyMap
		} else {
			return map[string]interface{}{"data": bodyData}
		}
	}

	// For non-2xx status codes, return error format
	return map[string]interface{}{
		"success":      false,
		"error":        fmt.Sprintf("HTTP %d error", statusCode),
		"statusCode":   statusCode,
		"responseBody": bodyData,
	}
}
