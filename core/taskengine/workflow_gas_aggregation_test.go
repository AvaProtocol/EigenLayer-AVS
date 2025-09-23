package taskengine

import (
	"math/big"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestAdvancedGasCostAggregation(t *testing.T) {
	t.Run("VM CalculateTotalGasCost with multiple blockchain steps", func(t *testing.T) {
		// Create a VM with multiple execution steps that have gas costs
		vm := NewVM()

		// Add mock execution steps with gas costs
		step1 := &avsproto.Execution_Step{
			Id:           "step1",
			Type:         "CONTRACT_WRITE",
			Success:      true,
			GasUsed:      "100000",           // 100k gas
			GasPrice:     "20000000000",      // 20 gwei
			TotalGasCost: "2000000000000000", // 0.002 ETH in wei
		}
		step2 := &avsproto.Execution_Step{
			Id:           "step2",
			Type:         "CONTRACT_WRITE",
			Success:      true,
			GasUsed:      "150000",           // 150k gas
			GasPrice:     "25000000000",      // 25 gwei
			TotalGasCost: "3750000000000000", // 0.00375 ETH in wei
		}
		step3 := &avsproto.Execution_Step{
			Id:           "step3",
			Type:         "ETH_TRANSFER",
			Success:      true,
			GasUsed:      "21000",           // Standard ETH transfer
			GasPrice:     "30000000000",     // 30 gwei
			TotalGasCost: "630000000000000", // 0.00063 ETH in wei
		}
		// Add a step without gas costs (should not affect aggregation)
		step4 := &avsproto.Execution_Step{
			Id:      "step4",
			Type:    "CUSTOM_CODE",
			Success: true,
			// No gas fields set
		}

		vm.ExecutionLogs = []*avsproto.Execution_Step{step1, step2, step3, step4}

		// Test gas cost calculation
		totalGasCost := vm.CalculateTotalGasCost()
		expectedTotal := new(big.Int)
		expectedTotal.SetString("6380000000000000", 10) // Sum of all gas costs

		assert.Equal(t, expectedTotal.String(), totalGasCost,
			"Total gas cost should be sum of all blockchain operation steps")
	})

	t.Run("Gas aggregation from mixed step types", func(t *testing.T) {
		// Create a VM with mixed step types
		vm := NewVM()

		// Contract write step with gas
		contractStep := &avsproto.Execution_Step{
			Id:           "contract1",
			Type:         "CONTRACT_WRITE",
			Success:      true,
			GasUsed:      "200000",
			GasPrice:     "15000000000",      // 15 gwei
			TotalGasCost: "3000000000000000", // 0.003 ETH
		}

		// ETH transfer step with gas
		ethStep := &avsproto.Execution_Step{
			Id:           "eth1",
			Type:         "ETH_TRANSFER",
			Success:      true,
			GasUsed:      "21000",
			GasPrice:     "15000000000",     // 15 gwei
			TotalGasCost: "315000000000000", // 0.000315 ETH
		}

		// Custom code step (no gas)
		codeStep := &avsproto.Execution_Step{
			Id:      "code1",
			Type:    "CUSTOM_CODE",
			Success: true,
		}

		vm.ExecutionLogs = []*avsproto.Execution_Step{contractStep, ethStep, codeStep}

		// Calculate expected total
		expectedTotal := new(big.Int)
		expectedTotal.SetString("3315000000000000", 10) // 3000000000000000 + 315000000000000

		// Test CalculateTotalGasCost
		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, expectedTotal.String(), totalGasCost,
			"Should aggregate gas costs only from blockchain operations")
	})

	t.Run("Gas cost aggregation with empty and invalid gas data", func(t *testing.T) {
		vm := NewVM()

		// Step with valid gas data
		validStep := &avsproto.Execution_Step{
			Id:           "valid",
			Type:         "CONTRACT_WRITE",
			Success:      true,
			GasUsed:      "50000",
			GasPrice:     "10000000000",
			TotalGasCost: "500000000000000",
		}

		// Step with empty gas data (should be ignored)
		emptyStep := &avsproto.Execution_Step{
			Id:           "empty",
			Type:         "CONTRACT_WRITE",
			Success:      true,
			GasUsed:      "",
			GasPrice:     "",
			TotalGasCost: "",
		}

		// Step with zero gas cost (should be ignored)
		zeroStep := &avsproto.Execution_Step{
			Id:           "zero",
			Type:         "ETH_TRANSFER",
			Success:      true,
			GasUsed:      "0",
			GasPrice:     "10000000000",
			TotalGasCost: "0",
		}

		// Non-blockchain step (should be ignored)
		nonBlockchainStep := &avsproto.Execution_Step{
			Id:      "custom",
			Type:    "CUSTOM_CODE",
			Success: true,
			// Gas fields should be ignored even if set
			GasUsed:      "100000",
			GasPrice:     "10000000000",
			TotalGasCost: "1000000000000000",
		}

		vm.ExecutionLogs = []*avsproto.Execution_Step{validStep, emptyStep, zeroStep, nonBlockchainStep}

		// Only the valid step should contribute to total gas cost
		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, "500000000000000", totalGasCost,
			"Should only aggregate valid gas costs from blockchain operations")
	})

	t.Run("Gas cost aggregation with no blockchain operations", func(t *testing.T) {
		vm := NewVM()

		// Only non-blockchain steps
		step1 := &avsproto.Execution_Step{
			Id:      "custom1",
			Type:    "CUSTOM_CODE",
			Success: true,
		}
		step2 := &avsproto.Execution_Step{
			Id:      "contract_read1",
			Type:    "CONTRACT_READ",
			Success: true,
		}

		vm.ExecutionLogs = []*avsproto.Execution_Step{step1, step2}

		totalGasCost := vm.CalculateTotalGasCost()
		assert.Equal(t, "0", totalGasCost,
			"Should return zero when no blockchain operations with gas costs are present")
	})
}

