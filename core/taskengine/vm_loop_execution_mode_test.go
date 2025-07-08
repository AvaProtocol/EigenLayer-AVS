package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateNodeFromType_LoopExecutionMode_Sequential(t *testing.T) {
	config := map[string]interface{}{
		"source_id":      "testSource",
		"iter_val":       "item",
		"iter_key":       "index",
		"execution_mode": "sequential",
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return item * 2;",
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

	assert.Equal(t, "testSource", loopNode.Config.SourceId)
	assert.Equal(t, "item", loopNode.Config.IterVal)
	assert.Equal(t, "index", loopNode.Config.IterKey)
	assert.Equal(t, avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL, loopNode.Config.ExecutionMode)

	// Verify the runner is properly configured
	assert.NotNil(t, loopNode.GetCustomCode())
}

func TestCreateNodeFromType_LoopExecutionMode_Parallel(t *testing.T) {
	config := map[string]interface{}{
		"source_id":      "testSource",
		"iter_val":       "item",
		"iter_key":       "index",
		"execution_mode": "parallel",
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return item * 3;",
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

func TestCreateNodeFromType_LoopExecutionMode_CamelCase(t *testing.T) {
	config := map[string]interface{}{
		"sourceId":      "testSource",
		"iterVal":       "item",
		"iterKey":       "index",
		"executionMode": "sequential", // camelCase version
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return item;",
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

func TestCreateNodeFromType_LoopExecutionMode_Default(t *testing.T) {
	config := map[string]interface{}{
		"source_id": "testSource",
		"iter_val":  "item",
		"iter_key":  "index",
		// execution_mode not specified - should default to sequential
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return item;",
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
		"source_id":      "testSource",
		"iter_val":       "item",
		"iter_key":       "index",
		"execution_mode": "invalid_mode", // Invalid value - should default to sequential
		"runner": map[string]interface{}{
			"type": "customCode",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"source": "return item;",
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
				"source_id":      "testSource",
				"iter_val":       "item",
				"iter_key":       "index",
				"execution_mode": tc.mode,
				"runner": map[string]interface{}{
					"type": "customCode",
					"data": map[string]interface{}{
						"config": map[string]interface{}{
							"source": "return item;",
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
		"source_id":      "testSource",
		"iter_val":       "item",
		"iter_key":       "index",
		"execution_mode": "sequential",
		"runner": map[string]interface{}{
			"type": "restApi",
			"data": map[string]interface{}{
				"config": map[string]interface{}{
					"url":    "https://api.example.com",
					"method": "GET",
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
	assert.NotNil(t, loopNode.GetRestApi())
}

// TestCreateNodeFromType_LoopExecutionMode_BackwardCompatibility tests that
// the old loop structure (without runner field) still works
func TestCreateNodeFromType_LoopExecutionMode_BackwardCompatibility(t *testing.T) {
	config := map[string]interface{}{
		"source_id":      "testSource",
		"iter_val":       "item",
		"iter_key":       "index",
		"execution_mode": "sequential",
		// Using old structure for backward compatibility
		"customCode": map[string]interface{}{
			"config": map[string]interface{}{
				"source": "return item;",
				"lang":   "javascript",
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
	assert.NotNil(t, loopNode.GetCustomCode())
}
