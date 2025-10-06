package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualTrigger_SizeLimit(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	t.Run("Valid size - under limit", func(t *testing.T) {
		// Create a valid JSON payload that's under the limit
		validData := `{"user": 123, "data": "some reasonable payload"}`

		triggerConfig := map[string]interface{}{
			"triggerType": "manual",
			"data":        validData,
			"lang":        avsproto.Lang_LANG_JSON, // REQUIRED: explicit language field
		}

		result, err := engine.RunNodeImmediately("manualTrigger", triggerConfig, map[string]interface{}{}, nil)

		require.NoError(t, err, "Should succeed with valid size")
		require.NotNil(t, result)
		assert.Equal(t, validData, result["data"])
	})

	t.Run("Invalid size - exceeds limit", func(t *testing.T) {
		// Create a payload that exceeds MaxManualTriggerDataSize (1MB)
		largeData := `{"data": "` + strings.Repeat("x", MaxManualTriggerDataSize) + `"}`

		triggerConfig := map[string]interface{}{
			"triggerType": "manual",
			"data":        largeData,
			"lang":        avsproto.Lang_LANG_JSON, // REQUIRED: explicit language field
		}

		result, err := engine.RunNodeImmediately("manualTrigger", triggerConfig, map[string]interface{}{}, nil)

		require.Error(t, err, "Should fail with oversized data")
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "exceeds maximum size limit")
		assert.Contains(t, err.Error(), "ManualTrigger data")

		// Check if it's a structured error with correct error code
		if structuredErr, ok := err.(*StructuredError); ok {
			assert.Equal(t, avsproto.ErrorCode_INVALID_TRIGGER_CONFIG, structuredErr.Code)
			assert.Equal(t, "size limit exceeded", structuredErr.Details["issue"])
			assert.Equal(t, MaxManualTriggerDataSize, structuredErr.Details["maxSize"])
		}
	})

	t.Run("Edge case - exactly at limit", func(t *testing.T) {
		// Create a payload that's exactly at the limit
		dataAtLimit := strings.Repeat("x", MaxManualTriggerDataSize)

		triggerConfig := map[string]interface{}{
			"triggerType": "manual",
			"data":        dataAtLimit,
			"lang":        avsproto.Lang_LANG_JSON, // REQUIRED: explicit language field
		}

		_, err := engine.RunNodeImmediately("manualTrigger", triggerConfig, map[string]interface{}{}, nil)

		// At the limit should fail JSON parsing but not size check
		require.Error(t, err, "Should fail JSON validation")
		assert.Contains(t, err.Error(), "valid JSON") // JSON error, not size error
	})
}

func TestRestAPI_SizeLimit(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	t.Run("Valid size - under limit", func(t *testing.T) {
		nodeConfig := map[string]interface{}{
			"url":    "https://mock-api.ap-aggregator.local/test",
			"method": "POST",
			"body":   `{"data": "valid payload"}`,
			"headers": map[string]interface{}{
				"Content-Type": "application/json",
			},
		}

		result, err := engine.RunNodeImmediately("restAPI", nodeConfig, map[string]interface{}{}, nil)

		require.NoError(t, err, "Should succeed with valid size")
		require.NotNil(t, result)
	})

	t.Run("Invalid size - exceeds limit", func(t *testing.T) {
		// Create a body that exceeds MaxRestAPIBodySize (10MB)
		largeBody := `{"data": "` + strings.Repeat("x", MaxRestAPIBodySize) + `"}`

		nodeConfig := map[string]interface{}{
			"url":    "https://mock-api.ap-aggregator.local/test",
			"method": "POST",
			"body":   largeBody,
		}

		result, err := engine.RunNodeImmediately("restAPI", nodeConfig, map[string]interface{}{}, nil)

		require.Error(t, err, "Should fail with oversized body")
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "exceeds maximum size limit")
		assert.Contains(t, err.Error(), "REST API request body")

		// Check if it's a structured error with correct error code
		if structuredErr, ok := err.(*StructuredError); ok {
			assert.Equal(t, avsproto.ErrorCode_INVALID_NODE_CONFIG, structuredErr.Code)
			assert.Equal(t, "size limit exceeded", structuredErr.Details["issue"])
			assert.Equal(t, MaxRestAPIBodySize, structuredErr.Details["maxSize"])
		}
	})
}

