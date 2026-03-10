package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventTriggerOraclePriceConditions(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	t.Log("=== Testing Oracle Price EventTrigger with Conditions ===")

	// Oracle contract ABI for AnswerUpdated event (now returns event format instead of method calls)
	oracleABI := []interface{}{
		map[string]interface{}{
			"inputs":          []interface{}{},
			"name":            "decimals",
			"outputs":         []interface{}{map[string]interface{}{"internalType": "uint8", "name": "", "type": "uint8"}},
			"stateMutability": "view",
			"type":            "function",
		},
		map[string]interface{}{
			"inputs": []interface{}{},
			"name":   "latestRoundData",
			"outputs": []interface{}{
				map[string]interface{}{"internalType": "uint80", "name": "roundId", "type": "uint80"},
				map[string]interface{}{"internalType": "int256", "name": "answer", "type": "int256"},
				map[string]interface{}{"internalType": "uint256", "name": "startedAt", "type": "uint256"},
				map[string]interface{}{"internalType": "uint256", "name": "updatedAt", "type": "uint256"},
				map[string]interface{}{"internalType": "uint80", "name": "answeredInRound", "type": "uint80"},
			},
			"stateMutability": "view",
			"type":            "function",
		},
		// AnswerUpdated event definition - this is what we now return
		map[string]interface{}{
			"anonymous": false,
			"inputs": []interface{}{
				map[string]interface{}{"indexed": true, "internalType": "int256", "name": "current", "type": "int256"},
				map[string]interface{}{"indexed": true, "internalType": "uint256", "name": "roundId", "type": "uint256"},
				map[string]interface{}{"indexed": false, "internalType": "uint256", "name": "updatedAt", "type": "uint256"},
			},
			"name": "AnswerUpdated",
			"type": "event",
		},
	}

	// Test oracle contract address (Chainlink ETH/USD on Sepolia)
	oracleAddress := "0x694AA1769357215DE4FAC081bf1f309aDC325306"

	testCases := []struct {
		name               string
		methodCalls        []map[string]interface{}
		conditions         []map[string]interface{}
		expectedSuccess    bool
		expectedDataFields []string
		description        string
	}{
		{
			name: "Oracle AnswerUpdated event - no conditions (always success)",
			methodCalls: []map[string]interface{}{
				{
					"methodName":   "decimals",
					"methodParams": []string{},
				},
			},
			conditions:         []map[string]interface{}{},
			expectedSuccess:    true,
			expectedDataFields: []string{"current", "roundId", "updatedAt"}, // AnswerUpdated event fields (eventName is now the key)
			description:        "Should always succeed when no conditions are specified",
		},
		{
			name: "Oracle AnswerUpdated event - conditions met (price < 5000)",
			methodCalls: []map[string]interface{}{
				{
					"methodName":   "decimals",
					"methodParams": []string{},
				},
				{
					"methodName":   "latestRoundData",
					"methodParams": []string{},
				},
			},
			conditions: []map[string]interface{}{
				{
					"fieldName": "current", // AnswerUpdated event field (current price)
					"operator":  "lt",
					"value":     "500000000000", // 5000 * 10^8 (assuming 8 decimals)
					"fieldType": "int256",
				},
			},
			expectedSuccess:    true,                                        // Assuming current ETH price < $5000
			expectedDataFields: []string{"current", "roundId", "updatedAt"}, // AnswerUpdated event fields (eventName is now the key)
			description:        "Should succeed when price is below threshold",
		},
		{
			name: "Oracle AnswerUpdated event - conditions NOT met (price < 1000)",
			methodCalls: []map[string]interface{}{
				{
					"methodName":   "decimals",
					"methodParams": []string{},
				},
				{
					"methodName":   "latestRoundData",
					"methodParams": []string{},
				},
			},
			conditions: []map[string]interface{}{
				{
					"fieldName": "current", // AnswerUpdated event field (current price)
					"operator":  "lt",
					"value":     "1000", // Much smaller value to ensure condition fails
					"fieldType": "int256",
				},
			},
			expectedSuccess:    false,                                       // Assuming current ETH price > $1000
			expectedDataFields: []string{"current", "roundId", "updatedAt"}, // AnswerUpdated event fields (eventName is now the key)
			description:        "Should fail when price is above threshold, but data should be in error",
		},
		{
			name: "Oracle AnswerUpdated event - multiple conditions",
			methodCalls: []map[string]interface{}{
				{
					"methodName":   "decimals",
					"methodParams": []string{},
				},
				{
					"methodName":   "latestRoundData",
					"methodParams": []string{},
				},
			},
			conditions: []map[string]interface{}{
				{
					"fieldName": "current", // AnswerUpdated event field (current price)
					"operator":  "gt",
					"value":     "100000000000", // $1000 * 10^8 (with 8 decimals)
					"fieldType": "int256",
				},
				{
					"fieldName": "roundId", // AnswerUpdated event field
					"operator":  "gt",
					"value":     "0",
					"fieldType": "uint256",
				},
			},
			expectedSuccess:    true,                                        // Assuming price > $1000 and roundId > 0
			expectedDataFields: []string{"current", "roundId", "updatedAt"}, // AnswerUpdated event fields (eventName is now the key)
			description:        "Should succeed when both conditions are met",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			// Build query configuration
			query := map[string]interface{}{
				"addresses":   []interface{}{oracleAddress},
				"contractAbi": oracleABI,
				"methodCalls": tc.methodCalls,
				"conditions":  tc.conditions,
			}

			// Remove topics to force direct method calls path (no event simulation)

			triggerConfig := map[string]interface{}{
				"queries": []interface{}{query},
			}

			// Execute the trigger
			result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, map[string]interface{}{})
			require.NoError(t, err, "runTriggerImmediately should not return an error")

			// Debug: Print actual response for analysis
			t.Logf("📋 ACTUAL RESPONSE: %+v", result)

			// Verify the response structure
			require.Contains(t, result, "success", "Response should have success field")
			require.Contains(t, result, "executionContext", "Response should have executionContext field")

			actualSuccess, ok := result["success"].(bool)
			require.True(t, ok, "success field should be boolean")

			if tc.expectedSuccess {
				// SUCCESS CASE: Conditions met
				assert.True(t, actualSuccess, "Expected success=true when conditions are met")
				assert.Contains(t, result, "data", "Success response should have data field")
				assert.Contains(t, result, "error", "Response should have error field")
				assert.Equal(t, "", result["error"], "Error should be empty on success")

				// Verify data contains expected fields in structured format
				data, ok := result["data"].(map[string]interface{})
				require.True(t, ok, "data should be a map")

				// Check for AnswerUpdated event structure
				answerUpdatedData, exists := data["AnswerUpdated"].(map[string]interface{})
				require.True(t, exists, "data should contain AnswerUpdated event")

				// Verify expected fields exist in the AnswerUpdated structure
				expectedEventFields := []string{"current", "roundId", "updatedAt"} // Remove eventName as it's now the key
				for _, expectedField := range expectedEventFields {
					assert.Contains(t, answerUpdatedData, expectedField, "AnswerUpdated should contain %s field", expectedField)
				}

				// Verify executionContext
				execCtx, ok := result["executionContext"].(map[string]interface{})
				require.True(t, ok, "executionContext should be a map")
				assert.Contains(t, execCtx, "chainId", "executionContext should have chainId")
				assert.Contains(t, execCtx, "isSimulated", "executionContext should have isSimulated")
				assert.Equal(t, true, execCtx["isSimulated"], "Simulated event calls should have isSimulated=true")

			} else {
				// FAILURE CASE: Conditions not met
				assert.False(t, actualSuccess, "Expected success=false when conditions are not met")
				assert.Contains(t, result, "error", "Failure response should have error field")
				assert.NotEmpty(t, result["error"], "Error should not be empty on failure")

				// Verify error contains data as JSON
				errorMsg, ok := result["error"].(string)
				require.True(t, ok, "error should be a string")
				assert.Contains(t, errorMsg, "Conditions not met", "Error should mention conditions not met")

				// Note: After security fix, error messages no longer contain full data/conditions
				// to prevent sensitive information leakage. Only failed reason descriptions are included.

				// Verify executionContext is still present
				execCtx, ok := result["executionContext"].(map[string]interface{})
				require.True(t, ok, "executionContext should be present even on failure")
				assert.Equal(t, true, execCtx["isSimulated"], "Simulated event calls should have isSimulated=true")
			}

			t.Logf("✅ Test passed: %s", tc.name)
		})
	}
}

