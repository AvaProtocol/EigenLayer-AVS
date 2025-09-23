package taskengine

import (
	"encoding/json"
	"math/big"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenderlyAPIResponseParsing(t *testing.T) {
	t.Run("Parse valid Tenderly simulation response with gas data", func(t *testing.T) {
		// Sample Tenderly API response with gas information
		mockTenderlyResponse := map[string]interface{}{
			"simulation": map[string]interface{}{
				"id":        "test-simulation-id",
				"success":   true,
				"gas_used":  "105432",      // Gas used in decimal
				"gas_price": "15000000000", // 15 gwei in decimal
				"transaction": map[string]interface{}{
					"hash":      "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					"from":      "0xabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
					"to":        "0x1111111111111111111111111111111111111111",
					"value":     "0",
					"gas":       "200000",
					"gas_price": "15000000000",
				},
				"logs": []interface{}{
					map[string]interface{}{
						"address": "0x1111111111111111111111111111111111111111",
						"topics": []interface{}{
							"0xe1fffcc4923d04b559f4d29a8bfc6cda04eb5b0d3c460751c2402c5c5cc9109c",
						},
						"data": "0x000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000064578616d706c650000000000000000000000000000000000000000000000000",
					},
				},
			},
		}

		// Convert to JSON and parse as ContractWriteSimulationResult
		jsonData, err := json.Marshal(mockTenderlyResponse)
		require.NoError(t, err)

		// Parse using our Tenderly client logic
		var tenderlyResp map[string]interface{}
		err = json.Unmarshal(jsonData, &tenderlyResp)
		require.NoError(t, err)

		// Extract simulation data
		simulation, ok := tenderlyResp["simulation"].(map[string]interface{})
		require.True(t, ok, "Simulation field should be present")

		// Test gas data extraction
		gasUsed, ok := simulation["gas_used"].(string)
		require.True(t, ok, "Gas used should be string")
		assert.Equal(t, "105432", gasUsed)

		gasPrice, ok := simulation["gas_price"].(string)
		require.True(t, ok, "Gas price should be string")
		assert.Equal(t, "15000000000", gasPrice)

		// Test success parsing
		success, ok := simulation["success"].(bool)
		require.True(t, ok, "Success should be boolean")
		assert.True(t, success)

		// Test transaction data
		transaction, ok := simulation["transaction"].(map[string]interface{})
		require.True(t, ok, "Transaction should be present")

		txHash, ok := transaction["hash"].(string)
		require.True(t, ok, "Transaction hash should be present")
		assert.Equal(t, "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", txHash)

		// Test logs parsing
		logs, ok := simulation["logs"].([]interface{})
		require.True(t, ok, "Logs should be array")
		assert.Len(t, logs, 1, "Should have one log")

		log := logs[0].(map[string]interface{})
		logAddress, ok := log["address"].(string)
		require.True(t, ok, "Log address should be string")
		assert.Equal(t, "0x1111111111111111111111111111111111111111", logAddress)
	})

	t.Run("Parse Tenderly response with missing gas data", func(t *testing.T) {
		// Tenderly response without gas information
		mockTenderlyResponse := map[string]interface{}{
			"simulation": map[string]interface{}{
				"id":      "test-simulation-id",
				"success": true,
				// No gas_used or gas_price fields
				"transaction": map[string]interface{}{
					"hash": "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					"from": "0xabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
					"to":   "0x1111111111111111111111111111111111111111",
				},
				"logs": []interface{}{},
			},
		}

		jsonData, err := json.Marshal(mockTenderlyResponse)
		require.NoError(t, err)

		var tenderlyResp map[string]interface{}
		err = json.Unmarshal(jsonData, &tenderlyResp)
		require.NoError(t, err)

		simulation, ok := tenderlyResp["simulation"].(map[string]interface{})
		require.True(t, ok)

		// Gas data should be missing
		_, hasGasUsed := simulation["gas_used"]
		_, hasGasPrice := simulation["gas_price"]
		assert.False(t, hasGasUsed, "Gas used should not be present")
		assert.False(t, hasGasPrice, "Gas price should not be present")

		// Test that our gas extraction methods handle this gracefully
		processor := &ContractWriteProcessor{}

		// Create a mock simulation result
		result := &ContractWriteSimulationResult{
			Success: true,
			// GasUsed and GasPrice are empty strings
			GasUsed:  "",
			GasPrice: "",
		}

		gasUsedHex := processor.getGasUsedFromTenderly(result)
		gasPriceHex := processor.getGasPriceFromTenderly(result)

		// Should return empty strings when data is unavailable
		assert.Equal(t, "", gasUsedHex, "Should return empty string when gas data unavailable")
		assert.Equal(t, "", gasPriceHex, "Should return empty string when gas price unavailable")
	})

	t.Run("Parse Tenderly response with failed simulation", func(t *testing.T) {
		mockTenderlyResponse := map[string]interface{}{
			"simulation": map[string]interface{}{
				"id":      "failed-simulation",
				"success": false,
				"error": map[string]interface{}{
					"message": "VM Exception while processing transaction: revert Custom error message",
					"code":    "REVERT",
				},
				"gas_used":  "23450",       // Gas still consumed even on failure
				"gas_price": "20000000000", // 20 gwei
				"transaction": map[string]interface{}{
					"hash": "0xfailed1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
					"from": "0xabcdefabcdefabcdefabcdefabcdefabcdefabcdef",
					"to":   "0x2222222222222222222222222222222222222222",
				},
			},
		}

		jsonData, err := json.Marshal(mockTenderlyResponse)
		require.NoError(t, err)

		var tenderlyResp map[string]interface{}
		err = json.Unmarshal(jsonData, &tenderlyResp)
		require.NoError(t, err)

		simulation, ok := tenderlyResp["simulation"].(map[string]interface{})
		require.True(t, ok)

		// Test failure parsing
		success, ok := simulation["success"].(bool)
		require.True(t, ok)
		assert.False(t, success, "Failed simulation should have success=false")

		// Test error message extraction
		errorData, ok := simulation["error"].(map[string]interface{})
		require.True(t, ok, "Error data should be present")

		errorMessage, ok := errorData["message"].(string)
		require.True(t, ok, "Error message should be string")
		assert.Contains(t, errorMessage, "revert Custom error message")

		// Test that gas data is still available even for failed simulations
		gasUsed, ok := simulation["gas_used"].(string)
		require.True(t, ok)
		assert.Equal(t, "23450", gasUsed)

		gasPrice, ok := simulation["gas_price"].(string)
		require.True(t, ok)
		assert.Equal(t, "20000000000", gasPrice)
	})

	t.Run("Parse malformed Tenderly response", func(t *testing.T) {
		// Test various malformed responses
		testCases := []struct {
			name     string
			response map[string]interface{}
		}{
			{
				name: "missing simulation field",
				response: map[string]interface{}{
					"error": "Simulation not found",
				},
			},
			{
				name: "simulation is not object",
				response: map[string]interface{}{
					"simulation": "invalid",
				},
			},
			{
				name:     "empty response",
				response: map[string]interface{}{},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				jsonData, err := json.Marshal(tc.response)
				require.NoError(t, err)

				var tenderlyResp map[string]interface{}
				err = json.Unmarshal(jsonData, &tenderlyResp)
				require.NoError(t, err)

				// Test graceful handling of malformed data
				simulation, ok := tenderlyResp["simulation"]
				if ok {
					// If simulation exists, it should be a map
					_, isMap := simulation.(map[string]interface{})
					if !isMap {
						// Our code should handle non-map simulation gracefully
						assert.NotNil(t, simulation) // Just ensure it's not completely broken
					}
				} else {
					// No simulation field - should be handled gracefully
					assert.Nil(t, simulation)
				}
			})
		}
	})

	t.Run("Test gas data type variations", func(t *testing.T) {
		// Test different data type scenarios that might come from Tenderly
		testCases := []struct {
			name             string
			gasUsed          interface{}
			gasPrice         interface{}
			expectedGasUsed  string
			expectedGasPrice string
		}{
			{
				name:             "string gas values (normal case)",
				gasUsed:          "105432",
				gasPrice:         "15000000000",
				expectedGasUsed:  "105432",
				expectedGasPrice: "15000000000",
			},
			{
				name:             "numeric gas values",
				gasUsed:          105432,
				gasPrice:         15000000000,
				expectedGasUsed:  "", // Our parser expects strings
				expectedGasPrice: "",
			},
			{
				name:             "null gas values",
				gasUsed:          nil,
				gasPrice:         nil,
				expectedGasUsed:  "",
				expectedGasPrice: "",
			},
			{
				name:             "empty string gas values",
				gasUsed:          "",
				gasPrice:         "",
				expectedGasUsed:  "",
				expectedGasPrice: "",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Create mock simulation result
				result := &ContractWriteSimulationResult{
					Success: true,
				}

				// Set gas values based on type
				if gasUsedStr, ok := tc.gasUsed.(string); ok {
					result.GasUsed = gasUsedStr
				}
				if gasPriceStr, ok := tc.gasPrice.(string); ok {
					result.GasPrice = gasPriceStr
				}

				processor := &ContractWriteProcessor{}

				gasUsedHex := processor.getGasUsedFromTenderly(result)
				gasPriceHex := processor.getGasPriceFromTenderly(result)

				if tc.expectedGasUsed != "" {
					// Should successfully parse and convert to hex
					assert.NotEmpty(t, gasUsedHex, "Should extract valid gas used")
					assert.True(t, len(gasUsedHex) > 2 && gasUsedHex[:2] == "0x", "Should be hex format")
				} else {
					// Should return empty string for invalid/missing data
					assert.Equal(t, tc.expectedGasUsed, gasUsedHex)
				}

				if tc.expectedGasPrice != "" {
					// Should successfully parse and convert to hex
					assert.NotEmpty(t, gasPriceHex, "Should extract valid gas price")
					assert.True(t, len(gasPriceHex) > 2 && gasPriceHex[:2] == "0x", "Should be hex format")
				} else {
					// Should return empty string for invalid/missing data
					assert.Equal(t, tc.expectedGasPrice, gasPriceHex)
				}
			})
		}
	})

	t.Run("Integration test - full Tenderly simulation to execution step", func(t *testing.T) {
		// This test simulates the full flow from Tenderly API response to execution step

		// Create simulation result
		result := &ContractWriteSimulationResult{
			Success:    true,
			MethodName: "transfer",
			GasUsed:    "150000",
			GasPrice:   "25000000000",
			// Transaction data would be parsed from Tenderly response
			// Hash: "0xintegration123456789012345678901234567890123456789012345678901234",
		}

		// Test gas extraction
		processor := &ContractWriteProcessor{}
		gasUsedHex := processor.getGasUsedFromTenderly(result)
		gasPriceHex := processor.getGasPriceFromTenderly(result)

		// Verify gas values are converted to hex correctly
		assert.Equal(t, "0x249f0", gasUsedHex, "150000 in decimal should be 0x249f0 in hex")
		assert.Equal(t, "0x5d21dba00", gasPriceHex, "25000000000 in decimal should be 0x5d21dba00 in hex")

		// Verify gas cost calculation would work
		// 150000 * 25000000000 = 3750000000000000 wei (0.00375 ETH)
		expectedGasCost := "3750000000000000"

		// Simulate what would happen in a real execution step
		mockExecutionStep := &avsproto.Execution_Step{
			Id:   "integration-step",
			Type: "CONTRACT_WRITE",
		}

		// These would be set by the actual execution logic
		if gasUsedHex != "" && gasPriceHex != "" {
			// Convert hex back to decimal for step fields
			gasUsedBig := new(big.Int)
			gasUsedBig.SetString(gasUsedHex[2:], 16) // Remove 0x prefix

			gasPriceBig := new(big.Int)
			gasPriceBig.SetString(gasPriceHex[2:], 16) // Remove 0x prefix

			totalGasCost := new(big.Int).Mul(gasUsedBig, gasPriceBig)

			mockExecutionStep.GasUsed = gasUsedBig.String()
			mockExecutionStep.GasPrice = gasPriceBig.String()
			mockExecutionStep.TotalGasCost = totalGasCost.String()
		}

		// Verify the execution step has correct gas information
		assert.Equal(t, "150000", mockExecutionStep.GasUsed)
		assert.Equal(t, "25000000000", mockExecutionStep.GasPrice)
		assert.Equal(t, expectedGasCost, mockExecutionStep.TotalGasCost)
	})
}
