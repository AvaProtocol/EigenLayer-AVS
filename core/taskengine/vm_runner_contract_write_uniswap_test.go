package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestContractWriteNode_UniswapV3Quote tests the complete flow of calling
// a contract method with a tuple parameter using the Uniswap V3 QuoterV2 as an example.
// This validates:
// 1. Proper tuple ABI definition with components field
// 2. Template variable resolution in JSON array format for tuple parameters
// 3. Calldata generation for tuple parameters
// 4. Tenderly simulation with the generated calldata
//
// Note: The QuoterV2.quoteExactInputSingle is actually a view function that uses try/catch
// and reverts to return data, so the simulation will show "execution reverted" but still
// demonstrates that the calldata generation for tuple parameters works correctly.
func TestContractWriteNode_UniswapV3Quote(t *testing.T) {
	// Get test configuration and create Tenderly client
	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	if testConfig == nil {
		t.Skip("No test config available (requires aggregator.yaml + Tenderly)")
	}

	// Create Tenderly client
	tenderlyClient := NewTenderlyClient(testConfig, logger)
	if tenderlyClient == nil {
		t.Skip("No Tenderly client available")
	}

	// Setup the test VM with necessary config
	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	vm, err := NewVMWithData(nil, nil, smartWalletConfig, nil)
	require.NoError(t, err)
	require.NotNil(t, vm)

	// Set the Tenderly client on the VM
	vm.tenderlyClient = tenderlyClient

	// Set simulation mode
	vm.SetSimulation(true)
	vm.TaskOwner = common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")

	// Create input variables matching the Uniswap V3 quote scenario.
	// aa_sender is required for contract write nodes and is passed via inputVariables
	// so that RunNodeWithInputs populates it in the temporary VM.
	inputVars := map[string]interface{}{
		"aa_sender": "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
		"settings": map[string]interface{}{
			"chain":    "Sepolia",
			"amount":   "10",
			"runner":   "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
			"chain_id": 11155111,
			"uniswap_v3_pool": map[string]interface{}{
				"id": "0xee8027d8430344ba3419f844ba858ac7f1a92095",
				"token0": map[string]interface{}{
					"id":     "0x019d3c1576190e5396db92e987e5631fbb318aeb",
					"symbol": "WETH",
				},
				"token1": map[string]interface{}{
					"id":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					"symbol": "USDC",
				},
				"feeTier": "3000",
			},
			"uniswap_v3_contracts": map[string]interface{}{
				"quoterV2":     "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
				"swapRouter02": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
			},
		},
	}

	// Create node config
	// Convert ABI to protobuf Value
	// Note: For tuple parameters, we MUST define the components field for proper ABI parsing
	abiValue, err := structpb.NewValue(map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{
				"name": "params",
				"type": "tuple",
				"components": []interface{}{
					map[string]interface{}{
						"name": "tokenIn",
						"type": "address",
					},
					map[string]interface{}{
						"name": "tokenOut",
						"type": "address",
					},
					map[string]interface{}{
						"name": "amountIn",
						"type": "uint256",
					},
					map[string]interface{}{
						"name": "fee",
						"type": "uint24",
					},
					map[string]interface{}{
						"name": "sqrtPriceLimitX96",
						"type": "uint160",
					},
				},
			},
		},
		"name":            "quoteExactInputSingle",
		"outputs":         []interface{}{},
		"stateMutability": "nonpayable",
		"type":            "function",
	})
	require.NoError(t, err)

	// Create the node config
	nodeConfig := &avsproto.ContractWriteNode_Config{
		ContractAddress: "0xed1f6473345f45b75f8179591dd5ba1888cf2fb3",
		ContractAbi:     []*structpb.Value{abiValue},
		MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
			{
				MethodName: "quoteExactInputSingle",
				MethodParams: []string{
					// One parameter - a JSON array representing the tuple
					// Template variables resolve to their values, which are then placed in the JSON
					`["{{settings.uniswap_v3_pool.token1.id}}", "{{settings.uniswap_v3_pool.token0.id}}", {{settings.amount}}, {{settings.uniswap_v3_pool.feeTier}}, 0]`,
				},
			},
		},
	}

	// Create the contract write node
	node := &avsproto.TaskNode{
		Name: "uniswap_quote_test",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: nodeConfig,
			},
		},
	}

	// Seed ERC20 state overrides so the simulation no longer reverts with
	// "transfer amount exceeds allowance/balance" before reaching the swap logic.
	// This exercises the same SimulationStateMap path that RunNodeImmediately
	// populates from a request's erc20_overrides. We approve the SwapRouter02 to
	// spend the runner's USDC and give the runner a large USDC balance.
	usdc := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	owner := "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e"
	swapRouter02 := "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
	require.NotNil(t, vm.simulationState, "simulation state must exist in simulation mode")
	bigBalance := "0x38d7ea4c68000"                                                      // 1,000,000 USDC (6 decimals)
	maxAllowance := "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" // max uint256
	// Cover both the standard ERC20 layout (balance slot 0 / allowance slot 3) and
	// USDC's FiatToken layout (9 / 10) so the override lands regardless of which
	// the deployed test token uses. Balance and allowance get their OWN slots.
	for _, balSlot := range []uint64{0, 9} {
		s := balSlot
		require.NoError(t, vm.simulationState.ApplyUserERC20Override(
			usdc, owner, "", bigBalance, "", &s, nil))
	}
	for _, allowSlot := range []uint64{3, 10} {
		s := allowSlot
		require.NoError(t, vm.simulationState.ApplyUserERC20Override(
			usdc, owner, swapRouter02, "", maxAllowance, nil, &s))
	}

	// Execute the node
	step, err := vm.RunNodeWithInputs(node, inputVars)
	require.NoError(t, err)
	require.NotNil(t, step)

	// With balance + allowance seeded, the simulation must never fail on an
	// approval/funding precondition — that's exactly what erc20_overrides fix.
	// QuoterV2.quoteExactInputSingle still reverts by design (it returns the
	// quote via revert data), so "execution reverted" remains acceptable; an
	// allowance/balance revert would mean the override path regressed.
	assert.NotContains(t, step.Error, "transfer amount exceeds allowance",
		"erc20_overrides should have seeded the allowance — got: %s", step.Error)
	assert.NotContains(t, step.Error, "transfer amount exceeds balance",
		"erc20_overrides should have seeded the balance — got: %s", step.Error)
	assert.False(t, vm.simulationState.IsEmpty(),
		"simulation state should hold the seeded ERC20 overrides")

	if step.Success {
		t.Logf("✅ Swap simulation succeeded with ERC20 overrides")
		contractWrite, ok := step.OutputData.(*avsproto.Execution_Step_ContractWrite)
		require.True(t, ok, "Step output should be ContractWrite")
		require.NotNil(t, contractWrite)
		require.NotNil(t, contractWrite.ContractWrite)
		require.NotNil(t, contractWrite.ContractWrite.Data)
		t.Logf("Swap output: %v", contractWrite.ContractWrite.Data.AsInterface())
	} else {
		// Acceptable: QuoterV2's intentional revert-with-data. The key assertions
		// above already prove the allowance/balance preconditions were satisfied.
		require.Contains(t, step.Error, "execution reverted",
			"only QuoterV2's intentional revert is acceptable once overrides are seeded, got: %s", step.Error)
		t.Logf("ℹ️  QuoterV2 reverted by design; allowance/balance overrides were applied correctly")
	}
}
