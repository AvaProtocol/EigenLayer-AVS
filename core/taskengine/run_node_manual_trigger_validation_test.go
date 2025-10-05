package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualTrigger_JSONValidation(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	testCases := []struct {
		name          string
		data          interface{}
		expectError   bool
		expectedError avsproto.ErrorCode
		errorContains string
	}{
		{
			name:        "Valid JSON string",
			data:        `{"user": 123, "user2": 456}`,
			expectError: false,
		},
		{
			name:        "Valid JSON object (not string)",
			data:        map[string]interface{}{"user": 123, "user2": 456},
			expectError: false,
		},
		{
			name:          "Invalid JSON - missing closing brace",
			data:          `{"user": 123, "user2": 456`,
			expectError:   true,
			expectedError: avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			errorContains: "valid JSON",
		},
		{
			name:          "Invalid JSON - malformed syntax",
			data:          `{"user": 123 "user2": 456}`,
			expectError:   true,
			expectedError: avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			errorContains: "valid JSON",
		},
		{
			name:          "Invalid JSON - unclosed string",
			data:          `{"user": "unclosed string}`,
			expectError:   true,
			expectedError: avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			errorContains: "valid JSON",
		},
		{
			name:          "Empty data string",
			data:          "",
			expectError:   true,
			expectedError: avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			errorContains: "valid JSON",
		},
		{
			name:          "Null data",
			data:          nil,
			expectError:   true,
			expectedError: avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			errorContains: "data is required and cannot be null",
		},
		{
			name:        "Valid empty JSON object",
			data:        `{}`,
			expectError: false,
		},
		{
			name:        "Valid empty JSON array",
			data:        `[]`,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			triggerConfig := map[string]interface{}{
				"triggerType": "manual",
				"lang":        avsproto.Lang_JSON, // REQUIRED: explicit language field
			}

			// Only add data if it's not the "missing data" test case
			if tc.name != "Missing data" {
				triggerConfig["data"] = tc.data
			}

			result, err := engine.RunNodeImmediately("manualTrigger", triggerConfig, map[string]interface{}{}, nil)

			if tc.expectError {
				require.Error(t, err, "Expected error for test case: %s", tc.name)
				assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.errorContains),
					"Error should contain expected text. Got: %s", err.Error())
				assert.Nil(t, result, "Result should be nil when error occurs")

				// Check if it's a structured error with correct error code
				if structuredErr, ok := err.(*StructuredError); ok {
					assert.Equal(t, tc.expectedError, structuredErr.Code,
						"Expected error code %v, got %v", tc.expectedError, structuredErr.Code)
				}
			} else {
				require.NoError(t, err, "Expected no error for test case: %s", tc.name)
				require.NotNil(t, result, "Result should not be nil for successful cases")

				// Verify the result structure contains the expected data
				assert.Contains(t, result, "data", "Result should contain 'data' field")
				assert.Equal(t, tc.data, result["data"], "Result data should match input data")

				// Verify the input structure is created correctly
				if input, ok := result["input"].(map[string]interface{}); ok {
					assert.Contains(t, input, "data", "Input should contain 'data' field")
					assert.Contains(t, input, "headers", "Input should contain 'headers' field")
					assert.Contains(t, input, "pathParams", "Input should contain 'pathParams' field")
				} else {
					t.Error("Result should contain properly structured 'input' field")
				}
			}
		})
	}
}

func TestManualTrigger_MissingDataField(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	// Test case where 'data' field is completely missing from triggerConfig
	triggerConfig := map[string]interface{}{
		"triggerType": "manual",
		"lang":        avsproto.Lang_JSON, // REQUIRED: explicit language field
		// No 'data' field
	}

	result, err := engine.RunNodeImmediately("manualTrigger", triggerConfig, map[string]interface{}{}, nil)

	require.Error(t, err, "Expected error when data field is missing")
	assert.Contains(t, err.Error(), "data is required and cannot be null", "Error should mention missing data")
	assert.Nil(t, result, "Result should be nil when error occurs")

	// Check if it's a structured error with correct error code
	if structuredErr, ok := err.(*StructuredError); ok {
		assert.Equal(t, avsproto.ErrorCode_INVALID_TRIGGER_CONFIG, structuredErr.Code,
			"Expected USER_INPUT_ERROR, got %v", structuredErr.Code)
	}
}

// Test the exact scenario from the user's logs
func TestManualTrigger_UserScenario(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	// This is the exact malformed JSON from the user's logs
	malformedJSON := `{
  "user": 123,
  "user2": 456`

	triggerConfig := map[string]interface{}{
		"triggerType": "manual",
		"data":        malformedJSON,
		"lang":        avsproto.Lang_JSON, // REQUIRED: explicit language field
		"headers":     map[string]interface{}{},
		"pathParams":  map[string]interface{}{},
	}

	result, err := engine.RunNodeImmediately("manualTrigger", triggerConfig, map[string]interface{}{}, nil)

	// This should now fail with a proper user input error
	require.Error(t, err, "Expected error for malformed JSON")
	assert.Contains(t, err.Error(), "valid JSON", "Error should mention valid JSON requirement")
	assert.Nil(t, result, "Result should be nil when JSON is invalid")

	// Check if it's a structured error with correct error code
	if structuredErr, ok := err.(*StructuredError); ok {
		assert.Equal(t, avsproto.ErrorCode_INVALID_TRIGGER_CONFIG, structuredErr.Code,
			"Expected USER_INPUT_ERROR, got %v", structuredErr.Code)

		// Check the error details contain helpful information
		if details, ok := structuredErr.Details["data"]; ok {
			assert.Equal(t, malformedJSON, details, "Error details should include the malformed JSON")
		}
	}

	t.Logf("✅ Malformed JSON correctly rejected with error: %s", err.Error())
}
