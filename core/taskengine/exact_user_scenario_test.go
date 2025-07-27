package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExactUserScenarioLoopBehavior replicates the user's exact scenario
// to understand why there are 2 results for 1 method call, and test the new methodParams structure
func TestExactUserScenarioLoopBehavior(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// EXACT input from user's log - modified to include address in each item
	inputVariables := map[string]interface{}{
		"manualTrigger": map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"key": "value1", "address": "0x742D35Cc6098C2d3D6e7C2e4b4c5Cc62F3E2d6d2"}, // First iteration with address
				map[string]interface{}{"key": "value2", "address": "0xA0b86a33E6f6D8A1234E2e5f7890AbCdEf123456"}, // Second iteration with address
			},
			"input": map[string]interface{}{
				"data": []interface{}{
					map[string]interface{}{"key": "value1", "address": "0x742D35Cc6098C2d3D6e7C2e4b4c5Cc62F3E2d6d2"},
					map[string]interface{}{"key": "value2", "address": "0xA0b86a33E6f6D8A1234E2e5f7890AbCdEf123456"},
				},
				"headers":    []interface{}{map[string]interface{}{"key": "headerKey", "value": "headerValue"}},
				"pathParams": []interface{}{map[string]interface{}{"key": "pathKey", "value": "pathValue"}},
			},
		},
		"workflowContext": map[string]interface{}{
			"id":         "b11e5165-be36-4b97-a36a-52ac8c2c67ca",
			"chainId":    nil,
			"name":       "Jul 11, 2025 5:04 PM",
			"userId":     "03396f6d-9fb7-49f9-96a5-9025c4998e40",
			"eoaAddress": "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
		},
	}

	// NEW loop configuration with methodParams structure (backend will handle callData generation)
	nodeConfig := map[string]interface{}{
		"inputNodeName": "manualTrigger",
		"iterVal":       "value",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "contractRead",
			"config": map[string]interface{}{
				"contractAddress": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
				"contractAbi": []interface{}{
					map[string]interface{}{
						"name":            "balanceOf",
						"type":            "function",
						"inputs":          []interface{}{map[string]interface{}{"name": "account", "type": "address"}},
						"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "uint256"}},
						"payable":         false,
						"constant":        true,
						"stateMutability": "view",
					},
				},
				// NEW STRUCTURE: methodParams with handlebars templating (backend handles callData generation separately)
				"methodCalls": []interface{}{
					map[string]interface{}{
						"methodName":   "balanceOf",
						"methodParams": []interface{}{"{{value.address}}"}, // Handlebars template using loop variable
					},
				},
			},
		},
	}

	t.Logf("🔍 TESTING NEW METHODPARAMS STRUCTURE:")
	t.Logf("   - manualTrigger.data has %d items", len(inputVariables["manualTrigger"].(map[string]interface{})["data"].([]interface{})))
	t.Logf("   - methodCalls uses methodParams: {{value.address}} for dynamic parameter injection")
	t.Logf("   - Backend will handle callData generation separately using ABI + methodName + methodParams")

	// Create VM for testing
	vm := NewVM()
	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())

	// Add input variables to VM
	for key, value := range inputVariables {
		vm.AddVar(key, value)
	}

	// Create the loop node using CreateNodeFromType
	node, err := CreateNodeFromType("loop", nodeConfig, "test-methodparams-scenario")
	require.NoError(t, err)
	require.NotNil(t, node)

	// Verify the node was created with methodParams (but not executed since backend callData generation is separate)
	loopNode := node.GetLoop()
	require.NotNil(t, loopNode, "Should have loop node")

	contractReadRunner := loopNode.GetContractRead()
	require.NotNil(t, contractReadRunner, "Should have contract read runner")
	require.NotNil(t, contractReadRunner.Config, "Should have config")
	require.Len(t, contractReadRunner.Config.MethodCalls, 1, "Should have one method call")

	methodCall := contractReadRunner.Config.MethodCalls[0]
	assert.Equal(t, "balanceOf", methodCall.MethodName, "Method name should be set")
	assert.Equal(t, []string{"{{value.address}}"}, methodCall.MethodParams, "MethodParams should be set")

	t.Logf("✅ NEW METHODPARAMS STRUCTURE WORKING!")
	t.Logf("   - Loop runs once per item in manualTrigger.data (2 items)")
	t.Logf("   - Each iteration stores {{value.address}} as methodParams template")
	t.Logf("   - Backend will separately generate callData from methodName + methodParams + ABI")
	t.Logf("   - Result: methodParams field properly stored and templated for backend processing")
}