func TestEventTriggerOracleWorkflowContinuation(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	t.Log("=== Testing Oracle EventTrigger Workflow Continuation Logic ===")

	// Test that shouldContinueWorkflowExecution works correctly
	testCases := []struct {
		name             string
		triggerOutput    map[string]interface{}
		expectedContinue bool
		description      string
	}{
		{
			name: "Success response - should continue",
			triggerOutput: map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"decimals": 18,
					"current":  3800,
				},
				"error": "",
				"executionContext": map[string]interface{}{
					"chainId":     11155111,
					"isSimulated": false,
				},
			},
			expectedContinue: true,
			description:      "When success=true, workflow should continue",
		},
		{
			name: "Failure response - should NOT continue",
			triggerOutput: map[string]interface{}{
				"success": false,
				"error":   "Conditions not met. Data: {\"decimals\":18,\"current\":4200}",
				"executionContext": map[string]interface{}{
					"chainId":     11155111,
					"isSimulated": false,
				},
			},
			expectedContinue: false,
			description:      "When success=false, workflow should NOT continue",
		},
		{
			name: "Legacy response with triggered field - should continue",
			triggerOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"decimals": 18,
					"current":  3800,
				},
				"metadata": map[string]interface{}{
					"triggered": true,
				},
			},
			expectedContinue: true,
			description:      "Legacy triggered=true should continue (backward compatibility)",
		},
		{
			name: "Legacy response with triggered=false - should NOT continue",
			triggerOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"decimals": 18,
					"current":  4200,
				},
				"metadata": map[string]interface{}{
					"triggered": false,
				},
			},
			expectedContinue: false,
			description:      "Legacy triggered=false should NOT continue (backward compatibility)",
		},
		{
			name: "No success or triggered field - should continue (default)",
			triggerOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"decimals": 18,
					"current":  3800,
				},
			},
			expectedContinue: true,
			description:      "When no success/triggered field, assume triggered (for historical events)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			// Test the shouldContinueWorkflowExecution function
			shouldContinue := engine.shouldContinueWorkflowExecution(tc.triggerOutput, avsproto.TriggerType_TRIGGER_TYPE_EVENT)

			assert.Equal(t, tc.expectedContinue, shouldContinue,
				"shouldContinueWorkflowExecution should return %v for %s",
				tc.expectedContinue, tc.name)

			t.Logf("✅ Workflow continuation: %v (expected: %v)", shouldContinue, tc.expectedContinue)
		})
	}
}

