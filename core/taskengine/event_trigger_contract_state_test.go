package taskengine

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
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

	// Derive the test user's smart wallet address from OWNER_EOA (set in .env).
	// This is a real Sepolia wallet with balance, consistent with other integration tests.
	// For an address with no AAVE position, getUserAccountData returns all zeros
	// except healthFactor = type(uint256).max (no debt = infinite health).
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("OWNER_EOA or TEST_PRIVATE_KEY not set, skipping AAVE integration test")
	}
	smartWalletConfig := config.SmartWallet
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()
	smartWalletAddr, err := aa.GetSenderAddress(client, *ownerAddr, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")
	testUserAddress := smartWalletAddr.Hex()

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

func TestResolveMethodParamsForSync(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	t.Run("resolves settings.runner template", func(t *testing.T) {
		smartWallet := "0x1234567890abcdef1234567890abcdef12345678"

		task := &model.Task{
			Task: &avsproto.Task{
				Id:                 "test-task-1",
				SmartWalletAddress: smartWallet,
				Trigger: &avsproto.TaskTrigger{
					TriggerType: &avsproto.TaskTrigger_Event{
						Event: &avsproto.EventTrigger{
							Config: &avsproto.EventTrigger_Config{
								Queries: []*avsproto.EventTrigger_Query{
									{
										Addresses: []string{"0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951"},
										MethodCalls: []*avsproto.EventTrigger_MethodCall{
											{
												MethodName:   "getUserAccountData",
												MethodParams: []string{"{{settings.runner}}"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		engine.resolveMethodParamsForSync(task)

		// Verify the template was resolved
		queries := task.Trigger.GetEvent().GetConfig().GetQueries()
		require.Len(t, queries, 1)
		methodCalls := queries[0].GetMethodCalls()
		require.Len(t, methodCalls, 1)
		assert.Equal(t, smartWallet, methodCalls[0].MethodParams[0],
			"{{settings.runner}} should resolve to SmartWalletAddress")
	})

	t.Run("resolves from InputVariables", func(t *testing.T) {
		userAddr := "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"

		settingsValue, _ := structpb.NewValue(map[string]interface{}{
			"runner": userAddr,
			"pool":   "0x1111111111111111111111111111111111111111",
		})

		task := &model.Task{
			Task: &avsproto.Task{
				Id: "test-task-2",
				InputVariables: map[string]*structpb.Value{
					"settings": settingsValue,
				},
				Trigger: &avsproto.TaskTrigger{
					TriggerType: &avsproto.TaskTrigger_Event{
						Event: &avsproto.EventTrigger{
							Config: &avsproto.EventTrigger_Config{
								Queries: []*avsproto.EventTrigger_Query{
									{
										Addresses: []string{"0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951"},
										MethodCalls: []*avsproto.EventTrigger_MethodCall{
											{
												MethodName:   "getUserAccountData",
												MethodParams: []string{"{{settings.runner}}"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		engine.resolveMethodParamsForSync(task)

		queries := task.Trigger.GetEvent().GetConfig().GetQueries()
		methodCalls := queries[0].GetMethodCalls()
		assert.Equal(t, userAddr, methodCalls[0].MethodParams[0],
			"{{settings.runner}} should resolve from InputVariables")
	})

	t.Run("no-op when no templates", func(t *testing.T) {
		literalAddr := "0x0000000000000000000000000000000000000001"

		task := &model.Task{
			Task: &avsproto.Task{
				Id: "test-task-3",
				Trigger: &avsproto.TaskTrigger{
					TriggerType: &avsproto.TaskTrigger_Event{
						Event: &avsproto.EventTrigger{
							Config: &avsproto.EventTrigger_Config{
								Queries: []*avsproto.EventTrigger_Query{
									{
										Addresses: []string{"0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951"},
										MethodCalls: []*avsproto.EventTrigger_MethodCall{
											{
												MethodName:   "getUserAccountData",
												MethodParams: []string{literalAddr},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		engine.resolveMethodParamsForSync(task)

		queries := task.Trigger.GetEvent().GetConfig().GetQueries()
		methodCalls := queries[0].GetMethodCalls()
		assert.Equal(t, literalAddr, methodCalls[0].MethodParams[0],
			"Literal addresses should not be modified")
	})

	t.Run("no-op when no method calls", func(t *testing.T) {
		task := &model.Task{
			Task: &avsproto.Task{
				Id: "test-task-4",
				Trigger: &avsproto.TaskTrigger{
					TriggerType: &avsproto.TaskTrigger_Event{
						Event: &avsproto.EventTrigger{
							Config: &avsproto.EventTrigger_Config{
								Queries: []*avsproto.EventTrigger_Query{
									{
										Addresses: []string{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
										Topics:    []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
									},
								},
							},
						},
					},
				},
			},
		}

		// Should not panic or modify anything
		engine.resolveMethodParamsForSync(task)
	})

	t.Run("no-op when trigger is nil", func(t *testing.T) {
		task := &model.Task{
			Task: &avsproto.Task{
				Id: "test-task-5",
			},
		}
		// Should not panic
		engine.resolveMethodParamsForSync(task)
	})
}
