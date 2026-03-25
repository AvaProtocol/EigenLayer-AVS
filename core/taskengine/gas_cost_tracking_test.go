package taskengine

import (
	"math/big"
	"sync"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGasCostTracking(t *testing.T) {
	t.Run("CalculateTotalGasCost with CONTRACT_WRITE steps", func(t *testing.T) {
		// Create a VM with some execution steps
		vm := &VM{
			mu: &sync.Mutex{},
			ExecutionLogs: []*avsproto.Execution_Step{
				{
					Id:           "step1",
					Type:         avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
					Success:      true,
					GasUsed:      "21000",          // 21k gas
					GasPrice:     "1000000000",     // 1 gwei
					TotalGasCost: "21000000000000", // 21k * 1 gwei = 21000 gwei
				},
				{
					Id:           "step2",
					Type:         avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
					Success:      true,
					GasUsed:      "50000",           // 50k gas
					GasPrice:     "2000000000",      // 2 gwei
					TotalGasCost: "100000000000000", // 50k * 2 gwei = 100000 gwei
				},
				{
					Id:           "step3",
					Type:         "CUSTOM_CODE", // Should be ignored
					Success:      true,
					GasUsed:      "10000",
					GasPrice:     "1000000000",
					TotalGasCost: "10000000000000",
				},
			},
		}

		totalGasCost := vm.CalculateTotalGasCost()

		// Expected: 21000000000000 + 100000000000000 = 121000000000000
		expected := new(big.Int)
		expected.SetString("121000000000000", 10)

		actual := new(big.Int)
		actual.SetString(totalGasCost, 10)

		assert.Equal(t, expected.String(), actual.String(), "Total gas cost should be sum of CONTRACT_WRITE steps only")
	})

	t.Run("CalculateTotalGasCost with no gas steps", func(t *testing.T) {
		vm := &VM{
			mu: &sync.Mutex{},
			ExecutionLogs: []*avsproto.Execution_Step{
				{
					Id:      "step1",
					Type:    avsproto.NodeType_NODE_TYPE_CUSTOM_CODE.String(),
					Success: true,
				},
				{
					Id:      "step2",
					Type:    avsproto.NodeType_NODE_TYPE_REST_API.String(),
					Success: true,
				},
			},
		}

		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, "", totalGasCost, "Should return empty string when no gas-consuming steps are present")
	})

	t.Run("CalculateTotalGasCost with ETH_TRANSFER steps", func(t *testing.T) {
		vm := &VM{
			mu: &sync.Mutex{},
			ExecutionLogs: []*avsproto.Execution_Step{
				{
					Id:           "transfer1",
					Type:         avsproto.NodeType_NODE_TYPE_ETH_TRANSFER.String(),
					Success:      true,
					GasUsed:      "21000",
					GasPrice:     "1500000000",     // 1.5 gwei
					TotalGasCost: "31500000000000", // 21k * 1.5 gwei
				},
			},
		}

		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, "31500000000000", totalGasCost, "Should include ETH_TRANSFER steps in gas cost calculation")
	})

	t.Run("Gas cost fields in protobuf step", func(t *testing.T) {
		step := &avsproto.Execution_Step{
			Id:           "test_step",
			Type:         avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Success:      true,
			GasUsed:      "75000",
			GasPrice:     "2500000000",      // 2.5 gwei
			TotalGasCost: "187500000000000", // 75k * 2.5 gwei
		}

		// Verify the fields are set correctly
		assert.Equal(t, "75000", step.GasUsed)
		assert.Equal(t, "2500000000", step.GasPrice)
		assert.Equal(t, "187500000000000", step.TotalGasCost)

		// Verify calculation matches
		gasUsed, _ := new(big.Int).SetString(step.GasUsed, 10)
		gasPrice, _ := new(big.Int).SetString(step.GasPrice, 10)
		expectedCost := new(big.Int).Mul(gasUsed, gasPrice)

		assert.Equal(t, expectedCost.String(), step.TotalGasCost)
	})
}

