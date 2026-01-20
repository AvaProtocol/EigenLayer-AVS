package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// ========== ExtractStepConfig Tests ==========

func TestExtractStepConfig_NilStep(t *testing.T) {
	result := ExtractStepConfig(nil, nil, nil)
	assert.Nil(t, result, "ExtractStepConfig should return nil for nil step")
}

func TestExtractStepConfig_TriggerStep_WithTriggerDefinition(t *testing.T) {
	// Create a trigger step
	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_BLOCK",
		Name: "BlockTrigger",
	}

	// Create a trigger definition
	trigger := &avsproto.TaskTrigger{
		Name: "BlockTrigger",
		TriggerType: &avsproto.TaskTrigger_Block{
			Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{
					Interval: 10,
				},
			},
		},
	}

	result := ExtractStepConfig(step, nil, trigger)
	require.NotNil(t, result, "ExtractStepConfig should return config for trigger step")

	configMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, int64(10), configMap["interval"], "Should have correct interval from TaskTriggerToConfig")
}

func TestExtractStepConfig_TriggerStep_EventTrigger(t *testing.T) {
	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_EVENT",
		Name: "EventTrigger",
	}

	trigger := &avsproto.TaskTrigger{
		Name: "EventTrigger",
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{
					Queries: []*avsproto.EventTrigger_Query{
						{
							Addresses: []string{"0x1234567890123456789012345678901234567890"},
						},
					},
				},
			},
		},
	}

	result := ExtractStepConfig(step, nil, trigger)
	require.NotNil(t, result, "ExtractStepConfig should return config for event trigger step")

	configMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Contains(t, configMap, "queries", "Should have queries from TaskTriggerToConfig")
}

func TestExtractStepConfig_NodeStep_WithTaskNode(t *testing.T) {
	stepID := "contract-read-node"
	step := &avsproto.Execution_Step{
		Id:   stepID,
		Type: "NODE_TYPE_CONTRACT_READ",
		Name: "ReadDecimals",
	}

	taskNodes := map[string]*avsproto.TaskNode{
		stepID: {
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: &avsproto.ContractReadNode{
					Config: &avsproto.ContractReadNode_Config{
						ContractAddress: "0x1234567890123456789012345678901234567890",
						MethodCalls: []*avsproto.ContractReadNode_MethodCall{
							{
								MethodName: "decimals",
							},
						},
					},
				},
			},
		},
	}

	result := ExtractStepConfig(step, taskNodes, nil)
	require.NotNil(t, result, "ExtractStepConfig should return config from TaskNodes")

	configMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "0x1234567890123456789012345678901234567890", configMap["contractAddress"], "Should have contractAddress from ExtractNodeConfiguration")
	assert.Contains(t, configMap, "methodCalls", "Should have methodCalls from ExtractNodeConfiguration")
}

func TestExtractStepConfig_NodeStep_FallbackToStepConfig(t *testing.T) {
	// When TaskNode is not available, should fallback to step.Config
	stepID := "unknown-node"
	configValue, _ := structpb.NewValue(map[string]interface{}{
		"url":    "https://api.example.com",
		"method": "GET",
	})

	step := &avsproto.Execution_Step{
		Id:     stepID,
		Type:   "NODE_TYPE_REST_API",
		Name:   "FetchData",
		Config: configValue,
	}

	// Empty taskNodes - node not found
	taskNodes := map[string]*avsproto.TaskNode{}

	result := ExtractStepConfig(step, taskNodes, nil)
	require.NotNil(t, result, "ExtractStepConfig should fallback to step.Config")

	configMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "https://api.example.com", configMap["url"], "Should have url from step.Config fallback")
	assert.Equal(t, "GET", configMap["method"], "Should have method from step.Config fallback")
}

