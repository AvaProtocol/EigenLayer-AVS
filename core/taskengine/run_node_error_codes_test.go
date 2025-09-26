package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

func createTestEngineForErrorTests() *Engine {
	logger := testutil.GetLogger()
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	return New(db, config, nil, logger)
}

// TestRunNodeImmediatelyErrorCodes tests that structured error codes are preserved
// when running nodes immediately through the RunNodeImmediately function
func TestRunNodeImmediatelyErrorCodes(t *testing.T) {
	engine := createTestEngineForErrorTests()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	testCases := []struct {
		name           string
		nodeType       string
		nodeConfig     map[string]interface{}
		inputVariables map[string]interface{}
		expectedCode   avsproto.ErrorCode
		expectedError  string
	}{
		{
			name:     "CustomCode with empty source",
			nodeType: "customCode",
			nodeConfig: map[string]interface{}{
				"lang":   "JavaScript",
				"source": "",
			},
			inputVariables: map[string]interface{}{
				"settings": map[string]interface{}{
					"runner": "0x1234567890123456789012345678901234567890",
				},
			},
			expectedCode:  avsproto.ErrorCode_MISSING_REQUIRED_FIELD,
			expectedError: "source is required",
		},
		{
			name:     "CustomCode with missing source field",
			nodeType: "customCode",
			nodeConfig: map[string]interface{}{
				"lang": "JavaScript",
				// source field is missing entirely
			},
			inputVariables: map[string]interface{}{
				"settings": map[string]interface{}{
					"runner": "0x1234567890123456789012345678901234567890",
				},
			},
			expectedCode:  avsproto.ErrorCode_MISSING_REQUIRED_FIELD,
			expectedError: "source is required",
		},
		{
			name:     "RestAPI with missing URL",
			nodeType: "restAPI",
			nodeConfig: map[string]interface{}{
				"method": "GET",
				// url field is missing
			},
			inputVariables: map[string]interface{}{
				"settings": map[string]interface{}{
					"runner": "0x1234567890123456789012345678901234567890",
				},
			},
			expectedCode:  avsproto.ErrorCode_MISSING_REQUIRED_FIELD,
			expectedError: "url is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Execute the node
			result, err := engine.RunNodeImmediately(tc.nodeType, tc.nodeConfig, tc.inputVariables)

			// Verify that an error occurred
			require.Error(t, err, "Expected error for %s", tc.name)
			require.Nil(t, result, "Result should be nil when error occurs")

			// Check if it's a structured error
			structErr, isStructured := IsStructuredError(err)
			require.True(t, isStructured, "Error should be a structured error for %s", tc.name)

			// Verify the error code
			require.Equal(t, tc.expectedCode, structErr.GetCode(),
				"Expected error code %v, got %v for %s", tc.expectedCode, structErr.GetCode(), tc.name)

			// Verify the error message
			require.Contains(t, structErr.Error(), tc.expectedError,
				"Expected error message to contain '%s', got '%s' for %s", tc.expectedError, structErr.Error(), tc.name)

			t.Logf("✅ %s: Error code %v preserved correctly", tc.name, tc.expectedCode)
		})
	}
}

// TestNonStructuredErrorsReturnUnspecified tests that non-structured errors
// return ERROR_CODE_UNSPECIFIED
func TestNonStructuredErrorsReturnUnspecified(t *testing.T) {
	engine := createTestEngineForErrorTests()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	// Test with a node type that doesn't exist to trigger a non-structured error
	nodeType := "nonExistentNodeType"
	nodeConfig := map[string]interface{}{
		"someField": "someValue",
	}
	inputVariables := map[string]interface{}{
		"settings": map[string]interface{}{
			"runner": "0x1234567890123456789012345678901234567890",
		},
	}

	// Execute the node
	result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables)

	// Verify that an error occurred
	require.Error(t, err, "Expected error for non-existent node type")
	require.Nil(t, result, "Result should be nil when error occurs")

	// Check if it's a structured error
	structErr, isStructured := IsStructuredError(err)
	if isStructured {
		// If it is structured, it should be UNSPECIFIED
		require.Equal(t, avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED, structErr.GetCode(),
			"Non-structured errors should return ERROR_CODE_UNSPECIFIED")
	} else {
		// If it's not structured, GetErrorCode should return UNSPECIFIED
		errorCode := GetErrorCode(err)
		require.Equal(t, avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED, errorCode,
			"Non-structured errors should return ERROR_CODE_UNSPECIFIED")
	}

	t.Logf("✅ Non-structured error correctly returns ERROR_CODE_UNSPECIFIED")
}

// TestGetErrorCodeForProtobuf tests the GetErrorCodeForProtobuf function
func TestGetErrorCodeForProtobuf(t *testing.T) {
	testCases := []struct {
		name         string
		err          error
		expectedCode avsproto.ErrorCode
	}{
		{
			name:         "Structured error with MISSING_REQUIRED_FIELD",
			err:          NewMissingRequiredFieldError("testField"),
			expectedCode: avsproto.ErrorCode_MISSING_REQUIRED_FIELD,
		},
		{
			name:         "Structured error with INVALID_ADDRESS",
			err:          NewInvalidAddressError("0xinvalid"),
			expectedCode: avsproto.ErrorCode_INVALID_ADDRESS,
		},
		{
			name:         "Structured error with INVALID_NODE_CONFIG",
			err:          NewInvalidNodeConfigError("test reason"),
			expectedCode: avsproto.ErrorCode_INVALID_NODE_CONFIG,
		},
		{
			name:         "Non-structured error",
			err:          fmt.Errorf("generic error"),
			expectedCode: avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED,
		},
		{
			name:         "Nil error",
			err:          nil,
			expectedCode: avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code := GetErrorCodeForProtobuf(tc.err)
			require.Equal(t, tc.expectedCode, code,
				"Expected error code %v, got %v for %s", tc.expectedCode, code, tc.name)
		})
	}
}
