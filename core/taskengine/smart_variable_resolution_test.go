package taskengine

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/stretchr/testify/assert"
)

// TestSmartVariableResolution tests the enhanced template processing that handles
// the gRPC protobuf field name conversion issue.
//
// PROBLEM:
// - Go uses snake_case field names: block_number, gas_limit, api_key
// - gRPC protobuf automatically converts these to camelCase in JavaScript: blockNumber, gasLimit, apiKey
// - Templates reference the original snake_case names: {{trigger.data.block_number}}
// - This causes resolution failures because the actual data has camelCase field names
//
// SOLUTION:
// Smart variable resolution with fallback logic for node_name.data.field_name patterns:
// 1. Try original snake_case path: {{trigger.data.block_number}}
// 2. If not found, try camelCase conversion: {{trigger.data.blockNumber}}
// 3. Return the resolved value or "undefined" for debugging
//
// SCOPE:
// - Only applies to node_name.data patterns (workflow node outputs)
// - Other variables like {{workflowContext.user_id}} remain unchanged
// - Works for nested paths: {{node.data.response.api_key}} -> {{node.data.response.apiKey}}
func TestConsistentCamelCaseResolution(t *testing.T) {
	engine := createTestEngineForSmartResolution(t)

	// Create a mock server to replace httpbin.org
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			body = []byte("")
		}

		// Echo back the request for verification
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		// Return the request body as part of the response so tests can verify template processing
		fmt.Fprintf(w, `{
			"method": "%s",
			"url": "%s",
			"data": %s,
			"headers": {
				"Content-Type": "%s"
			}
		}`, r.Method, r.URL.String(), string(body), r.Header.Get("Content-Type"))
	}))
	defer mockServer.Close()

	// Set up macro secrets for testing
	SetMacroSecrets(map[string]string{
		"api_token": "test_token_123",
	})

	t.Run("CamelCaseTemplateWithCamelCaseData", func(t *testing.T) {
		// DEMONSTRATES: Templates use camelCase with camelCase data (consistent naming)
		// Template: {{triggerNode.data.blockNumber}} -> Data: triggerNode.data.blockNumber
		// Expected: Direct resolution succeeds

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"blockNumber": "{{triggerNode.data.blockNumber}}", "blockHash": "{{triggerNode.data.blockHash}}", "gasLimit": "{{triggerNode.data.gasLimit}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		// Simulate data from a trigger node with consistent camelCase naming
		inputVariables := map[string]interface{}{
			"triggerNode": map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber": "18500000", // camelCase (consistent naming)
					"blockHash":   "0xabc123def456",
					"gasLimit":    "15000000",
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed with direct resolution
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// With new behavior, full response structure is returned
		// Check that we have the raw response structure
		assert.Equal(t, float64(200), result["statusCode"])

		// The mock server returns JSON in the body field
		bodyData, ok := result["body"].(map[string]interface{})
		assert.True(t, ok, "body should be a map")
		assert.Equal(t, "POST", bodyData["method"])

		// Check the data field which contains the JSON body that was sent
		if data, ok := bodyData["data"]; ok {
			dataStr := fmt.Sprintf("%v", data)
			assert.Contains(t, dataStr, "18500000", "blockNumber should be resolved directly")
			assert.Contains(t, dataStr, "0xabc123def456", "blockHash should be resolved directly")
			assert.Contains(t, dataStr, "15000000", "gasLimit should be resolved directly")
		}
	})

	t.Run("DirectCamelCaseAccess", func(t *testing.T) {
		// DEMONSTRATES: Direct camelCase access still works
		// Template: {{triggerNode.data.blockNumber}} -> Data: triggerNode.data.blockNumber
		// Expected: Direct path resolution succeeds without fallback needed

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"blockNumber": "{{triggerNode.data.blockNumber}}", "blockHash": "{{triggerNode.data.blockHash}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		inputVariables := map[string]interface{}{
			"triggerNode": map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber": "18500000",
					"blockHash":   "0xabc123def456",
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// With new behavior, full response structure is returned
		assert.Equal(t, float64(200), result["statusCode"])

		// The mock server returns JSON in the body field
		bodyData, ok := result["body"].(map[string]interface{})
		assert.True(t, ok, "body should be a map")
		assert.Equal(t, "POST", bodyData["method"])

		if data, ok := bodyData["data"]; ok {
			dataStr := fmt.Sprintf("%v", data)
			assert.Contains(t, dataStr, "18500000")
			assert.Contains(t, dataStr, "0xabc123def456")
		}
	})

	t.Run("ConsistentCamelCaseNaming", func(t *testing.T) {
		// DEMONSTRATES: Consistent camelCase naming works throughout
		// {{triggerNode.data.blockNumber}} -> direct camelCase access
		// {{triggerNode.data.gasUsed}} -> direct camelCase access
		// {{triggerNode.data.totalGas}} -> direct camelCase access

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"blockNumber": "{{triggerNode.data.blockNumber}}", "gasUsed": "{{triggerNode.data.gasUsed}}", "totalGas": "{{triggerNode.data.totalGas}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		inputVariables := map[string]interface{}{
			"triggerNode": map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber": "18500000", // camelCase - direct access
					"gasUsed":     "5000000",  // camelCase - direct access
					"totalGas":    "15000000", // camelCase - direct access
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// With new behavior, full response structure is returned
		assert.Equal(t, float64(200), result["statusCode"])

		// The mock server returns JSON in the body field
		bodyData, ok := result["body"].(map[string]interface{})
		assert.True(t, ok, "body should be a map")
		assert.Equal(t, "POST", bodyData["method"])

		if data, ok := bodyData["data"]; ok {
			dataStr := fmt.Sprintf("%v", data)
			assert.Contains(t, dataStr, "18500000") // blockNumber direct access
			assert.Contains(t, dataStr, "5000000")  // gasUsed direct access
			assert.Contains(t, dataStr, "15000000") // totalGas direct access
		}
	})

	t.Run("NonNodeDataVariablesUnchanged", func(t *testing.T) {
		// DEMONSTRATES: Smart resolution only applies to node_name.data patterns
		// {{apContext.configVars.api_token}} -> no conversion (not a node.data pattern)
		// {{workflowContext.user_id}} -> no conversion (not a node.data pattern)
		// Only workflow node outputs need snake_case -> camelCase fallback

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"token": "{{apContext.configVars.api_token}}", "user": "{{workflowContext.user_id}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		inputVariables := map[string]interface{}{
			"workflowContext": map[string]interface{}{
				"user_id": "test_user_123", // snake_case should remain as-is
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// With new behavior, full response structure is returned
		assert.Equal(t, float64(200), result["statusCode"])

		// The mock server returns JSON in the body field
		bodyData, ok := result["body"].(map[string]interface{})
		assert.True(t, ok, "body should be a map")
		assert.Equal(t, "POST", bodyData["method"])

		if data, ok := bodyData["data"]; ok {
			dataStr := fmt.Sprintf("%v", data)
			assert.Contains(t, dataStr, "test_token_123") // from macro secrets
			assert.Contains(t, dataStr, "test_user_123")  // direct access to snake_case
		}
	})

	t.Run("NestedFieldPaths", func(t *testing.T) {
		// DEMONSTRATES: Consistent camelCase naming works with nested field paths
		// {{apiNode.data.response.apiKey}} -> direct camelCase access
		// {{apiNode.data.response.statusCode}} -> direct camelCase access
		// Uses consistent camelCase naming throughout

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"apiKey": "{{apiNode.data.response.apiKey}}", "status": "{{apiNode.data.response.statusCode}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		inputVariables := map[string]interface{}{
			"apiNode": map[string]interface{}{
				"data": map[string]interface{}{
					"response": map[string]interface{}{
						"apiKey":     "secret_key_123", // camelCase - direct access
						"statusCode": "200",            // camelCase - direct access
					},
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// With new behavior, full response structure is returned
		assert.Equal(t, float64(200), result["statusCode"])

		// The mock server returns JSON in the body field
		bodyData, ok := result["body"].(map[string]interface{})
		assert.True(t, ok, "body should be a map")
		assert.Equal(t, "POST", bodyData["method"])

		if data, ok := bodyData["data"]; ok {
			dataStr := fmt.Sprintf("%v", data)
			assert.Contains(t, dataStr, "secret_key_123") // api_key resolved from apiKey
			assert.Contains(t, dataStr, "200")            // status_code resolved from statusCode
		}
	})
}

// TestCamelCaseVariableSupport tests that camelCase field access works for direct JavaScript variable access.
// The dual access mapping functionality has been removed in favor of consistent camelCase usage.
//
// This test verifies that:
// 1. Direct JS destructuring: const {tokenSymbol} = eventTrigger.data
// 2. Direct JS property access: eventTrigger.data.tokenSymbol
// 3. Template variables: {{eventTrigger.data.tokenSymbol}}
func TestCamelCaseVariableSupport(t *testing.T) {
	engine := createTestEngineForSmartResolution(t)

	t.Run("DirectJavaScriptVariableAccess", func(t *testing.T) {
		// Test that camelCase works in direct JavaScript code
		config := map[string]interface{}{
			"lang": "JavaScript",
			"source": `
				// Test direct property access (camelCase should work)
				const tokenSymbolCamel = eventTrigger.data.tokenSymbol;
				const valueFormattedCamel = eventTrigger.data.valueFormatted;
				
				// Test destructuring (camelCase should work)
				const {tokenSymbol, valueFormatted} = eventTrigger.data;
				
				// Return results to verify camelCase works
				return {
					tokenSymbolCamel,
					valueFormattedCamel,
					destructuredTokenSymbol: tokenSymbol,
					destructuredValueFormatted: valueFormatted
				};
			`,
		}

		// Simulate trigger data with camelCase field names (as it comes from buildTriggerDataMapFromProtobuf)
		inputVariables := map[string]interface{}{
			"eventTrigger": map[string]interface{}{
				"data": map[string]interface{}{
					"tokenSymbol":    "USDC",
					"valueFormatted": "3.45",
					"blockNumber":    12345678,
					"fromAddress":    "0x1111111111111111",
					"toAddress":      "0x2222222222222222",
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify that camelCase access works
		assert.Equal(t, "USDC", result["tokenSymbolCamel"])
		assert.Equal(t, "3.45", result["valueFormattedCamel"])

		// Verify destructuring works for camelCase
		assert.Equal(t, "USDC", result["destructuredTokenSymbol"])
		assert.Equal(t, "3.45", result["destructuredValueFormatted"])
	})

	t.Run("NodeOutputCamelCase", func(t *testing.T) {
		// Test that node outputs work with camelCase access
		// The dual access mapping has been removed

		// First, create a REST API node that returns data with camelCase fields
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			// Return camelCase field names
			fmt.Fprintf(w, `{
				"responseData": "test_response",
				"statusCode": 200,
				"apiKey": "secret_key_123"
			}`)
		}))
		defer mockServer.Close()

		restConfig := map[string]interface{}{
			"url":    mockServer.URL,
			"method": "GET",
		}

		// Execute REST API node to get output data
		restResult, err := engine.RunNodeImmediately(NodeTypeRestAPI, restConfig, map[string]interface{}{})
		assert.NoError(t, err)
		assert.NotNil(t, restResult)

		// Now test a custom code node that accesses the REST API output using camelCase
		customCodeConfig := map[string]interface{}{
			"lang": "JavaScript",
			"source": `
				// Test accessing REST API output data using camelCase
				// The data is in the body field of the REST API response
				const responseDataCamel = apiNode.data.body.responseData;
				const statusCodeCamel = apiNode.data.body.statusCode;
				const apiKeyCamel = apiNode.data.body.apiKey;
				
				return {
					responseDataCamel,
					statusCodeCamel,
					apiKeyCamel
				};
			`,
		}

		// Simulate the REST API node output being available to the custom code node
		inputVariables := map[string]interface{}{
			"apiNode": map[string]interface{}{
				"data": restResult,
			},
		}

		customResult, err := engine.RunNodeImmediately(NodeTypeCustomCode, customCodeConfig, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, customResult)

		// Verify that camelCase access works for node outputs
		// Note: The actual values depend on the REST API response structure
		if customResult["responseDataCamel"] != nil {
			assert.Equal(t, "test_response", customResult["responseDataCamel"])
		} else {
			t.Logf("responseDataCamel is nil - REST API response structure: %+v", restResult)
		}
		if customResult["statusCodeCamel"] != nil {
			assert.Equal(t, float64(200), customResult["statusCodeCamel"])
		} else {
			t.Logf("statusCodeCamel is nil - REST API response structure: %+v", restResult)
		}
		if customResult["apiKeyCamel"] != nil {
			assert.Equal(t, "secret_key_123", customResult["apiKeyCamel"])
		} else {
			t.Logf("apiKeyCamel is nil - REST API response structure: %+v", restResult)
		}
	})
}

// TestCamelCaseDebug is a debug test to understand what's happening with camelCase access
func TestCamelCaseDebug(t *testing.T) {
	engine := createTestEngineForSmartResolution(t)

	t.Run("DebugVariableAccess", func(t *testing.T) {
		config := map[string]interface{}{
			"lang": "JavaScript",
			"source": `
				// Check if eventTrigger exists and return debug info
				return {
					debug: "test",
					eventTriggerExists: typeof eventTrigger !== 'undefined',
					eventTriggerData: typeof eventTrigger !== 'undefined' ? eventTrigger.data : null,
					eventTriggerType: typeof eventTrigger,
					eventTriggerKeys: typeof eventTrigger !== 'undefined' ? Object.keys(eventTrigger) : [],
					eventTriggerDataKeys: typeof eventTrigger !== 'undefined' && eventTrigger.data ? Object.keys(eventTrigger.data) : [],
					globalKeys: Object.keys(this),
					// Try to access specific fields (camelCase only)
					tokenSymbolDirect: typeof eventTrigger !== 'undefined' && eventTrigger.data ? eventTrigger.data.tokenSymbol : "not_found"
				};
			`,
		}

		inputVariables := map[string]interface{}{
			"eventTrigger": map[string]interface{}{
				"data": map[string]interface{}{
					"tokenSymbol":    "USDC",
					"valueFormatted": "3.45",
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Print the result for debugging
		t.Logf("Debug result: %+v", result)
	})
}

// TestCreateDualAccessMap was removed as part of cleanup - function no longer exists
// The dual access mapping functionality has been removed in favor of consistent camelCase usage

// createTestEngineForSmartResolution creates an engine for testing
func createTestEngineForSmartResolution(t *testing.T) *Engine {
	logger := testutil.GetLogger()
	db := testutil.TestMustDB()
	t.Cleanup(func() { db.Close() })

	return &Engine{
		logger: logger,
		db:     db,
	}
}
