package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManualTriggerWebhookFields(t *testing.T) {
	t.Run("convertArrayOfObjectsToProtobufCompatible", func(t *testing.T) {
		// Test the conversion utility function
		input := []interface{}{
			map[string]interface{}{"headerKeyA": "headerValueA"},
			map[string]interface{}{"headerKeyB": "headerValueB"},
		}

		result := convertArrayOfObjectsToProtobufCompatible(input)

		assert.Equal(t, "headerValueA", result["headerKeyA"])
		assert.Equal(t, "headerValueB", result["headerKeyB"])
		assert.Len(t, result, 2)
	})

	t.Run("buildManualTriggerOutput with headers and pathParams", func(t *testing.T) {
		// Test building manual trigger output with webhook fields
		triggerOutput := map[string]interface{}{
			"data": map[string]interface{}{
				"message": "test message",
			},
			"headers": map[string]interface{}{
				"Authorization": "Bearer token123",
				"Content-Type":  "application/json",
			},
			"pathParams": map[string]interface{}{
				"userId": "user123",
				"orgId":  "org456",
			},
		}

		result := buildManualTriggerOutput(triggerOutput)

		// Verify data field
		assert.NotNil(t, result.Data)
		dataInterface := result.Data.AsInterface()
		dataMap, ok := dataInterface.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "test message", dataMap["message"])

		// Verify headers field
		assert.NotNil(t, result.Headers)
		headersInterface := result.Headers.AsInterface()
		headersMap, ok := headersInterface.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "Bearer token123", headersMap["Authorization"])
		assert.Equal(t, "application/json", headersMap["Content-Type"])

		// Verify pathParams field
		assert.NotNil(t, result.PathParams)
		pathParamsInterface := result.PathParams.AsInterface()
		pathParamsMap, ok := pathParamsInterface.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "user123", pathParamsMap["userId"])
		assert.Equal(t, "org456", pathParamsMap["orgId"])
	})

	t.Run("buildManualTriggerOutput with nil fields", func(t *testing.T) {
		// Test building manual trigger output with only data field
		triggerOutput := map[string]interface{}{
			"data": map[string]interface{}{
				"message": "test message",
			},
		}

		result := buildManualTriggerOutput(triggerOutput)

		// Verify data field is present
		assert.NotNil(t, result.Data)

		// Verify headers and pathParams are nil when not provided
		assert.Nil(t, result.Headers)
		assert.Nil(t, result.PathParams)
	})
}

func TestManualTriggerExecutionWithWebhookFields(t *testing.T) {
	// Create a mock engine
	engine := &Engine{
		logger: nil, // Use nil logger for testing
	}

	t.Run("runManualTriggerImmediately with headers and pathParams", func(t *testing.T) {
		triggerConfig := map[string]interface{}{
			"data": map[string]interface{}{
				"payload": "test payload",
			},
			"headers": []interface{}{
				map[string]interface{}{"Authorization": "Bearer token123"},
				map[string]interface{}{"Content-Type": "application/json"},
			},
			"pathParams": []interface{}{
				map[string]interface{}{"userId": "user123"},
				map[string]interface{}{"orgId": "org456"},
			},
		}

		inputVariables := map[string]interface{}{}

		result, err := engine.runManualTriggerImmediately(triggerConfig, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify data field
		assert.Equal(t, map[string]interface{}{"payload": "test payload"}, result["data"])

		// Verify headers field is converted correctly
		headers, exists := result["headers"]
		assert.True(t, exists)
		headersMap, ok := headers.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "Bearer token123", headersMap["Authorization"])
		assert.Equal(t, "application/json", headersMap["Content-Type"])

		// Verify pathParams field is converted correctly
		pathParams, exists := result["pathParams"]
		assert.True(t, exists)
		pathParamsMap, ok := pathParams.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, "user123", pathParamsMap["userId"])
		assert.Equal(t, "org456", pathParamsMap["orgId"])
	})

	t.Run("runManualTriggerImmediately with non-array webhook fields", func(t *testing.T) {
		triggerConfig := map[string]interface{}{
			"data": map[string]interface{}{
				"payload": "test payload",
			},
			"headers": map[string]interface{}{
				"Authorization": "Bearer token123",
			},
			"pathParams": map[string]interface{}{
				"userId": "user123",
			},
		}

		inputVariables := map[string]interface{}{}

		result, err := engine.runManualTriggerImmediately(triggerConfig, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify non-array webhook fields are passed through as-is
		assert.Equal(t, map[string]interface{}{"Authorization": "Bearer token123"}, result["headers"])
		assert.Equal(t, map[string]interface{}{"userId": "user123"}, result["pathParams"])
	})
}
