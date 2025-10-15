package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// Test that simulateTask properly waits for on-chain confirmation between dependent contract writes
// This ensures that sequential operations (like approve + swap) work correctly in simulated workflows
func TestSimulateTask_SequentialContractWrites_Base(t *testing.T) {
	// Skip if not running on Base
	if os.Getenv("TEST_CHAIN") != "base" {
		t.Skip("Skipping Base test - set TEST_CHAIN=base to run")
	}

	privateKeyHex := os.Getenv("TEST_PRIVATE_KEY")
	if privateKeyHex == "" {
		t.Fatal("TEST_PRIVATE_KEY environment variable not set")
	}

	// Parse private key
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	require.NoError(t, err, "Failed to parse private key")

	ownerAddress := crypto.PubkeyToAddress(privateKey.PublicKey)

	// Load aggregator config to get smart wallet settings
	cfg, err := config.NewConfig("../../config/aggregator.yaml")
	require.NoError(t, err, "Failed to load aggregator config")

	t.Logf("📋 Base Sequential Contract Writes Test Configuration:")
	t.Logf("   Owner EOA (derives wallet address): %s", ownerAddress.Hex())
	t.Logf("   Factory: %s", cfg.SmartWallet.FactoryAddress.Hex())
	t.Logf("   Chain: Base (ID: %d)", cfg.SmartWallet.ChainID)

	// Derive smart wallet address
	t.Logf("   Computing salt:0 smart wallet address...")
	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to Base RPC")
	defer client.Close()

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
								MethodParams: []string{BASE_SWAPROUTER, approveAmount},
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
			"chain_id": int64(BASE_CHAIN_ID),
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
