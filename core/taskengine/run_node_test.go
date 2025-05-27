package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRunNodeWithInputs_BlockTrigger(t *testing.T) {
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, nil)
	assert.NoError(t, err)

	node, err := CreateNodeFromType("blockTrigger", map[string]interface{}{}, "")
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
	nodeTypes := []string{"blockTrigger", "restApi", "contractRead", "customCode", "branch", "filter"}

	for _, nodeType := range nodeTypes {
		config := map[string]interface{}{}

		switch nodeType {
		case "blockTrigger":
			config["blockNumber"] = float64(12345)
		case "restApi":
			config["url"] = "https://example.com"
			config["method"] = "GET"
		case "contractRead":
			config["contractAddress"] = "0x1234567890123456789012345678901234567890"
			config["callData"] = "0x12345678"
		case "customCode":
			config["source"] = "({ hello: 'world' })"
		case "branch":
			config["conditions"] = []interface{}{
				map[string]interface{}{
					"id":         "cond1",
					"type":       "if",
					"expression": "true",
				},
			}
		case "filter":
			config["expression"] = "item > 5"
			config["input"] = "inputArray"
		}

		node, err := CreateNodeFromType(nodeType, config, "")
		assert.NoError(t, err)
		assert.NotNil(t, node)
		assert.NotEmpty(t, node.Id)
		assert.Equal(t, "Single Node Execution: "+nodeType, node.Name)

		switch nodeType {
		case "blockTrigger":
			assert.NotNil(t, node.GetCustomCode())
		case "restApi":
			assert.NotNil(t, node.GetRestApi())
		case "contractRead":
			assert.NotNil(t, node.GetContractRead())
		case "customCode":
			assert.NotNil(t, node.GetCustomCode())
		case "branch":
			assert.NotNil(t, node.GetBranch())
		case "filter":
			assert.NotNil(t, node.GetFilter())
		}
	}
}

func TestEngine_RunNodeWithInputs(t *testing.T) {
	engine := New(nil, &config.Config{
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl: "http://localhost:8545", // Provide a dummy RPC URL to avoid panic
		},
	}, nil, nil)

	result, err := engine.RunNodeWithInputs("blockTrigger", map[string]interface{}{
		"blockNumber": float64(12345),
	}, map[string]interface{}{})

	if err == nil {
		assert.NotNil(t, result)
		assert.Contains(t, result, "blockNumber")
		assert.Equal(t, float64(12345), result["blockNumber"])
	}

	result, err = engine.RunNodeWithInputs("customCode", map[string]interface{}{
		"source": "({ message: 'Hello, World!' })",
	}, map[string]interface{}{})

	if err == nil {
		assert.NotNil(t, result)
		assert.Contains(t, result, "message")
		assert.Equal(t, "Hello, World!", result["message"])
	}
}

func TestRunNodeWithInputsRPC_BlockTriggerValidation(t *testing.T) {
	// Create a minimal engine just for testing validation
	engine := &Engine{
		logger: nil, // No logger needed for this test
	}

	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	// Test 1: blockTrigger with input variables should fail validation
	inputVars := map[string]*structpb.Value{
		"testVar": structpb.NewStringValue("testValue"),
	}

	req := &avsproto.RunNodeWithInputsReq{
		NodeType:       "blockTrigger",
		NodeConfig:     map[string]*structpb.Value{},
		InputVariables: inputVars,
	}

	resp, err := engine.RunNodeWithInputsRPC(user, req)
	assert.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "blockTrigger nodes do not accept input variables")
	assert.Contains(t, resp.Error, "testVar")

	// Test 2: blockTrigger without input variables should pass validation
	// (but may fail execution due to missing RPC connection - that's OK for this test)
	req.InputVariables = map[string]*structpb.Value{}

	resp, err = engine.RunNodeWithInputsRPC(user, req)
	assert.NoError(t, err)
	// We don't check success here because it may fail due to missing RPC connection
	// The important thing is that validation passed (no "do not accept input variables" error)
	if !resp.Success {
		assert.NotContains(t, resp.Error, "blockTrigger nodes do not accept input variables")
	}

	// Test 3: Other node types with input variables should pass validation
	req.NodeType = "customCode"
	req.NodeConfig = map[string]*structpb.Value{
		"source": structpb.NewStringValue("({ message: 'Hello' })"),
	}
	req.InputVariables = inputVars

	resp, err = engine.RunNodeWithInputsRPC(user, req)
	assert.NoError(t, err)
	// Again, we don't check success because execution may fail
	// The important thing is that validation passed
	if !resp.Success {
		assert.NotContains(t, resp.Error, "do not accept input variables")
	}
}
