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

		// With new behavior, successful (2xx) responses return the body directly
		// The mock server returns JSON like: {"method": "POST", "url": "...", "data": "{...}", "headers": {...}}
		assert.Equal(t, "POST", result["method"])

		// Check the data field which contains the JSON body that was sent
		if data, ok := result["data"]; ok {
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

		// With new behavior, successful (2xx) responses return the body directly
		assert.Equal(t, "POST", result["method"])

		if data, ok := result["data"]; ok {
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

		// With new behavior, successful (2xx) responses return the body directly
		assert.Equal(t, "POST", result["method"])

		if data, ok := result["data"]; ok {
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

		// With new behavior, successful (2xx) responses return the body directly
		assert.Equal(t, "POST", result["method"])

		if data, ok := result["data"]; ok {
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

		// With new behavior, successful (2xx) responses return the body directly
		assert.Equal(t, "POST", result["method"])

		if data, ok := result["data"]; ok {
			dataStr := fmt.Sprintf("%v", data)
			assert.Contains(t, dataStr, "secret_key_123") // api_key resolved from apiKey
			assert.Contains(t, dataStr, "200")            // status_code resolved from statusCode
		}
	})
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
