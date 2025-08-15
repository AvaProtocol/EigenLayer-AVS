package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/stretchr/testify/assert"
)

func TestObjectSerializationInTemplates(t *testing.T) {
	// Test that objects are properly serialized as JSON instead of "[object Object]"
	// This reproduces the contract write issue where struct parameters were not working

	t.Run("Object_Serialization_JSON_Instead_Of_Object_Object", func(t *testing.T) {
		vm := NewVM()
		vm.logger = testutil.GetLogger()
		vm.IsSimulation = true

		// Add test data that matches the problematic case from the user's logs
		testData := map[string]interface{}{
			"amountIn":          "80000000000000000",
			"fee":               500,
			"sqrtPriceLimitX96": 0,
			"tokenIn":           "WETH9",
			"tokenOut":          "USDC",
		}

		vm.AddVar("get_quote_params", map[string]interface{}{
			"data": testData,
		})

		// Test template resolution
		template := "{{get_quote_params.data}}"
		result := vm.preprocessTextWithVariableMapping(template)

		t.Logf("Template: %s", template)
		t.Logf("Result: %s", result)

		// Should NOT be "[object Object]"
		assert.NotEqual(t, "[object Object]", result, "Object should not be converted to '[object Object]'")

		// Should be valid JSON containing the expected fields
		assert.Contains(t, result, "amountIn", "Result should contain amountIn field")
		assert.Contains(t, result, "80000000000000000", "Result should contain the amountIn value")
		assert.Contains(t, result, "fee", "Result should contain fee field")
		assert.Contains(t, result, "500", "Result should contain the fee value")
		assert.Contains(t, result, "tokenIn", "Result should contain tokenIn field")
		assert.Contains(t, result, "WETH9", "Result should contain the tokenIn value")
		assert.Contains(t, result, "tokenOut", "Result should contain tokenOut field")
		assert.Contains(t, result, "USDC", "Result should contain the tokenOut value")

		// Should start and end with JSON object braces
		assert.True(t, result[0] == '{' && result[len(result)-1] == '}',
			"Result should be a JSON object (start with { and end with })")

		t.Logf("✅ Object properly serialized as JSON: %s", result)
	})

	t.Run("Nested_Object_Access", func(t *testing.T) {
		vm := NewVM()
		vm.logger = testutil.GetLogger()
		vm.IsSimulation = true

		// Test accessing nested object fields
		vm.AddVar("test_data", map[string]interface{}{
			"nested": map[string]interface{}{
				"field1": "value1",
				"field2": 42,
			},
		})

		// Test individual field access (should work as before)
		result1 := vm.preprocessTextWithVariableMapping("{{test_data.nested.field1}}")
		assert.Equal(t, "value1", result1, "Individual field access should work")

		// Test whole nested object access (should be JSON)
		result2 := vm.preprocessTextWithVariableMapping("{{test_data.nested}}")
		assert.NotEqual(t, "[object Object]", result2, "Nested object should not be '[object Object]'")
		assert.Contains(t, result2, "field1", "Nested object JSON should contain field1")
		assert.Contains(t, result2, "value1", "Nested object JSON should contain value1")

		t.Logf("✅ Individual field: %s", result1)
		t.Logf("✅ Nested object JSON: %s", result2)
	})
}
