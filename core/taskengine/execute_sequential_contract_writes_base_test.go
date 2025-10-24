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
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// setupSequentialContractWritesTest performs common setup for sequential contract write tests
type sequentialContractWritesTestSetup struct {
	cfg               *config.Config
	ownerAddress      common.Address
	controllerAddress common.Address
	smartWalletAddr   *common.Address
	client            *ethclient.Client
	executor          *TaskExecutor
}

func setupSequentialContractWritesTest(t *testing.T) *sequentialContractWritesTestSetup {
	// Skip in short mode
	if testing.Short() {
		t.Skip("Skipping real execution test in short mode")
	}

	// Load Base aggregator config - skip if not available (local dev only)
	baseAggregatorCfg, err := config.NewConfig(testutil.GetConfigPath("aggregator-base.yaml"))
	if err != nil {
		t.Skipf("aggregator-base.yaml not found - skipping Base test (local dev only): %v", err)
	}

	// Skip if not running on Base (chain ID 8453)
	tempClient, err := ethclient.Dial(baseAggregatorCfg.SmartWallet.EthRpcUrl)
	if err != nil {
		t.Skipf("Cannot connect to Base RPC: %v", err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}
	if chainID.Int64() != BASE_CHAIN_ID {
		t.Skipf("Test requires Base network connection (current chain ID: %d)", chainID.Int64())
	}

	// Architecture: The smart wallet address is derived from the OWNER's EOA address
	// but the UserOperation is SIGNED by the CONTROLLER's private key (from config)
	// This allows the controller to automate transactions on behalf of the owner's smart wallet
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping real execution test")
	}
	ownerAddress := *ownerAddr
	controllerAddress := baseAggregatorCfg.SmartWallet.ControllerAddress

	t.Logf("📋 Base Sequential Contract Writes Execution Test:")
	t.Logf("   Owner EOA (derives wallet address): %s", ownerAddress.Hex())
	t.Logf("   Controller (signs UserOps): %s", controllerAddress.Hex())
	t.Logf("   Factory: %s", BASE_FACTORY)
	t.Logf("   Chain: Base (ID: %d)", BASE_CHAIN_ID)

	// Set factory for AA library
	aa.SetFactoryAddress(common.HexToAddress(BASE_FACTORY))
	t.Logf("   Computing salt:0 smart wallet address...")

	// Connect to Base
	client, err := ethclient.Dial(baseAggregatorCfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to Base RPC")
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
	factory := common.HexToAddress(BASE_FACTORY)
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddr,
		Factory: &factory,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to register smart wallet")
	t.Logf("   ✅ Smart wallet registered in database")

	// Create task engine and executor with engine reference for atomic indexing
	engine := New(db, baseAggregatorCfg, nil, testutil.GetLogger())
	executor := NewExecutor(baseAggregatorCfg.SmartWallet, db, testutil.GetLogger(), engine)

	return &sequentialContractWritesTestSetup{
		cfg:               baseAggregatorCfg,
		ownerAddress:      ownerAddress,
		controllerAddress: controllerAddress,
		smartWalletAddr:   smartWalletAddr,
		client:            client,
		executor:          executor,
	}
}

// buildSequentialContractWritesWorkflow creates the task with approve + swap nodes
func buildSequentialContractWritesWorkflow(smartWalletAddr *common.Address, ownerAddress common.Address) *model.Task {
	// Use small amount for repeated testing (0.01 USDC)
	// Matches BASE_SWAP_AMOUNT constant defined in run_node_uniswap_swap_base_test.go

	// Build nodes
	nodes := []*avsproto.TaskNode{
		{
			Id:   "approve_node",
			Name: "approve_node",
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: BASE_USDC,
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
								MethodParams: []string{BASE_SWAPROUTER, BASE_SWAP_AMOUNT},
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
						ContractAddress: BASE_SWAPROUTER,
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
										BASE_USDC,
										BASE_WETH,
										BASE_FEE_TIER,
										smartWalletAddr.Hex(),
										BASE_SWAP_AMOUNT, // Use same small amount (0.01 USDC)
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

	// Return the task
	return &model.Task{
		Task: &avsproto.Task{
			Id:                 "test-sequential-writes-real-exec-base",
			SmartWalletAddress: smartWalletAddr.Hex(),
			Owner:              ownerAddress.Hex(),
			Trigger:            trigger,
			Nodes:              nodes,
			Edges:              edges,
		},
	}
}

// TestExecuteTask_SequentialContractWrites_Base_SelfFunded tests deployed workflow
// execution with self-funded UserOperations (no paymaster sponsorship).
func TestExecuteTask_SequentialContractWrites_Base_SelfFunded(t *testing.T) {
	setup := setupSequentialContractWritesTest(t)
	defer setup.client.Close()

	t.Logf("💰 Test Mode: SELF-FUNDED (no paymaster)")
	task := buildSequentialContractWritesWorkflow(setup.smartWalletAddr, setup.ownerAddress)
	t.Logf("   ✅ Task created: %s", task.Id)

	t.Logf("🚀 Starting real execution with sequential contract writes on Base...")
	t.Logf("   Node 1: Approve %s USDC to SwapRouter (0.01 USDC)", BASE_SWAP_AMOUNT)
	t.Logf("   Node 2: Swap %s USDC for WETH (0.01 USDC)", BASE_SWAP_AMOUNT)
	t.Logf("   Funding: Self-funded (smart wallet pays gas)")

	// Check USDC balance before swap (fail early with clear message)
	usdcContract, err := erc20.NewErc20(common.HexToAddress(BASE_USDC), setup.client)
	require.NoError(t, err, "Failed to create USDC contract binding")

	usdcBalance, err := usdcContract.BalanceOf(nil, *setup.smartWalletAddr)
	require.NoError(t, err, "Failed to get USDC balance")

	requiredAmount := big.NewInt(10000) // 0.01 USDC (6 decimals) - tiny amount for repeated testing
	require.True(t, usdcBalance.Cmp(requiredAmount) >= 0,
		"wallet %s needs at least 0.01 USDC (10000 raw) before swap; have %s",
		setup.smartWalletAddr.Hex(), usdcBalance.String())

	t.Logf("✅ USDC Balance Check: %s (%.6f USDC) - sufficient for swap",
		usdcBalance.String(), float64(usdcBalance.Int64())/1e6)

	// Create trigger data for manual trigger (no paymaster override = auto-decision)
	triggerData := &avsproto.ManualTrigger_Output{
		Data: func() *structpb.Value {
			data, _ := structpb.NewValue(map[string]interface{}{
				"settings": map[string]interface{}{
					"runner":   setup.smartWalletAddr.Hex(),
					"chain_id": int64(BASE_CHAIN_ID),
					// No shouldUsePaymaster override - let the system decide based on balance
				},
			})
			return data
		}(),
	}

	// Execute the task (real execution, not simulation, self-funded)
	execution, err := setup.executor.RunTask(task, &QueueExecutionData{
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: triggerData,
		ExecutionID:   fmt.Sprintf("exec-self-funded-%d", time.Now().Unix()),
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

	t.Logf("🎉 SUCCESS: Self-funded sequential contract writes executed on-chain!")
}

// TestExecuteTask_SequentialContractWrites_Base_Paymaster tests deployed workflow
// execution with paymaster-sponsored UserOperations.
func TestExecuteTask_SequentialContractWrites_Base_Paymaster(t *testing.T) {
	setup := setupSequentialContractWritesTest(t)
	defer setup.client.Close()

	t.Logf("💳 Test Mode: PAYMASTER SPONSORED")

	// Check if paymaster is configured
	if setup.cfg.SmartWallet.PaymasterAddress == (common.Address{}) {
		t.Skip("Paymaster not configured - skipping paymaster sponsorship test")
	}

	t.Logf("   Paymaster: %s", setup.cfg.SmartWallet.PaymasterAddress.Hex())

	task := buildSequentialContractWritesWorkflow(setup.smartWalletAddr, setup.ownerAddress)

	// Modify task to use a different ID for paymaster test
	task.Id = "test-sequential-writes-paymaster-base"
	t.Logf("   ✅ Task created: %s", task.Id)

	t.Logf("🚀 Starting real execution with sequential contract writes on Base...")
	t.Logf("   Node 1: Approve %s USDC to SwapRouter (0.01 USDC)", BASE_SWAP_AMOUNT)
	t.Logf("   Node 2: Swap %s USDC for WETH (0.01 USDC)", BASE_SWAP_AMOUNT)
	t.Logf("   Funding: Paymaster sponsored (gas covered by paymaster)")

	// Create trigger data for manual trigger with paymaster override in settings
	triggerData := &avsproto.ManualTrigger_Output{
		Data: func() *structpb.Value {
			data, _ := structpb.NewValue(map[string]interface{}{
				"settings": map[string]interface{}{
					"runner":             setup.smartWalletAddr.Hex(),
					"chain_id":           int64(BASE_CHAIN_ID),
					"shouldUsePaymaster": true, // Force paymaster sponsorship
				},
			})
			return data
		}(),
	}

	// Execute the task (real execution, not simulation, paymaster-sponsored)
	execution, err := setup.executor.RunTask(task, &QueueExecutionData{
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: triggerData,
		ExecutionID:   fmt.Sprintf("exec-paymaster-%d", time.Now().Unix()),
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
	t.Logf("✅ Approve step executed successfully with paymaster sponsorship")

	// Swap step must exist and succeed for the test to pass
	require.NotNil(t, swapStep, "Should have swap_node execution step")
	require.True(t, swapStep.Success, "Swap step should succeed, error: %s", swapStep.Error)
	t.Logf("✅ Swap step executed successfully with paymaster sponsorship")

	// Verify overall execution status
	require.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status,
		"Overall execution should have SUCCESS status")

	t.Logf("🎉 SUCCESS: Paymaster-sponsored sequential contract writes executed on-chain!")
}
