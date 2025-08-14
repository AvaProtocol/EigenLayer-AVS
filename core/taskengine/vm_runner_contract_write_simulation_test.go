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

	// Use a well-known ERC20 contract for testing - USDC on Base Sepolia
	baseSepoliaUsdcAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")

	// Create approve calldata for testing
	approveCallData := "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"

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
			"contractAddress": baseSepoliaUsdcAddress.Hex(),
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
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, triggerData)

		require.NoError(t, err, "RunNodeImmediately should succeed with Tenderly simulation")
		require.NotNil(t, result, "Should get simulation result")

		// Verify simulation metadata is present
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			if firstResult, ok := results[0].(*avsproto.ContractWriteNode_MethodResult); ok {
				assert.NotNil(t, firstResult.Receipt, "Should have receipt data")

				// Extract receipt data
				var txHash string
				if firstResult.Receipt != nil {
					if receiptMap := firstResult.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
						if hash, ok := receiptMap["transactionHash"].(string); ok {
							txHash = hash
						}
					}
				}

				assert.NotEmpty(t, txHash, "Should have transaction hash in receipt")

				t.Logf("✅ Contract write simulation successful:")
				t.Logf("   Method: %s", firstResult.MethodName)
				t.Logf("   Success: %t", firstResult.Success)
				t.Logf("   Transaction Hash: %s", txHash)
				t.Logf("   Receipt: %+v", firstResult.Receipt.AsInterface())
			}
		}
	})
}
