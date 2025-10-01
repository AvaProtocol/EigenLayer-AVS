package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContractWrite_WithSettingsAndUserAuth(t *testing.T) {
	t.Run("ContractWrite_USDC_Approve_WithSettingsFormat", func(t *testing.T) {
		// Setup test environment
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
		aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

		// Create test user (simulating authenticated user from JWT)
		ownerEOA := common.HexToAddress("0xD7050816337a3f8f690F8083B5Ff8019D50c0E50")
		runnerAddr := common.HexToAddress("0x5Df343de7d99fd64b2479189692C1dAb8f46184a")
		factory := testutil.GetAggregatorConfig().SmartWallet.FactoryAddress

		// Create authenticated user model
		user := &model.User{
			Address: ownerEOA,
		}

		// Seed wallet in DB for validation
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: &runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		// Contract configuration for USDC approve
		nodeConfig := map[string]interface{}{
			"contractAddress": "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b", // USDC Sepolia
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{"internalType": "address", "name": "spender", "type": "address"},
						map[string]interface{}{"internalType": "uint256", "name": "amount", "type": "uint256"},
					},
					"name":            "approve",
					"outputs":         []interface{}{map[string]interface{}{"internalType": "bool", "name": "", "type": "bool"}},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{"0x1234567890123456789012345678901234567890", "1000000"},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		// Input variables using NEW settings format (not workflowContext)
		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   runnerAddr.Hex(),
				"chain_id": 11155111, // Sepolia testnet
			},
		}

		t.Logf("🧪 Testing contractWrite with new settings format:")
		t.Logf("   User (from JWT): %s", user.Address.Hex())
		t.Logf("   Runner (from settings): %s", runnerAddr.Hex())
		t.Logf("   Chain ID (from settings): %d", 11155111)
		t.Logf("   Method: approve")

		// Execute with authenticated user
		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, inputVariables, user)

		// Assertions
		require.NoError(t, err, "RunNodeImmediately should succeed with settings format")
		require.NotNil(t, result, "Should get simulation result")

		// Verify success field
		if success, ok := result["success"].(bool); ok {
			assert.True(t, success, "Contract write should succeed in simulation")
		}

		// Verify results structure
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			t.Logf("✅ Got %d method results", len(results))

			// Check first result
			firstResult := results[0]
			if methodResult, ok := firstResult.(map[string]interface{}); ok {
				assert.Equal(t, "approve", methodResult["methodName"], "Method name should be approve")
				if success, ok := methodResult["success"].(bool); ok {
					assert.True(t, success, "Method should succeed")
				}
			}
		} else {
			t.Logf("ℹ️ No results array found in result: %+v", result)
		}

		t.Logf("✅ Contract write with settings format completed successfully")
	})

	t.Run("ContractWrite_ErrorWhenSettingsMissing", func(t *testing.T) {
		// Setup test environment
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		// Create test user
		ownerEOA := common.HexToAddress("0xD7050816337a3f8f690F8083B5Ff8019D50c0E50")
		user := &model.User{Address: ownerEOA}

		nodeConfig := map[string]interface{}{
			"contractAddress": "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{"internalType": "address", "name": "spender", "type": "address"},
						map[string]interface{}{"internalType": "uint256", "name": "amount", "type": "uint256"},
					},
					"name":            "approve",
					"outputs":         []interface{}{map[string]interface{}{"internalType": "bool", "name": "", "type": "bool"}},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{"0x1234567890123456789012345678901234567890", "1000000"},
				},
			},
		}

		// Input variables WITHOUT settings (should fail)
		inputVariables := map[string]interface{}{
			"someOtherData": "test",
		}

		// Execute - should fail
		_, err := engine.RunNodeImmediately("contractWrite", nodeConfig, inputVariables, user)

		// Should get specific error about missing settings
		require.Error(t, err, "Should fail when settings are missing")
		assert.Contains(t, err.Error(), "settings is required for contractWrite", "Should get specific error message")

		t.Logf("✅ Correctly rejected contractWrite without settings: %v", err)
	})

	t.Run("ContractWrite_ErrorWhenRunnerMissing", func(t *testing.T) {
		// Setup test environment
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		// Create test user
		ownerEOA := common.HexToAddress("0xD7050816337a3f8f690F8083B5Ff8019D50c0E50")
		user := &model.User{Address: ownerEOA}

		nodeConfig := map[string]interface{}{
			"contractAddress": "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{"internalType": "address", "name": "spender", "type": "address"},
						map[string]interface{}{"internalType": "uint256", "name": "amount", "type": "uint256"},
					},
					"name":            "approve",
					"outputs":         []interface{}{map[string]interface{}{"internalType": "bool", "name": "", "type": "bool"}},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{"0x1234567890123456789012345678901234567890", "1000000"},
				},
			},
		}

		// Input variables with settings but missing runner
		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"chain_id": 11155111,
				// "runner" is missing
			},
		}

		// Execute - should fail
		_, err := engine.RunNodeImmediately("contractWrite", nodeConfig, inputVariables, user)

		// Should get specific error about missing runner
		require.Error(t, err, "Should fail when runner is missing")
		assert.Contains(t, err.Error(), "settings.runner is required for contractWrite", "Should get specific error message")

		t.Logf("✅ Correctly rejected contractWrite without runner: %v", err)
	})

	t.Run("ContractWrite_ErrorWhenUserNotAuthenticated", func(t *testing.T) {
		// Setup test environment
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		nodeConfig := map[string]interface{}{
			"contractAddress": "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{"internalType": "address", "name": "spender", "type": "address"},
						map[string]interface{}{"internalType": "uint256", "name": "amount", "type": "uint256"},
					},
					"name":            "approve",
					"outputs":         []interface{}{map[string]interface{}{"internalType": "bool", "name": "", "type": "bool"}},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{"0x1234567890123456789012345678901234567890", "1000000"},
				},
			},
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   "0x5Df343de7d99fd64b2479189692C1dAb8f46184a",
				"chain_id": 11155111,
			},
		}

		// Execute with NIL user (simulates missing JWT authentication)
		// In real usage, user comes from verifyAuth() parsing JWT token claims["sub"] -> user.Address
		_, err := engine.RunNodeImmediately("contractWrite", nodeConfig, inputVariables, nil)

		// Should get specific error about missing authentication
		require.Error(t, err, "Should fail when user is not authenticated")
		assert.Contains(t, err.Error(), "authentication required for contractWrite", "Should get specific error message")

		t.Logf("✅ Correctly rejected contractWrite without authentication: %v", err)
	})
}
