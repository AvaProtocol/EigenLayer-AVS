package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunNodeImmediately_ContractWrite_MissingNodeDependency tests that when a ContractWrite node
// references another node's output in template variables (e.g., {{get_quote.data.amountOut}}),
// but that node's data is not provided in inputVariables, the system properly returns an error
// with a helpful message telling users to run the preceding node first.
func TestRunNodeImmediately_ContractWrite_MissingNodeDependency(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create test user - get owner EOA from environment
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping missing dependency test")
	}

	ownerEOA := *ownerAddr
	factory := config.SmartWallet.FactoryAddress

	// Connect to RPC client for GetSenderAddress
	client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()

	// Derive actual salt:0 smart wallet address
	aa.SetFactoryAddress(factory)
	runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")

	// Seed wallet in DB for validation
	_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
		Owner:   &ownerEOA,
		Address: runnerAddr,
		Factory: &factory,
		Salt:    big.NewInt(0),
	})

	user := &model.User{
		Address: ownerEOA,
	}

	t.Run("MissingNodeDependency_SimpleParam", func(t *testing.T) {
		// This simulates a swap node that depends on a quote node's output
		// but the quote node data is not provided in inputVariables
		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", // Uniswap SwapRouter02
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{
							"components": []interface{}{
								map[string]interface{}{"internalType": "address", "name": "tokenIn", "type": "address"},
								map[string]interface{}{"internalType": "address", "name": "tokenOut", "type": "address"},
								map[string]interface{}{"internalType": "uint24", "name": "fee", "type": "uint24"},
								map[string]interface{}{"internalType": "address", "name": "recipient", "type": "address"},
								map[string]interface{}{"internalType": "uint256", "name": "amountIn", "type": "uint256"},
								map[string]interface{}{"internalType": "uint256", "name": "amountOutMinimum", "type": "uint256"},
								map[string]interface{}{"internalType": "uint160", "name": "sqrtPriceLimitX96", "type": "uint160"},
							},
							"internalType": "struct IV3SwapRouter.ExactInputSingleParams",
							"name":         "params",
							"type":         "tuple",
						},
					},
					"name": "exactInputSingle",
					"outputs": []interface{}{
						map[string]interface{}{"internalType": "uint256", "name": "amountOut", "type": "uint256"},
					},
					"stateMutability": "payable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "exactInputSingle",
					// This methodParam references get_quote.data.amountOut which doesn't exist
					"methodParams": []interface{}{
						`["{{settings.pool.token0}}", "{{settings.pool.token1}}", "{{settings.pool.fee}}", "{{settings.runner}}", "{{settings.amount}}", "{{get_quote.data.amountOut}}", 0]`,
					},
				},
			},
			"value":       "0",
			"gasLimit":    "210000",
			"isSimulated": true,
		}

		// inputVariables only contains settings, NOT get_quote
		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"chain":    "Sepolia",
				"amount":   "10000",
				"runner":   runnerAddr.Hex(),
				"chain_id": 11155111,
				"pool": map[string]interface{}{
					"token0": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
					"token1": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					"fee":    "3000",
				},
			},
		}

		// Execute the node - should fail with helpful error
		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		// Should return an error
		require.Error(t, err, "Should return error when node dependency is missing")

		// Error message should mention the missing node
		assert.Contains(t, err.Error(), "get_quote", "Error should mention the missing node name")
		assert.Contains(t, err.Error(), "Make sure to run the 'get_quote' node first", "Error should provide helpful instruction")

		// Result should indicate failure
		if result != nil {
			success, ok := result["success"].(bool)
			if ok {
				assert.False(t, success, "Result should indicate failure")
			}
			errorMsg, ok := result["error"].(string)
			if ok {
				assert.NotEmpty(t, errorMsg, "Should have error message")
				assert.Contains(t, errorMsg, "get_quote", "Error message should mention missing node")
			}
		}

		t.Logf("✅ Missing node dependency error message: %v", err)
	})

	t.Run("MissingNodeDependency_MultipleParams", func(t *testing.T) {
		// Test with multiple template variables, some resolved and some not
		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{"name": "recipient", "type": "address"},
						map[string]interface{}{"name": "amount", "type": "uint256"},
					},
					"name":            "transfer",
					"outputs":         []interface{}{},
					"stateMutability": "payable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "transfer",
					// First param is OK (settings.recipient), second param is missing (node_a.data.amount)
					"methodParams": []interface{}{
						"{{settings.recipient}}",
						"{{node_a.data.amount}}",
					},
				},
			},
			"value":       "0",
			"gasLimit":    "100000",
			"isSimulated": true,
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"recipient": "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
				"runner":    runnerAddr.Hex(),
				"chain_id":  11155111,
			},
		}

		_, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		require.Error(t, err, "Should return error for missing node_a")
		assert.Contains(t, err.Error(), "node_a", "Error should mention node_a")
		assert.Contains(t, strings.ToLower(err.Error()), "could not resolve", "Error should mention variable resolution failure")

		t.Logf("✅ Multiple params error: %v", err)
	})

	t.Run("AllDependenciesProvided_ShouldSucceed", func(t *testing.T) {
		// Positive test: when all dependencies are provided, execution should succeed
		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{
							"components": []interface{}{
								map[string]interface{}{"internalType": "address", "name": "tokenIn", "type": "address"},
								map[string]interface{}{"internalType": "address", "name": "tokenOut", "type": "address"},
								map[string]interface{}{"internalType": "uint24", "name": "fee", "type": "uint24"},
								map[string]interface{}{"internalType": "address", "name": "recipient", "type": "address"},
								map[string]interface{}{"internalType": "uint256", "name": "amountIn", "type": "uint256"},
								map[string]interface{}{"internalType": "uint256", "name": "amountOutMinimum", "type": "uint256"},
								map[string]interface{}{"internalType": "uint160", "name": "sqrtPriceLimitX96", "type": "uint160"},
							},
							"internalType": "struct IV3SwapRouter.ExactInputSingleParams",
							"name":         "params",
							"type":         "tuple",
						},
					},
					"name": "exactInputSingle",
					"outputs": []interface{}{
						map[string]interface{}{"internalType": "uint256", "name": "amountOut", "type": "uint256"},
					},
					"stateMutability": "payable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "exactInputSingle",
					// Now get_quote.data.amountOut IS provided in inputVariables
					"methodParams": []interface{}{
						`["{{settings.pool.token0}}", "{{settings.pool.token1}}", "{{settings.pool.fee}}", "{{settings.runner}}", "{{settings.amount}}", "{{get_quote.data.amountOut}}", 0]`,
					},
				},
			},
			"value":       "0",
			"gasLimit":    "210000",
			"isSimulated": true,
		}

		// Now we provide BOTH settings AND get_quote data
		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"chain":    "Sepolia",
				"amount":   "10000",
				"runner":   runnerAddr.Hex(),
				"chain_id": 11155111,
				"pool": map[string]interface{}{
					"token0": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
					"token1": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					"fee":    "3000",
				},
			},
			"get_quote": map[string]interface{}{
				"data": map[string]interface{}{
					"amountOut": "9500", // Mock quote result
				},
			},
		}

		// Execute the node - should succeed now
		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		// Should NOT return an error
		require.NoError(t, err, "Should succeed when all dependencies are provided")
		require.NotNil(t, result, "Result should not be nil")

		// Check that execution succeeded
		success, ok := result["success"].(bool)
		require.True(t, ok, "Result should have success field")
		assert.True(t, success, "Execution should succeed with all dependencies provided")

		t.Logf("✅ All dependencies provided - execution succeeded")
	})
}
