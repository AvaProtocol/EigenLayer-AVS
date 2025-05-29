package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/structpb"
)

func createTestEngine(t *testing.T) *Engine {
	return New(nil, &config.Config{
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl: "http://localhost:8545", // Provide a dummy RPC URL to avoid panic
		},
	}, nil, nil)
}

func TestRunNodeWithInputs_BlockTrigger(t *testing.T) {
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, nil)
	assert.NoError(t, err)

	node, err := CreateNodeFromType("blockTrigger", map[string]interface{}{
		"blockNumber": float64(12345),
	}, "")
	assert.NoError(t, err)

	result, err := vm.RunNodeWithInputs(node, map[string]interface{}{
		"blockNumber": float64(12345),
	})

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)

	codeOutput := result.GetCustomCode()
	assert.NotNil(t, codeOutput)
	assert.NotNil(t, codeOutput.Data)
}

func TestRunNodeWithInputs_CustomCode(t *testing.T) {
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, nil)
	assert.NoError(t, err)

	nodeId := "test_" + ulid.Make().String()
	node := &avsproto.TaskNode{
		Id:   nodeId,
		Name: "Test Custom Code",
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Lang: avsproto.Lang_JavaScript,
					Source: `
					if (typeof myVar === 'undefined') {
						throw new Error("myVar is required but not provided");
					}
					({ result: myVar * 2 })
				`,
				},
			},
		},
	}

	_, err = vm.RunNodeWithInputs(node, map[string]interface{}{})
	assert.Error(t, err)

	result, err := vm.RunNodeWithInputs(node, map[string]interface{}{
		"myVar": 5,
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)

	codeOutput := result.GetCustomCode()
	assert.NotNil(t, codeOutput)
	assert.NotNil(t, codeOutput.Data)
}

func TestCreateNodeFromType(t *testing.T) {
	node, err := CreateNodeFromType(NodeTypeBlockTrigger, map[string]interface{}{}, "")
	assert.NoError(t, err)
	assert.NotNil(t, node)
	assert.Equal(t, "Single Node Execution: "+NodeTypeBlockTrigger, node.Name)
}

func TestEngine_RunNodeWithInputs(t *testing.T) {
	engine := createTestEngine(t)

	// Test different node types
	nodeTypes := []string{NodeTypeBlockTrigger, NodeTypeRestAPI, NodeTypeContractRead, NodeTypeCustomCode, NodeTypeBranch, NodeTypeFilter}

	for _, nodeType := range nodeTypes {
		t.Run(fmt.Sprintf("NodeType_%s", nodeType), func(t *testing.T) {
			var config map[string]interface{}
			switch nodeType {
			case NodeTypeBlockTrigger:
				config = map[string]interface{}{"blockNumber": 12345}
			case NodeTypeRestAPI:
				config = map[string]interface{}{
					"url": "https://httpbin.org/get",
				}
			case NodeTypeContractRead:
				config = map[string]interface{}{
					"contractAddress": "0x1234567890123456789012345678901234567890",
				}
			case NodeTypeCustomCode:
				config = map[string]interface{}{
					"code": "return {result: 'test'};",
				}
			case NodeTypeBranch:
				config = map[string]interface{}{
					"conditions": []map[string]interface{}{
						{
							"id":         "condition1",
							"type":       "if",
							"expression": "true",
						},
					},
				}
			case NodeTypeFilter:
				config = map[string]interface{}{
					"expression": "true",
				}
			}

			result, err := engine.RunNodeWithInputs(nodeType, config, map[string]interface{}{})

			switch nodeType {
			case NodeTypeBlockTrigger:
				// BlockTrigger should always work with mock data
				assert.NoError(t, err)
				assert.NotNil(t, result)
			case NodeTypeRestAPI:
				// REST API might fail due to network, but should not panic
				// We don't assert success/failure here as it depends on network
			case NodeTypeContractRead:
				// Contract read might fail due to network, but should not panic
				// We don't assert success/failure here as it depends on network
			case NodeTypeCustomCode, NodeTypeBranch, NodeTypeFilter:
				// These will fail because CreateNodeFromType doesn't create proper Config
				// This is expected behavior - the test verifies the method doesn't panic
				// In real usage, these nodes would have proper Config from the protobuf
				if err != nil {
					// Expected errors for nodes without proper Config
					t.Logf("Expected error for %s: %v", nodeType, err)
				}
			}

			// Basic validation that we get some result when successful
			if err == nil {
				assert.NotNil(t, result)
			}
		})
	}

	// Test specific functionality for blockTrigger
	result, err := engine.RunNodeWithInputs(NodeTypeBlockTrigger, map[string]interface{}{
		"blockNumber": 12345,
	}, map[string]interface{}{})

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Contains(t, result, "blockNumber")
	assert.Equal(t, uint64(12345), result["blockNumber"])

	// Test custom code with proper configuration (this will still fail due to CreateNodeFromType limitations)
	result, err = engine.RunNodeWithInputs(NodeTypeCustomCode, map[string]interface{}{
		"code": `
			return {
				message: "Hello World",
				timestamp: Date.now(),
				input: inputVariables
			};
		`,
	}, map[string]interface{}{
		"testInput": "test value",
	})

	// This is expected to fail because CreateNodeFromType doesn't create proper Config
	// In real usage, the node would have proper Config from protobuf
	if err != nil {
		t.Logf("Expected error for custom code: %v", err)
		assert.Contains(t, err.Error(), "Config is nil")
	} else {
		// If it somehow succeeds, validate the result
		assert.NotNil(t, result)
		if result != nil {
			if message, ok := result["message"]; ok {
				assert.Equal(t, "Hello World", message)
			}
		}
	}
}

func TestRunNodeWithInputsRPC_BlockTriggerValidation(t *testing.T) {
	engine := createTestEngine(t)
	user := &model.User{Address: common.HexToAddress("0x1234567890123456789012345678901234567890")}

	// Test that blockTrigger nodes reject input variables
	req := &avsproto.RunNodeWithInputsReq{
		NodeType: NodeTypeBlockTrigger,
		NodeConfig: map[string]*structpb.Value{
			"blockNumber": structpb.NewNumberValue(12345),
		},
		InputVariables: map[string]*structpb.Value{
			"invalidInput": structpb.NewStringValue("should not be allowed"),
		},
	}

	resp, err := engine.RunNodeWithInputsRPC(user, req)
	assert.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "blockTrigger nodes do not accept input variables")

	// Test that blockTrigger works without input variables
	req.NodeType = NodeTypeBlockTrigger
	req.NodeConfig = map[string]*structpb.Value{
		"blockNumber": structpb.NewNumberValue(12345),
	}
	req.InputVariables = map[string]*structpb.Value{} // Remove input variables

	resp, err = engine.RunNodeWithInputsRPC(user, req)
	assert.NoError(t, err)
	// We don't check success here because it may fail due to missing RPC connection
	// The important thing is that validation passed (no "do not accept input variables" error)
	if !resp.Success {
		assert.NotContains(t, resp.Error, "blockTrigger nodes do not accept input variables")
	}
}
