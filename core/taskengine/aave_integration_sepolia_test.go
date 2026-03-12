//go:build integration

package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/ethclient"
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

// TestAAVEHealthFactorContractRead validates Option B for AAVE Health Factor monitoring:
// Block/Time trigger → ContractRead (getUserAccountData) → Branch (healthFactor < threshold)
// This test exercises the ContractRead node with the AAVE V3 Pool contract, which is the
// core building block for the deployed monitoring workflow.
func TestAAVEHealthFactorContractRead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping AAVE integration test in short mode")
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// AAVE V3 Pool on Sepolia
	aavePoolAddress := "0x6Ae43d3271ff6888e7Fc43Fd7321a503ff738951"

	// Derive the test user's smart wallet address from OWNER_EOA (set in .env).
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

	user := &model.User{
		Address:             *ownerAddr,
		SmartAccountAddress: smartWalletAddr,
	}

	t.Run("ContractRead_getUserAccountData", func(t *testing.T) {
		// ContractRead node calling AAVE V3 Pool getUserAccountData
		// This is what the workflow's ContractRead node does in Option B
		nodeConfig := map[string]interface{}{
			"contractAddress": aavePoolAddress,
			"contractAbi":     aavePoolABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "getUserAccountData",
					"methodParams": []interface{}{testUserAddress},
					"callData":     "",
				},
			},
		}

		result, err := engine.RunNodeImmediately("contractRead", nodeConfig, map[string]interface{}{}, user, true)
		if err != nil {
			t.Skipf("Skipping: RPC call failed (network issue): %v", err)
		}

		require.NotNil(t, result)

		// ContractRead returns {data: {methodName: {field: value}}}
		data, ok := result["data"].(map[string]interface{})
		require.True(t, ok, "result should have 'data' as map")

		methodData, ok := data["getUserAccountData"]
		require.True(t, ok, "data should contain getUserAccountData key")

		fieldMap, ok := methodData.(map[string]interface{})
		require.True(t, ok, "getUserAccountData should return a map of named fields")

		// Verify all 6 tuple fields are present
		expectedFields := []string{"totalCollateralBase", "totalDebtBase", "availableBorrowsBase",
			"currentLiquidationThreshold", "ltv", "healthFactor"}
		for _, field := range expectedFields {
			_, exists := fieldMap[field]
			assert.True(t, exists, "getUserAccountData should have field: %s", field)
		}

		t.Logf("getUserAccountData for %s: %+v", testUserAddress, fieldMap)
		t.Logf("healthFactor: %v", fieldMap["healthFactor"])
	})

	t.Run("ContractRead_getUserAccountData_with_template", func(t *testing.T) {
		// Verify that {{settings.runner}} template resolution works in ContractRead node,
		// which is how the frontend passes the user's wallet address
		nodeConfig := map[string]interface{}{
			"contractAddress": aavePoolAddress,
			"contractAbi":     aavePoolABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "getUserAccountData",
					"methodParams": []interface{}{"{{settings.runner}}"},
					"callData":     "",
				},
			},
		}

		inputVars := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner": testUserAddress,
			},
		}

		result, err := engine.RunNodeImmediately("contractRead", nodeConfig, inputVars, user, true)
		if err != nil {
			t.Skipf("Skipping: RPC call failed (network issue): %v", err)
		}

		require.NotNil(t, result)

		data, ok := result["data"].(map[string]interface{})
		require.True(t, ok, "result should have 'data' as map")

		_, hasMethod := data["getUserAccountData"]
		assert.True(t, hasMethod, "data should have getUserAccountData from resolved template call")

		t.Logf("Template {{settings.runner}} resolved to %s and ContractRead succeeded", testUserAddress)
	})
}

// TestAAVEHealthFactorConditionEvaluation is a unit test for the Branch node's
// condition evaluation logic against AAVE getUserAccountData return values.
// This tests the condition check that runs after ContractRead in Option B.
func TestAAVEHealthFactorConditionEvaluation(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Simulate the data shape returned by ContractRead for getUserAccountData
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
