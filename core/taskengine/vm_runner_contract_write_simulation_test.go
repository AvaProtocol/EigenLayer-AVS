package taskengine

import (
	"math/big"
	"os"
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

func TestContractWriteTenderlySimulation(t *testing.T) {
	// Run when Tenderly Gateway is configured; skip only if missing API key/URL
	if os.Getenv("TENDERLY_API_KEY") == "" {
		t.Skip("Skipping Tenderly simulation: TENDERLY_API_KEY not set")
	}
	if os.Getenv("FACTORY_ADDRESS") == "" {
		t.Skip("Skipping Tenderly simulation: FACTORY_ADDRESS not set (needed to resolve salt:0 wallet)")
	}
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

		// Seed a wallet in DB to make ListWallets(owner) deterministic in CI
		// Use distinct EOA (owner) and smart wallet (runner)
		ownerEOA := common.HexToAddress("0xD7050816337a3f8f690F8083B5Ff8019D50c0E50")
		runnerAddr := common.HexToAddress("0x5Df343de7d99fd64b2479189692C1dAb8f46184a")
		factory := testutil.GetAggregatorConfig().SmartWallet.FactoryAddress
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{Owner: &ownerEOA, Address: &runnerAddr, Factory: &factory, Salt: big.NewInt(0)})

		// Provide minimal workflowContext to satisfy backend validation
		triggerData := map[string]interface{}{
			"workflowContext": map[string]interface{}{
				"eoaAddress": ownerEOA.Hex(),
				"runner":     runnerAddr.Hex(),
				"chainId":    11155111, // Sepolia
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData)

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

	// Replicate client request: transfer(to, amount) using derived runner (salt:0)
	t.Run("RunNodeImmediately_Transfer_WithDerivedRunner_UsesTenderlySimulation", func(t *testing.T) {
		if os.Getenv("TENDERLY_API_KEY") == "" {
			t.Skip("Skipping Tenderly simulation: TENDERLY_API_KEY not set")
		}
		if os.Getenv("FACTORY_ADDRESS") == "" {
			t.Skip("Skipping Tenderly simulation: FACTORY_ADDRESS not set")
		}

		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
		aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

		// Owner EOA and transfer params from client sample
		ownerEOA := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
		toAddr := "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788"
		amount := "1000000"

		// Derive runner using salt:0 via factory on chain
		rpcURL := smartWalletConfig.EthRpcUrl
		if rpcURL == "" {
			t.Skip("Skipping: SEPOLIA_RPC not configured")
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
			"workflowContext": map[string]interface{}{
				"id":         "test-run-node-immediately-transfer",
				"chainId":    11155111,
				"name":       "Recurring Transfer with report",
				"eoaAddress": ownerEOA.Hex(),
				"runner":     derivedRunner.Hex(),
				"chain":      "Sepolia",
			},
			"timeTrigger": map[string]interface{}{"data": map[string]interface{}{}, "input": map[string]interface{}{"schedules": []interface{}{"*/5 * * * *"}}},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData)
		require.NoError(t, err, "RunNodeImmediately should not error for simulation")
		require.NotNil(t, result)
	})
}
