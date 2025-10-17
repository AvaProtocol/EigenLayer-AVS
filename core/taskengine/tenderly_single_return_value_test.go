package taskengine

import (
	"encoding/json"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSingleReturnValueFlattening verifies that single unnamed return values
// are flattened (not wrapped in output_0) for better user experience
func TestSingleReturnValueFlattening(t *testing.T) {
	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	client := NewTenderlyClient(testConfig, logger)

	t.Run("Approve_Returns_Flattened_Bool", func(t *testing.T) {
		// approve(address,uint256) returns (bool) with empty name
		// Output: 0x0000000000000000000000000000000000000000000000000000000000000001 (true)
		outputHex := "0x0000000000000000000000000000000000000000000000000000000000000001"

		// Minimal ABI for approve with unnamed bool return
		contractABI := `[{
			"inputs": [
				{"internalType": "address", "name": "spender", "type": "address"},
				{"internalType": "uint256", "name": "amount", "type": "uint256"}
			],
			"name": "approve",
			"outputs": [{"internalType": "bool", "name": "", "type": "bool"}],
			"stateMutability": "nonpayable",
			"type": "function"
		}]`

		methodName := "approve"

		// Test the decoding function
		returnData := client.decodeReturnDataComplete(outputHex, contractABI, methodName)

		// Verify we got a result
		require.NotNil(t, returnData, "decodeReturnDataComplete should return a result")
		assert.NotEmpty(t, returnData.Value, "decoded value should not be empty")

		t.Logf("Decoded approve output: %s", returnData.Value)

		// Parse the JSON result - should be just "true", not {"output_0": true}
		var decodedResult interface{}
		err := json.Unmarshal([]byte(returnData.Value), &decodedResult)
		require.NoError(t, err, "decoded result should be valid JSON")

		// Verify it's a boolean, not an object
		boolValue, isBool := decodedResult.(bool)
		assert.True(t, isBool, "approve should return a boolean value directly, not wrapped in object")
		assert.True(t, boolValue, "approve should return true")

		// CRITICAL: Ensure output_0 is NOT present
		objValue, isObject := decodedResult.(map[string]interface{})
		if isObject {
			t.Errorf("❌ FAILED: approve returned object %+v instead of flattened bool", objValue)
			if _, hasOutput0 := objValue["output_0"]; hasOutput0 {
				t.Errorf("❌ CRITICAL: output_0 field still present - flattening did not work!")
			}
		}
	})

	t.Run("Transfer_Returns_Flattened_Bool", func(t *testing.T) {
		// transfer(address,uint256) returns (bool) with empty name
		// Output: 0x0000000000000000000000000000000000000000000000000000000000000001 (true)
		outputHex := "0x0000000000000000000000000000000000000000000000000000000000000001"

		contractABI := `[{
			"inputs": [
				{"internalType": "address", "name": "to", "type": "address"},
				{"internalType": "uint256", "name": "value", "type": "uint256"}
			],
			"name": "transfer",
			"outputs": [{"internalType": "bool", "name": "", "type": "bool"}],
			"stateMutability": "nonpayable",
			"type": "function"
		}]`

		methodName := "transfer"

		returnData := client.decodeReturnDataComplete(outputHex, contractABI, methodName)

		require.NotNil(t, returnData, "transfer should decode return value")

		var decodedResult interface{}
		err := json.Unmarshal([]byte(returnData.Value), &decodedResult)
		require.NoError(t, err, "decoded result should be valid JSON")

		// Verify it's a boolean, not an object
		boolValue, isBool := decodedResult.(bool)
		assert.True(t, isBool, "transfer should return a boolean value directly")
		assert.True(t, boolValue, "transfer should return true")
	})

	t.Run("BalanceOf_Returns_Flattened_Uint256", func(t *testing.T) {
		// balanceOf(address) returns (uint256) with empty name
		// Output: 0x00000000000000000000000000000000000000000000000000000000000f4240 (1000000)
		outputHex := "0x00000000000000000000000000000000000000000000000000000000000f4240"

		contractABI := `[{
			"inputs": [{"internalType": "address", "name": "account", "type": "address"}],
			"name": "balanceOf",
			"outputs": [{"internalType": "uint256", "name": "", "type": "uint256"}],
			"stateMutability": "view",
			"type": "function"
		}]`

		methodName := "balanceOf"

		returnData := client.decodeReturnDataComplete(outputHex, contractABI, methodName)

		require.NotNil(t, returnData, "balanceOf should decode return value")

		var decodedResult interface{}
		err := json.Unmarshal([]byte(returnData.Value), &decodedResult)
		require.NoError(t, err, "decoded result should be valid JSON")

		// Verify it's a string (for uint256 precision), not an object
		strValue, isString := decodedResult.(string)
		assert.True(t, isString, "balanceOf should return a string value directly (uint256 as string)")
		assert.Equal(t, "1000000", strValue, "balanceOf should return 1000000")
	})

	t.Run("MultipleOutputs_Returns_Object_With_Fields", func(t *testing.T) {
		// Method with multiple unnamed outputs should still use output_0, output_1 pattern
		// This is expected behavior for multiple returns
		outputHex := "0x00000000000000000000000000000000000000000000000000000000000f424000000000000000000000000000000000000000000000000000000000000003e8"

		contractABI := `[{
			"inputs": [],
			"name": "getBalances",
			"outputs": [
				{"internalType": "uint256", "name": "", "type": "uint256"},
				{"internalType": "uint256", "name": "", "type": "uint256"}
			],
			"stateMutability": "view",
			"type": "function"
		}]`

		methodName := "getBalances"

		returnData := client.decodeReturnDataComplete(outputHex, contractABI, methodName)

		require.NotNil(t, returnData, "getBalances should decode return value")

		var decodedResult map[string]interface{}
		err := json.Unmarshal([]byte(returnData.Value), &decodedResult)
		require.NoError(t, err, "decoded result should be valid JSON")

		// For multiple outputs, we expect output_0, output_1 pattern
		assert.Contains(t, decodedResult, "output_0", "multiple outputs should have output_0 field")
		assert.Contains(t, decodedResult, "output_1", "multiple outputs should have output_1 field")
		assert.Equal(t, "1000000", decodedResult["output_0"], "output_0 should be 1000000")
		assert.Equal(t, "1000", decodedResult["output_1"], "output_1 should be 1000")
	})

	t.Run("NamedOutput_Returns_Named_Field", func(t *testing.T) {
		// Method with named output should use the actual name
		outputHex := "0x00000000000000000000000000000000000000000000000000000000000f4240"

		contractABI := `[{
			"inputs": [{"internalType": "address", "name": "account", "type": "address"}],
			"name": "balanceOf",
			"outputs": [{"internalType": "uint256", "name": "balance", "type": "uint256"}],
			"stateMutability": "view",
			"type": "function"
		}]`

		methodName := "balanceOf"

		returnData := client.decodeReturnDataComplete(outputHex, contractABI, methodName)

		require.NotNil(t, returnData, "balanceOf should decode return value")

		var decodedResult map[string]interface{}
		err := json.Unmarshal([]byte(returnData.Value), &decodedResult)
		require.NoError(t, err, "decoded result should be valid JSON")

		// Named output should use the actual name
		assert.Contains(t, decodedResult, "balance", "named output should use field name 'balance'")
		assert.NotContains(t, decodedResult, "output_0", "named output should NOT have output_0")
		assert.Equal(t, "1000000", decodedResult["balance"], "balance should be 1000000")
	})
}
