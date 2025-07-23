package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopNode_ManualTriggerDataAccess(t *testing.T) {
	t.Run("should access ManualTrigger.data automatically", func(t *testing.T) {
		// Create VM
		vm := NewVM()
		if vm == nil {
			t.Fatal("Failed to create VM")
		}

		// Simulate the exact scenario from the user's input
		// This matches the structure that comes from the client
		inputVariables := map[string]interface{}{
			"manualTrigger": map[string]interface{}{
				"data": []interface{}{
					map[string]interface{}{"key": "value1"},
					map[string]interface{}{"key": "value2"},
				},
				"input": map[string]interface{}{
					"data": []interface{}{
						map[string]interface{}{"key": "value1"},
						map[string]interface{}{"key": "value2"},
					},
					"headers":    []interface{}{map[string]interface{}{"key": "headerKey", "value": "headerValue"}},
					"pathParams": []interface{}{map[string]interface{}{"key": "pathKey", "value": "pathValue"}},
				},
			},
			"workflowContext": map[string]interface{}{
				"id":             "b11e5165-be36-4b97-a36a-52ac8c2c67ca",
				"chainId":        nil,
				"name":           "Jul 11, 2025 5:04 PM",
				"userId":         "03396f6d-9fb7-49f9-96a5-9025c4998e40",
				"eoaAddress":     "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
				"startAt":        "2025-07-12T00:04:25.789Z",
				"expiredAt":      "2025-08-12T00:04:25.789Z",
				"maxExecution":   0,
				"status":         "draft",
				"completedAt":    nil,
				"lastRanAt":      nil,
				"executionCount": nil,
				"createdAt":      "2025-07-12T00:04:25.794Z",
				"updatedAt":      "2025-07-23T05:49:59.386Z",
			},
		}

		// Create the exact loop node configuration from the user's example
		nodeConfig := map[string]interface{}{
			"inputNodeName": "manualTrigger", // This should automatically access .data
			"iterVal":       "value",
			"iterKey":       "index",
			"executionMode": "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   0, // JavaScript
					"source": "return value;",
				},
			},
		}

		// Create the task node using the same method as the client
		node, err := CreateNodeFromType("loop", nodeConfig, "")
		require.NoError(t, err, "Should create loop node successfully")
		require.NotNil(t, node, "Node should not be nil")

		// Set a valid JavaScript identifier name
		node.Name = "testLoopNode"

		t.Logf("🧪 Testing loop node with inputNodeName: %s", nodeConfig["inputNodeName"])
		t.Logf("📥 Input data structure: %+v", inputVariables["manualTrigger"])

		// Execute the loop node with the input variables
		executionStep, err := vm.RunNodeWithInputs(node, inputVariables)

		// Verify the execution was successful
		require.NoError(t, err, "Loop node execution should not return error")
		require.NotNil(t, executionStep, "Execution step should not be nil")
		assert.True(t, executionStep.Success, "Loop node execution should be successful. Error: %s", executionStep.Error)

		// Verify the loop processed the correct number of items
		if loopOutput := executionStep.GetLoop(); loopOutput != nil && loopOutput.Data != nil {
			results := loopOutput.Data.AsInterface()
			t.Logf("📤 Loop results: %+v", results)

			if resultsArray, ok := results.([]interface{}); ok {
				assert.Len(t, resultsArray, 2, "Should process 2 items from manualTrigger.data")

				// Verify each result matches the expected output
				expectedResults := []interface{}{
					map[string]interface{}{"key": "value1"},
					map[string]interface{}{"key": "value2"},
				}

				for i, expectedResult := range expectedResults {
					assert.Equal(t, expectedResult, resultsArray[i], "Result %d should match expected value", i)
				}

				t.Logf("✅ Successfully processed %d items from manualTrigger.data", len(resultsArray))
			} else {
				t.Errorf("❌ Expected array results, got %T: %+v", results, results)
			}
		} else {
			t.Error("❌ Loop output data is nil")
		}
	})

	t.Run("should handle error when data field is not an array", func(t *testing.T) {
		vm := NewVM()
		if vm == nil {
			t.Fatal("Failed to create VM")
		}

		// Test with non-array data
		inputVariables := map[string]interface{}{
			"manualTrigger": map[string]interface{}{
				"data": "not an array", // This should cause an error
				"input": map[string]interface{}{
					"data": "not an array",
				},
			},
		}

		nodeConfig := map[string]interface{}{
			"inputNodeName": "manualTrigger",
			"iterVal":       "value",
			"iterKey":       "index",
			"executionMode": "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   0,
					"source": "return value;",
				},
			},
		}

		node, err := CreateNodeFromType("loop", nodeConfig, "")
		require.NoError(t, err)
		node.Name = "testLoopNode"

		// Execute the loop node - this should fail with a clear error message
		executionStep, err := vm.RunNodeWithInputs(node, inputVariables)

		if err != nil {
			// Error returned directly from RunNodeWithInputs
			assert.Contains(t, err.Error(), "expected array", "Error message should indicate array was expected")
			t.Logf("✅ Correctly handled non-array data with error: %s", err.Error())
		} else {
			// Error in execution step
			require.NotNil(t, executionStep, "Execution step should not be nil")
			assert.False(t, executionStep.Success, "Loop node execution should fail")
			assert.Contains(t, executionStep.Error, "expected array", "Error message should indicate array was expected")
			t.Logf("✅ Correctly handled non-array data with error: %s", executionStep.Error)
		}
	})

	t.Run("should handle missing data field gracefully", func(t *testing.T) {
		vm := NewVM()
		if vm == nil {
			t.Fatal("Failed to create VM")
		}

		// Test with missing data field
		inputVariables := map[string]interface{}{
			"manualTrigger": map[string]interface{}{
				"input": map[string]interface{}{
					"headers": []interface{}{},
				},
				// No "data" field
			},
		}

		nodeConfig := map[string]interface{}{
			"inputNodeName": "manualTrigger",
			"iterVal":       "value",
			"iterKey":       "index",
			"executionMode": "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   0,
					"source": "return value;",
				},
			},
		}

		node, err := CreateNodeFromType("loop", nodeConfig, "")
		require.NoError(t, err)
		node.Name = "testLoopNode"

		// Execute the loop node - this should fail with a clear error message
		executionStep, err := vm.RunNodeWithInputs(node, inputVariables)

		if err != nil {
			// Error returned directly from RunNodeWithInputs
			assert.Contains(t, err.Error(), "no 'data' field", "Error message should indicate missing data field")
			t.Logf("✅ Correctly handled missing data field with error: %s", err.Error())
		} else {
			// Error in execution step
			require.NotNil(t, executionStep, "Execution step should not be nil")
			assert.False(t, executionStep.Success, "Loop node execution should fail")
			assert.Contains(t, executionStep.Error, "no 'data' field", "Error message should indicate missing data field")
			t.Logf("✅ Correctly handled missing data field with error: %s", executionStep.Error)
		}
	})
}
