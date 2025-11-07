package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTemplateVariableResolution(t *testing.T) {
	// Create a simple VM for testing
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, make(map[string]string))
	require.NoError(t, err)

	// Add some test variables
	vm.AddVar("settings", map[string]interface{}{
		"amount": "1000",
		"runner": "0x1234",
	})

	t.Run("Valid resolution - no error", func(t *testing.T) {
		original := "{{settings.amount}}"
		resolved := "1000"
		err := ValidateTemplateVariableResolution(resolved, original, vm, "test field")
		assert.NoError(t, err)
	})

	t.Run("Undefined as exact value", func(t *testing.T) {
		original := "{{missing_node.data}}"
		resolved := "undefined"
		err := ValidateTemplateVariableResolution(resolved, original, vm, "test field")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing_node.data")
		assert.Contains(t, err.Error(), "Make sure to run the 'missing_node' node first")
		// Should not contain "=undefined"
		assert.NotContains(t, err.Error(), "=undefined")
	})

	t.Run("Undefined in JSON array string", func(t *testing.T) {
		original := `["{{settings.runner}}", "{{settings.amount}}", "{{get_quote.data.amountOut}}"]`
		resolved := `["0x1234", "1000", "undefined"]`
		err := ValidateTemplateVariableResolution(resolved, original, vm, "methodParams")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get_quote.data.amountOut")
		assert.Contains(t, err.Error(), "Make sure to run the 'get_quote' node first")
		// Should not contain "=undefined"
		assert.NotContains(t, err.Error(), "=undefined")
	})

	t.Run("Undefined in middle of string", func(t *testing.T) {
		original := "The value is: {{missing.value}}"
		resolved := "The value is: undefined"
		err := ValidateTemplateVariableResolution(resolved, original, vm, "body")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing.value")
		assert.Contains(t, err.Error(), "Make sure to run the 'missing' node first")
		// Should not contain "=undefined"
		assert.NotContains(t, err.Error(), "=undefined")
	})

	t.Run("System variable doesn't trigger error", func(t *testing.T) {
		original := "{{settings.missingField}}"
		resolved := "undefined"
		err := ValidateTemplateVariableResolution(resolved, original, vm, "test field")
		require.Error(t, err)
		// Should NOT suggest running 'settings' node since it's a system variable
		assert.NotContains(t, err.Error(), "Make sure to run the 'settings' node first")
	})
}

func TestValidateResolvedParams(t *testing.T) {
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, make(map[string]string))
	require.NoError(t, err)

	vm.AddVar("settings", map[string]interface{}{
		"token0": "0xaaa",
		"token1": "0xbbb",
	})

	t.Run("All params valid", func(t *testing.T) {
		resolved := []string{"0xaaa", "0xbbb", "3000"}
		original := []string{"{{settings.token0}}", "{{settings.token1}}", "3000"}
		err := ValidateResolvedParams(resolved, original, vm, "exactInputSingle")
		assert.NoError(t, err)
	})

	t.Run("One param undefined", func(t *testing.T) {
		resolved := []string{"0xaaa", "undefined", "3000"}
		original := []string{"{{settings.token0}}", "{{node_b.data.token}}", "3000"}
		err := ValidateResolvedParams(resolved, original, vm, "exactInputSingle")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "node_b.data.token")
		assert.Contains(t, err.Error(), "Make sure to run the 'node_b' node first")
		// Should not contain parameter index or "=undefined"
		assert.NotContains(t, err.Error(), "parameter 1")
		assert.NotContains(t, err.Error(), "=undefined")
	})

	t.Run("Undefined substring in param", func(t *testing.T) {
		resolved := []string{"0xaaa", "value_undefined_end", "3000"}
		original := []string{"{{settings.token0}}", "value_{{missing.field}}_end", "3000"}
		err := ValidateResolvedParams(resolved, original, vm, "method")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing.field")
		assert.Contains(t, err.Error(), "Make sure to run the 'missing' node first")
		// Should not contain "=undefined"
		assert.NotContains(t, err.Error(), "=undefined")
	})
}
