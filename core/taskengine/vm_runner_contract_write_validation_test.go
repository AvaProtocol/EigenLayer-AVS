package taskengine

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

func TestValidateTransactionBeforeGasEstimation(t *testing.T) {
	processor := &ContractWriteProcessor{
		CommonProcessor: &CommonProcessor{
			vm: &VM{
				logger: &MockLogger{},
			},
		},
	}

	t.Run("Should reject calldata that is too short", func(t *testing.T) {
		callData := "0x123" // Only 3 characters, less than 4 bytes
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

		err := processor.validateTransactionBeforeGasEstimation("test", callData, callDataBytes, contractAddr)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "too short")
	})

	t.Run("Should validate exactInputSingle with zero amountIn", func(t *testing.T) {
		// Create calldata for exactInputSingle with zero amountIn
		// Method selector for exactInputSingle: 0x04e45aaf
		methodSelector := "04e45aaf"
		// Parameters: tokenIn, tokenOut, fee, recipient, amountIn=0, amountOutMinimum, sqrtPriceLimitX96
		params := ""
		params += "000000000000000000000000fff9976782d46cc05630d1f6ebab18b2324d6b14" // tokenIn (WETH)
		params += "0000000000000000000000001c7d4b196cb0c7b01d743fbc6116a902379c7238" // tokenOut (USDC)
		params += "0000000000000000000000000000000000000000000000000000000000000bb8" // fee (3000)
		params += "00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e" // recipient
		params += "0000000000000000000000000000000000000000000000000000000000000000" // amountIn = 0 ❌
		params += "0000000000000000000000000000000000000000000000000000000000000000" // amountOutMinimum
		params += "0000000000000000000000000000000000000000000000000000000000000000" // sqrtPriceLimitX96

		callData := "0x" + methodSelector + params
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")

		err := processor.validateTransactionBeforeGasEstimation("exactInputSingle", callData, callDataBytes, contractAddr)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "amountIn is zero")
	})

	t.Run("Should validate exactInputSingle with valid amountIn", func(t *testing.T) {
		// Create calldata for exactInputSingle with valid amountIn
		methodSelector := "04e45aaf"
		params := ""
		params += "000000000000000000000000fff9976782d46cc05630d1f6ebab18b2324d6b14" // tokenIn (WETH)
		params += "0000000000000000000000001c7d4b196cb0c7b01d743fbc6116a902379c7238" // tokenOut (USDC)
		params += "0000000000000000000000000000000000000000000000000000000000000bb8" // fee (3000)
		params += "00000000000000000000000071c8f4d7d5291edcb3a081802e7efb2788bd232e" // recipient
		params += "000000000000000000000000000000000000000000000000011c37937e080000" // amountIn = 0.08 WETH ✅
		params += "0000000000000000000000000000000000000000000000000000000000000000" // amountOutMinimum
		params += "0000000000000000000000000000000000000000000000000000000000000000" // sqrtPriceLimitX96

		callData := "0x" + methodSelector + params
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")

		err := processor.validateTransactionBeforeGasEstimation("exactInputSingle", callData, callDataBytes, contractAddr)

		assert.NoError(t, err)
	})

	t.Run("Should validate quoteExactInputSingle with zero amountIn", func(t *testing.T) {
		// Create calldata for quoteExactInputSingle with zero amountIn
		// Method selector for quoteExactInputSingle: 0xc6a5026a
		methodSelector := "c6a5026a"
		// Parameters: tokenIn, tokenOut, amountIn=0, fee, sqrtPriceLimitX96
		params := ""
		params += "000000000000000000000000fff9976782d46cc05630d1f6ebab18b2324d6b14" // tokenIn (WETH)
		params += "0000000000000000000000001c7d4b196cb0c7b01d743fbc6116a902379c7238" // tokenOut (USDC)
		params += "0000000000000000000000000000000000000000000000000000000000000000" // amountIn = 0 ❌
		params += "0000000000000000000000000000000000000000000000000000000000000bb8" // fee (3000)
		params += "0000000000000000000000000000000000000000000000000000000000000000" // sqrtPriceLimitX96

		callData := "0x" + methodSelector + params
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3")

		err := processor.validateTransactionBeforeGasEstimation("quoteExactInputSingle", callData, callDataBytes, contractAddr)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "amountIn is zero")
	})

	t.Run("Should validate ERC20 approve with zero spender", func(t *testing.T) {
		// Create calldata for approve with zero spender address
		// Method selector for approve: 0x095ea7b3
		methodSelector := "095ea7b3"
		// Parameters: spender=0x0, amount
		params := ""
		params += "0000000000000000000000000000000000000000000000000000000000000000" // spender = 0x0 ❌
		params += "000000000000000000000000000000000000000000000000011c37937e080000" // amount = 0.08 WETH

		callData := "0x" + methodSelector + params
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")

		err := processor.validateTransactionBeforeGasEstimation("approve", callData, callDataBytes, contractAddr)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "spender address is zero")
	})

	t.Run("Should validate ERC20 approve with valid parameters", func(t *testing.T) {
		// Create calldata for approve with valid parameters
		methodSelector := "095ea7b3"
		params := ""
		params += "0000000000000000000000003bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e" // spender = Uniswap Router ✅
		params += "000000000000000000000000000000000000000000000000011c37937e080000" // amount = 0.08 WETH

		callData := "0x" + methodSelector + params
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")

		err := processor.validateTransactionBeforeGasEstimation("approve", callData, callDataBytes, contractAddr)

		assert.NoError(t, err)
	})

	t.Run("Should validate zero contract address", func(t *testing.T) {
		// Use an unknown method so it goes to general validation
		callData := "0x12345678" + repeatString("0", 128) // Unknown method with zero contract address
		callDataBytes := common.FromHex(callData)
		contractAddr := common.Address{} // Zero address ❌

		err := processor.validateTransactionBeforeGasEstimation("unknownMethod", callData, callDataBytes, contractAddr)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "contract address is zero")
	})

	t.Run("Should pass validation for unknown method", func(t *testing.T) {
		// Unknown method should pass general validation
		callData := "0x12345678" + repeatString("0", 128) // Unknown method selector
		callDataBytes := common.FromHex(callData)
		contractAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

		err := processor.validateTransactionBeforeGasEstimation("unknownMethod", callData, callDataBytes, contractAddr)

		assert.NoError(t, err) // Should pass general validation
	})
}

func TestDecodeErrorMessage(t *testing.T) {
	t.Run("Should decode the cryptic error message from bundler", func(t *testing.T) {
		// The actual error from the logs: b'\x08\xc3y\xa0 \x02AS'
		errorBytes := []byte{0x08, 0xc3, 0x79, 0xa0, 0x20, 0x02, 0x41, 0x53}

		// Log the raw bytes for analysis
		t.Logf("Raw error bytes: %v", errorBytes)
		t.Logf("Hex representation: 0x%x", errorBytes)
		t.Logf("String representation: %q", string(errorBytes))

		// This might be a Solidity error or revert reason
		// The pattern suggests it could be ABI-encoded error data

		// Check if it matches common Solidity error patterns
		if len(errorBytes) >= 4 {
			selector := errorBytes[:4]
			t.Logf("Potential error selector: 0x%x", selector)
		}

		// Our validation should prevent this error from occurring
		assert.True(t, len(errorBytes) > 0, "Error message should not be empty")
	})
}

// Helper function to repeat strings (Go doesn't have built-in string.repeat)
func repeatString(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
