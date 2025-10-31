package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// maskKey masks sensitive key for logging
func maskKey(key string) string {
	if len(key) <= 4 {
		return "***"
	}
	return key[:2] + "***" + key[len(key)-2:]
}

func TestContractWriteTenderlySimulation(t *testing.T) {
	// Require Tenderly credentials in config
	_ = testutil.GetTestTenderlyAccount()
	_ = testutil.GetTestTenderlyProject()
	_ = testutil.GetTestTenderlyAccessKey()

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Use an ERC20 contract deployed on Sepolia to match default test chain
	// USDC (Sepolia): 0x1c7d4b196cb0c7b01d743fbc6116a902379c7238
	sepoliaUsdcAddress := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")

	// Create approve calldata for testing: approve(address,uint256)
	// selector 0x095ea7b3, spender 0xe0f7d11fd714674722d325cd86062a5f1882e13a, amount 1000 (0x3e8)
	approveCallData := "0x095ea7b3000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8"

	t.Run("RunNodeImmediately_UsesTenderlySimulation", func(t *testing.T) {
		// Create test engine
		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		// Create ABI as parsed array (what CreateNodeFromType expects)
		contractAbi := []interface{}{
			map[string]interface{}{
				"inputs": []interface{}{
					map[string]interface{}{
						"internalType": "address",
						"name":         "spender",
						"type":         "address",
					},
					map[string]interface{}{
						"internalType": "uint256",
						"name":         "amount",
						"type":         "uint256",
					},
				},
				"name": "approve",
				"outputs": []interface{}{
					map[string]interface{}{
						"internalType": "bool",
						"name":         "",
						"type":         "bool",
					},
				},
				"stateMutability": "nonpayable",
				"type":            "function",
			},
		}

		// Test run_node_immediately
		nodeConfig := map[string]interface{}{
			"contractAddress": sepoliaUsdcAddress.Hex(),
			"contractAbi":     contractAbi, // Now using parsed array instead of JSON string
			"methodCalls": []interface{}{
				map[string]interface{}{
					"callData":   approveCallData,
					"methodName": "approve",
				},
			},
		}

		// Get owner EOA from environment and derive salt:0 smart wallet
		ownerAddr, ok := testutil.MustGetTestOwnerAddress()
		if !ok {
			t.Skip("Owner EOA address not set, skipping simulation test")
		}
		ownerEOA := *ownerAddr
		factory := testutil.GetAggregatorConfig().SmartWallet.FactoryAddress

		// Derive actual salt:0 smart wallet address (no mock needed)
		aa.SetFactoryAddress(factory)

		// Connect to RPC to derive address
		client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
		require.NoError(t, err, "Failed to connect to RPC")
		defer client.Close()

		runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
		require.NoError(t, err, "Failed to derive smart wallet address")

		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{Owner: &ownerEOA, Address: runnerAddr, Factory: &factory, Salt: big.NewInt(0)})

		// Provide settings for new backend validation structure
		triggerData := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   runnerAddr.Hex(),
				"chain_id": 11155111, // Sepolia
			},
		}

		// Create user for authentication (EOA from the stored wallet)
		user := &model.User{Address: ownerEOA}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData, user)

		require.NoError(t, err, "RunNodeImmediately should succeed with Tenderly simulation")
		require.NotNil(t, result, "Should get simulation result")

		// Verify simulation metadata is present
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			if firstResult, ok := results[0].(*avsproto.ContractWriteNode_MethodResult); ok {
				assert.NotNil(t, firstResult.Receipt, "Should have receipt data")

				// Prefer asserting on receipt.logs presence; transactionHash may be absent in some simulations
				if firstResult.Receipt != nil {
					if receiptMap, ok := firstResult.Receipt.AsInterface().(map[string]interface{}); ok && receiptMap != nil {
						if logs, ok := receiptMap["logs"].([]interface{}); ok {
							assert.Greater(t, len(logs), 0, "receipt.logs should contain entries")
						}
					}
				}

				t.Logf("✅ Contract write simulation executed:")
				t.Logf("   Method: %s", firstResult.MethodName)
				t.Logf("   Success: %t", firstResult.Success)
				t.Logf("   Receipt: %+v", firstResult.Receipt.AsInterface())
			}
		}
	})

	// Test exact client request to replicate the failing scenario
	t.Run("ExactClientRequest_USDC_Transfer", func(t *testing.T) {
		// Create test engine with same setup as real scenario
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
		aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

		// Get owner EOA from environment and derive salt:0 smart wallet
		ownerAddr, ok := testutil.MustGetTestOwnerAddress()
		if !ok {
			t.Skip("Owner EOA address not set, skipping simulation test")
		}
		ownerEOA := *ownerAddr
		factory := smartWalletConfig.FactoryAddress

		// Derive actual salt:0 smart wallet address (need ethclient)
		client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
		require.NoError(t, err, "Failed to connect to RPC")
		defer client.Close()

		runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
		require.NoError(t, err, "Failed to derive smart wallet address")

		// Seed wallet for validation
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{Owner: &ownerEOA, Address: runnerAddr, Factory: &factory, Salt: big.NewInt(0)})

		// Full USDC ABI as provided in client request (truncated for readability but key functions included)
		contractAbi := []interface{}{
			map[string]interface{}{
				"inputs": []interface{}{
					map[string]interface{}{"internalType": "address", "name": "to", "type": "address"},
					map[string]interface{}{"internalType": "uint256", "name": "value", "type": "uint256"},
				},
				"name":            "transfer",
				"outputs":         []interface{}{map[string]interface{}{"internalType": "bool", "name": "", "type": "bool"}},
				"stateMutability": "nonpayable",
				"type":            "function",
			},
		}

		// Exact node config from client request
		nodeConfig := map[string]interface{}{
			"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
			"contractAbi":     contractAbi,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "transfer",
					"methodParams": []interface{}{"0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788", "100000"},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		// Updated input variables with new settings structure
		// Use the derived runner address instead of hardcoded value
		inputVariables := map[string]interface{}{
			"timeTrigger": map[string]interface{}{
				"data":  map[string]interface{}{},
				"input": map[string]interface{}{"schedules": []interface{}{"*/5 * * * *"}},
			},
			"settings": map[string]interface{}{
				"runner":   runnerAddr.Hex(), // Use derived address, not hardcoded
				"chain_id": 11155111,
			},
			// Keep some legacy fields for context but authentication should come from user
			"workflowMeta": map[string]interface{}{
				"id":           "7625882c-8d1c-40dc-8d04-13eee0ba8b2f",
				"name":         "Recurring Transfer with report",
				"startAt":      "2025-08-14T23:20:37.988Z",
				"expiredAt":    "2025-09-14T22:23:37.084Z",
				"maxExecution": 2,
				"status":       "draft",
				"chain":        "Sepolia",
			},
		}

		t.Logf("🚀 Testing exact client request:")
		t.Logf("   Contract: %s", nodeConfig["contractAddress"])
		t.Logf("   Method: transfer")
		t.Logf("   From: %s", inputVariables["settings"].(map[string]interface{})["runner"])
		t.Logf("   To: %s", nodeConfig["methodCalls"].([]interface{})[0].(map[string]interface{})["methodParams"].([]interface{})[0])
		t.Logf("   Amount: %s", nodeConfig["methodCalls"].([]interface{})[0].(map[string]interface{})["methodParams"].([]interface{})[1])

		// Create user for authentication (EOA from the exact client request)
		user := &model.User{Address: ownerEOA}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, inputVariables, user)

		if err != nil {
			t.Logf("❌ RunNodeImmediately failed: %v", err)
			// This should now succeed with the fixed storage slot calculation
			require.NoError(t, err, "Expected successful simulation with balance override")
		} else {
			t.Logf("✅ RunNodeImmediately succeeded!")
			require.NotNil(t, result, "Should get simulation result")

			// Verify we get a valid transfer object
			if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
				if firstResult, ok := results[0].(*avsproto.ContractWriteNode_MethodResult); ok {
					t.Logf("   Method: %s", firstResult.MethodName)
					t.Logf("   Success: %t", firstResult.Success)

					// Check for transfer data in the result
					if firstResult.Value != nil {
						t.Logf("   Value: %+v", firstResult.Value.AsInterface())
					}

					if firstResult.Receipt != nil {
						t.Logf("   Receipt: %+v", firstResult.Receipt.AsInterface())
					}

					// The goal is to show users what a successful transfer looks like
					assert.True(t, firstResult.Success, "Transfer should succeed in simulation")
				}
			}
		}
	})

	// Replicate client request: transfer(to, amount) using derived runner (salt:0)
	t.Run("RunNodeImmediately_Transfer_WithDerivedRunner_UsesTenderlySimulation", func(t *testing.T) {
		// Require Tenderly credentials in config
		_ = testutil.GetTestTenderlyAccount()
		_ = testutil.GetTestTenderlyProject()
		_ = testutil.GetTestTenderlyAccessKey()

		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
		aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

		// Get owner EOA from environment
		ownerAddr, ok := testutil.MustGetTestOwnerAddress()
		if !ok {
			t.Skip("Owner EOA address not set, skipping simulation test")
		}
		ownerEOA := *ownerAddr
		toAddr := ownerEOA.Hex() // Transfer to owner EOA itself
		amount := "1000000"

		// Derive runner using salt:0 via factory on chain
		rpcURL := smartWalletConfig.EthRpcUrl
		if rpcURL == "" {
			t.Fatal("RPC URL not configured in config/aggregator.yaml")
		}
		ethc, err := ethclient.Dial(rpcURL)
		require.NoError(t, err, "Failed to connect RPC for derivation")
		derivedRunner, derr := aa.GetSenderAddress(ethc, ownerEOA, big.NewInt(0))
		require.NoError(t, derr, "Failed to derive runner")

		// Seed wallet for validation
		factory := smartWalletConfig.FactoryAddress
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{Owner: &ownerEOA, Address: derivedRunner, Factory: &factory, Salt: big.NewInt(0)})

		// Minimal ABI for transfer(address,uint256)
		transferAbi := []interface{}{
			map[string]interface{}{
				"inputs": []interface{}{
					map[string]interface{}{"internalType": "address", "name": "to", "type": "address"},
					map[string]interface{}{"internalType": "uint256", "name": "value", "type": "uint256"},
				},
				"name":            "transfer",
				"outputs":         []interface{}{map[string]interface{}{"internalType": "bool", "name": "", "type": "bool"}},
				"stateMutability": "nonpayable",
				"type":            "function",
			},
		}

		nodeConfig := map[string]interface{}{
			"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
			"contractAbi":     transferAbi,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "transfer",
					"methodParams": []interface{}{toAddr, amount},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		triggerData := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   derivedRunner.Hex(),
				"chain_id": 11155111,
			},
			"workflowMeta": map[string]interface{}{
				"id":    "test-run-node-immediately-transfer",
				"name":  "Recurring Transfer with report",
				"chain": "Sepolia",
			},
			"timeTrigger": map[string]interface{}{"data": map[string]interface{}{}, "input": map[string]interface{}{"schedules": []interface{}{"*/5 * * * *"}}},
		}

		// Create user for authentication (EOA from the derived runner test)
		user := &model.User{Address: ownerEOA}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData, user)
		if err != nil {
			t.Logf("❌ RunNodeImmediately failed: %v", err)
		} else {
			t.Logf("✅ RunNodeImmediately succeeded: %+v", result)
		}
		require.NotNil(t, result)
	})
}
