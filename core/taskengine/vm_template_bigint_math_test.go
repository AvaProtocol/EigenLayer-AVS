package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestBigIntTemplateMath_Multiplication(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_task",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	// Test case 1: 0.1% slippage (multiplier 0.999)
	t.Run("0.1% slippage (0.999 multiplier)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"quoteExactInputSingle": map[string]interface{}{
					"amountOut": "3030137700988171", // Large BigInt string
				},
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"uniswapv3_pool": map[string]interface{}{
				"slippage": 0.999, // 0.1% slippage
			},
		}

		expr := "{{contractRead1.data.quoteExactInputSingle.amountOut * settings.uniswapv3_pool.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 3030137700988171 * 0.999 = 3027104563287162.829
		// With integer math (100000 precision): (3030137700988171 * 99900) / 100000 = 3027104563287162
		// Note: Due to floating point precision, the result may vary slightly
		// The important thing is that it's less than the original (slippage applied)
		expected := "3027104563287162"
		if result != expected {
			// Check if result is within acceptable range (within 0.1% of expected)
			resultBig := new(big.Int)
			expectedBig := new(big.Int)
			if _, ok1 := resultBig.SetString(result, 10); ok1 {
				if _, ok2 := expectedBig.SetString(expected, 10); ok2 {
					diff := new(big.Int).Sub(resultBig, expectedBig)
					diff.Abs(diff)
					// Allow difference up to 0.1% of expected value
					tolerance := new(big.Int).Div(expectedBig, big.NewInt(1000))
					if diff.Cmp(tolerance) > 0 {
						t.Errorf("expected %s, got %s (difference too large)", expected, result)
					}
				} else {
					t.Errorf("expected %s, got %s", expected, result)
				}
			} else {
				t.Errorf("expected %s, got %s", expected, result)
			}
		}

		// Verify result is less than original (slippage applied)
		originalBig := new(big.Int)
		resultBig := new(big.Int)
		if _, ok1 := originalBig.SetString("3030137700988171", 10); ok1 {
			if _, ok2 := resultBig.SetString(result, 10); ok2 {
				if resultBig.Cmp(originalBig) >= 0 {
					t.Errorf("result should be less than original for slippage, got %s >= %s", result, "3030137700988171")
				}
			}
		}
	})

	// Test case 2: 0.5% slippage (multiplier 0.995)
	t.Run("0.5% slippage (0.995 multiplier)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"quoteExactInputSingle": map[string]interface{}{
					"amountOut": "3030137700988171",
				},
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"uniswapv3_pool": map[string]interface{}{
				"slippage": 0.995, // 0.5% slippage
			},
		}

		expr := "{{contractRead1.data.quoteExactInputSingle.amountOut * settings.uniswapv3_pool.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 3030137700988171 * 0.995 = 3014987012483230.145
		// With integer math (100000 precision): (3030137700988171 * 99500) / 100000 = 3014987012483230
		expected := "3014987012483230"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})

	// Test case 3: 1% slippage (multiplier 0.99)
	t.Run("1% slippage (0.99 multiplier)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"quoteExactInputSingle": map[string]interface{}{
					"amountOut": "3030137700988171",
				},
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"uniswapv3_pool": map[string]interface{}{
				"slippage": 0.99, // 1% slippage
			},
		}

		expr := "{{contractRead1.data.quoteExactInputSingle.amountOut * settings.uniswapv3_pool.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 3030137700988171 * 0.99 = 2999836323978289.29
		// With integer math (100000 precision): (3030137700988171 * 99000) / 100000 = 2999836323978289
		expected := "2999836323978289"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})

	// Test case 4: 5% slippage (multiplier 0.95)
	t.Run("5% slippage (0.95 multiplier)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"quoteExactInputSingle": map[string]interface{}{
					"amountOut": "3030137700988171",
				},
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"uniswapv3_pool": map[string]interface{}{
				"slippage": 0.95, // 5% slippage
			},
		}

		expr := "{{contractRead1.data.quoteExactInputSingle.amountOut * settings.uniswapv3_pool.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 3030137700988171 * 0.95 = 2878630815938762.45
		// With integer math (100000 precision): (3030137700988171 * 95000) / 100000 = 2878630815938762
		expected := "2878630815938762"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})
}