func TestGasCostExtractionFromTenderly(t *testing.T) {
	t.Run("getGasUsedFromTenderly with valid data", func(t *testing.T) {
		processor := &ContractWriteProcessor{}

		result := &ContractWriteSimulationResult{
			GasUsed: "45123", // Decimal string
		}

		gasUsedHex := processor.getGasUsedFromTenderly(result)

		// Convert back to verify
		gasUsed, ok := new(big.Int).SetString("45123", 10)
		require.True(t, ok, "Should parse gas used")
		expectedHex := "0x" + gasUsed.Text(16)

		assert.Equal(t, expectedHex, gasUsedHex)
	})

	t.Run("getGasUsedFromTenderly with fallback", func(t *testing.T) {
		processor := &ContractWriteProcessor{}

		// Test with nil result - should return empty string
		gasUsedHex := processor.getGasUsedFromTenderly(nil)
		assert.Equal(t, "", gasUsedHex, "Should return empty string when Tenderly result is nil")

		// Test with empty gas used - should return empty string
		result := &ContractWriteSimulationResult{
			GasUsed: "",
		}
		gasUsedHex = processor.getGasUsedFromTenderly(result)
		assert.Equal(t, "", gasUsedHex, "Should return empty string when gas data is unavailable")
	})

	t.Run("getGasPriceFromTenderly with valid data", func(t *testing.T) {
		processor := &ContractWriteProcessor{}

		result := &ContractWriteSimulationResult{
			GasPrice: "1234567890", // Decimal string
		}

		gasPriceHex := processor.getGasPriceFromTenderly(result)

		// Convert back to verify
		gasPrice, ok := new(big.Int).SetString("1234567890", 10)
		require.True(t, ok, "Should parse gas price")
		expectedHex := "0x" + gasPrice.Text(16)

		assert.Equal(t, expectedHex, gasPriceHex)
	})

	t.Run("getGasPriceFromTenderly with fallback", func(t *testing.T) {
		processor := &ContractWriteProcessor{}

		// Test with nil result - should return empty string
		gasPriceHex := processor.getGasPriceFromTenderly(nil)
		assert.Equal(t, "", gasPriceHex, "Should return empty string when Tenderly result is nil")

		// Test with empty gas price - should return empty string
		result := &ContractWriteSimulationResult{
			GasPrice: "",
		}
		gasPriceHex = processor.getGasPriceFromTenderly(result)
		assert.Equal(t, "", gasPriceHex, "Should return empty string when gas price data is unavailable")
	})
}

func TestWorkflowLevelGasCostAggregation(t *testing.T) {
	t.Run("Execution message should have TotalGasCost field", func(t *testing.T) {
		execution := &avsproto.Execution{
			Id:           "execution_123",
			TotalGasCost: "250000000000000", // Total gas cost for workflow
			Steps: []*avsproto.Execution_Step{
				{
					Id:           "step1",
					Type:         avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
					TotalGasCost: "150000000000000",
				},
				{
					Id:           "step2",
					Type:         avsproto.NodeType_NODE_TYPE_ETH_TRANSFER.String(),
					TotalGasCost: "100000000000000",
				},
			},
		}

		// Verify the total matches the sum of steps
		totalFromSteps := new(big.Int)
		for _, step := range execution.Steps {
			if step.TotalGasCost != "" {
				stepCost, ok := new(big.Int).SetString(step.TotalGasCost, 10)
				if ok {
					totalFromSteps.Add(totalFromSteps, stepCost)
				}
			}
		}

		actualTotal, ok := new(big.Int).SetString(execution.TotalGasCost, 10)
		require.True(t, ok, "Should parse total gas cost")

		assert.Equal(t, totalFromSteps.String(), actualTotal.String(), "Total gas cost should equal sum of step costs")
	})
}

func TestAggregateIterationGasCosts(t *testing.T) {
	t.Run("aggregates gas from iteration steps into parent loop step", func(t *testing.T) {
		parentStep := &avsproto.Execution_Step{
			Id:   "loop1",
			Type: avsproto.NodeType_NODE_TYPE_LOOP.String(),
		}

		iterSteps := []*avsproto.Execution_Step{
			{
				Id:           "loop1_iter_0",
				GasUsed:      "50000",
				GasPrice:     "1000000000",
				TotalGasCost: "50000000000000",
			},
			{
				Id:           "loop1_iter_1",
				GasUsed:      "60000",
				GasPrice:     "1000000000",
				TotalGasCost: "60000000000000",
			},
			{
				Id:           "loop1_iter_2",
				GasUsed:      "55000",
				GasPrice:     "1200000000",
				TotalGasCost: "66000000000000",
			},
		}

		aggregateIterationGasCosts(parentStep, iterSteps, nil)

		assert.Equal(t, "165000", parentStep.GasUsed, "GasUsed should be sum of all iterations")
		assert.Equal(t, "176000000000000", parentStep.TotalGasCost, "TotalGasCost should be sum of all iterations")
		// Implied price = totalGasCost / totalGasUsed = 176000000000000 / 165000 = 1066666666 (integer div)
		assert.Equal(t, "1066666666", parentStep.GasPrice, "GasPrice should be implied average (totalCost/totalUsed)")
	})

	t.Run("skips iterations without gas costs", func(t *testing.T) {
		parentStep := &avsproto.Execution_Step{Id: "loop1"}

		iterSteps := []*avsproto.Execution_Step{
			{Id: "iter_0", TotalGasCost: "0"},
			{Id: "iter_1", TotalGasCost: ""},
			{Id: "iter_2", GasUsed: "21000", GasPrice: "1000000000", TotalGasCost: "21000000000000"},
		}

		aggregateIterationGasCosts(parentStep, iterSteps, nil)

		assert.Equal(t, "21000", parentStep.GasUsed)
		assert.Equal(t, "21000000000000", parentStep.TotalGasCost)
	})

	t.Run("no-op with empty iteration steps", func(t *testing.T) {
		parentStep := &avsproto.Execution_Step{Id: "loop1"}
		aggregateIterationGasCosts(parentStep, nil, nil)
		assert.Equal(t, "", parentStep.TotalGasCost)
		assert.Equal(t, "", parentStep.GasUsed)
	})
}

