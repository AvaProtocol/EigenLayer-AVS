package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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

	t.Run("buildManualTriggerOutput with only data field", func(t *testing.T) {
		// Test building manual trigger output with only data field (headers/pathParams are config-only)
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

		// Note: Headers and PathParams fields have been removed from ManualTrigger.Output
		// They are now config-only fields and not part of the execution step output
	})

	t.Run("buildManualTriggerOutput with only data field (no config)", func(t *testing.T) {
		// Test building manual trigger output with only data field
		triggerOutput := map[string]interface{}{
			"data": map[string]interface{}{
				"message": "test message",
			},
		}

		result := buildManualTriggerOutput(triggerOutput)

		// Verify data field is present
		assert.NotNil(t, result.Data)

		// Note: Headers and PathParams fields have been removed from ManualTrigger.Output
		// They are now config-only fields and not part of the execution step output
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
			"lang": avsproto.Lang_JSON, // REQUIRED: explicit language field
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

		// Headers and pathParams should be nested under input field
		input, inputExists := result["input"]
		assert.True(t, inputExists, "Input field should exist")
		assert.NotNil(t, input, "Input should not be nil")

		inputMap, ok := input.(map[string]interface{})
		assert.True(t, ok, "Input should be a map")

		headers, exists := inputMap["headers"]
		assert.True(t, exists, "Headers should be processed and nested under input")
		assert.NotNil(t, headers, "Headers should not be nil")
		pathParams, pathParamsExists := inputMap["pathParams"]
		assert.True(t, pathParamsExists, "PathParams should be processed and nested under input")
		assert.NotNil(t, pathParams, "PathParams should not be nil")

		// Note: These fields are used for configuration and internal processing
		// but will be filtered out when building the final output protobuf structure
	})

	t.Run("runManualTriggerImmediately with non-array webhook fields", func(t *testing.T) {
		triggerConfig := map[string]interface{}{
			"data": map[string]interface{}{
				"payload": "test payload",
			},
			"lang": avsproto.Lang_JSON, // REQUIRED: explicit language field
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

		// Verify non-array webhook fields are processed and nested under input
		input, inputExists := result["input"]
		assert.True(t, inputExists, "Input field should exist")
		inputMap, ok := input.(map[string]interface{})
		assert.True(t, ok, "Input should be a map")

		assert.Equal(t, map[string]interface{}{"Authorization": "Bearer token123"}, inputMap["headers"])
		assert.Equal(t, map[string]interface{}{"userId": "user123"}, inputMap["pathParams"])

		// Note: These fields are processed internally but will be filtered out in final output
	})
}
