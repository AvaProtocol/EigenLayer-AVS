package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiscoverPoolAddressesFromLogs(t *testing.T) {
	// Create a mock ContractWriteProcessor for testing
	processor := &ContractWriteProcessor{
		CommonProcessor: &CommonProcessor{
			vm: &VM{
				logger: &MockLogger{}, // Use existing MockLogger
			},
		},
	}

	t.Run("Should discover Uniswap V3 Swap pool address", func(t *testing.T) {
		// Mock logs array based on the actual transaction from the workflow
		logsArray := []interface{}{
			map[string]interface{}{
				"address": "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789", // EntryPoint
				"topics": []interface{}{
					"0x2da466a7b24304f47e87fa2e1e5a81b9831ce54fec19055ce277ca2f39ba42c4", // UserOperationEvent
				},
			},
			map[string]interface{}{
				"address": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC Transfer
				"topics": []interface{}{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // ERC20 Transfer
					"0x0000000000000000000000006ce0896eae6d4bd668fde41bb784548fb8f59b50",
					"0x00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e",
				},
			},
			map[string]interface{}{
				"address": "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14", // WETH Transfer
				"topics": []interface{}{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // ERC20 Transfer
					"0x00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e",
					"0x0000000000000000000000006ce0896eae6d4bd668fde41bb784548fb8f59b50",
				},
			},
			map[string]interface{}{
				"address": "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50", // Pool Swap Event
				"topics": []interface{}{
					"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", // Uniswap V3 Swap
					"0x0000000000000000000000003bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
					"0x00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e",
				},
				"data": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffe1b7ed69000000000000000000000000000000000000000000000000011c37937e08000000000000000000000000000000000000000031028903a111a141cb2c2a361f1f0000000000000000000000000000000000000000000000000008771969119c60000000000000000000000000000000000000000000000000000000000002e151",
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		// Should find 3 addresses: 2 ERC20 transfers + 1 Uniswap V3 Swap
		expectedAddresses := []string{
			"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC
			"0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14", // WETH
			"0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50", // Pool
		}

		assert.Equal(t, len(expectedAddresses), len(poolAddresses))
		for _, expected := range expectedAddresses {
			assert.Contains(t, poolAddresses, expected)
		}
	})

	t.Run("Should discover Uniswap V2 Swap pool address", func(t *testing.T) {
		logsArray := []interface{}{
			map[string]interface{}{
				"address": "0x1234567890123456789012345678901234567890", // Mock V2 Pool
				"topics": []interface{}{
					"0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822", // Uniswap V2 Swap
				},
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		assert.Equal(t, 1, len(poolAddresses))
		assert.Contains(t, poolAddresses, "0x1234567890123456789012345678901234567890")
	})

	t.Run("Should discover SushiSwap pool address", func(t *testing.T) {
		logsArray := []interface{}{
			map[string]interface{}{
				"address": "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd", // Mock SushiSwap Pool
				"topics": []interface{}{
					"0x1c411e9a96e071241c2f21f7726b17ae89e3cab4c78be50e062b03a9fffbbad1", // SushiSwap Swap
				},
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		assert.Equal(t, 1, len(poolAddresses))
		assert.Contains(t, poolAddresses, "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	})

	t.Run("Should handle empty logs array", func(t *testing.T) {
		logsArray := []interface{}{}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		assert.Equal(t, 0, len(poolAddresses))
	})

	t.Run("Should handle logs with no DEX events", func(t *testing.T) {
		logsArray := []interface{}{
			map[string]interface{}{
				"address": "0x1234567890123456789012345678901234567890",
				"topics": []interface{}{
					"0x0000000000000000000000000000000000000000000000000000000000000000", // Unknown event
				},
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		assert.Equal(t, 0, len(poolAddresses))
	})

	t.Run("Should deduplicate addresses", func(t *testing.T) {
		logsArray := []interface{}{
			map[string]interface{}{
				"address": "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50",
				"topics": []interface{}{
					"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", // Uniswap V3 Swap
				},
			},
			map[string]interface{}{
				"address": "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50", // Same address
				"topics": []interface{}{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // ERC20 Transfer
				},
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		// Should only have one unique address
		assert.Equal(t, 1, len(poolAddresses))
		assert.Contains(t, poolAddresses, "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50")
	})

	t.Run("Should handle malformed log entries", func(t *testing.T) {
		logsArray := []interface{}{
			"invalid_log_entry", // Not a map
			map[string]interface{}{
				"address": 12345, // Not a string
				"topics": []interface{}{
					"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67",
				},
			},
			map[string]interface{}{
				"address": "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50",
				"topics":  "invalid_topics", // Not an array
			},
			map[string]interface{}{
				"address": "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50",
				"topics": []interface{}{
					12345, // Not a string
				},
			},
			map[string]interface{}{
				"address": "0x1234567890123456789012345678901234567890",
				"topics": []interface{}{
					"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", // Valid
				},
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		// Should only find the one valid entry
		assert.Equal(t, 1, len(poolAddresses))
		assert.Contains(t, poolAddresses, "0x1234567890123456789012345678901234567890")
	})

	t.Run("Should handle mixed DEX events in single transaction", func(t *testing.T) {
		logsArray := []interface{}{
			map[string]interface{}{
				"address": "0x1111111111111111111111111111111111111111",
				"topics": []interface{}{
					"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", // Uniswap V3 Swap
				},
			},
			map[string]interface{}{
				"address": "0x2222222222222222222222222222222222222222",
				"topics": []interface{}{
					"0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822", // Uniswap V2 Swap
				},
			},
			map[string]interface{}{
				"address": "0x3333333333333333333333333333333333333333",
				"topics": []interface{}{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // ERC20 Transfer
				},
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(logsArray)

		expectedAddresses := []string{
			"0x1111111111111111111111111111111111111111",
			"0x2222222222222222222222222222222222222222",
			"0x3333333333333333333333333333333333333333",
		}

		assert.Equal(t, len(expectedAddresses), len(poolAddresses))
		for _, expected := range expectedAddresses {
			assert.Contains(t, poolAddresses, expected)
		}
	})
}

func TestPoolDiscoveryIntegration(t *testing.T) {
	t.Run("Should match the actual workflow transaction logs", func(t *testing.T) {
		// Create processor with mock
		processor := &ContractWriteProcessor{
			CommonProcessor: &CommonProcessor{
				vm: &VM{
					logger: &MockLogger{},
				},
			},
		}

		// Use the exact logs from the actual workflow transaction 0xad3bd8dc627df001d7cf7ab2fc9982b7f82799c4fe92582af5910a702b255720
		actualWorkflowLogs := []interface{}{
			// EntryPoint events (should be ignored)
			map[string]interface{}{
				"address": "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789",
				"topics": []interface{}{
					"0x2da466a7b24304f47e87fa2e1e5a81b9831ce54fec19055ce277ca2f39ba42c4",
				},
			},
			// USDC Transfer (should be discovered)
			map[string]interface{}{
				"address": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
				"topics": []interface{}{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"0x0000000000000000000000006ce0896eae6d4bd668fde41bb784548fb8f59b50",
					"0x00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e",
				},
				"data": "000000000000000000000000000000000000000000000000000000001e481297",
			},
			// WETH Transfer (should be discovered)
			map[string]interface{}{
				"address": "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14",
				"topics": []interface{}{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"0x00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e",
					"0x0000000000000000000000006ce0896eae6d4bd668fde41bb784548fb8f59b50",
				},
				"data": "000000000000000000000000000000000000000000000000011c37937e080000",
			},
			// Pool Swap Event (should be discovered) - THE CRITICAL ONE
			map[string]interface{}{
				"address": "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50",
				"topics": []interface{}{
					"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", // Uniswap V3 Swap
					"0x0000000000000000000000003bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
					"0x00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e",
				},
				"data": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffe1b7ed69000000000000000000000000000000000000000000000000011c37937e08000000000000000000000000000000000000000031028903a111a141cb2c2a361f1f0000000000000000000000000000000000000000000000000008771969119c60000000000000000000000000000000000000000000000000000000000002e151",
			},
		}

		poolAddresses := processor.discoverPoolAddressesFromLogs(actualWorkflowLogs)

		// Should discover exactly the addresses we expect from the real transaction
		expectedDiscoveredAddresses := []string{
			"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC contract
			"0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14", // WETH contract
			"0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50", // The actual pool that emitted the Swap event
		}

		assert.Equal(t, len(expectedDiscoveredAddresses), len(poolAddresses),
			"Should discover exactly 3 addresses from the real workflow transaction")

		for _, expected := range expectedDiscoveredAddresses {
			assert.Contains(t, poolAddresses, expected,
				"Should discover address %s from the transaction logs", expected)
		}

		// The critical assertion: ensure we discover the ACTUAL pool address that emitted the Swap event
		assert.Contains(t, poolAddresses, "0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50",
			"Must discover the actual pool address 0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50 that emitted the Swap event")
	})
}

// MockLogger is already defined in token_metadata_test.go - no need to redeclare
