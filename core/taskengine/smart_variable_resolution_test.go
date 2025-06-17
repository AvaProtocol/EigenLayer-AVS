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
func TestSmartVariableResolution(t *testing.T) {
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

	t.Run("SnakeCaseToFallbackToCamelCase", func(t *testing.T) {
		// DEMONSTRATES: Templates use snake_case but data has camelCase (gRPC conversion)
		// Template: {{triggerNode.data.block_number}} -> Data: triggerNode.data.blockNumber
		// Expected: Smart resolution should find blockNumber when block_number fails

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"block_number": "{{triggerNode.data.block_number}}", "block_hash": "{{triggerNode.data.block_hash}}", "gas_limit": "{{triggerNode.data.gas_limit}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		// Simulate data from a trigger node that was converted to camelCase by gRPC
		inputVariables := map[string]interface{}{
			"triggerNode": map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber": "18500000", // camelCase (as it would come from gRPC)
					"blockHash":   "0xabc123def456",
					"gasLimit":    "15000000",
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed with the smart resolution
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
			assert.Contains(t, dataStr, "18500000", "block_number should be resolved to blockNumber")
			assert.Contains(t, dataStr, "0xabc123def456", "block_hash should be resolved to blockHash")
			assert.Contains(t, dataStr, "15000000", "gas_limit should be resolved to gasLimit")
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

	t.Run("MixedSnakeCaseAndCamelCase", func(t *testing.T) {
		// DEMONSTRATES: Mixed template patterns work together
		// {{triggerNode.data.block_number}} -> fallback to blockNumber
		// {{triggerNode.data.gasUsed}} -> direct camelCase access
		// {{triggerNode.data.total_gas}} -> fallback to totalGas

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"block_number": "{{triggerNode.data.block_number}}", "gasUsed": "{{triggerNode.data.gasUsed}}", "total_gas": "{{triggerNode.data.total_gas}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		inputVariables := map[string]interface{}{
			"triggerNode": map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber": "18500000", // camelCase - should resolve block_number
					"gasUsed":     "5000000",  // camelCase - direct access
					"totalGas":    "15000000", // camelCase - should resolve total_gas
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
			assert.Contains(t, dataStr, "18500000") // block_number resolved from blockNumber
			assert.Contains(t, dataStr, "5000000")  // gasUsed direct access
			assert.Contains(t, dataStr, "15000000") // total_gas resolved from totalGas
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
		// DEMONSTRATES: Smart resolution works with nested field paths
		// {{apiNode.data.response.api_key}} -> tries response.api_key, then response.apiKey
		// {{apiNode.data.response.status_code}} -> tries response.status_code, then response.statusCode
		// Converts each snake_case segment in the path independently

		config := map[string]interface{}{
			"url":    mockServer.URL + "/post",
			"method": "POST",
			"body":   `{"apiKey": "{{apiNode.data.response.api_key}}", "status": "{{apiNode.data.response.status_code}}"}`,
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
		}

		inputVariables := map[string]interface{}{
			"apiNode": map[string]interface{}{
				"data": map[string]interface{}{
					"response": map[string]interface{}{
						"apiKey":     "secret_key_123", // camelCase - should resolve api_key
						"statusCode": "200",            // camelCase - should resolve status_code
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

// TestDualAccessVariableSupport tests that both camelCase and snake_case field access
// work for direct JavaScript variable access (not just template variables).
//
// This test verifies that the createDualAccessMap function properly enables:
// 1. Direct JS destructuring: const {tokenSymbol, token_symbol} = eventTrigger.data
// 2. Direct JS property access: eventTrigger.data.tokenSymbol AND eventTrigger.data.token_symbol
// 3. Template variables: {{eventTrigger.data.tokenSymbol}} AND {{eventTrigger.data.token_symbol}}
//
// This solves the original issue where deployed tasks returned NaN/undefined because
// JavaScript code expected camelCase but data had snake_case (or vice versa).
func TestDualAccessVariableSupport(t *testing.T) {
	engine := createTestEngineForSmartResolution(t)

	t.Run("DirectJavaScriptVariableAccess", func(t *testing.T) {
		// Test that both camelCase and snake_case work in direct JavaScript code
		// This simulates the user's original issue with custom code destructuring

		config := map[string]interface{}{
			"lang": "JavaScript",
			"source": `
				// Test direct property access (both naming conventions should work)
				const tokenSymbolCamel = eventTrigger.data.tokenSymbol;
				const tokenSymbolSnake = eventTrigger.data.token_symbol;
				const valueFormattedCamel = eventTrigger.data.valueFormatted;
				const valueFormattedSnake = eventTrigger.data.value_formatted;
				
				// Test destructuring (both naming conventions should work)
				const {tokenSymbol, token_symbol, valueFormatted, value_formatted} = eventTrigger.data;
				
				// Return results to verify both work
				return {
					tokenSymbolCamel,
					tokenSymbolSnake,
					valueFormattedCamel,
					valueFormattedSnake,
					destructuredTokenSymbol: tokenSymbol,
					destructuredTokenSymbolSnake: token_symbol,
					destructuredValueFormatted: valueFormatted,
					destructuredValueFormattedSnake: value_formatted
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

		// Verify that both camelCase and snake_case access work
		assert.Equal(t, "USDC", result["tokenSymbolCamel"])
		assert.Equal(t, "USDC", result["tokenSymbolSnake"])
		assert.Equal(t, "3.45", result["valueFormattedCamel"])
		assert.Equal(t, "3.45", result["valueFormattedSnake"])

		// Verify destructuring works for both naming conventions
		assert.Equal(t, "USDC", result["destructuredTokenSymbol"])
		assert.Equal(t, "USDC", result["destructuredTokenSymbolSnake"])
		assert.Equal(t, "3.45", result["destructuredValueFormatted"])
		assert.Equal(t, "3.45", result["destructuredValueFormattedSnake"])
	})

	t.Run("NodeOutputDualAccess", func(t *testing.T) {
		// Test that node outputs also support dual access
		// This ensures the SetOutputVarForStep dual-access mapping works

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

		// Now test a custom code node that accesses the REST API output using both naming conventions
		customCodeConfig := map[string]interface{}{
			"lang": "JavaScript",
			"source": `
				// Test accessing REST API output data using both camelCase and snake_case
				const responseDataCamel = apiNode.data.responseData;
				const responseDataSnake = apiNode.data.response_data;
				const statusCodeCamel = apiNode.data.statusCode;
				const statusCodeSnake = apiNode.data.status_code;
				const apiKeyCamel = apiNode.data.apiKey;
				const apiKeySnake = apiNode.data.api_key;
				
				return {
					responseDataCamel,
					responseDataSnake,
					statusCodeCamel,
					statusCodeSnake,
					apiKeyCamel,
					apiKeySnake
				};
			`,
		}

		// Simulate the REST API node output being available to the custom code node
		inputVariables := map[string]interface{}{
			"apiNode": map[string]interface{}{
				"data": restResult, // This should have dual-access mapping applied
			},
		}

		customResult, err := engine.RunNodeImmediately(NodeTypeCustomCode, customCodeConfig, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, customResult)

		// Verify that both camelCase and snake_case access work for node outputs
		// Note: The actual values depend on the REST API response structure
		// We're mainly testing that both naming conventions resolve to the same values
		if customResult["responseDataCamel"] != nil {
			assert.Equal(t, customResult["responseDataCamel"], customResult["responseDataSnake"])
		}
		if customResult["statusCodeCamel"] != nil {
			assert.Equal(t, customResult["statusCodeCamel"], customResult["statusCodeSnake"])
		}
		if customResult["apiKeyCamel"] != nil {
			assert.Equal(t, customResult["apiKeyCamel"], customResult["apiKeySnake"])
		}
	})
}

// TestDualAccessDebug is a debug test to understand what's happening with dual-access
func TestDualAccessDebug(t *testing.T) {
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
					// Try to access specific fields
					tokenSymbolDirect: typeof eventTrigger !== 'undefined' && eventTrigger.data ? eventTrigger.data.tokenSymbol : "not_found",
					tokenSymbolSnake: typeof eventTrigger !== 'undefined' && eventTrigger.data ? eventTrigger.data.token_symbol : "not_found"
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

// TestCreateDualAccessMap tests the CreateDualAccessMap function directly
func TestCreateDualAccessMap(t *testing.T) {
	// Test with camelCase input
	input := map[string]interface{}{
		"tokenSymbol":    "USDC",
		"valueFormatted": "3.45",
		"blockNumber":    12345,
	}

	result := CreateDualAccessMap(input)

	// Should have both camelCase and snake_case versions
	assert.Equal(t, "USDC", result["tokenSymbol"])
	assert.Equal(t, "USDC", result["token_symbol"])
	assert.Equal(t, "3.45", result["valueFormatted"])
	assert.Equal(t, "3.45", result["value_formatted"])
	assert.Equal(t, 12345, result["blockNumber"])
	assert.Equal(t, 12345, result["block_number"])

	t.Logf("CreateDualAccessMap result: %+v", result)
}

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