func TestExtractStepConfig_NodeStep_NilTaskNodes(t *testing.T) {
	// When taskNodes is nil (not just empty), should fallback to step.Config
	stepID := "node-1"
	configValue, _ := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1234567890123456789012345678901234567890",
		"methodName":      "balanceOf",
	})

	step := &avsproto.Execution_Step{
		Id:     stepID,
		Type:   "NODE_TYPE_CONTRACT_READ",
		Name:   "ReadBalance",
		Config: configValue,
	}

	// nil taskNodes (different from empty map)
	result := ExtractStepConfig(step, nil, nil)
	require.NotNil(t, result, "ExtractStepConfig should fallback to step.Config when taskNodes is nil")

	configMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "0x1234567890123456789012345678901234567890", configMap["contractAddress"])
	assert.Equal(t, "balanceOf", configMap["methodName"])
}

func TestExtractStepConfig_TriggerStep_NilTrigger_FallbackToStepConfig(t *testing.T) {
	// When trigger is nil, should fallback to step.Config for trigger steps
	configValue, _ := structpb.NewValue(map[string]interface{}{
		"interval": float64(10),
	})

	step := &avsproto.Execution_Step{
		Id:     "trigger-step",
		Type:   "TRIGGER_TYPE_BLOCK",
		Name:   "BlockTrigger",
		Config: configValue,
	}

	// nil trigger - should fallback to step.Config
	result := ExtractStepConfig(step, nil, nil)
	require.NotNil(t, result, "ExtractStepConfig should fallback to step.Config when trigger is nil")

	configMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(10), configMap["interval"], "Should have interval from step.Config fallback")
}

func TestExtractStepConfig_TriggerStep_NilTrigger_NoStepConfig(t *testing.T) {
	// When trigger is nil and step.Config is nil, should return nil
	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_BLOCK",
		Name: "BlockTrigger",
		// No Config set
	}

	result := ExtractStepConfig(step, nil, nil)
	assert.Nil(t, result, "ExtractStepConfig should return nil when trigger is nil and step.Config is nil")
}

func TestExtractStepConfig_ProtobufCompatibility(t *testing.T) {
	// Test that extracted config can be converted to protobuf
	// Using BlockTrigger which has simple types that are protobuf-compatible
	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_BLOCK",
		Name: "BlockTrigger",
	}

	trigger := &avsproto.TaskTrigger{
		Name: "BlockTrigger",
		TriggerType: &avsproto.TaskTrigger_Block{
			Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{
					Interval: 5,
				},
			},
		},
	}

	result := ExtractStepConfig(step, nil, trigger)
	require.NotNil(t, result, "ExtractStepConfig should return config")

	// Should be convertible to protobuf without error
	_, err := structpb.NewValue(result)
	assert.NoError(t, err, "Extracted config should be protobuf-compatible")
}

// ========== ExtractStepOutput Tests ==========

func TestExtractStepOutput_NilStep(t *testing.T) {
	result := ExtractStepOutput(nil)
	assert.Nil(t, result, "ExtractStepOutput should return nil for nil step")
}

func TestExtractStepOutput_BlockTrigger(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"block_number": float64(12345678),
		"block_hash":   "0xabc123",
		"timestamp":    float64(1700000000),
	})

	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_BLOCK",
		OutputData: &avsproto.Execution_Step_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for block trigger")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(12345678), outputMap["block_number"])
}

func TestExtractStepOutput_EventTrigger(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"tx_hash": "0xdef456",
		"events": []interface{}{
			map[string]interface{}{
				"name":    "Transfer",
				"address": "0x1234",
			},
		},
	})

	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_EVENT",
		OutputData: &avsproto.Execution_Step_EventTrigger{
			EventTrigger: &avsproto.EventTrigger_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for event trigger")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "0xdef456", outputMap["tx_hash"])
}

func TestExtractStepOutput_ManualTrigger(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"user_input": "test value",
		"count":      float64(42),
	})

	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_MANUAL",
		OutputData: &avsproto.Execution_Step_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for manual trigger")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "test value", outputMap["user_input"])
	assert.Equal(t, float64(42), outputMap["count"])
}

