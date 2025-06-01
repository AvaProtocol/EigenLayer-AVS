package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestSimulateTask_ManualTriggerWithCustomCode(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	user := testutil.TestUser1()

	// Define task components for simulation (no need to save to storage)
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger_1",
		Name: "manual",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: "custom_code",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "Hello from trigger: " + trigger.data.triggered })`,
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "trigger_1",
			Target: "step_1",
		},
	}

	// Simulate the task with provided definition
	triggerConfig := map[string]interface{}{
		"manual": true,
	}
	inputVariables := map[string]interface{}{
		"testInput": map[string]interface{}{
			"data": "test value",
		},
	}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, NodeTypeManualTrigger, triggerConfig, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)
	assert.Equal(t, avsproto.TriggerType_TRIGGER_TYPE_MANUAL, execution.TriggerType)
	assert.Equal(t, "manual", execution.TriggerName)

	// Verify execution steps
	assert.Len(t, execution.Steps, 2) // Trigger + Custom Code node

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.NodeId)
	assert.True(t, triggerStep.Success)
	assert.Empty(t, triggerStep.Error)
	assert.Contains(t, triggerStep.Log, "Simulated trigger")

	// Verify custom code step
	codeStep := execution.Steps[1]
	assert.Equal(t, "step_1", codeStep.NodeId)
	assert.True(t, codeStep.Success)
	assert.Empty(t, codeStep.Error)

	// Verify trigger data was available at the execution level
	assert.NotNil(t, execution.OutputData)
}

func TestSimulateTask_FixedTimeTriggerWithRestAPI(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	user := testutil.TestUser1()

	// Define task components for simulation (no need to save to storage)
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger_1",
		Name: "time_trigger",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: "call_api",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://jsonplaceholder.typicode.com/posts/1",
						Method: "GET",
						Headers: map[string]string{
							"Content-Type": "application/json",
						},
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "trigger_1",
			Target: "step_1",
		},
	}

	// Simulate the task with provided definition
	triggerConfig := map[string]interface{}{} // Fixed time trigger doesn't need config
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, NodeTypeFixedTimeTrigger, triggerConfig, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)
	assert.Equal(t, avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME, execution.TriggerType)
	assert.Equal(t, "time_trigger", execution.TriggerName)

	// Verify execution steps
	assert.Len(t, execution.Steps, 2) // Trigger + REST API node

	// Verify trigger step provided timestamp data
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.NodeId)
	assert.True(t, triggerStep.Success)
	assert.Empty(t, triggerStep.Error)
	assert.Contains(t, triggerStep.Log, "Simulated trigger")

	// Verify REST API step
	apiStep := execution.Steps[1]
	assert.Equal(t, "step_1", apiStep.NodeId)
	assert.True(t, apiStep.Success)
}

func TestSimulateTask_WithBranchNode(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	user := testutil.TestUser1()

	// Define task components for simulation (no need to save to storage)
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger_1",
		Name: "manual",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch_1",
			Name: "condition_check",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition_1",
								Type:       "if",
								Expression: "trigger.data.triggered === true",
							},
							{
								Id:         "else",
								Type:       "else",
								Expression: "",
							},
						},
					},
				},
			},
		},
		{
			Id:   "step_1",
			Name: "success_action",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "Branch condition met!" })`,
					},
				},
			},
		},
		{
			Id:   "step_2",
			Name: "else_action",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "Branch condition not met!" })`,
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "trigger_1",
			Target: "branch_1",
		},
		{
			Id:     "edge_2",
			Source: "branch_1.condition_1",
			Target: "step_1",
		},
		{
			Id:     "edge_3",
			Source: "branch_1.else",
			Target: "step_2",
		},
	}

	// Simulate the task with provided definition
	triggerConfig := map[string]interface{}{
		"manual": true,
	}
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, NodeTypeManualTrigger, triggerConfig, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)
	assert.Equal(t, avsproto.TriggerType_TRIGGER_TYPE_MANUAL, execution.TriggerType)
	assert.Equal(t, "manual", execution.TriggerName)

	// Verify execution steps (trigger + branch + success action)
	assert.Len(t, execution.Steps, 3)

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.NodeId)
	assert.True(t, triggerStep.Success)

	// Verify branch step
	branchStep := execution.Steps[1]
	assert.Equal(t, "branch_1", branchStep.NodeId)
	assert.True(t, branchStep.Success)

	// Verify action step (should be step_1 since condition met)
	actionStep := execution.Steps[2]
	assert.Equal(t, "step_1", actionStep.NodeId)
	assert.True(t, actionStep.Success)
}

func TestSimulateTask_TaskNotFound(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	user := testutil.TestUser1()

	// Test with invalid trigger (nil trigger should cause validation error)
	triggerConfig := map[string]interface{}{}
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, nil, nil, nil, NodeTypeManualTrigger, triggerConfig, inputVariables)

	// Verify the simulation failed with appropriate error
	assert.Error(t, err)
	assert.Nil(t, execution)
	assert.Contains(t, err.Error(), "task trigger is required for simulation")
}

func TestSimulateTask_InvalidTriggerType(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	user := testutil.TestUser1()

	// Define valid task components
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger_1",
		Name: "manual",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: "custom_code",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "test" })`,
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "trigger_1",
			Target: "step_1",
		},
	}

	// Try to simulate with invalid trigger type
	triggerConfig := map[string]interface{}{}
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, "invalid_trigger_type", triggerConfig, inputVariables)

	// Verify the simulation failed with appropriate error
	assert.Error(t, err)
	assert.Nil(t, execution)
	assert.Contains(t, err.Error(), "failed to simulate trigger")
}
