package taskengine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestTriggerOutputDataSerializationFix verifies that all trigger types properly handle
// both protobuf objects and JSON-serialized maps in execution step output data.
// This test addresses the issue where trigger output data was empty in execution steps
// when tasks went through the queue system (JSON serialization/deserialization).
func TestTriggerOutputDataSerializationFix(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	tests := []struct {
		name        string
		triggerType avsproto.TriggerType
		// Direct protobuf output (as used in TriggerTask RPC)
		directOutput interface{}
		// JSON-serialized output (as stored in queue after serialization/deserialization)
		mapOutput map[string]interface{}
	}{
		{
			name:        "ManualTrigger",
			triggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
			directOutput: &avsproto.ManualTrigger_Output{
				Data: createTestStructValue(map[string]interface{}{
					"userInput": "test-value",
					"timestamp": time.Now().Unix(),
				}),
			},
			mapOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"userInput": "test-value",
					"timestamp": time.Now().Unix(),
				},
			},
		},
		{
			name:        "FixedTimeTrigger",
			triggerType: avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
			directOutput: &avsproto.FixedTimeTrigger_Output{
				Data: createTestStructValue(map[string]interface{}{
					"scheduledTime": time.Now().Unix(),
					"epochIndex":    int64(5),
				}),
			},
			mapOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"scheduledTime": time.Now().Unix(),
					"epochIndex":    int64(5),
				},
			},
		},
		{
			name:        "CronTrigger",
			triggerType: avsproto.TriggerType_TRIGGER_TYPE_CRON,
			directOutput: &avsproto.CronTrigger_Output{
				Data: createTestStructValue(map[string]interface{}{
					"cronExpression": "0 */5 * * * *",
					"nextRun":        time.Now().Add(5 * time.Minute).Unix(),
				}),
			},
			mapOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"cronExpression": "0 */5 * * * *",
					"nextRun":        time.Now().Add(5 * time.Minute).Unix(),
				},
			},
		},
		{
			name:        "BlockTrigger",
			triggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
			directOutput: &avsproto.BlockTrigger_Output{
				Data: createTestStructValue(map[string]interface{}{
					"blockNumber":   int64(18500000),
					"blockHash":     "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					"timestamp":     time.Now().Unix(),
					"gasLimit":      int64(30000000),
					"gasUsed":       int64(25000000),
					"baseFeePerGas": int64(20000000000),
				}),
			},
			mapOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber":   int64(18500000),
					"blockHash":     "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					"timestamp":     time.Now().Unix(),
					"gasLimit":      int64(30000000),
					"gasUsed":       int64(25000000),
					"baseFeePerGas": int64(20000000000),
				},
			},
		},
		{
			name:        "EventTrigger",
			triggerType: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
			directOutput: &avsproto.EventTrigger_Output{
				Data: createTestStructValue(map[string]interface{}{
					"tokenName":      "USDC",
					"tokenSymbol":    "USDC",
					"tokenDecimals":  6,
					"from":           "0x2A6CEbeDF9e737A9C6188c62A68655919c7314DB",
					"to":             "0xC114FB059434563DC65AC8D57e7976e3eaC534F4",
					"value":          "3453120",
					"valueFormatted": "3.45312",
					"blockNumber":    int64(7212417),
				}),
			},
			mapOutput: map[string]interface{}{
				"data": map[string]interface{}{
					"tokenName":      "USDC",
					"tokenSymbol":    "USDC",
					"tokenDecimals":  6,
					"from":           "0x2A6CEbeDF9e737A9C6188c62A68655919c7314DB",
					"to":             "0xC114FB059434563DC65AC8D57e7976e3eaC534F4",
					"value":          "3453120",
					"valueFormatted": "3.45312",
					"blockNumber":    int64(7212417),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test both direct protobuf and JSON-serialized map cases
			testCases := []struct {
				name          string
				triggerOutput interface{}
			}{
				{"Direct Protobuf", tt.directOutput},
				{"JSON Serialized Map", tt.mapOutput},
			}

			for _, tc := range testCases {
				t.Run(tc.name, func(t *testing.T) {
					// Create a simple task with a custom code node that logs the trigger data
					nodes := []*avsproto.TaskNode{
						{
							Id:   "testNode",
							Name: "testNode",
							TaskType: &avsproto.TaskNode_CustomCode{
								CustomCode: &avsproto.CustomCodeNode{
									Config: &avsproto.CustomCodeNode_Config{
										Lang:   avsproto.Lang_JavaScript,
										Source: "({ message: 'trigger data test' })",
									},
								},
							},
						},
					}

					trigger := &avsproto.TaskTrigger{
						Id:   "testTrigger",
						Name: "testTrigger",
						Type: tt.triggerType,
					}

					edges := []*avsproto.TaskEdge{
						{
							Id:     "e1",
							Source: trigger.Id,
							Target: "testNode",
						},
					}

					task := &model.Task{
						Task: &avsproto.Task{
							Id:      "test-task-" + tt.name + "-" + tc.name,
							Nodes:   nodes,
							Edges:   edges,
							Trigger: trigger,
						},
					}

					// Create executor
					executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())

					// Create queue execution data with the test trigger output
					queueData := &QueueExecutionData{
						TriggerType:   tt.triggerType,
						TriggerOutput: tc.triggerOutput,
						ExecutionID:   "test-exec-" + tt.name + "-" + tc.name,
					}

					// Run the task
					execution, err := executor.RunTask(task, queueData)
					if err != nil {
						t.Fatalf("RunTask failed: %v", err)
					}

					if execution == nil {
						t.Fatal("Execution is nil")
					}

					if len(execution.Steps) == 0 {
						t.Fatal("No execution steps found")
					}

					// Find the trigger step (should be the first step)
					var triggerStep *avsproto.Execution_Step
					for _, step := range execution.Steps {
						if step.Id == trigger.Id {
							triggerStep = step
							break
						}
					}

					if triggerStep == nil {
						t.Fatalf("Trigger step not found. Available steps: %v", getStepIds(execution.Steps))
					}

					// Verify that the trigger step has output data
					if triggerStep.OutputData == nil {
						t.Fatal("Trigger step OutputData is nil - the fix is not working")
					}

					// Verify the output data matches the trigger type and contains data
					switch tt.triggerType {
					case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
						manualOutput := triggerStep.GetManualTrigger()
						if manualOutput == nil {
							t.Fatal("ManualTrigger output is nil")
						}
						if manualOutput.Data == nil {
							t.Fatal("ManualTrigger data is nil")
						}
						verifyTriggerData(t, manualOutput.Data, "ManualTrigger")

					case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
						fixedTimeOutput := triggerStep.GetFixedTimeTrigger()
						if fixedTimeOutput == nil {
							t.Fatal("FixedTimeTrigger output is nil")
						}
						if fixedTimeOutput.Data == nil {
							t.Fatal("FixedTimeTrigger data is nil")
						}
						verifyTriggerData(t, fixedTimeOutput.Data, "FixedTimeTrigger")

					case avsproto.TriggerType_TRIGGER_TYPE_CRON:
						cronOutput := triggerStep.GetCronTrigger()
						if cronOutput == nil {
							t.Fatal("CronTrigger output is nil")
						}
						if cronOutput.Data == nil {
							t.Fatal("CronTrigger data is nil")
						}
						verifyTriggerData(t, cronOutput.Data, "CronTrigger")

					case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
						blockOutput := triggerStep.GetBlockTrigger()
						if blockOutput == nil {
							t.Fatal("BlockTrigger output is nil")
						}
						if blockOutput.Data == nil {
							t.Fatal("BlockTrigger data is nil")
						}
						verifyTriggerData(t, blockOutput.Data, "BlockTrigger")

						// Special verification for BlockTrigger - check for block-specific fields
						dataMap := convertStructValueToMap(blockOutput.Data)
						if dataMap["blockNumber"] == nil {
							t.Error("BlockTrigger data missing blockNumber field")
						}
						if dataMap["blockHash"] == nil {
							t.Error("BlockTrigger data missing blockHash field")
						}

					case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
						eventOutput := triggerStep.GetEventTrigger()
						if eventOutput == nil {
							t.Fatal("EventTrigger output is nil")
						}
						if eventOutput.Data == nil {
							t.Fatal("EventTrigger data is nil")
						}
						verifyTriggerData(t, eventOutput.Data, "EventTrigger")

						// Special verification for EventTrigger - check for event-specific fields
						dataMap := convertStructValueToMap(eventOutput.Data)
						if dataMap["tokenSymbol"] == nil {
							t.Error("EventTrigger data missing tokenSymbol field")
						}
						if dataMap["valueFormatted"] == nil {
							t.Error("EventTrigger data missing valueFormatted field")
						}

					default:
						t.Fatalf("Unknown trigger type: %v", tt.triggerType)
					}

					t.Logf("✅ %s %s: Trigger output data properly preserved", tt.name, tc.name)
				})
			}
		})
	}
}