func TestRestAPI_JSONValidation(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	t.Run("Valid JSON with Content-Type application/json", func(t *testing.T) {
		nodeConfig := map[string]interface{}{
			"url":    "https://mock-api.ap-aggregator.local/test",
			"method": "POST",
			"body":   `{"user": 123, "data": "valid"}`,
			"headers": map[string]interface{}{
				"Content-Type": "application/json",
			},
		}

		result, err := engine.RunNodeImmediately("restAPI", nodeConfig, map[string]interface{}{}, nil)

		require.NoError(t, err, "Should succeed with valid JSON")
		require.NotNil(t, result)
	})

	t.Run("Invalid JSON with Content-Type application/json", func(t *testing.T) {
		nodeConfig := map[string]interface{}{
			"url":    "https://mock-api.ap-aggregator.local/test",
			"method": "POST",
			"body":   `{"user": 123, "data": "unclosed`,
			"headers": map[string]interface{}{
				"Content-Type": "application/json",
			},
		}

		result, err := engine.RunNodeImmediately("restAPI", nodeConfig, map[string]interface{}{}, nil)

		require.Error(t, err, "Should fail with invalid JSON")
		assert.Nil(t, result)
		// Check for JSON-related error message
		hasInvalidJSON := assert.Contains(t, err.Error(), "invalid JSON")
		if !hasInvalidJSON {
			assert.Contains(t, err.Error(), "JSON")
		}

		// Check if it's a structured error with correct error code
		if structuredErr, ok := err.(*StructuredError); ok {
			assert.Equal(t, avsproto.ErrorCode_INVALID_NODE_CONFIG, structuredErr.Code)
			assert.Equal(t, "invalid JSON format", structuredErr.Details["issue"])
		}
	})

	t.Run("Invalid JSON is ignored when Content-Type is not JSON", func(t *testing.T) {
		nodeConfig := map[string]interface{}{
			"url":    "https://mock-api.ap-aggregator.local/test",
			"method": "POST",
			"body":   `not json at all`,
			"headers": map[string]interface{}{
				"Content-Type": "text/plain",
			},
		}

		result, err := engine.RunNodeImmediately("restAPI", nodeConfig, map[string]interface{}{}, nil)

		// Should succeed because we don't validate JSON for non-JSON content types
		require.NoError(t, err, "Should succeed with non-JSON content type")
		require.NotNil(t, result)
	})
}

func TestCustomCode_SizeLimit(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	t.Run("Valid size - under limit", func(t *testing.T) {
		nodeConfig := map[string]interface{}{
			"lang":   0, // JavaScript
			"source": "return { result: 'ok' };",
		}

		result, err := engine.RunNodeImmediately("customCode", nodeConfig, map[string]interface{}{}, nil)

		require.NoError(t, err, "Should succeed with valid size")
		require.NotNil(t, result)
	})

	t.Run("Invalid size - exceeds limit", func(t *testing.T) {
		// Create source that exceeds MaxCustomCodeSourceSize (100KB)
		largeSource := "return { data: '" + strings.Repeat("x", MaxCustomCodeSourceSize) + "' };"

		nodeConfig := map[string]interface{}{
			"lang":   0,
			"source": largeSource,
		}

		result, err := engine.RunNodeImmediately("customCode", nodeConfig, map[string]interface{}{}, nil)

		require.Error(t, err, "Should fail with oversized source")
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "exceeds maximum size limit")
		assert.Contains(t, err.Error(), "Custom code source")

		// Check if it's a structured error with correct error code
		if structuredErr, ok := err.(*StructuredError); ok {
			assert.Equal(t, avsproto.ErrorCode_INVALID_NODE_CONFIG, structuredErr.Code)
			assert.Equal(t, "size limit exceeded", structuredErr.Details["issue"])
			assert.Equal(t, MaxCustomCodeSourceSize, structuredErr.Details["maxSize"])
		}
	})
}

func TestValidationConstants(t *testing.T) {
	t.Run("Size limits are reasonable", func(t *testing.T) {
		// Verify the constants are set to reasonable values
		assert.Equal(t, 1024*1024, MaxManualTriggerDataSize, "ManualTrigger limit should be 1MB")
		assert.Equal(t, 10*1024*1024, MaxRestAPIBodySize, "REST API limit should be 10MB")
		assert.Equal(t, 100*1024, MaxCustomCodeSourceSize, "CustomCode limit should be 100KB")
		assert.Equal(t, 1024*1024, MaxContractABISize, "Contract ABI limit should be 1MB")
		assert.Equal(t, 100*1024, MaxEventTriggerABIItemSize, "ABI item limit should be 100KB")
	})

	t.Run("Error messages are defined", func(t *testing.T) {
		// Verify all error messages are non-empty
		assert.NotEmpty(t, ValidationErrorMessages.ManualTriggerDataTooLarge)
		assert.NotEmpty(t, ValidationErrorMessages.RestAPIBodyTooLarge)
		assert.NotEmpty(t, ValidationErrorMessages.CustomCodeSourceTooLarge)
		assert.NotEmpty(t, ValidationErrorMessages.ContractABITooLarge)
		assert.NotEmpty(t, ValidationErrorMessages.EventTriggerABIItemTooLarge)
		assert.NotEmpty(t, ValidationErrorMessages.RestAPIBodyInvalidJSON)
	})
}
