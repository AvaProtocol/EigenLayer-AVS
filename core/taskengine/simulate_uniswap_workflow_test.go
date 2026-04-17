package taskengine

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// Sepolia Uniswap V3 addresses (from studio/app/lib/uniswap/v3/data/sepolia.json)
const (
	SEPOLIA_QUOTER_V2 = "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3"
)

// TestSimulateTask_StopLossWorkflow_Sepolia replicates the complete stop-loss-on-uniswap
// Studio template as a SimulateTask integration test. This exercises the full node chain:
//
//	trigger → get_balance → calculate_swap_amount → has_enough_balance (branch)
//	  └─ if true → approve_token → get_swap_quote → calculate_slippage → execute_swap
//
// The critical assertion is that approve_token's allowance state propagates through
// to execute_swap via SimulationStateMap. Secondary assertions verify that balance,
// code, branch, and contractRead nodes all work in the full workflow context.
//
// Uses salt:0 wallet with real Sepolia USDC balance but zero on-chain allowance
// to SwapRouter02, making the approve→swap propagation conclusive.
func TestSimulateTask_StopLossWorkflow_Sepolia(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Sepolia integration test in short mode")
	}

	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("OWNER_EOA not set; skipping")
	}
	ownerAddress := *ownerAddr

	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "load aggregator config")

	aa.SetFactoryAddress(cfg.SmartWallet.FactoryAddress)

	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "connect to Sepolia RPC")
	defer client.Close()

	// salt:0 — zero on-chain allowance to SwapRouter02
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "derive smart wallet address")
	t.Logf("Owner EOA:    %s", ownerAddress.Hex())
	t.Logf("Smart wallet: %s (salt:0)", smartWalletAddr.Hex())

	usdcBalance := fetchERC20Balance(t, client, SEPOLIA_USDC, smartWalletAddr.Hex())
	t.Logf("USDC balance: %s (6 decimals)", usdcBalance.String())
	require.True(t, usdcBalance.Cmp(big.NewInt(2_000_000)) >= 0,
		"wallet must hold at least 2 USDC; fund %s", smartWalletAddr.Hex())

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(func() { engine.Stop() })

	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddr,
	}
	require.NoError(t,
		StoreWallet(db, ownerAddress, &model.SmartWallet{
			Owner:   &ownerAddress,
			Address: smartWalletAddr,
			Factory: &cfg.SmartWallet.FactoryAddress,
			Salt:    big.NewInt(0),
		}),
		"register smart wallet",
	)

	runner := smartWalletAddr.Hex()
	swapAmount := "2000000"    // 2 USDC (fixed, not MAX — simpler for test)
	approveAmount := "4000000" // 4 USDC (> swap to cover Uniswap fee buffer)

	// ========================================================================
	// Nodes — mirrors studio/templates/stop-loss-on-uniswap.json node graph
	// ========================================================================

	// Node 1: get_balance — fetch wallet ETH + USDC balances
	getBalance := &avsproto.TaskNode{
		Id:   "get_balance",
		Name: "get_balance",
		Type: avsproto.NodeType_NODE_TYPE_BALANCE,
		TaskType: &avsproto.TaskNode_Balance{
			Balance: &avsproto.BalanceNode{
				Config: &avsproto.BalanceNode_Config{
					Address: runner,
					Chain:   "sepolia",
					TokenAddresses: []string{
						"0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", // ETH
						SEPOLIA_USDC,
					},
				},
			},
		},
	}

	// Node 2: calculate_swap_amount — JS code that reads balance + settings
	// Simplified from template: uses fixed amount instead of MAX logic
	calculateSwapAmount := &avsproto.TaskNode{
		Id:   "calculate_swap_amount",
		Name: "calculate_swap_amount",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Lang: avsproto.Lang_LANG_JAVASCRIPT,
					Source: fmt.Sprintf(`
const inputBalance = get_balance.data.find(
  (token) => token.tokenAddress &&
    token.tokenAddress.toLowerCase() === "%s".toLowerCase()
);
const hasEnoughBalance = inputBalance && Number(inputBalance.balanceFormatted) > 0;
const swapAmount = "%s";
return { inputBalance, hasEnoughBalance, swapAmount };
`, SEPOLIA_USDC, swapAmount),
				},
			},
		},
	}

	// Node 3: has_enough_balance — branch on balance check
	hasEnoughBalance := &avsproto.TaskNode{
		Id:   "has_enough_balance",
		Name: "has_enough_balance",
		Type: avsproto.NodeType_NODE_TYPE_BRANCH,
		TaskType: &avsproto.TaskNode_Branch{
			Branch: &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: []*avsproto.BranchNode_Condition{
						{Id: "0", Type: "if", Expression: "{{calculate_swap_amount.data.hasEnoughBalance}}"},
						{Id: "1", Type: "else", Expression: ""},
					},
				},
			},
		},
	}

	// Node 4: approve_token — ERC20 approve(SwapRouter02, amount)
	approveToken := &avsproto.TaskNode{
		Id:   "approve_token",
		Name: "approve_token",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: SEPOLIA_USDC,
					ContractAbi: []*structpb.Value{sv(t, map[string]interface{}{
						"type": "function", "name": "approve", "stateMutability": "nonpayable",
						"inputs": []interface{}{
							map[string]interface{}{"name": "spender", "type": "address"},
							map[string]interface{}{"name": "amount", "type": "uint256"},
						},
						"outputs": []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
					})},
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{
						MethodName:   "approve",
						MethodParams: []string{SEPOLIA_SWAPROUTER, approveAmount},
					}},
				},
			},
		},
	}

	// Node 5: get_swap_quote — QuoterV2.quoteExactInputSingle (contractRead)
	getSwapQuote := &avsproto.TaskNode{
		Id:   "get_swap_quote",
		Name: "get_swap_quote",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
		TaskType: &avsproto.TaskNode_ContractRead{
			ContractRead: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: SEPOLIA_QUOTER_V2,
					ContractAbi: []*structpb.Value{sv(t, map[string]interface{}{
						"type": "function", "name": "quoteExactInputSingle", "stateMutability": "nonpayable",
						"inputs": []interface{}{
							map[string]interface{}{
								"name": "params", "type": "tuple",
								"components": []interface{}{
									map[string]interface{}{"name": "tokenIn", "type": "address"},
									map[string]interface{}{"name": "tokenOut", "type": "address"},
									map[string]interface{}{"name": "amountIn", "type": "uint256"},
									map[string]interface{}{"name": "fee", "type": "uint24"},
									map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
								},
							},
						},
						"outputs": []interface{}{
							map[string]interface{}{"name": "amountOut", "type": "uint256"},
							map[string]interface{}{"name": "sqrtPriceX96After", "type": "uint160"},
							map[string]interface{}{"name": "initializedTicksCrossed", "type": "uint32"},
							map[string]interface{}{"name": "gasEstimate", "type": "uint256"},
						},
					})},
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{{
						MethodName: "quoteExactInputSingle",
						MethodParams: []string{fmt.Sprintf(
							`["%s", "%s", "{{calculate_swap_amount.data.swapAmount}}", 3000, 0]`,
							SEPOLIA_USDC, SEPOLIA_WETH,
						)},
					}},
				},
			},
		},
	}

	// Node 6: calculate_slippage — JS code applying slippage to quote output
	calculateSlippage := &avsproto.TaskNode{
		Id:   "calculate_slippage",
		Name: "calculate_slippage",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Lang: avsproto.Lang_LANG_JAVASCRIPT,
					Source: `
const amountOut = BigInt(get_swap_quote.data.quoteExactInputSingle.amountOut);
const slippagePercent = 3n;
const amountOutMinimum = (amountOut * (100n - slippagePercent)) / 100n;
return { amountOutMinimum: amountOutMinimum.toString() };
`,
				},
			},
		},
	}

	// Node 7: execute_swap — SwapRouter02.exactInputSingle (contractWrite)
	executeSwap := &avsproto.TaskNode{
		Id:   "execute_swap",
		Name: "execute_swap",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: SEPOLIA_SWAPROUTER,
					ContractAbi: []*structpb.Value{sv(t, map[string]interface{}{
						"type": "function", "name": "exactInputSingle", "stateMutability": "payable",
						"inputs": []interface{}{
							map[string]interface{}{
								"name": "params", "type": "tuple",
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
						"outputs": []interface{}{map[string]interface{}{"name": "amountOut", "type": "uint256"}},
					})},
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{
						MethodName: "exactInputSingle",
						MethodParams: []string{fmt.Sprintf(
							`["%s", "%s", 3000, "%s", "{{calculate_swap_amount.data.swapAmount}}", "{{calculate_slippage.data.amountOutMinimum}}", 0]`,
							SEPOLIA_USDC, SEPOLIA_WETH, runner,
						)},
					}},
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		getBalance, calculateSwapAmount, hasEnoughBalance,
		approveToken, getSwapQuote, calculateSlippage, executeSwap,
	}

	// ========================================================================
	// Edges — mirrors the template's node graph
	// ========================================================================
	edges := []*avsproto.TaskEdge{
		{Id: "e1", Source: "manual_trigger", Target: "get_balance"},
		{Id: "e2", Source: "get_balance", Target: "calculate_swap_amount"},
		{Id: "e3", Source: "calculate_swap_amount", Target: "has_enough_balance"},
		// Branch: condition "0" (if true) → approve path
		{Id: "e4", Source: "has_enough_balance.0", Target: "approve_token"},
		{Id: "e5", Source: "approve_token", Target: "get_swap_quote"},
		{Id: "e6", Source: "get_swap_quote", Target: "calculate_slippage"},
		{Id: "e7", Source: "calculate_slippage", Target: "execute_swap"},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "manual_trigger",
		Name: "manual_trigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: sv(t, map[string]interface{}{"test": "full_workflow"}),
				},
			},
		},
	}

	inputVariables := map[string]interface{}{
		"settings": map[string]interface{}{
			"name":     "Stop-Loss on Uniswap V3",
			"runner":   runner,
			"chain_id": int64(11155111),
			"chain":    "sepolia",
		},
	}

	// ========================================================================
	// Execute
	// ========================================================================
	t.Log("--- Running SimulateTask (full stop-loss workflow) ---")
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
	require.NoError(t, err, "SimulateTask should not return a transport error")
	require.NotNil(t, execution, "execution must not be nil")

	// ========================================================================
	// Diagnostic dump
	// ========================================================================
	stepsByID := map[string]*avsproto.Execution_Step{}
	for _, s := range execution.Steps {
		stepsByID[s.Id] = s
	}

	t.Logf("Execution status: %s", execution.Status)
	t.Logf("Execution error : %q", execution.Error)
	for i, s := range execution.Steps {
		t.Logf("Step %d: id=%-25s name=%-25s success=%v", i, s.Id, s.Name, s.Success)
		if s.Error != "" {
			t.Logf("  error: %s", s.Error)
		}
		if s.Log != "" {
			for _, ln := range strings.Split(strings.TrimSpace(s.Log), "\n") {
				t.Logf("  log: %s", ln)
			}
		}
	}

	// ========================================================================
	// Per-step assertions
	// ========================================================================

	// Trigger
	triggerStep := stepsByID["manual_trigger"]
	require.NotNil(t, triggerStep, "trigger step must exist")
	assert.True(t, triggerStep.Success, "trigger must succeed")

	// get_balance — fetches real on-chain balances via RPC
	balanceStep := stepsByID["get_balance"]
	require.NotNil(t, balanceStep, "get_balance step must exist")
	assert.True(t, balanceStep.Success, "get_balance must succeed; error=%q", balanceStep.Error)

	// calculate_swap_amount — JS code reads balance output
	calcStep := stepsByID["calculate_swap_amount"]
	require.NotNil(t, calcStep, "calculate_swap_amount step must exist")
	assert.True(t, calcStep.Success, "calculate_swap_amount must succeed; error=%q", calcStep.Error)

	// has_enough_balance — branch should take the "if" (true) path
	branchStep := stepsByID["has_enough_balance"]
	require.NotNil(t, branchStep, "branch step must exist")
	assert.True(t, branchStep.Success, "branch must succeed; error=%q", branchStep.Error)

	// approve_token — ERC20 approve simulation
	approveStep := stepsByID["approve_token"]
	require.NotNil(t, approveStep, "approve_token step must exist (branch took true path)")
	assert.True(t, approveStep.Success, "approve must succeed; error=%q", approveStep.Error)

	// get_swap_quote — QuoterV2 contractRead
	quoteStep := stepsByID["get_swap_quote"]
	require.NotNil(t, quoteStep, "get_swap_quote step must exist")
	assert.True(t, quoteStep.Success, "quote must succeed; error=%q", quoteStep.Error)

	// calculate_slippage — JS code reads quote output
	slippageStep := stepsByID["calculate_slippage"]
	require.NotNil(t, slippageStep, "calculate_slippage step must exist")
	assert.True(t, slippageStep.Success, "slippage calc must succeed; error=%q", slippageStep.Error)

	// execute_swap — the final assertion: swap must succeed
	swapStep := stepsByID["execute_swap"]
	require.NotNil(t, swapStep, "execute_swap step must exist")
	if !swapStep.Success {
		t.Logf("SWAP FAILURE DIAGNOSIS:")
		t.Logf("  error: %s", swapStep.Error)
		switch {
		case strings.Contains(strings.ToLower(swapStep.Error), "allowance"):
			t.Log("  => Allowance propagation from approve_token failed")
		case strings.Contains(strings.ToLower(swapStep.Error), "balance"):
			t.Log("  => Insufficient token balance")
		case strings.Contains(strings.ToLower(swapStep.Error), "slippage") ||
			strings.Contains(strings.ToLower(swapStep.Error), "too little received"):
			t.Log("  => Slippage tolerance exceeded — pool liquidity or price moved")
		default:
			t.Log("  => Unrecognized failure — check step log above")
		}
	}
	assert.True(t, swapStep.Success,
		"swap must succeed — full workflow propagation test. error=%q", swapStep.Error)

	// Whole-execution status
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status,
		"workflow terminal status should be SUCCESS")
}

// sv is a shorthand for mustStructValue to keep node definitions compact.
func sv(t *testing.T, v interface{}) *structpb.Value {
	t.Helper()
	val, err := structpb.NewValue(v)
	require.NoError(t, err, "structpb.NewValue")
	return val
}

// fetchERC20Balance calls balanceOf(holder) on the token contract.
func fetchERC20Balance(t *testing.T, client *ethclient.Client, token, holder string) *big.Int {
	t.Helper()
	holderBytes := common.HexToAddress(holder).Bytes()
	callData := append([]byte{0x70, 0xa0, 0x82, 0x31}, common.LeftPadBytes(holderBytes, 32)...)
	to := common.HexToAddress(token)

	result, err := client.CallContract(context.Background(), ethereum.CallMsg{
		To:   &to,
		Data: callData,
	}, nil)
	if err != nil {
		t.Logf("fetchERC20Balance: CallContract failed: %v (raw callData=0x%s)", err, hex.EncodeToString(callData))
		return big.NewInt(0)
	}
	return new(big.Int).SetBytes(result)
}