// TestTriggerOutputDataJSONSerialization tests that trigger outputs survive JSON serialization
// This simulates what happens when tasks go through the queue system
func TestTriggerOutputDataJSONSerialization(t *testing.T) {
	// Create sample BlockTrigger output
	originalOutput := &avsproto.BlockTrigger_Output{
		Data: createTestStructValue(map[string]interface{}{
			"blockNumber": int64(18500000),
			"timestamp":   time.Now().Unix(),
		}),
	}

	queueData := &QueueExecutionData{
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: originalOutput,
		ExecutionID:   "test-serialization",
	}

	// Serialize to JSON (simulates queue storage)
	jsonData, err := json.Marshal(queueData)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	// Deserialize from JSON (simulates queue retrieval)
	var deserializedData QueueExecutionData
	err = json.Unmarshal(jsonData, &deserializedData)
	if err != nil {
		t.Fatalf("JSON unmarshaling failed: %v", err)
	}

	// Verify that the deserialized output is now a map[string]interface{}
	if mapOutput, ok := deserializedData.TriggerOutput.(map[string]interface{}); ok {
		if mapOutput["data"] == nil {
			t.Error("Deserialized trigger output missing data field")
		}
		t.Log("✅ JSON serialization converts protobuf to map as expected")
	} else {
		t.Errorf("Expected deserialized trigger output to be map[string]interface{}, got %T", deserializedData.TriggerOutput)
	}
}

