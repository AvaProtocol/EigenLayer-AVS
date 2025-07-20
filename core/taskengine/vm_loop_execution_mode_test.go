package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateNodeFromType_LoopExecutionMode_Sequential(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "testSource",
		"iterVal":       "item",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return value * 2;",
					"lang":   "javascript",
				},
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, avsproto.NodeType_NODE_TYPE_LOOP, node.Type)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	assert.Equal(t, "testSource", loopNode.Config.InputNodeName)
	assert.Equal(t, "item", loopNode.Config.IterVal)
	assert.Equal(t, "index", loopNode.Config.IterKey)
	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)

	// Verify the runner is properly configured
	assert.NotNil(t, loopNode.GetCustomCode())
}

func TestCreateNodeFromType_LoopExecutionMode_Parallel(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "testSource",
		"iterVal":       "item",
		"iterKey":       "index",
		"executionMode": "parallel",
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return value * 3;",
					"lang":   "javascript",
				},
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL, loopNode.Config.ExecutionMode)
}

func TestCreateNodeFromType_LoopExecutionMode_Default(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "testSource",
		"iterVal":       "item",
		"iterKey":       "index",
		// executionMode not specified - should default to sequential
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return value;",
					"lang":   "javascript",
				},
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)
}

func TestCreateNodeFromType_LoopExecutionMode_InvalidValue(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "testSource",
		"iterVal":       "item",
		"iterKey":       "index",
		"executionMode": "invalid_mode", // Invalid value - should default to sequential
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return value;",
					"lang":   "javascript",
				},
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	// Invalid values should default to sequential
	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)
}

func TestCreateNodeFromType_LoopExecutionMode_CaseInsensitive(t *testing.T) {
	testCases := []struct {
		name         string
		mode         string
		expectedMode avsproto.ExecutionMode
	}{
		{"uppercase_sequential", "SEQUENTIAL", avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL},
		{"mixed_case_sequential", "Sequential", avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL},
		{"uppercase_parallel", "PARALLEL", avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL},
		{"mixed_case_parallel", "Parallel", avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]interface{}{
				"inputNodeName": "testSource",
				"iterVal":       "item",
				"iterKey":       "index",
				"executionMode": tc.mode,
				"runner": map[string]interface{}{
					"type": "customCode",
					"data": map[string]interface{}{
						"config": map[string]interface{}{
							"source": "return value;",
							"lang":   "javascript",
						},
					},
				},
			}

			node, err := CreateNodeFromType(NodeTypeLoop, config, "test-loop-node")

			require.NoError(t, err)
			require.NotNil(t, node)

			loopNode := node.GetLoop()
			require.NotNil(t, loopNode)
			require.NotNil(t, loopNode.Config)

			assert.Equal(t, tc.expectedMode, loopNode.Config.ExecutionMode)
		})
	}
}

func TestCreateNodeFromType_LoopExecutionMode_WithRestApiRunner(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "testSource",
		"iterVal":       "item",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "restApi",
			"config": map[string]interface{}{
				"url":    "https://api.example.com",
				"method": "GET",
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)
	assert.NotNil(t, loopNode.GetRestApi())
}

func TestCreateNodeFromType_LoopExecutionMode_WithContractReadRunner(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "contractAddresses",
		"iterVal":       "value",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "contractRead",
			"config": map[string]interface{}{
				"contractAddress": "{{value}}",
				"contractAbi":     `[{"constant":true,"inputs":[],"name":"totalSupply","outputs":[{"name":"","type":"uint256"}],"payable":false,"stateMutability":"view","type":"function"}]`,
				"methodCalls": []interface{}{
					map[string]interface{}{
						"methodName": "totalSupply",
						"callData":   "0x18160ddd",
					},
				},
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-contractread-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, avsproto.NodeType_NODE_TYPE_LOOP, node.Type)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	assert.Equal(t, "contractAddresses", loopNode.Config.InputNodeName)
	assert.Equal(t, "value", loopNode.Config.IterVal)
	assert.Equal(t, "index", loopNode.Config.IterKey)
	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)

	// Verify the contract read runner is properly configured
	contractRead := loopNode.GetContractRead()
	require.NotNil(t, contractRead)
	assert.Equal(t, "{{value}}", contractRead.Config.ContractAddress)
	assert.NotEmpty(t, contractRead.Config.ContractAbi)
	assert.Len(t, contractRead.Config.MethodCalls, 1)
	assert.Equal(t, "totalSupply", contractRead.Config.MethodCalls[0].MethodName)
}

func TestCreateNodeFromType_LoopExecutionMode_WithContractWriteRunner(t *testing.T) {
	config := map[string]interface{}{
		"inputNodeName": "contractAddresses",
		"iterVal":       "value",
		"iterKey":       "index",
		"executionMode": "parallel", // Will be forced to sequential due to contract write
		"runner": map[string]interface{}{
			"type": "contractWrite",
			"config": map[string]interface{}{
				"contractAddress": "{{value}}",
				"contractAbi":     `[{"constant":false,"inputs":[{"name":"_spender","type":"address"},{"name":"_value","type":"uint256"}],"name":"approve","outputs":[{"name":"","type":"bool"}],"payable":false,"stateMutability":"nonpayable","type":"function"}]`,
				"methodCalls": []interface{}{
					map[string]interface{}{
						"methodName": "approve",
						"callData":   "0x095ea7b3000000000000000000000000{{value}}0000000000000000000000000000000000000000000000000000000000000064",
					},
				},
			},
		},
	}

	node, err := CreateNodeFromType(NodeTypeLoop, config, "test-contractwrite-loop-node")

	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, avsproto.NodeType_NODE_TYPE_LOOP, node.Type)

	loopNode := node.GetLoop()
	require.NotNil(t, loopNode)
	require.NotNil(t, loopNode.Config)

	assert.Equal(t, "contractAddresses", loopNode.Config.InputNodeName)
	assert.Equal(t, "value", loopNode.Config.IterVal)
	assert.Equal(t, "index", loopNode.Config.IterKey)
	// Note: executionMode is set at configuration time, the sequential enforcement happens at runtime
	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL, loopNode.Config.ExecutionMode)

	// Verify the contract write runner is properly configured
	contractWrite := loopNode.GetContractWrite()
	require.NotNil(t, contractWrite)
	assert.Equal(t, "{{value}}", contractWrite.Config.ContractAddress)
	assert.NotEmpty(t, contractWrite.Config.ContractAbi)
	assert.Len(t, contractWrite.Config.MethodCalls, 1)
	assert.Equal(t, "approve", contractWrite.Config.MethodCalls[0].MethodName)
}

func TestCreateNodeFromType_LoopExecutionMode_CamelCaseFields(t *testing.T) {
	// Test that camelCase field names are the standard format
	config := map[string]interface{}{
		"inputNodeName": "testSource",
		"iterVal":       "value",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return { doubled: item * 2 };",
					"lang":   "javascript",
				},
			},
		},
	}

	node, err := CreateNodeFromType("loop", config, "testNodeId")
	assert.NoError(t, err)
	assert.NotNil(t, node)
	assert.Equal(t, avsproto.NodeType_NODE_TYPE_LOOP, node.Type)

	loopNode := node.TaskType.(*avsproto.TaskNode_Loop).Loop
	assert.NotNil(t, loopNode)
	assert.NotNil(t, loopNode.Config)
	assert.Equal(t, "testSource", loopNode.Config.InputNodeName)
	assert.Equal(t, "value", loopNode.Config.IterVal)
	assert.Equal(t, "index", loopNode.Config.IterKey)
	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)
}
