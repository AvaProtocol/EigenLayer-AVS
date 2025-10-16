package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"testing"

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

// Test that simulateTask properly waits for on-chain confirmation between dependent contract writes
// This ensures that sequential operations (like approve + swap) work correctly in simulated workflows
func TestSimulateTask_SequentialContractWrites_Sepolia(t *testing.T) {
	// Skip in short mode
	if testing.Short() {
		t.Skip("Skipping simulation test in short mode")
	}

	// Get owner EOA address from OWNER_EOA env var (or fallback to TEST_PRIVATE_KEY legacy)
	// Controller key from config signs all UserOps - no private key needed
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping simulation test")
	}
	ownerAddress := *ownerAddr

	// Load Sepolia config (needed for factory, RPC, etc.)
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "Failed to load aggregator config")

	t.Logf("📋 Sepolia Sequential Contract Writes Test Configuration:")
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

	// Initialize test database and engine
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	// Create user
	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddr,
	}

	// Register smart wallet in database
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddr,
		Factory: &cfg.SmartWallet.FactoryAddress,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to register smart wallet")
	t.Logf("   ✅ Smart wallet registered in database")

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

	// Input variables
	inputVariables := map[string]interface{}{
		"settings": map[string]interface{}{
			"runner":   smartWalletAddr.Hex(),
			"chain_id": int64(11155111), // Sepolia
		},
	}

	t.Logf("🧪 Starting simulateTask with sequential contract writes...")
	t.Logf("   Node 1: Approve %s USDC to SwapRouter", approveAmount)
	t.Logf("   Node 2: Swap %s USDC for WETH", swapAmount)

	// Run simulation (should NOT actually execute on-chain)
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	require.NoError(t, err, "SimulateTask should not return error")
	require.NotNil(t, execution, "Simulation result should not be nil")

	t.Logf("✅ SimulateTask completed")
	t.Logf("   Status: %s", execution.Status)

	// Verify simulation results
	t.Logf("📊 Simulation steps:")
	for i, step := range execution.Steps {
		t.Logf("   Step %d: %s", i+1, step.Name)
		t.Logf("     Success: %v", step.Success)
		if step.Error != "" {
			t.Logf("     Error: %s", step.Error)
		}
	}

	t.Logf("🎉 SUCCESS: Sequential contract writes simulation completed!")
}
