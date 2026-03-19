package taskengine

import (
	"reflect"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilter(t *testing.T) {
	nodeConfig := map[string]interface{}{
		"expression":    "value.cost > 5",
		"inputVariable": "{{trades}}",
	}

	node, err := CreateNodeFromType("filter", nodeConfig, "")
	require.NoError(t, err)
	node.Name = "filterNode"

	inputVariables := map[string]interface{}{
		"trades": []interface{}{
			map[string]interface{}{"cost": 12, "name": "abc"},
			map[string]interface{}{"cost": 2, "name": "def"},
		},
	}

	vm := NewVM()
	step, err := vm.RunNodeWithInputs(node, inputVariables)
	require.NoError(t, err)
	require.True(t, step.Success, "expected success, got error: %s", step.Error)

	filterOutput := step.GetFilter()
	require.NotNil(t, filterOutput)
	require.NotNil(t, filterOutput.Data)

	data := gow.ValueToSlice(filterOutput.Data)
	require.Len(t, data, 1, "expect 1 element with cost > 5")

	name := data[0].(map[string]interface{})["name"].(string)
	assert.Equal(t, "abc", name)
}

func TestFilterComplexLogic(t *testing.T) {
	nodeConfig := map[string]interface{}{
		"expression":    "if (index<=2) { return value.cost > 13; } else { return value.cost < 21; }",
		"inputVariable": "{{trades}}",
	}

	node, err := CreateNodeFromType("filter", nodeConfig, "")
	require.NoError(t, err)
	node.Name = "filterNode"

	inputVariables := map[string]interface{}{
		"trades": []interface{}{
			map[string]interface{}{"cost": 12, "name": "first"},
			map[string]interface{}{"cost": 15, "name": "second"},
			map[string]interface{}{"cost": 20, "name": "third"},
			map[string]interface{}{"cost": 25, "name": "fourth"},
			map[string]interface{}{"cost": 19, "name": "sixth"},
		},
	}

	vm := NewVM()
	step, err := vm.RunNodeWithInputs(node, inputVariables)
	require.NoError(t, err)
	require.True(t, step.Success, "expected success, got error: %s", step.Error)

	filterOutput := step.GetFilter()
	require.NotNil(t, filterOutput)
	require.NotNil(t, filterOutput.Data)

	data := gow.ValueToSlice(filterOutput.Data)
	require.Len(t, data, 3, "expect 3 elements")

	assert.True(t, reflect.DeepEqual(
		[]float64{15, 20, 19},
		[]float64{
			data[0].(map[string]any)["cost"].(float64),
			data[1].(map[string]any)["cost"].(float64),
			data[2].(map[string]any)["cost"].(float64),
		}), "expected costs 15, 20, 19 but got: %v", data)

	assert.True(t, reflect.DeepEqual(
		[]string{"second", "third", "sixth"},
		[]string{
			data[0].(map[string]any)["name"].(string),
			data[1].(map[string]any)["name"].(string),
			data[2].(map[string]any)["name"].(string),
		}), "expected names second, third, sixth but got: %v", data)
}

// TestFilterWithBigIntValues verifies that filtering items containing BigInt
// string values (typical for wei amounts from CustomCode nodes) works correctly
// and doesn't fail at protobuf serialization.
func TestFilterWithBigIntValues(t *testing.T) {
	nodeConfig := map[string]interface{}{
		"expression":    "BigInt(value.balance) > BigInt('1000000000000000')",
		"inputVariable": "{{tokens}}",
	}

	node, err := CreateNodeFromType("filter", nodeConfig, "")
	require.NoError(t, err)
	node.Name = "filterNode"

	inputVariables := map[string]interface{}{
		"tokens": []interface{}{
			map[string]interface{}{"symbol": "ETH", "balance": "90864223405041968"},
			map[string]interface{}{"symbol": "USDC", "balance": "500000"},
			map[string]interface{}{"symbol": "WETH", "balance": "5000000000000000000"},
		},
	}

	vm := NewVM()
	step, err := vm.RunNodeWithInputs(node, inputVariables)
	require.NoError(t, err)
	require.True(t, step.Success, "expected success, got error: %s", step.Error)

	filterOutput := step.GetFilter()
	require.NotNil(t, filterOutput)
	require.NotNil(t, filterOutput.Data)

	data := gow.ValueToSlice(filterOutput.Data)
	require.Len(t, data, 2, "expect 2 tokens with balance > 1e15")

	assert.Equal(t, "ETH", data[0].(map[string]interface{})["symbol"].(string))
	assert.Equal(t, "WETH", data[1].(map[string]interface{})["symbol"].(string))
}

// TestFilterWithDateValues verifies that filtering items containing Date-like
// fields works and the result serializes correctly.
func TestFilterWithDateValues(t *testing.T) {
	nodeConfig := map[string]interface{}{
		"expression":    "new Date(value.timestamp) > new Date('2026-06-01')",
		"inputVariable": "{{events}}",
	}

	node, err := CreateNodeFromType("filter", nodeConfig, "")
	require.NoError(t, err)
	node.Name = "filterNode"

	inputVariables := map[string]interface{}{
		"events": []interface{}{
			map[string]interface{}{"name": "early", "timestamp": "2026-03-01T00:00:00Z"},
			map[string]interface{}{"name": "late", "timestamp": "2026-09-15T00:00:00Z"},
		},
	}

	vm := NewVM()
	step, err := vm.RunNodeWithInputs(node, inputVariables)
	require.NoError(t, err)
	require.True(t, step.Success, "expected success, got error: %s", step.Error)

	filterOutput := step.GetFilter()
	require.NotNil(t, filterOutput)
	require.NotNil(t, filterOutput.Data)

	data := gow.ValueToSlice(filterOutput.Data)
	require.Len(t, data, 1, "expect 1 event after June 2026")
	assert.Equal(t, "late", data[0].(map[string]interface{})["name"].(string))
}