func TestExtractStepOutput_CronTrigger(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"epoch": float64(1700000000),
	})

	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_CRON",
		OutputData: &avsproto.Execution_Step_CronTrigger{
			CronTrigger: &avsproto.CronTrigger_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for cron trigger")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(1700000000), outputMap["epoch"])
}

func TestExtractStepOutput_FixedTimeTrigger(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"epoch": float64(1700000000),
	})

	step := &avsproto.Execution_Step{
		Id:   "trigger-step",
		Type: "TRIGGER_TYPE_FIXED_TIME",
		OutputData: &avsproto.Execution_Step_FixedTimeTrigger{
			FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for fixed time trigger")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(1700000000), outputMap["epoch"])
}

func TestExtractStepOutput_ContractRead(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"decimals": float64(18),
		"symbol":   "USDC",
	})

	step := &avsproto.Execution_Step{
		Id:   "contract-read-node",
		Type: "NODE_TYPE_CONTRACT_READ",
		OutputData: &avsproto.Execution_Step_ContractRead{
			ContractRead: &avsproto.ContractReadNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for contract read")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(18), outputMap["decimals"])
	assert.Equal(t, "USDC", outputMap["symbol"])
}

func TestExtractStepOutput_ContractWrite(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"tx_hash": "0x123abc",
		"status":  "success",
	})

	step := &avsproto.Execution_Step{
		Id:   "contract-write-node",
		Type: "NODE_TYPE_CONTRACT_WRITE",
		OutputData: &avsproto.Execution_Step_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for contract write")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "0x123abc", outputMap["tx_hash"])
}