func TestStepTypeFiltering(t *testing.T) {
	t.Run("Only CONTRACT_WRITE and ETH_TRANSFER contribute to gas costs", func(t *testing.T) {
		vm := NewVM()

		// Create steps of various types, all with gas fields set
		gasCost := "1000000000000000" // 0.001 ETH

		steps := []*avsproto.Execution_Step{
			{
				Id: "contract_write", Type: "CONTRACT_WRITE", Success: true,
				GasUsed: "100000", GasPrice: "10000000000", TotalGasCost: gasCost,
			},
			{
				Id: "eth_transfer", Type: "ETH_TRANSFER", Success: true,
				GasUsed: "21000", GasPrice: "10000000000", TotalGasCost: "210000000000000",
			},
			{
				Id: "contract_read", Type: "CONTRACT_READ", Success: true,
				GasUsed: "50000", GasPrice: "10000000000", TotalGasCost: gasCost, // Should be ignored
			},
			{
				Id: "custom_code", Type: "CUSTOM_CODE", Success: true,
				GasUsed: "0", GasPrice: "10000000000", TotalGasCost: gasCost, // Should be ignored
			},
			{
				Id: "graphql_query", Type: "GRAPHQL_QUERY", Success: true,
				GasUsed: "75000", GasPrice: "10000000000", TotalGasCost: gasCost, // Should be ignored
			},
		}

		vm.ExecutionLogs = steps

		totalGasCost := vm.CalculateTotalGasCost()

		// Only CONTRACT_WRITE and ETH_TRANSFER should contribute
		expectedTotal := new(big.Int)
		expectedTotal.SetString("1000000000000000", 10) // CONTRACT_WRITE
		ethTotal := new(big.Int)
		ethTotal.SetString("210000000000000", 10) // ETH_TRANSFER
		expectedTotal.Add(expectedTotal, ethTotal)

		assert.Equal(t, expectedTotal.String(), totalGasCost,
			"Only CONTRACT_WRITE and ETH_TRANSFER steps should contribute to total gas cost")
	})
}
