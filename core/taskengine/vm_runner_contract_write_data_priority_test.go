package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContractWriteDataPriority tests the three priority levels for response data:
// PRIORITY 1: Return values from function ABI outputs (when no events found)
// PRIORITY 2: Event data (when events are found and parsed, overrides return values)
// PRIORITY 3: Boolean true fallback (when no return value and no events)
func TestContractWriteDataPriority(t *testing.T) {
	// Require Tenderly credentials for simulation
	_ = testutil.GetTestTenderlyAccount()
	_ = testutil.GetTestTenderlyProject()
	_ = testutil.GetTestTenderlyAccessKey()

	// Common test setup
	setupTest := func(t *testing.T) (*Engine, *model.User, common.Address, map[string]interface{}) {
		db := testutil.TestMustDB()
		t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
		aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

		ownerAddr, ok := testutil.MustGetTestOwnerAddress()
		if !ok {
			t.Skip("Owner EOA address not set, skipping test")
		}
		ownerEOA := *ownerAddr
		factory := config.SmartWallet.FactoryAddress

		client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
		require.NoError(t, err, "Failed to connect to RPC")
		t.Cleanup(func() { client.Close() })

		runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
		require.NoError(t, err, "Failed to derive smart wallet address")

		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		user := &model.User{Address: ownerEOA}

		triggerData := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   runnerAddr.Hex(),
				"chain_id": 11155111, // Sepolia
			},
		}

		return engine, user, *runnerAddr, triggerData
	}

	// PRIORITY 2: Event data takes precedence when events are found
	// Tests ERC20 approve() which returns bool BUT emits Approval event
	t.Run("Priority2_EventData_OverridesReturnValue", func(t *testing.T) {
		engine, user, _, triggerData := setupTest(t)

		sepoliaUsdcAddress := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")

		// ERC20 ABI with BOTH approve function AND Approval event
		contractAbi := []interface{}{
			map[string]interface{}{
				"type": "function",
				"name": "approve",
				"inputs": []interface{}{
					map[string]interface{}{"name": "spender", "type": "address", "internalType": "address"},
					map[string]interface{}{"name": "amount", "type": "uint256", "internalType": "uint256"},
				},
				"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool", "internalType": "bool"}},
				"stateMutability": "nonpayable",
			},
			map[string]interface{}{
				"type":      "event",
				"name":      "Approval",
				"anonymous": false,
				"inputs": []interface{}{
					map[string]interface{}{"indexed": true, "name": "owner", "type": "address", "internalType": "address"},
					map[string]interface{}{"indexed": true, "name": "spender", "type": "address", "internalType": "address"},
					map[string]interface{}{"indexed": false, "name": "value", "type": "uint256", "internalType": "uint256"},
				},
			},
		}

		// approve(spender, amount) calldata
		approveCallData := "0x095ea7b30000000000000000000000003bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e0000000000000000000000000000000000000000000000000000000000002710"

		nodeConfig := map[string]interface{}{
			"contractAddress": sepoliaUsdcAddress.Hex(),
			"contractAbi":     contractAbi,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "approve",
					"callData":   approveCallData,
				},
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData, user)

		require.NoError(t, err, "RunNodeImmediately should succeed")
		require.NotNil(t, result, "Should get result")

		// Verify the data field contains EVENT data, not just boolean
		data, hasData := result["data"]
		require.True(t, hasData, "Result should have 'data' field")
		require.NotNil(t, data, "Data should not be nil")

		dataMap, isMap := data.(map[string]interface{})
		require.True(t, isMap, "Data should be a map")

		approveData, hasApprove := dataMap["approve"]
		require.True(t, hasApprove, "Data should have 'approve' field")

		// CRITICAL: Should be event data (map), NOT boolean
		approveMap, isApproveMap := approveData.(map[string]interface{})
		require.True(t, isApproveMap, "PRIORITY 2 FAILED: Expected event data map, got %T: %v", approveData, approveData)

		// Verify event fields
		assert.Contains(t, approveMap, "owner", "Event should have 'owner' field")
		assert.Contains(t, approveMap, "spender", "Event should have 'spender' field")
		assert.Contains(t, approveMap, "value", "Event should have 'value' field")

		t.Logf("✅ PRIORITY 2 SUCCESS: Event data used (owner=%v, spender=%v, value=%v)",
			approveMap["owner"], approveMap["spender"], approveMap["value"])
	})

	// PRIORITY 1: Return value used when NO events are defined in ABI
	// Tests ERC20 decimals() which returns uint8 and emits no events
	t.Run("Priority1_ReturnValue_NoEvents", func(t *testing.T) {
		engine, user, _, triggerData := setupTest(t)

		sepoliaUsdcAddress := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")

		// ERC20 decimals() - returns uint8, NO events in ABI
		contractAbi := []interface{}{
			map[string]interface{}{
				"type":            "function",
				"name":            "decimals",
				"inputs":          []interface{}{},
				"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "uint8", "internalType": "uint8"}},
				"stateMutability": "view",
			},
		}

		// decimals() calldata
		decimalsCallData := "0x313ce567"

		nodeConfig := map[string]interface{}{
			"contractAddress": sepoliaUsdcAddress.Hex(),
			"contractAbi":     contractAbi,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "decimals",
					"callData":   decimalsCallData,
				},
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData, user)

		require.NoError(t, err, "RunNodeImmediately should succeed")
		require.NotNil(t, result, "Should get result")

		data, hasData := result["data"]
		require.True(t, hasData, "Result should have 'data' field")

		dataMap, isMap := data.(map[string]interface{})
		require.True(t, isMap, "Data should be a map")

		decimalsData, hasDecimals := dataMap["decimals"]
		require.True(t, hasDecimals, "Data should have 'decimals' field")

		// CRITICAL: Should be return value (string representation of uint8), NOT a map
		decimalsStr, isString := decimalsData.(string)
		require.True(t, isString, "PRIORITY 1 FAILED: Expected string return value, got %T: %v", decimalsData, decimalsData)

		// USDC has 6 decimals
		assert.Equal(t, "6", decimalsStr, "USDC should have 6 decimals")

		t.Logf("✅ PRIORITY 1 SUCCESS: Return value used (decimals=%s)", decimalsStr)
	})

	// PRIORITY 3: Boolean true fallback when NO return value AND NO events
	// Tests a hypothetical write method that returns nothing
	t.Run("Priority3_BooleanFallback_NoReturnNoEvents", func(t *testing.T) {
		engine, user, _, triggerData := setupTest(t)

		sepoliaUsdcAddress := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")

		// Hypothetical function with NO outputs and NO events
		// Using approve but with empty outputs to simulate this case
		contractAbi := []interface{}{
			map[string]interface{}{
				"type": "function",
				"name": "approve",
				"inputs": []interface{}{
					map[string]interface{}{"name": "spender", "type": "address"},
					map[string]interface{}{"name": "amount", "type": "uint256"},
				},
				"outputs":         []interface{}{}, // NO outputs
				"stateMutability": "nonpayable",
			},
			// NO events defined in ABI
		}

		approveCallData := "0x095ea7b30000000000000000000000003bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e0000000000000000000000000000000000000000000000000000000000002710"

		nodeConfig := map[string]interface{}{
			"contractAddress": sepoliaUsdcAddress.Hex(),
			"contractAbi":     contractAbi,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "approve",
					"callData":   approveCallData,
				},
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData, user)

		require.NoError(t, err, "RunNodeImmediately should succeed")
		require.NotNil(t, result, "Should get result")

		data, hasData := result["data"]
		require.True(t, hasData, "Result should have 'data' field")

		dataMap, isMap := data.(map[string]interface{})
		require.True(t, isMap, "Data should be a map")

		approveData, hasApprove := dataMap["approve"]
		require.True(t, hasApprove, "Data should have 'approve' field")

		// CRITICAL: Should be boolean true (fallback)
		approveBool, isBool := approveData.(bool)
		require.True(t, isBool, "PRIORITY 3 FAILED: Expected boolean fallback, got %T: %v", approveData, approveData)
		assert.True(t, approveBool, "Boolean fallback should be true for successful call")

		t.Logf("✅ PRIORITY 3 SUCCESS: Boolean fallback used (approve=%v)", approveBool)
	})

	// Edge case: Function returns complex type (map/array) - should keep return value even if events exist
	t.Run("Priority1_ComplexReturnValue_KeepsDespiteEvents", func(t *testing.T) {
		engine, user, _, triggerData := setupTest(t)

		sepoliaUsdcAddress := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")

		// ERC20 balanceOf returns uint256 (treated as string in our system)
		// Even if Approval events exist in ABI, balanceOf doesn't emit them
		contractAbi := []interface{}{
			map[string]interface{}{
				"type":   "function",
				"name":   "balanceOf",
				"inputs": []interface{}{map[string]interface{}{"name": "account", "type": "address"}},
				"outputs": []interface{}{
					map[string]interface{}{"name": "", "type": "uint256", "internalType": "uint256"},
				},
				"stateMutability": "view",
			},
			// Include Approval event in ABI (but balanceOf won't emit it)
			map[string]interface{}{
				"type":      "event",
				"name":      "Approval",
				"anonymous": false,
				"inputs": []interface{}{
					map[string]interface{}{"indexed": true, "name": "owner", "type": "address"},
					map[string]interface{}{"indexed": true, "name": "spender", "type": "address"},
					map[string]interface{}{"indexed": false, "name": "value", "type": "uint256"},
				},
			},
		}

		// balanceOf(address) calldata - checking balance of a known address
		balanceOfCallData := "0x70a082310000000000000000000000005d814cc9e94b2656f59ee439d44aa1b6ca21434f"

		nodeConfig := map[string]interface{}{
			"contractAddress": sepoliaUsdcAddress.Hex(),
			"contractAbi":     contractAbi,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "balanceOf",
					"callData":   balanceOfCallData,
				},
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData, user)

		require.NoError(t, err, "RunNodeImmediately should succeed")
		require.NotNil(t, result, "Should get result")

		data, hasData := result["data"]
		require.True(t, hasData, "Result should have 'data' field")

		dataMap, isMap := data.(map[string]interface{})
		require.True(t, isMap, "Data should be a map")

		balanceData, hasBalance := dataMap["balanceOf"]
		require.True(t, hasBalance, "Data should have 'balanceOf' field")

		// Should be return value (string), not event data
		balanceStr, isString := balanceData.(string)
		require.True(t, isString, "Expected string return value, got %T: %v", balanceData, balanceData)

		t.Logf("✅ EDGE CASE SUCCESS: Return value kept despite events in ABI (balance=%s)", balanceStr)
	})
}