func TestExtractStepOutput_RestAPI(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"status_code": float64(200),
		"body": map[string]interface{}{
			"result": "success",
		},
	})

	step := &avsproto.Execution_Step{
		Id:   "rest-api-node",
		Type: "NODE_TYPE_REST_API",
		OutputData: &avsproto.Execution_Step_RestApi{
			RestApi: &avsproto.RestAPINode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for REST API")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(200), outputMap["status_code"])
}

func TestExtractStepOutput_GraphQL(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"data": map[string]interface{}{
			"user": map[string]interface{}{
				"name": "Alice",
			},
		},
	})

	step := &avsproto.Execution_Step{
		Id:   "graphql-node",
		Type: "NODE_TYPE_GRAPHQL_QUERY",
		OutputData: &avsproto.Execution_Step_Graphql{
			Graphql: &avsproto.GraphQLQueryNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for GraphQL")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Contains(t, outputMap, "data")
}

func TestExtractStepOutput_CustomCode(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"result":    float64(100),
		"processed": true,
	})

	step := &avsproto.Execution_Step{
		Id:   "custom-code-node",
		Type: "NODE_TYPE_CUSTOM_CODE",
		OutputData: &avsproto.Execution_Step_CustomCode{
			CustomCode: &avsproto.CustomCodeNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for custom code")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(100), outputMap["result"])
	assert.Equal(t, true, outputMap["processed"])
}

func TestExtractStepOutput_Branch(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"condition_id": "condition-1",
		"matched":      true,
	})

	step := &avsproto.Execution_Step{
		Id:   "branch-node",
		Type: "NODE_TYPE_BRANCH",
		OutputData: &avsproto.Execution_Step_Branch{
			Branch: &avsproto.BranchNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for branch")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "condition-1", outputMap["condition_id"])
}

func TestExtractStepOutput_Filter(t *testing.T) {
	dataValue, _ := structpb.NewValue([]interface{}{
		map[string]interface{}{"id": float64(1), "name": "Alice"},
		map[string]interface{}{"id": float64(3), "name": "Charlie"},
	})

	step := &avsproto.Execution_Step{
		Id:   "filter-node",
		Type: "NODE_TYPE_FILTER",
		OutputData: &avsproto.Execution_Step_Filter{
			Filter: &avsproto.FilterNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for filter")

	outputArray, ok := result.([]interface{})
	require.True(t, ok, "Result should be an array")
	assert.Len(t, outputArray, 2)
}

func TestExtractStepOutput_Loop(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"iterations": float64(5),
		"results": []interface{}{
			float64(1), float64(2), float64(3), float64(4), float64(5),
		},
	})

	step := &avsproto.Execution_Step{
		Id:   "loop-node",
		Type: "NODE_TYPE_LOOP",
		OutputData: &avsproto.Execution_Step_Loop{
			Loop: &avsproto.LoopNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for loop")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, float64(5), outputMap["iterations"])
}

func TestExtractStepOutput_Balance(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"native_balance": "1000000000000000000",
		"tokens": []interface{}{
			map[string]interface{}{
				"address": "0x1234",
				"balance": "5000000",
				"symbol":  "USDC",
			},
		},
	})

	step := &avsproto.Execution_Step{
		Id:   "balance-node",
		Type: "NODE_TYPE_BALANCE",
		OutputData: &avsproto.Execution_Step_Balance{
			Balance: &avsproto.BalanceNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for balance")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "1000000000000000000", outputMap["native_balance"])
}

func TestExtractStepOutput_ETHTransfer(t *testing.T) {
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"tx_hash":     "0x789def",
		"amount":      "1000000000000000000",
		"destination": "0x1234567890123456789012345678901234567890",
	})

	step := &avsproto.Execution_Step{
		Id:   "eth-transfer-node",
		Type: "NODE_TYPE_ETH_TRANSFER",
		OutputData: &avsproto.Execution_Step_EthTransfer{
			EthTransfer: &avsproto.ETHTransferNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output for ETH transfer")

	outputMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "0x789def", outputMap["tx_hash"])
}

func TestExtractStepOutput_NoOutputData(t *testing.T) {
	// Step with no output data set
	step := &avsproto.Execution_Step{
		Id:   "empty-node",
		Type: "NODE_TYPE_CONTRACT_READ",
	}

	result := ExtractStepOutput(step)
	assert.Nil(t, result, "ExtractStepOutput should return nil when no output data is set")
}

func TestExtractStepOutput_ProtobufCompatibility(t *testing.T) {
	// Test that extracted output can be converted back to protobuf
	dataValue, _ := structpb.NewValue(map[string]interface{}{
		"nested": map[string]interface{}{
			"array": []interface{}{float64(1), float64(2), float64(3)},
			"value": "test",
		},
	})

	step := &avsproto.Execution_Step{
		Id:   "test-node",
		Type: "NODE_TYPE_CUSTOM_CODE",
		OutputData: &avsproto.Execution_Step_CustomCode{
			CustomCode: &avsproto.CustomCodeNode_Output{
				Data: dataValue,
			},
		},
	}

	result := ExtractStepOutput(step)
	require.NotNil(t, result, "ExtractStepOutput should return output")

	// Should be convertible to protobuf without error
	_, err := structpb.NewValue(result)
	assert.NoError(t, err, "Extracted output should be protobuf-compatible")
}

// ========== isTriggerStep Tests ==========

func TestIsTriggerStep(t *testing.T) {
	tests := []struct {
		stepType string
		expected bool
	}{
		{"TRIGGER_TYPE_BLOCK", true},
		{"TRIGGER_TYPE_EVENT", true},
		{"TRIGGER_TYPE_CRON", true},
		{"TRIGGER_TYPE_FIXED_TIME", true},
		{"TRIGGER_TYPE_MANUAL", true},
		{"trigger_type_block", true}, // lowercase should also work
		{"NODE_TYPE_CONTRACT_READ", false},
		{"NODE_TYPE_REST_API", false},
		{"NODE_TYPE_LOOP", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.stepType, func(t *testing.T) {
			result := isTriggerStep(tt.stepType)
			assert.Equal(t, tt.expected, result, "isTriggerStep(%q) should return %v", tt.stepType, tt.expected)
		})
	}
}
