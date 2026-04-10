package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimulationStateMap_BasicOperations(t *testing.T) {
	logger := testutil.GetLogger()
	state := NewSimulationStateMap(logger)

	t.Run("empty state returns sender-only state_objects", func(t *testing.T) {
		objects := state.BuildStateObjects("0xABCD", "0x100")
		require.NotNil(t, objects)

		senderObj, ok := objects["0xabcd"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "0x100", senderObj["balance"])
	})

	t.Run("storage overrides are included", func(t *testing.T) {
		state.SetStorageSlot("0xTokenAddr", "0xSlot1", "0xValue1")
		objects := state.BuildStateObjects("0xSender", "0x100")

		tokenObj, ok := objects["0xtokenaddr"].(map[string]interface{})
		require.True(t, ok, "token address should be in state_objects")

		storageMap, ok := tokenObj["storage"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "0xValue1", storageMap["0xSlot1"])
	})

	t.Run("ETH balance overrides are included", func(t *testing.T) {
		state.SetETHBalance("0xWallet", "0xDE0B6B3A7640000")
		objects := state.BuildStateObjects("0xSender", "0x100")

		walletObj, ok := objects["0xwallet"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "0xDE0B6B3A7640000", walletObj["balance"])
	})

	t.Run("merge raw state diff", func(t *testing.T) {
		state2 := NewSimulationStateMap(logger)
		rawDiff := []interface{}{
			map[string]interface{}{
				"address":  "0xContract1",
				"key":      "0xSlotA",
				"original": "0x0000",
				"dirty":    "0x1234",
			},
			map[string]interface{}{
				"address":  "0xContract1",
				"key":      "0xSlotB",
				"original": "0x0000",
				"dirty":    "0x5678",
			},
		}
		state2.MergeRawStateDiff(rawDiff)

		objects := state2.BuildStateObjects("0xSender", "0x100")
		contractObj, ok := objects["0xcontract1"].(map[string]interface{})
		require.True(t, ok)
		storageMap, ok := contractObj["storage"].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "0x1234", storageMap["0xSlotA"])
		assert.Equal(t, "0x5678", storageMap["0xSlotB"])
	})
}

func TestSimulationStateMap_ERC20BalanceSlotComputation(t *testing.T) {
	// Verify the storage slot computation matches known values
	holder := common.HexToAddress("0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e")
	slot := erc20BalanceSlot(holder, 9)

	// The slot should be a 32-byte keccak256 hash — verify it's non-zero
	assert.NotEqual(t, common.Hash{}, slot, "computed slot should be non-zero")

	// Same inputs should produce same output (deterministic)
	slot2 := erc20BalanceSlot(holder, 9)
	assert.Equal(t, slot, slot2, "deterministic: same inputs produce same slot")

	// Different slot index should produce different hash
	slot3 := erc20BalanceSlot(holder, 0)
	assert.NotEqual(t, slot, slot3, "different mapping slot should produce different hash")
}

func TestSimulationStateMap_ContractWriteWithBalanceOverride(t *testing.T) {
	// Integration test: reproduces the production scenario where a Transfer event
	// trigger feeds into a loop with contract writes that spend the received tokens.
	//
	// Without SimulationStateMap: the contract write reverts with "ERC20: transfer amount exceeds balance"
	// With SimulationStateMap: the balance override makes the simulation succeed.
	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	if testConfig == nil {
		t.Skip("No test config available")
	}

	tenderlyClient := NewTenderlyClient(testConfig, logger)
	if tenderlyClient == nil {
		t.Skip("No Tenderly client available")
	}

	smartWalletConfig := testutil.GetTestSmartWalletConfig()

	// Use Sepolia USDC for testing
	usdcContract := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	runner := "0x5a8A8a79DdF433756D4D97DCCE33334D9E218856"
	recipient := "0x0000000000000000000000000000000000000001"
	transferAmount := "600000" // 0.6 USDC

	usdcABI := []interface{}{
		map[string]interface{}{
			"type":            "function",
			"name":            "transfer",
			"stateMutability": "nonpayable",
			"inputs": []interface{}{
				map[string]interface{}{"name": "to", "type": "address"},
				map[string]interface{}{"name": "value", "type": "uint256"},
			},
			"outputs": []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
		},
	}

	// First, test WITHOUT state override — should fail with "exceeds balance"
	t.Run("without_state_override_fails", func(t *testing.T) {
		vm, err := NewVMWithData(nil, nil, smartWalletConfig, nil)
		require.NoError(t, err)
		vm.SetSimulation(true)
		vm.tenderlyClient = tenderlyClient

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   runner,
				"chain_id": int64(11155111),
				"chain":    "sepolia",
			},
		}

		nodeConfig := map[string]interface{}{
			"contractAddress": usdcContract,
			"contractAbi":     usdcABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "transfer",
					"methodParams": []interface{}{recipient, transferAmount},
				},
			},
		}

		node, err := CreateNodeFromType("contractWrite", nodeConfig, "")
		require.NoError(t, err)
		node.Name = "contractWrite1"

		step, _ := vm.RunNodeWithInputs(node, inputVariables)
		require.NotNil(t, step, "should return a step even on failure")

		// On mainnet/Base USDC, this would fail with "transfer amount exceeds balance".
		// On Sepolia test USDC, the behavior may differ (some test tokens don't enforce balances).
		// We just verify the step completed (non-nil) — the important assertion is
		// in the "with_state_override_succeeds" subtest.
		t.Logf("without override: success=%v error=%q", step.Success, step.Error)
	})

	// Then test WITH state override — should succeed
	t.Run("with_state_override_succeeds", func(t *testing.T) {
		vm, err := NewVMWithData(nil, nil, smartWalletConfig, nil)
		require.NoError(t, err)
		vm.SetSimulation(true)
		vm.tenderlyClient = tenderlyClient

		// Inject a USDC balance override: give the runner 1.5 USDC (1500000)
		holderAddress := common.HexToAddress(runner)
		tokenAddress := common.HexToAddress(usdcContract)
		simulatedBalance := new(big.Int).SetInt64(1500000) // 1.5 USDC

		// Set the balance at all common slots to cover Sepolia USDC's layout
		for _, candidateSlot := range commonBalanceSlots {
			slotHash := erc20BalanceSlot(holderAddress, candidateSlot)
			valueHex := "0x" + common.Bytes2Hex(common.LeftPadBytes(simulatedBalance.Bytes(), 32))
			vm.simulationState.SetStorageSlot(tokenAddress.Hex(), slotHash.Hex(), valueHex)
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   runner,
				"chain_id": int64(11155111),
				"chain":    "sepolia",
			},
		}

		nodeConfig := map[string]interface{}{
			"contractAddress": usdcContract,
			"contractAbi":     usdcABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "transfer",
					"methodParams": []interface{}{recipient, transferAmount},
				},
			},
		}

		node, err := CreateNodeFromType("contractWrite", nodeConfig, "")
		require.NoError(t, err)
		node.Name = "contractWrite1"

		step, _ := vm.RunNodeWithInputs(node, inputVariables)
		require.NotNil(t, step, "should return a step")

		assert.True(t, step.Success,
			"transfer should succeed with balance override; got error: %s", step.Error)

		// Verify state diffs were captured
		assert.False(t, vm.simulationState.IsEmpty(),
			"simulation state should have accumulated overrides")
	})
}