func TestEventTriggerSmartDetection(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	t.Log("=== Testing Smart Detection Logic ===")

	testCases := []struct {
		name           string
		queries        []interface{}
		expectedDirect bool
		description    string
	}{
		{
			name: "MethodCalls only - should use direct calls",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
					"contractAbi": []interface{}{
						map[string]interface{}{
							"inputs":          []interface{}{},
							"name":            "decimals",
							"outputs":         []interface{}{map[string]interface{}{"internalType": "uint8", "name": "", "type": "uint8"}},
							"stateMutability": "view",
							"type":            "function",
						},
					},
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName":   "decimals",
							"methodParams": []string{},
						},
					},
				},
			},
			expectedDirect: true,
			description:    "Oracle reading with methodCalls only should use direct calls",
		},
		{
			name: "Topics only - should use simulation",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f",
							},
						},
					},
				},
			},
			expectedDirect: false,
			description:    "Event-based query with topics should use simulation",
		},
		{
			name: "Both topics and methodCalls - should use simulation",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
					"contractAbi": []interface{}{
						map[string]interface{}{
							"inputs":          []interface{}{},
							"name":            "decimals",
							"outputs":         []interface{}{map[string]interface{}{"internalType": "uint8", "name": "", "type": "uint8"}},
							"stateMutability": "view",
							"type":            "function",
						},
					},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f",
							},
						},
					},
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName":   "decimals",
							"methodParams": []string{},
						},
					},
				},
			},
			expectedDirect: false,
			description:    "Mixed query with both topics and methodCalls should use simulation (events take precedence)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			// Test the shouldUseDirectCalls function
			shouldUseDirect := engine.shouldUseDirectCalls(tc.queries)

			assert.Equal(t, tc.expectedDirect, shouldUseDirect,
				"shouldUseDirectCalls should return %v for %s",
				tc.expectedDirect, tc.name)

			if shouldUseDirect {
				t.Logf("✅ Smart detection: Direct calls (oracle reading)")
			} else {
				t.Logf("✅ Smart detection: Simulation (event-based)")
			}
		})
	}
}
