package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestExecuteTask_SequentialContractWrites_Sepolia tests real execution (not simulation)
// of a deployed workflow with sequential contract writes (approve + swap) on Sepolia.
// This tests the full workflow execution path through the bundler.
func TestExecuteTask_SequentialContractWrites_Sepolia(t *testing.T) {
	// Skip in short mode
	if testing.Short() {
		t.Skip("Skipping real execution test in short mode")
	}

	// Get TEST_PRIVATE_KEY to derive the owner's EOA and smart wallet address
	// (automatically loaded from .env file by testutil)
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("TEST_PRIVATE_KEY not set, skipping real execution test")
	}
	ownerAddress := *ownerAddr

	// Load Sepolia config
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "Failed to load aggregator config")

	t.Logf("📋 Sepolia Sequential Contract Writes Execution Test:")
	t.Logf("   Owner EOA (derives wallet address): %s", ownerAddress.Hex())
	t.Logf("   Factory: %s", cfg.SmartWallet.FactoryAddress.Hex())
	t.Logf("   Chain: Sepolia (ID: %d)", cfg.SmartWallet.ChainID)

	// Set factory for AA library
	aa.SetFactoryAddress(cfg.SmartWallet.FactoryAddress)
	t.Logf("   Computing salt:0 smart wallet address...")

	// Connect to Sepolia
	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to Sepolia RPC")
	defer client.Close()

	// Compute the salt:0 smart wallet address for this owner
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")
	t.Logf("   ✅ Smart Wallet (salt:0): %s", smartWalletAddr.Hex())

	// Check smart wallet balance
	balance, err := client.BalanceAt(context.Background(), *smartWalletAddr, nil)
	require.NoError(t, err, "Failed to get wallet balance")
	t.Logf("   ETH Balance: %s wei", balance.String())

	if balance.Cmp(big.NewInt(0)) == 0 {
		t.Skip("Smart wallet has 0 ETH balance - fund it to run real execution test")
	}

	// Initialize test database and engine
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	// Register smart wallet in database
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddr,
		Factory: &cfg.SmartWallet.FactoryAddress,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to register smart wallet")
	t.Logf("   ✅ Smart wallet registered in database")

	// Create task executor
	executor := NewExecutor(cfg.SmartWallet, db, testutil.GetLogger())

	// ========================================
	// Create workflow with sequential contract writes:
	// 1. Approve USDC to SwapRouter
	// 2. Swap USDC for WETH
	// ========================================

	approveAmount := "1000000" // 1 USDC (6 decimals)
	swapAmount := "1000000"    // 1 USDC

	// Build nodes
	nodes := []*avsproto.TaskNode{
		{
			Id:   "approve_node",
			Name: "approve_node",
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: SEPOLIA_USDC,
						ContractAbi: func() []*structpb.Value {
							abi, _ := structpb.NewValue(map[string]interface{}{
								"inputs": []interface{}{
									map[string]interface{}{"name": "spender", "type": "address"},
									map[string]interface{}{"name": "amount", "type": "uint256"},
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
								MethodName:   "approve",
								MethodParams: []string{SEPOLIA_SWAPROUTER, approveAmount},
							},
						},
					},
				},
			},
		},
		{
			Id:   "swap_node",
			Name: "swap_node",
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: SEPOLIA_SWAPROUTER,
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
									fmt.Sprintf(`["%s", "%s", %d, "%s", "%s", "1", 0]`,
										SEPOLIA_USDC,
										SEPOLIA_WETH,
										SEPOLIA_FEE_TIER,
										smartWalletAddr.Hex(),
										swapAmount,
									),
								},
							},
						},
					},
				},
			},
		},
	}

	// Create edges
	edges := []*avsproto.TaskEdge{
		{
			Id:     "trigger_to_approve",
			Source: "manual_trigger",
			Target: "approve_node",
		},
		{
			Id:     "approve_to_swap",
			Source: "approve_node",
			Target: "swap_node",
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
						data, _ := structpb.NewValue(map[string]interface{}{"test": "manual"})
						return data
					}(),
				},
			},
		},
	}

	// Create the task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:                 "test-sequential-writes-real-exec",
			SmartWalletAddress: smartWalletAddr.Hex(),
			Owner:              ownerAddress.Hex(),
			Trigger:            trigger,
			Nodes:              nodes,
			Edges:              edges,
		},
	}

	t.Logf("   ✅ Task created: %s", task.Id)

	t.Logf("🚀 Starting real execution with sequential contract writes...")
	t.Logf("   Node 1: Approve %s USDC to SwapRouter", approveAmount)
	t.Logf("   Node 2: Swap %s USDC for WETH", swapAmount)

	// Create trigger data for manual trigger
	triggerData := &avsproto.ManualTrigger_Output{
		Data: func() *structpb.Value {
			data, _ := structpb.NewValue(map[string]interface{}{
				"runner":   smartWalletAddr.Hex(),
				"chain_id": int64(11155111), // Sepolia
			})
			return data
		}(),
	}

	// Execute the task (real execution, not simulation)
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: triggerData,
		ExecutionID:   fmt.Sprintf("exec-%d", time.Now().Unix()),
	})

	require.NoError(t, err, "RunTask should not return error")
	require.NotNil(t, execution, "Execution result should not be nil")

	t.Logf("✅ Execution completed")
	t.Logf("   Execution ID: %s", execution.Id)
	t.Logf("   Status: %s", execution.Status)

	// Verify execution results
	t.Logf("📊 Execution steps:")
	for i, step := range execution.Steps {
		t.Logf("   Step %d: %s", i+1, step.Name)
		t.Logf("     Success: %v", step.Success)
		if step.Error != "" {
			t.Logf("     Error: %s", step.Error)
		}
	}

	// Check that approve step succeeded
	// Find steps by name (order may vary)
	var approveStep, swapStep *avsproto.Execution_Step
	for _, step := range execution.Steps {
		if step.Name == "approve_node" {
			approveStep = step
		} else if step.Name == "swap_node" {
			swapStep = step
		}
	}

	require.NotNil(t, approveStep, "Should have approve_node execution step")
	require.True(t, approveStep.Success, "Approve step should succeed, error: %s", approveStep.Error)
	t.Logf("✅ Approve step executed successfully")

	// Swap step must exist and succeed for the test to pass
	require.NotNil(t, swapStep, "Should have swap_node execution step")
	require.True(t, swapStep.Success, "Swap step should succeed, error: %s", swapStep.Error)
	t.Logf("✅ Swap step executed successfully")

	// Verify overall execution status
	require.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status,
		"Overall execution should have SUCCESS status")

	t.Logf("🎉 SUCCESS: Sequential contract writes executed on-chain through bundler!")
}