func TestCalculateTotalGasCostWithLoopSteps(t *testing.T) {
	t.Run("includes LOOP steps with aggregated gas costs", func(t *testing.T) {
		vm := &VM{
			mu: &sync.Mutex{},
			ExecutionLogs: []*avsproto.Execution_Step{
				{
					Id:      "trigger",
					Type:    "manualTrigger",
					Success: true,
				},
				{
					Id:           "contractWrite1",
					Type:         avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
					Success:      true,
					GasUsed:      "30000",
					GasPrice:     "1000000000",
					TotalGasCost: "30000000000000",
				},
				{
					// Loop step with aggregated gas from iterations
					Id:           "loopTransfer",
					Type:         avsproto.NodeType_NODE_TYPE_LOOP.String(),
					Success:      true,
					GasUsed:      "110000",
					GasPrice:     "1000000000",
					TotalGasCost: "110000000000000",
				},
			},
		}

		totalGasCost := vm.CalculateTotalGasCost()

		// Expected: 30000000000000 + 110000000000000 = 140000000000000
		assert.Equal(t, "140000000000000", totalGasCost, "Should include both CONTRACT_WRITE and LOOP steps")
	})

	t.Run("LOOP step with zero gas is excluded", func(t *testing.T) {
		vm := &VM{
			mu: &sync.Mutex{},
			ExecutionLogs: []*avsproto.Execution_Step{
				{
					Id:           "contractWrite1",
					Type:         avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
					Success:      true,
					TotalGasCost: "50000000000000",
				},
				{
					// Loop with non-on-chain iterations (e.g., customCode) — no gas
					Id:      "loopCustomCode",
					Type:    avsproto.NodeType_NODE_TYPE_LOOP.String(),
					Success: true,
				},
			},
		}

		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, "50000000000000", totalGasCost, "Loop with no gas should not affect total")
	})
}

func TestUpdateStepGasCostFromReceipt(t *testing.T) {
	t.Run("sets gas fields from valid receipt", func(t *testing.T) {
		step := &avsproto.Execution_Step{Id: "step1"}
		receipt := &types.Receipt{
			GasUsed:           75000,
			EffectiveGasPrice: big.NewInt(2000000000), // 2 gwei
			TxHash:            common.HexToHash("0xabc"),
		}

		updateStepGasCostFromReceipt(step, receipt, nil)

		assert.Equal(t, "75000", step.GasUsed)
		assert.Equal(t, "2000000000", step.GasPrice)
		// 75000 * 2000000000 = 150000000000000
		assert.Equal(t, "150000000000000", step.TotalGasCost)
	})

	t.Run("no-op when GasUsed is zero", func(t *testing.T) {
		step := &avsproto.Execution_Step{Id: "step1"}
		receipt := &types.Receipt{
			GasUsed:           0,
			EffectiveGasPrice: big.NewInt(2000000000),
		}

		updateStepGasCostFromReceipt(step, receipt, nil)

		assert.Equal(t, "", step.GasUsed)
		assert.Equal(t, "", step.TotalGasCost)
	})

	t.Run("no-op when EffectiveGasPrice is nil", func(t *testing.T) {
		step := &avsproto.Execution_Step{Id: "step1"}
		receipt := &types.Receipt{
			GasUsed:           75000,
			EffectiveGasPrice: nil,
		}

		updateStepGasCostFromReceipt(step, receipt, nil)

		assert.Equal(t, "", step.GasUsed)
		assert.Equal(t, "", step.TotalGasCost)
	})

	t.Run("no-op when step is nil", func(t *testing.T) {
		receipt := &types.Receipt{
			GasUsed:           75000,
			EffectiveGasPrice: big.NewInt(2000000000),
		}
		// Should not panic
		updateStepGasCostFromReceipt(nil, receipt, nil)
	})

	t.Run("no-op when receipt is nil", func(t *testing.T) {
		step := &avsproto.Execution_Step{Id: "step1"}
		updateStepGasCostFromReceipt(step, nil, nil)
		assert.Equal(t, "", step.GasUsed)
	})
}
