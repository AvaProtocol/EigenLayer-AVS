package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestExtractNodeConfiguration_LoopNode(t *testing.T) {
	// Create a LoopNode with REST API runner
	loopNode := &avsproto.TaskNode{
		Id:   "test-loop-node",
		Name: "Test Loop Node",
		Type: avsproto.NodeType_NODE_TYPE_LOOP,
		TaskType: &avsproto.TaskNode_Loop{
			Loop: &avsproto.LoopNode{
				Config: &avsproto.LoopNode_Config{
					SourceId:      "source-node-id",
					IterVal:       "value",
					IterKey:       "index",
					ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL,
				},
				Runner: &avsproto.LoopNode_RestApi{
					RestApi: &avsproto.RestAPINode{
						Config: &avsproto.RestAPINode_Config{
							Url:    "{{value}}",
							Method: "GET",
							Body:   "",
							Headers: map[string]string{
								"User-Agent": "AvaProtocol-Loop-Test",
							},
						},
					},
				},
			},
		},
	}

	// Test ExtractNodeConfiguration
	config := ExtractNodeConfiguration(loopNode)

	// Verify basic configuration
	assert.NotNil(t, config)
	assert.Equal(t, "source-node-id", config["sourceId"])
	assert.Equal(t, "value", config["iterVal"])
	assert.Equal(t, "index", config["iterKey"])
	assert.Equal(t, "EXECUTION_MODE_PARALLEL", config["executionMode"])

	// Verify runner configuration
	assert.Contains(t, config, "runner")
	runner, ok := config["runner"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "restApi", runner["type"])
	assert.Equal(t, "{{value}}", runner["url"])
	assert.Equal(t, "GET", runner["method"])
	assert.Equal(t, "", runner["body"])
	assert.Contains(t, runner, "headers")

	t.Logf("✅ ExtractNodeConfiguration for LoopNode works correctly")
	t.Logf("Config: %+v", config)
}

func TestExtractNodeConfiguration_CustomCodeNode(t *testing.T) {
	// Create a CustomCode node for comparison
	customCodeNode := &avsproto.TaskNode{
		Id:   "test-custom-code-node",
		Name: "Test Custom Code Node",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Source: "return 'hello world';",
					Lang:   avsproto.Lang_JavaScript,
				},
			},
		},
	}

	// Test ExtractNodeConfiguration
	config := ExtractNodeConfiguration(customCodeNode)

	// Verify configuration
	assert.NotNil(t, config)
	assert.Equal(t, "return 'hello world';", config["source"])
	assert.Equal(t, "JavaScript", config["lang"])

	t.Logf("✅ ExtractNodeConfiguration for CustomCodeNode works correctly")
	t.Logf("Config: %+v", config)
}
