package taskengine

import (
	"math/big"
	"sync"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
					Type:         "CONTRACT_WRITE",
					Success:      true,
					GasUsed:      "21000",          // 21k gas
					GasPrice:     "1000000000",     // 1 gwei
					TotalGasCost: "21000000000000", // 21k * 1 gwei = 21000 gwei
				},
				{
					Id:           "step2",
					Type:         "CONTRACT_WRITE",
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
					Type:    "CUSTOM_CODE",
					Success: true,
				},
				{
					Id:      "step2",
					Type:    "REST_API",
					Success: true,
				},
			},
		}

		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, "0", totalGasCost, "Should return 0 when no gas-consuming steps are present")
	})

	t.Run("CalculateTotalGasCost with ETH_TRANSFER steps", func(t *testing.T) {
		vm := &VM{
			mu: &sync.Mutex{},
			ExecutionLogs: []*avsproto.Execution_Step{
				{
					Id:           "transfer1",
					Type:         "ETH_TRANSFER",
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
			Type:         "CONTRACT_WRITE",
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
					Type:         "CONTRACT_WRITE",
					TotalGasCost: "150000000000000",
				},
				{
					Id:           "step2",
					Type:         "ETH_TRANSFER",
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
