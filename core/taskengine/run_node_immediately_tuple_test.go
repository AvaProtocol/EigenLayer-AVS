package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunNodeImmediately_ContractWrite_TupleWithTemplates tests contract write with tuple parameters
// that have template variable substitution - the exact scenario from the client app
func TestRunNodeImmediately_ContractWrite_TupleWithTemplates(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create test user - get owner EOA from environment
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping tuple test")
	}

	ownerEOA := *ownerAddr
	factory := config.SmartWallet.FactoryAddress

	// Derive actual salt:0 smart wallet address
	aa.SetFactoryAddress(factory)
	runnerAddr, err := aa.GetSenderAddress(nil, ownerEOA, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")

	user := &model.User{
		Address: ownerEOA,
	}

	// Uniswap V3 QuoterV2 ABI for quoteExactInputSingle
	quoterV2ABI := `[{
		"inputs": [{
			"components": [
				{"internalType": "address", "name": "tokenIn", "type": "address"},
				{"internalType": "address", "name": "tokenOut", "type": "address"},
				{"internalType": "uint256", "name": "amountIn", "type": "uint256"},
				{"internalType": "uint24", "name": "fee", "type": "uint24"},
				{"internalType": "uint160", "name": "sqrtPriceLimitX96", "type": "uint160"}
			],
			"internalType": "struct IQuoterV2.QuoteExactInputSingleParams",
			"name": "params",
			"type": "tuple"
		}],
		"name": "quoteExactInputSingle",
		"outputs": [
			{"internalType": "uint256", "name": "amountOut", "type": "uint256"},
			{"internalType": "uint160", "name": "sqrtPriceX96After", "type": "uint160"},
			{"internalType": "uint32", "name": "initializedTicksCrossed", "type": "uint32"},
			{"internalType": "uint256", "name": "gasEstimate", "type": "uint256"}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	}]`

	// Parse ABI to convert to protobuf Values
	parsedABI, err := abi.JSON(strings.NewReader(quoterV2ABI))
	require.NoError(t, err, "Should parse ABI successfully")

	t.Run("Tuple_Parameter_With_Template_Substitution_JSON_Array", func(t *testing.T) {
		// This test simulates the exact scenario from the client app:
		// - User provides settings with nested object structure
		// - methodParams contains a JSON array with template variables
		// - Templates reference settings.uniswapv3-pool.token0.id etc.

		// Seed wallet in DB for validation
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3", // Sepolia QuoterV2
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{
							"components": []interface{}{
								map[string]interface{}{"internalType": "address", "name": "tokenIn", "type": "address"},
								map[string]interface{}{"internalType": "address", "name": "tokenOut", "type": "address"},
								map[string]interface{}{"internalType": "uint256", "name": "amountIn", "type": "uint256"},
								map[string]interface{}{"internalType": "uint24", "name": "fee", "type": "uint24"},
								map[string]interface{}{"internalType": "uint160", "name": "sqrtPriceLimitX96", "type": "uint160"},
							},
							"internalType": "struct IQuoterV2.QuoteExactInputSingleParams",
							"name":         "params",
							"type":         "tuple",
						},
					},
					"name": "quoteExactInputSingle",
					"outputs": []interface{}{
						map[string]interface{}{"internalType": "uint256", "name": "amountOut", "type": "uint256"},
						map[string]interface{}{"internalType": "uint160", "name": "sqrtPriceX96After", "type": "uint160"},
						map[string]interface{}{"internalType": "uint32", "name": "initializedTicksCrossed", "type": "uint32"},
						map[string]interface{}{"internalType": "uint256", "name": "gasEstimate", "type": "uint256"},
					},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "quoteExactInputSingle",
					// ✅ This is the recommended format: JSON array with template variables inside
					"methodParams": []interface{}{
						`["{{settings.uniswapv3_pool.token0.id}}", "{{settings.uniswapv3_pool.token1.id}}", "{{settings.amount}}", "{{settings.uniswapv3_pool.fee_tier}}", 0]`,
					},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"chain":    "Sepolia",
				"amount":   "100000000000000000", // 0.1 ETH in wei
				"runner":   "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
				"chain_id": 11155111,
				"uniswapv3_pool": map[string]interface{}{
					"id": "0xeb502c739488180b106eded9902b7465a8c12edb",
					"token0": map[string]interface{}{
						"id":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", // WETH on Sepolia
						"symbol": "WETH",
					},
					"token1": map[string]interface{}{
						"id":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC on Sepolia
						"symbol": "USDC",
					},
					"fee_tier": "3000", // 0.3% fee tier
				},
			},
		}

		// Execute the node
		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		// Log the result for debugging
		t.Logf("RunNodeImmediately result: success=%v, error=%v", result["success"], result["error"])
		if metadata, ok := result["metadata"].([]interface{}); ok && len(metadata) > 0 {
			if metadataItem, ok := metadata[0].(map[string]interface{}); ok {
				t.Logf("Metadata: methodName=%v, success=%v, error=%v",
					metadataItem["methodName"], metadataItem["success"], metadataItem["error"])
			}
		}

		// Assertions
		require.NoError(t, err, "RunNodeImmediately should not return error")
		assert.NotNil(t, result, "Result should not be nil")

		// Check success flag
		success, ok := result["success"].(bool)
		require.True(t, ok, "Result should have success field")
		assert.True(t, success, "Execution should succeed")

		// Check metadata
		metadata, ok := result["metadata"].([]interface{})
		require.True(t, ok, "Result should have metadata array")
		require.Greater(t, len(metadata), 0, "Metadata should have at least one entry")

		metadataItem, ok := metadata[0].(map[string]interface{})
		require.True(t, ok, "Metadata item should be a map")

		// Verify method execution
		assert.Equal(t, "quoteExactInputSingle", metadataItem["methodName"], "Method name should match")
		assert.True(t, metadataItem["success"].(bool), "Method execution should succeed")

		// Verify that template substitution worked correctly
		// The backend should have:
		// 1. Resolved templates to actual values
		// 2. Parsed the JSON array
		// 3. Validated tuple element count (5 elements)
		// 4. Generated calldata successfully
		assert.Empty(t, metadataItem["error"], "Should not have any errors")

		t.Logf("✅ Tuple parameter with template substitution test passed")
		t.Logf("   - Templates resolved correctly")
		t.Logf("   - JSON array parsed successfully")
		t.Logf("   - Calldata generated for tuple parameter")
	})

	t.Run("Tuple_Parameter_With_Template_Substitution_JSON_Object", func(t *testing.T) {
		// Alternative format: JSON object with named fields
		// The backend will convert this to ordered array based on ABI

		// Seed wallet in DB for validation
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{
							"components": []interface{}{
								map[string]interface{}{"internalType": "address", "name": "tokenIn", "type": "address"},
								map[string]interface{}{"internalType": "address", "name": "tokenOut", "type": "address"},
								map[string]interface{}{"internalType": "uint256", "name": "amountIn", "type": "uint256"},
								map[string]interface{}{"internalType": "uint24", "name": "fee", "type": "uint24"},
								map[string]interface{}{"internalType": "uint160", "name": "sqrtPriceLimitX96", "type": "uint160"},
							},
							"internalType": "struct IQuoterV2.QuoteExactInputSingleParams",
							"name":         "params",
							"type":         "tuple",
						},
					},
					"name": "quoteExactInputSingle",
					"outputs": []interface{}{
						map[string]interface{}{"internalType": "uint256", "name": "amountOut", "type": "uint256"},
						map[string]interface{}{"internalType": "uint160", "name": "sqrtPriceX96After", "type": "uint160"},
						map[string]interface{}{"internalType": "uint32", "name": "initializedTicksCrossed", "type": "uint32"},
						map[string]interface{}{"internalType": "uint256", "name": "gasEstimate", "type": "uint256"},
					},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "quoteExactInputSingle",
					// ✅ Alternative format: JSON object (backend converts to array)
					"methodParams": []interface{}{
						`{"tokenIn": "{{settings.uniswapv3_pool.token0.id}}", "tokenOut": "{{settings.uniswapv3_pool.token1.id}}", "amountIn": "{{settings.amount}}", "fee": "{{settings.uniswapv3_pool.fee_tier}}", "sqrtPriceLimitX96": 0}`,
					},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"amount":   "100000000000000000",
				"runner":   "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
				"chain_id": 11155111,
				"uniswapv3_pool": map[string]interface{}{
					"token0": map[string]interface{}{
						"id": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
					},
					"token1": map[string]interface{}{
						"id": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					},
					"fee_tier": "3000",
				},
			},
		}

		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		require.NoError(t, err, "RunNodeImmediately should not return error")
		assert.NotNil(t, result, "Result should not be nil")

		success, ok := result["success"].(bool)
		require.True(t, ok, "Result should have success field")
		assert.True(t, success, "Execution should succeed")

		metadata, ok := result["metadata"].([]interface{})
		require.True(t, ok, "Result should have metadata array")
		require.Greater(t, len(metadata), 0, "Metadata should have at least one entry")

		metadataItem, ok := metadata[0].(map[string]interface{})
		require.True(t, ok, "Metadata item should be a map")

		assert.Equal(t, "quoteExactInputSingle", metadataItem["methodName"], "Method name should match")
		assert.True(t, metadataItem["success"].(bool), "Method execution should succeed")
		assert.Empty(t, metadataItem["error"], "Should not have any errors")

		t.Logf("✅ Tuple parameter with JSON object format test passed")
	})

	t.Run("Tuple_Parameter_Missing_Field_Error", func(t *testing.T) {
		// Test error handling when a required field is missing

		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
			"contractAbi":     quoterV2ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "quoteExactInputSingle",
					// Missing tokenOut field
					"methodParams": []interface{}{
						`{"tokenIn": "{{settings.token0}}", "amountIn": "{{settings.amount}}", "fee": "3000", "sqrtPriceLimitX96": 0}`,
					},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"token0": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
				"amount": "100000000000000000",
				"runner": "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
			},
		}

		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		// For node creation errors (like ABI format errors), expect nil result with error
		// For execution errors (like missing fields), expect result with success=false
		if err != nil && strings.Contains(err.Error(), "failed to create node") {
			// Node creation failed - this is expected for invalid ABI format
			assert.Nil(t, result, "Result should be nil for node creation errors")
			assert.Error(t, err, "Should have error for node creation failure")
			t.Logf("✅ Error handling test passed - node creation validation works")
			return
		}

		// If node was created but execution failed, check the result
		assert.NotNil(t, result, "Result should not be nil for execution errors")
		success, ok := result["success"].(bool)
		require.True(t, ok, "Result should have success field")
		assert.False(t, success, "Execution should fail due to missing field")

		metadata, ok := result["metadata"].([]interface{})
		require.True(t, ok, "Result should have metadata array")
		require.Greater(t, len(metadata), 0, "Metadata should have at least one entry")

		metadataItem, ok := metadata[0].(map[string]interface{})
		require.True(t, ok, "Metadata item should be a map")

		assert.False(t, metadataItem["success"].(bool), "Method execution should fail")
		assert.Contains(t, metadataItem["error"].(string), "tokenOut", "Error should mention missing field")

		t.Logf("✅ Error handling test passed - correctly detected missing field")
	})

	t.Run("Tuple_Parameter_Wrong_Element_Count_Error", func(t *testing.T) {
		// Test error handling when array has wrong number of elements

		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
			"contractAbi":     quoterV2ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "quoteExactInputSingle",
					// Only 3 elements instead of 5
					"methodParams": []interface{}{
						`["{{settings.token0}}", "{{settings.token1}}", "{{settings.amount}}"]`,
					},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"token0": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
				"token1": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
				"amount": "100000000000000000",
				"runner": "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
			},
		}

		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		// For node creation errors (like ABI format errors), expect nil result with error
		// For execution errors (like wrong element count), expect result with success=false
		if err != nil && strings.Contains(err.Error(), "failed to create node") {
			// Node creation failed - this is expected for invalid ABI format
			assert.Nil(t, result, "Result should be nil for node creation errors")
			assert.Error(t, err, "Should have error for node creation failure")
			t.Logf("✅ Error handling test passed - node creation validation works")
			return
		}

		assert.NotNil(t, result, "Result should not be nil for execution errors")
		success, ok := result["success"].(bool)
		require.True(t, ok, "Result should have success field")
		assert.False(t, success, "Execution should fail due to wrong element count")

		metadata, ok := result["metadata"].([]interface{})
		require.True(t, ok, "Result should have metadata array")
		require.Greater(t, len(metadata), 0, "Metadata should have at least one entry")

		metadataItem, ok := metadata[0].(map[string]interface{})
		require.True(t, ok, "Metadata item should be a map")

		assert.False(t, metadataItem["success"].(bool), "Method execution should fail")
		errorMsg := metadataItem["error"].(string)
		assert.True(t, strings.Contains(errorMsg, "expects 5 elements") || strings.Contains(errorMsg, "got 3"),
			"Error should mention element count mismatch")

		t.Logf("✅ Error handling test passed - correctly detected wrong element count")
	})

	t.Run("Mathematical_Expressions_With_Hyphen_Should_Work", func(t *testing.T) {
		// Test that mathematical expressions like {{var - 10}} are allowed
		// Only simple variable paths with hyphens should be rejected

		// Seed wallet in DB
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC Sepolia
			"contractAbi": []interface{}{
				map[string]interface{}{
					"inputs": []interface{}{
						map[string]interface{}{"name": "spender", "type": "address"},
						map[string]interface{}{"name": "amount", "type": "uint256"},
					},
					"name":            "approve",
					"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
					"stateMutability": "nonpayable",
					"type":            "function",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "approve",
					// Mathematical expression with hyphen should work
					"methodParams": []interface{}{
						"0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
						"{{settings.base_amount - 10}}", // Subtraction operator
					},
				},
			},
			"value":    "0",
			"gasLimit": "210000",
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"base_amount": "1000000", // Will subtract 10
				"runner":      "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
				"chain_id":    11155111,
			},
		}

		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		require.NoError(t, err, "RunNodeImmediately should not return error")
		assert.NotNil(t, result, "Result should not be nil")

		success, ok := result["success"].(bool)
		require.True(t, ok, "Result should have success field")
		assert.True(t, success, "Execution should succeed with mathematical expression")

		t.Logf("✅ Mathematical expression test passed - hyphens allowed in complex expressions")
	})

	// Verify ABI parsing works correctly
	t.Run("Verify_ABI_Structure", func(t *testing.T) {
		method := parsedABI.Methods["quoteExactInputSingle"]
		require.NotNil(t, method, "Should find quoteExactInputSingle method")
		require.Equal(t, 1, len(method.Inputs), "Method should have 1 input")
		require.Equal(t, abi.TupleTy, method.Inputs[0].Type.T, "Input should be a tuple type")
		require.Equal(t, 5, len(method.Inputs[0].Type.TupleElems), "Tuple should have 5 elements")

		// Verify field names
		expectedFields := []string{"tokenIn", "tokenOut", "amountIn", "fee", "sqrtPriceLimitX96"}
		for i, expected := range expectedFields {
			assert.Equal(t, expected, method.Inputs[0].Type.TupleRawNames[i],
				"Field %d name should match", i)
		}

		t.Logf("✅ ABI structure verified successfully")
		t.Logf("   - Method: %s", method.Name)
		t.Logf("   - Input type: %s", method.Inputs[0].Type.String())
		t.Logf("   - Tuple fields: %v", method.Inputs[0].Type.TupleRawNames)
	})
}
