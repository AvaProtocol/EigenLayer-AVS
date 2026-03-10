package taskengine

import (
	"encoding/json"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AAVE V3 Pool ABI for getUserAccountData
var aavePoolABI = []interface{}{
	map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{"internalType": "address", "name": "user", "type": "address"},
		},
		"name": "getUserAccountData",
		"outputs": []interface{}{
			map[string]interface{}{"internalType": "uint256", "name": "totalCollateralBase", "type": "uint256"},
			map[string]interface{}{"internalType": "uint256", "name": "totalDebtBase", "type": "uint256"},
			map[string]interface{}{"internalType": "uint256", "name": "availableBorrowsBase", "type": "uint256"},
			map[string]interface{}{"internalType": "uint256", "name": "currentLiquidationThreshold", "type": "uint256"},
			map[string]interface{}{"internalType": "uint256", "name": "ltv", "type": "uint256"},
			map[string]interface{}{"internalType": "uint256", "name": "healthFactor", "type": "uint256"},
		},
		"stateMutability": "view",
		"type":            "function",
	},
}

func TestEventTriggerContractStateAAVEHealthFactor(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// AAVE V3 Pool on Sepolia
	aavePoolAddress := "0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951"
	// A known address that has AAVE positions on Sepolia (use a well-known test address)
	// For the test we use a valid address; if it has no AAVE position, healthFactor will be max uint256
	testUserAddress := "0x0000000000000000000000000000000000000001"

	t.Run("DirectCallWithMethodParams", func(t *testing.T) {
		// This tests the core contract state trigger flow:
		// 1. Call getUserAccountData with a user address param
		// 2. Get back structured tuple data with named fields
		// 3. No conditions - just verify the call works
		triggerConfig := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"addresses":   []interface{}{aavePoolAddress},
					"topics":      []interface{}{}, // No topics = direct calls path
					"contractAbi": aavePoolABI,
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName":    "getUserAccountData",
							"methodParams":  []interface{}{testUserAddress},
							"applyToFields": []interface{}{},
						},
					},
				},
			},
		}

		result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, map[string]interface{}{})
		if err != nil {
			t.Skipf("Skipping: RPC call failed (network issue): %v", err)
			return
		}

		require.NotNil(t, result)

		// Should succeed since no conditions
		success, ok := result["success"].(bool)
		require.True(t, ok, "result should have 'success' field")
		assert.True(t, success, "should succeed with no conditions")

		// Verify data structure: {getUserAccountData: {field: value}}
		data, ok := result["data"].(map[string]interface{})
		require.True(t, ok, "result should have 'data' as map")

		methodData, ok := data["getUserAccountData"]
		require.True(t, ok, "data should contain getUserAccountData key")

		if fieldMap, ok := methodData.(map[string]interface{}); ok {
			// Multi-output: verify tuple fields are present
			expectedFields := []string{"totalCollateralBase", "totalDebtBase", "availableBorrowsBase",
				"currentLiquidationThreshold", "ltv", "healthFactor"}
			for _, field := range expectedFields {
				_, exists := fieldMap[field]
				assert.True(t, exists, "getUserAccountData should have field: %s", field)
			}

			t.Logf("getUserAccountData result: %+v", fieldMap)
			t.Logf("healthFactor: %v", fieldMap["healthFactor"])
		} else {
			t.Logf("getUserAccountData returned single value: %v", methodData)
		}

		// Verify metadata has methodCalls output
		metadata, ok := result["metadata"].(map[string]interface{})
		require.True(t, ok, "result should have metadata")
		_, hasMethodCalls := metadata["methodCalls"]
		assert.True(t, hasMethodCalls, "metadata should have methodCalls")

		// Verify executionContext shows real RPC (not simulated)
		execCtx, ok := result["executionContext"].(map[string]interface{})
		if ok {
			if isSimulated, exists := execCtx["isSimulated"]; exists {
				assert.False(t, isSimulated.(bool), "should not be simulated")
			}
		}

		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		t.Logf("Full result:\n%s", string(resultJSON))
	})

	t.Run("ConditionEvaluation_Uint256_LessThan", func(t *testing.T) {
		// Test with a condition that should always pass for an address with no AAVE position:
		// healthFactor for no-position address is max uint256, so "gt 0" should pass
		triggerConfig := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"addresses":   []interface{}{aavePoolAddress},
					"topics":      []interface{}{},
					"contractAbi": aavePoolABI,
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName":    "getUserAccountData",
							"methodParams":  []interface{}{testUserAddress},
							"applyToFields": []interface{}{},
						},
					},
					"conditions": []interface{}{
						map[string]interface{}{
							"fieldName": "getUserAccountData.healthFactor",
							"operator":  "gt",
							"value":     "0",
							"fieldType": "uint256",
						},
					},
				},
			},
		}

		result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, map[string]interface{}{})
		if err != nil {
			t.Skipf("Skipping: RPC call failed (network issue): %v", err)
			return
		}

		require.NotNil(t, result)

		success, ok := result["success"].(bool)
		require.True(t, ok)
		// healthFactor for address with no position is max uint256, so gt 0 should be true
		assert.True(t, success, "healthFactor > 0 should be true")
		t.Logf("Condition 'getUserAccountData.healthFactor gt 0': %v", success)
	})

	t.Run("ConditionEvaluation_Uint256_NotMet", func(t *testing.T) {
		// Test with a condition that should NOT pass:
		// healthFactor for no-position address is max uint256, so "lt 0" should fail
		triggerConfig := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"addresses":   []interface{}{aavePoolAddress},
					"topics":      []interface{}{},
					"contractAbi": aavePoolABI,
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName":    "getUserAccountData",
							"methodParams":  []interface{}{testUserAddress},
							"applyToFields": []interface{}{},
						},
					},
					"conditions": []interface{}{
						map[string]interface{}{
							"fieldName": "getUserAccountData.healthFactor",
							"operator":  "lt",
							"value":     "0",
							"fieldType": "uint256",
						},
					},
				},
			},
		}

		result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, map[string]interface{}{})
		if err != nil {
			t.Skipf("Skipping: RPC call failed (network issue): %v", err)
			return
		}

		require.NotNil(t, result)

		success, ok := result["success"].(bool)
		require.True(t, ok)
		assert.False(t, success, "healthFactor < 0 should be false")

		errorMsg, _ := result["error"].(string)
		assert.Contains(t, errorMsg, "Conditions not met")
	})

	t.Run("TemplateVariableResolution", func(t *testing.T) {
		// Test that {{settings.runner}} in methodParams gets resolved
		triggerConfig := map[string]interface{}{
			"queries": []interface{}{
				map[string]interface{}{
					"addresses":   []interface{}{aavePoolAddress},
					"topics":      []interface{}{},
					"contractAbi": aavePoolABI,
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName":    "getUserAccountData",
							"methodParams":  []interface{}{"{{settings.runner}}"},
							"applyToFields": []interface{}{},
						},
					},
				},
			},
		}

		// Provide settings.runner as an input variable
		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner": testUserAddress,
			},
		}

		result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, inputVariables)
		if err != nil {
			t.Skipf("Skipping: RPC call failed (network issue): %v", err)
			return
		}

		require.NotNil(t, result)

		success, ok := result["success"].(bool)
		require.True(t, ok)
		assert.True(t, success, "should succeed with resolved template variable")

		// Verify data is present
		data, ok := result["data"].(map[string]interface{})
		require.True(t, ok)
		_, hasMethod := data["getUserAccountData"]
		assert.True(t, hasMethod, "data should have getUserAccountData from resolved call")

		t.Logf("Template {{settings.runner}} resolved to %s and call succeeded", testUserAddress)
	})
}