// Helper functions

func createTestStructValue(data map[string]interface{}) *structpb.Value {
	value, err := structpb.NewValue(data)
	if err != nil {
		panic(err)
	}
	return value
}

func verifyTriggerData(t *testing.T, data *structpb.Value, triggerType string) {
	if data == nil {
		t.Fatalf("%s data is nil", triggerType)
	}

	// Convert to map for easy verification
	dataMap := convertStructValueToMap(data)
	if len(dataMap) == 0 {
		t.Errorf("%s data map is empty", triggerType)
	}
}

func convertStructValueToMap(value *structpb.Value) map[string]interface{} {
	if value == nil {
		return nil
	}

	switch v := value.Kind.(type) {
	case *structpb.Value_StructValue:
		result := make(map[string]interface{})
		for k, val := range v.StructValue.Fields {
			result[k] = convertStructValueToInterface(val)
		}
		return result
	default:
		return nil
	}
}

func convertStructValueToInterface(value *structpb.Value) interface{} {
	if value == nil {
		return nil
	}

	switch v := value.Kind.(type) {
	case *structpb.Value_StringValue:
		return v.StringValue
	case *structpb.Value_NumberValue:
		return v.NumberValue
	case *structpb.Value_BoolValue:
		return v.BoolValue
	case *structpb.Value_StructValue:
		result := make(map[string]interface{})
		for k, val := range v.StructValue.Fields {
			result[k] = convertStructValueToInterface(val)
		}
		return result
	case *structpb.Value_ListValue:
		result := make([]interface{}, len(v.ListValue.Values))
		for i, val := range v.ListValue.Values {
			result[i] = convertStructValueToInterface(val)
		}
		return result
	default:
		return nil
	}
}

func getStepIds(steps []*avsproto.Execution_Step) []string {
	var ids []string
	for _, step := range steps {
		ids = append(ids, step.Id)
	}
	return ids
}
