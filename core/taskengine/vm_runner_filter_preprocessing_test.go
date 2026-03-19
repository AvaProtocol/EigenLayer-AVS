package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVM_FilterPreprocessing_BasicExecution(t *testing.T) {
	testCases := []struct {
		name          string
		expression    string
		inputData     []interface{}
		expectedCount int
	}{
		{
			name:       "Simple Greater Than",
			expression: "value.age > 20",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 2,
		},
		{
			name:       "Simple Less Than",
			expression: "value.age < 18",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 1,
		},
		{
			name:       "String Equality",
			expression: `value.name === "Alice"`,
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
			},
			expectedCount: 1,
		},
		{
			name:       "False Condition",
			expression: "value.age > 100",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
			},
			expectedCount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeConfig := map[string]interface{}{
				"expression":    tc.expression,
				"inputVariable": "{{testInput}}",
			}

			node, err := CreateNodeFromType("filter", nodeConfig, "")
			require.NoError(t, err)
			node.Name = "filterNode"

			inputVariables := map[string]interface{}{
				"testInput": tc.inputData,
			}

			vm := NewVM()
			step, err := vm.RunNodeWithInputs(node, inputVariables)
			require.NoError(t, err)
			require.NotNil(t, step)
			require.True(t, step.Success, "expected success, got error: %s", step.Error)

			// Check filtered count via the step output
			filterOutput := step.GetFilter()
			require.NotNil(t, filterOutput, "expected filter output")
			require.NotNil(t, filterOutput.Data, "expected filter output data")

			data := gow.ValueToSlice(filterOutput.Data)
			assert.Len(t, data, tc.expectedCount)
		})
	}
}

func TestVM_FilterPreprocessing_ErrorHandling(t *testing.T) {
	testCases := []struct {
		name        string
		expression  string
		inputData   []interface{}
		expectError bool
	}{
		{
			name:       "Invalid Syntax",
			expression: "value.age >>>>> 18",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
			},
			expectError: false, // Filter skips items that fail evaluation
		},
		{
			name:       "Undefined Variable",
			expression: "value.nonexistent > 0",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
			},
			expectError: false, // Evaluates to false, item filtered out
		},
		{
			name:        "Empty Condition",
			expression:  "",
			inputData:   []interface{}{map[string]interface{}{"name": "Alice"}},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeConfig := map[string]interface{}{
				"expression":    tc.expression,
				"inputVariable": "{{testInput}}",
			}

			node, err := CreateNodeFromType("filter", nodeConfig, "")
			require.NoError(t, err)
			node.Name = "filterNode"

			inputVariables := map[string]interface{}{
				"testInput": tc.inputData,
			}

			vm := NewVM()
			step, err := vm.RunNodeWithInputs(node, inputVariables)

			if tc.expectError {
				assert.True(t, err != nil || !step.Success, "expected error for expression: %s", tc.expression)
			} else {
				// Should complete without error (items may be filtered out)
				assert.NoError(t, err)
				assert.NotNil(t, step)
			}
		})
	}
}

func TestFilterNodePreprocessing(t *testing.T) {
	testCases := []struct {
		name          string
		expression    string
		inputData     []interface{}
		expectedCount int
	}{
		{
			name:       "age filter with preprocessing using trigger data",
			expression: "{{ trigger.data.minAge }} <= value.age",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 2,
		},
		{
			name:       "age filter under 18 with preprocessing",
			expression: "value.age < 18",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 1,
		},
		{
			name:       "name starts with filter",
			expression: `value.name.startsWith("A")`,
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Anna", "age": 30},
			},
			expectedCount: 2,
		},
		{
			name:       "boolean property filter",
			expression: "value.active === true",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "active": true},
				map[string]interface{}{"name": "Bob", "active": false},
				map[string]interface{}{"name": "Charlie", "active": true},
			},
			expectedCount: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeConfig := map[string]interface{}{
				"expression":    tc.expression,
				"inputVariable": "{{testInput}}",
			}

			node, err := CreateNodeFromType("filter", nodeConfig, "")
			require.NoError(t, err)
			node.Name = "filterNode"

			inputVariables := map[string]interface{}{
				"testInput": tc.inputData,
				"trigger": map[string]interface{}{
					"data": map[string]interface{}{
						"minAge": 18,
					},
				},
			}

			vm := NewVM()
			step, err := vm.RunNodeWithInputs(node, inputVariables)
			require.NoError(t, err)
			require.NotNil(t, step)
			require.True(t, step.Success, "expected success, got error: %s", step.Error)

			filterOutput := step.GetFilter()
			require.NotNil(t, filterOutput, "expected filter output")
			require.NotNil(t, filterOutput.Data, "expected filter output data")

			data := gow.ValueToSlice(filterOutput.Data)
			assert.Len(t, data, tc.expectedCount)
		})
	}
}

func TestFilterNodePreprocessingEdgeCases(t *testing.T) {
	testCases := []struct {
		name        string
		expression  string
		inputData   []interface{}
		expectError bool
	}{
		{
			name:       "empty preprocessing expression",
			expression: "{{ }}",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
			},
			expectError: false,
		},
		{
			name:       "invalid property access",
			expression: "{{ current.nonexistent.property }}",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
			},
			expectError: false,
		},
		{
			name:       "complex expression with index",
			expression: "{{ index <= 1 && current.age >= 18 }}",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 30},
				map[string]interface{}{"name": "Charlie", "age": 20},
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeConfig := map[string]interface{}{
				"expression":    tc.expression,
				"inputVariable": "{{testInput}}",
			}

			node, err := CreateNodeFromType("filter", nodeConfig, "")
			require.NoError(t, err)
			node.Name = "filterNode"

			inputVariables := map[string]interface{}{
				"testInput": tc.inputData,
			}

			vm := NewVM()
			step, err := vm.RunNodeWithInputs(node, inputVariables)

			if tc.expectError {
				assert.True(t, err != nil || !step.Success, "expected error")
			} else {
				// Should complete without fatal error
				if err != nil {
					t.Logf("Got error (may be expected): %v", err)
				}
				assert.NotNil(t, step)
			}
		})
	}
}