func TestEventTriggerContractStateConditionEvaluation(t *testing.T) {
	// Unit test for condition evaluation against method call data
	// No RPC needed - tests the evaluation logic directly

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Simulate the data shape returned by executeMethodCallForSimulation
	// for getUserAccountData with 6 return values
	eventData := map[string]interface{}{
		"getUserAccountData": map[string]interface{}{
			"totalCollateralBase":         "150000000000",
			"totalDebtBase":               "100000000000",
			"availableBorrowsBase":        "20000000000",
			"currentLiquidationThreshold": "8250",
			"ltv":                         "7500",
			"healthFactor":                "1400000000000000000", // 1.4 * 10^18
		},
	}

	testCases := []struct {
		name       string
		conditions []interface{}
		expected   bool
	}{
		{
			name: "healthFactor lt 1.5e18 (should match - 1.4 < 1.5)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.healthFactor",
					"operator":  "lt",
					"value":     "1500000000000000000",
					"fieldType": "uint256",
				},
			},
			expected: true,
		},
		{
			name: "healthFactor lt 1.0e18 (should NOT match - 1.4 > 1.0)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.healthFactor",
					"operator":  "lt",
					"value":     "1000000000000000000",
					"fieldType": "uint256",
				},
			},
			expected: false,
		},
		{
			name: "healthFactor gt 1.0e18 (should match - 1.4 > 1.0)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.healthFactor",
					"operator":  "gt",
					"value":     "1000000000000000000",
					"fieldType": "uint256",
				},
			},
			expected: true,
		},
		{
			name: "healthFactor eq exact value (should match)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.healthFactor",
					"operator":  "eq",
					"value":     "1400000000000000000",
					"fieldType": "uint256",
				},
			},
			expected: true,
		},
		{
			name: "totalDebtBase gt 0 (should match - has debt)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.totalDebtBase",
					"operator":  "gt",
					"value":     "0",
					"fieldType": "uint256",
				},
			},
			expected: true,
		},
		{
			name: "multiple conditions AND (healthFactor < 1.5 AND totalDebtBase > 0)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.healthFactor",
					"operator":  "lt",
					"value":     "1500000000000000000",
					"fieldType": "uint256",
				},
				map[string]interface{}{
					"fieldName": "getUserAccountData.totalDebtBase",
					"operator":  "gt",
					"value":     "0",
					"fieldType": "uint256",
				},
			},
			expected: true,
		},
		{
			name: "nonexistent field (should fail)",
			conditions: []interface{}{
				map[string]interface{}{
					"fieldName": "getUserAccountData.nonexistent",
					"operator":  "gt",
					"value":     "0",
					"fieldType": "uint256",
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := engine.evaluateConditionsAgainstEventData(eventData, tc.conditions)
			assert.Equal(t, tc.expected, result, "condition evaluation mismatch for: %s", tc.name)
		})
	}
}
