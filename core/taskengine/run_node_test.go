package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
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
				Lang: avsproto.CustomCodeLang_JavaScript,
				Source: `
					if (typeof myVar === 'undefined') {
						throw new Error("myVar is required but not provided");
					}
					return { result: myVar * 2 };
				`,
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
			config["source"] = "return { hello: 'world' };"
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
		assert.Equal(t, "Single Node Execution", node.Name)

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
		"source": "return { message: 'Hello, World!' };",
	}, map[string]interface{}{})

	if err == nil {
		assert.NotNil(t, result)
		assert.Contains(t, result, "message")
		assert.Equal(t, "Hello, World!", result["message"])
	}
}