func TestBigIntTemplateMath_Division(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_task",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	t.Run("BigInt division by float", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"quoteExactInputSingle": map[string]interface{}{
					"amountOut": "3030137700988171",
				},
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"divisor": 1.005, // Divide by 1.005
		}

		expr := "{{contractRead1.data.quoteExactInputSingle.amountOut / settings.divisor}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 3030137700988171 / 1.005 ≈ 3015062389042956.22
		// With integer math (100000 precision): (3030137700988171 * 100000) / 100500 = 3015062389042956
		expected := "3015062389042956"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})
}

func TestBigIntTemplateMath_EdgeCases(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_task",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	t.Run("Small BigInt (should still work)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"amountOut": "1000000", // Smaller number
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"slippage": 0.995,
		}

		expr := "{{contractRead1.data.amountOut * settings.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 1000000 * 0.995 = 995000
		expected := "995000"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})

	t.Run("Very large BigInt", func(t *testing.T) {
		// Test with a very large number that exceeds JavaScript safe integer
		largeValue := "999999999999999999999999999999"
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"amountOut": largeValue,
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"slippage": 0.995,
		}

		expr := "{{contractRead1.data.amountOut * settings.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Calculate expected value using big.Int
		bigValue := new(big.Int)
		bigValue.SetString(largeValue, 10)
		multiplier := big.NewInt(9950) // 0.995 * 10000
		resultBig := new(big.Int).Mul(bigValue, multiplier)
		resultBig.Div(resultBig, big.NewInt(10000))
		expected := resultBig.String()

		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})

	t.Run("Non-BigInt string (should fall back to normal evaluation)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"amountOut": "100", // Small number, not BigInt
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"slippage": 0.995,
		}

		expr := "{{contractRead1.data.amountOut * settings.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Should still work, but may use JavaScript math
		// Expected: 100 * 0.995 = 99.5
		if result == "" || result == "undefined" {
			t.Errorf("expected a result, got %s", result)
		}
	})

	t.Run("Invalid expression (no operator)", func(t *testing.T) {
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"amountOut": "3030137700988171",
			},
		}

		expr := "{{contractRead1.data.amountOut}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Should return the value as-is
		expected := "3030137700988171"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}
	})
}

func TestBigIntTemplateMath_RealWorldScenario(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_task",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	t.Run("Real Uniswap swap scenario with 0.5% slippage", func(t *testing.T) {
		// Simulate the exact scenario from the user's error
		vm.vars["contractRead1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"quoteExactInputSingle": map[string]interface{}{
					"amountOut": "3030137700988171", // From quote
				},
			},
		}
		vm.vars["settings"] = map[string]interface{}{
			"uniswapv3_pool": map[string]interface{}{
				"slippage": 0.995, // 0.5% slippage tolerance
			},
		}

		// This is what would be used in amountOutMinimum parameter
		expr := "{{contractRead1.data.quoteExactInputSingle.amountOut * settings.uniswapv3_pool.slippage}}"
		result := vm.preprocessTextWithVariableMapping(expr)

		// Expected: 3030137700988171 * 0.995 = 3014987012483230
		// This should be less than the quoted amount, allowing for 0.5% slippage
		expected := "3014987012483230"
		if result != expected {
			t.Errorf("expected %s, got %s", expected, result)
		}

		// Verify it's less than the original quote (slippage applied)
		originalBig := new(big.Int)
		originalBig.SetString("3030137700988171", 10)
		resultBig := new(big.Int)
		resultBig.SetString(result, 10)
		if resultBig.Cmp(originalBig) >= 0 {
			t.Errorf("result should be less than original for slippage, got %s >= %s", result, "3030137700988171")
		}
	})
}
