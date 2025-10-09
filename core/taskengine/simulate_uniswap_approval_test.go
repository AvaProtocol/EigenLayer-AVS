package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestUniswapApprovalAmountSimulation tests whether Uniswap swap fails due to insufficient approval
// and validates the theory that approval needs to be higher than the exact swap amount
func TestUniswapApprovalAmountSimulation(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Get smart wallet address for funding
	smartWalletAddress := user.SmartAccountAddress.Hex()
	t.Logf("🏦 Smart Wallet Address for funding: %s", smartWalletAddress)
	t.Logf("Please fund this address with:")
	t.Logf("- USDC on Sepolia: 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	t.Logf("- At least 15 USDC for testing (you have this amount)")

	// Uniswap V3 contracts and pool on Sepolia
	uniswapV3Contracts := map[string]interface{}{
		"quoterV2":     "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
		"swapRouter02": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
	}

	uniswapV3Pool := map[string]interface{}{
		"id":      "0xee8027d8430344ba3419f844ba858ac7f1a92095",
		"feeTier": "3000",
		"token0": map[string]interface{}{
			"id":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", // WETH
			"symbol": "WETH",
		},
		"token1": map[string]interface{}{
			"id":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC
			"symbol": "USDC",
		},
	}

	testCases := []struct {
		name           string
		swapAmount     string
		approvalAmount string
		expectSuccess  bool
		description    string
	}{
		{
			name:           "Equal approval and swap amount",
			swapAmount:     "1000000", // 1 USDC in wei (6 decimals)
			approvalAmount: "1000000", // Same as swap amount
			expectSuccess:  false,
			description:    "Should fail - approval exactly equals swap amount",
		},
		{
			name:           "Higher approval than swap amount",
			swapAmount:     "1000000", // 1 USDC in wei
			approvalAmount: "5000000", // 5 USDC in wei (5x more)
			expectSuccess:  true,
			description:    "Should succeed - approval is higher than swap amount",
		},
		{
			name:           "Much higher approval",
			swapAmount:     "1000000",  // 1 USDC in wei
			approvalAmount: "10000000", // 10 USDC in wei (10x more)
			expectSuccess:  true,
			description:    "Should succeed - very high approval amount",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			// Create input variables for this test case
			inputVariables := map[string]interface{}{
				"settings": map[string]interface{}{
					"name":                 "Uniswap Approval Test",
					"chain":                "Sepolia",
					"amount":               tc.swapAmount,
					"runner":               smartWalletAddress,
					"chain_id":             11155111,
					"workflow_name":        "Approval Test",
					"uniswap_v3_pool":      uniswapV3Pool,
					"uniswap_v3_contracts": uniswapV3Contracts,
				},
				"approval_amount": tc.approvalAmount,
			}

			// Create workflow nodes
			nodes := []*avsproto.TaskNode{
				// 1. Balance check node
				{
					Id:   "balance_check",
					Name: "balance_check",
					Type: avsproto.NodeType_NODE_TYPE_BALANCE,
					TaskType: &avsproto.TaskNode_Balance{
						Balance: &avsproto.BalanceNode{
							Config: &avsproto.BalanceNode_Config{
								Address: smartWalletAddress,
								Chain:   "sepolia",
								TokenAddresses: []string{
									"{{settings.uniswap_v3_pool.token0.id}}",
									"{{settings.uniswap_v3_pool.token1.id}}",
								},
							},
						},
					},
				},
				// 2. Approval node with configurable amount
				{
					Id:   "approve_usdc",
					Name: "approve_usdc",
					Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
					TaskType: &avsproto.TaskNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "{{settings.uniswap_v3_pool.token1.id}}", // USDC
								ContractAbi: func() []*structpb.Value {
									abi, _ := structpb.NewValue(map[string]interface{}{
										"inputs": []interface{}{
											map[string]interface{}{"name": "spender", "type": "address"},
											map[string]interface{}{"name": "value", "type": "uint256"},
										},
										"name":            "approve",
										"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
										"stateMutability": "nonpayable",
										"type":            "function",
									})
									return []*structpb.Value{abi}
								}(),
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
									{
										MethodName: "approve",
										MethodParams: []string{
											"{{settings.uniswap_v3_contracts.swapRouter02}}",
											"{{approval_amount}}", // Use the configurable approval amount
										},
									},
								},
							},
						},
					},
				},
				// 3. Get quote
				{
					Id:   "get_quote",
					Name: "get_quote",
					Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
					TaskType: &avsproto.TaskNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "{{settings.uniswap_v3_contracts.quoterV2}}",
								ContractAbi: func() []*structpb.Value {
									abi, _ := structpb.NewValue(map[string]interface{}{
										"inputs": []interface{}{
											map[string]interface{}{
												"name": "params",
												"type": "tuple",
												"components": []interface{}{
													map[string]interface{}{"name": "tokenIn", "type": "address"},
													map[string]interface{}{"name": "tokenOut", "type": "address"},
													map[string]interface{}{"name": "amountIn", "type": "uint256"},
													map[string]interface{}{"name": "fee", "type": "uint24"},
													map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
												},
											},
										},
										"name": "quoteExactInputSingle",
										"outputs": []interface{}{
											map[string]interface{}{"name": "amountOut", "type": "uint256"},
											map[string]interface{}{"name": "sqrtPriceX96After", "type": "uint160"},
											map[string]interface{}{"name": "initializedTicksCrossed", "type": "uint32"},
											map[string]interface{}{"name": "gasEstimate", "type": "uint256"},
										},
										"stateMutability": "nonpayable",
										"type":            "function",
									})
									return []*structpb.Value{abi}
								}(),
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
									{
										MethodName: "quoteExactInputSingle",
										MethodParams: []string{
											`["{{settings.uniswap_v3_pool.token1.id}}", "{{settings.uniswap_v3_pool.token0.id}}", "{{settings.amount}}", "{{settings.uniswap_v3_pool.feeTier}}", 0]`,
										},
									},
								},
							},
						},
					},
				},
				// 4. Execute swap
				{
					Id:   "execute_swap",
					Name: "execute_swap",
					Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
					TaskType: &avsproto.TaskNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "{{settings.uniswap_v3_contracts.swapRouter02}}",
								ContractAbi: func() []*structpb.Value {
									abi, _ := structpb.NewValue(map[string]interface{}{
										"inputs": []interface{}{
											map[string]interface{}{
												"name": "params",
												"type": "tuple",
												"components": []interface{}{
													map[string]interface{}{"name": "tokenIn", "type": "address"},
													map[string]interface{}{"name": "tokenOut", "type": "address"},
													map[string]interface{}{"name": "fee", "type": "uint24"},
													map[string]interface{}{"name": "recipient", "type": "address"},
													map[string]interface{}{"name": "amountIn", "type": "uint256"},
													map[string]interface{}{"name": "amountOutMinimum", "type": "uint256"},
													map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
												},
											},
										},
										"name":            "exactInputSingle",
										"outputs":         []interface{}{map[string]interface{}{"name": "amountOut", "type": "uint256"}},
										"stateMutability": "payable",
										"type":            "function",
									})
									return []*structpb.Value{abi}
								}(),
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
									{
										MethodName: "exactInputSingle",
										MethodParams: []string{
											`["{{settings.uniswap_v3_pool.token1.id}}", "{{settings.uniswap_v3_pool.token0.id}}", "{{settings.uniswap_v3_pool.feeTier}}", "{{settings.runner}}", "{{settings.amount}}", "{{get_quote.data.quoteExactInputSingle.amountOut}}", 0]`,
										},
									},
								},
							},
						},
					},
				},
			}

			// Create trigger
			trigger := &avsproto.TaskTrigger{
				Id:   "manual_trigger",
				Name: "manual_trigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{
							Lang: avsproto.Lang_LANG_JSON,
							Data: func() *structpb.Value {
								data, _ := structpb.NewValue(map[string]interface{}{
									"test": "manual execution",
								})
								return data
							}(),
						},
					},
				},
			}

			// Create edges (simple linear flow)
			edges := []*avsproto.TaskEdge{
				{
					Id:     "trigger_to_balance",
					Source: "manual_trigger",
					Target: "balance_check",
				},
				{
					Id:     "balance_to_approve",
					Source: "balance_check",
					Target: "approve_usdc",
				},
				{
					Id:     "approve_to_quote",
					Source: "approve_usdc",
					Target: "get_quote",
				},
				{
					Id:     "quote_to_swap",
					Source: "get_quote",
					Target: "execute_swap",
				},
			}

			// Execute simulation
			execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
			require.NoError(t, err, "SimulateTask should not return an error")
			require.NotNil(t, execution, "Execution should not be nil")

			// Log execution results
			t.Logf("Execution Status: %s", execution.Status)
			if execution.Error != "" {
				t.Logf("Execution Error: %s", execution.Error)
			}
			t.Logf("Total Steps: %d", len(execution.Steps))

			// Find the swap step to check its result
			var swapStep *avsproto.Execution_Step
			for _, step := range execution.Steps {
				if step.Name == "execute_swap" {
					swapStep = step
					break
				}
			}

			if swapStep != nil {
				t.Logf("Swap Step Success: %t", swapStep.Success)
				if swapStep.Error != "" {
					t.Logf("Swap Step Error: %s", swapStep.Error)
				}

				// Validate expectation
				if tc.expectSuccess {
					assert.True(t, swapStep.Success, "Expected swap to succeed with higher approval amount")
					assert.Empty(t, swapStep.Error, "Expected no error for successful swap")
				} else {
					assert.False(t, swapStep.Success, "Expected swap to fail with equal approval amount")
					assert.Contains(t, swapStep.Error, "allowance", "Expected allowance-related error")
				}
			} else {
				t.Logf("⚠️ Swap step not found - workflow may have failed earlier")
				// If we expect success but didn't reach swap step, that's also a failure
				if tc.expectSuccess {
					t.Errorf("Expected to reach swap step but it was not found")
				}
			}

			t.Logf("✅ Test case '%s' completed", tc.name)
		})
	}
}

// TestUniswapApprovalAmountSimulation_PrintAddresses just prints the smart wallet address for manual funding
func TestUniswapApprovalAmountSimulation_PrintAddresses(t *testing.T) {
	user := testutil.TestUser1()
	smartWalletAddress := user.SmartAccountAddress.Hex()

	t.Logf("=== FUNDING INFORMATION ===")
	t.Logf("Smart Wallet Address: %s", smartWalletAddress)
	t.Logf("Network: Sepolia")
	t.Logf("USDC Contract: 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	t.Logf("WETH Contract: 0xfff9976782d46cc05630d1f6ebab18b2324d6b14")
	t.Logf("Fund the smart wallet with at least 15 USDC before running the main test")
	t.Logf("==========================")
}
