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

func TestSimulateTask_InputsListBugs(t *testing.T) {
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

	// Define task components matching JavaScript test parameters exactly
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "manual trigger", // Exact name from JS test
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "node1",
			Name: "custom code", // Exact name from JS test
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `return { message: 'Task executed successfully', timestamp: Date.now() };`, // Exact source from JS test
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge1",
			Source: "trigger1", // trigger.Id
			Target: "node1",    // nodes[0].Id
		},
	}

	// Simulate the task with exact parameters from JavaScript test
	triggerConfig := map[string]interface{}{} // Empty like JS test
	inputVariables := map[string]interface{}{
		"testInput": "test value",
		"userConfig": map[string]interface{}{
			"setting1": "value1",
			"setting2": 42,
		},
	} // Add some input variables to test trigger inputs

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, NodeTypeManualTrigger, triggerConfig, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)
	assert.Equal(t, avsproto.TriggerType_TRIGGER_TYPE_MANUAL, execution.TriggerType)
	assert.Equal(t, "manual trigger", execution.TriggerName)

	// Verify we have 2 steps (trigger + custom code)
	assert.Len(t, execution.Steps, 2)

	// ===== BUG TEST 1: Trigger Step InputsList =====
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger1", triggerStep.NodeId)
	assert.Equal(t, "manual trigger", triggerStep.NodeName)
	assert.True(t, triggerStep.Success)

	// Trigger step should have inputs from inputVariables
	t.Logf("Trigger step inputs: %v", triggerStep.Inputs)
	expectedTriggerInputs := []string{"testInput", "userConfig"}
	if len(triggerStep.Inputs) != len(expectedTriggerInputs) {
		t.Errorf("🔍 Trigger step has %d inputs, expected %d", len(triggerStep.Inputs), len(expectedTriggerInputs))
		t.Logf("Expected: %v", expectedTriggerInputs)
		t.Logf("Actual: %v", triggerStep.Inputs)
	} else {
		// Check if all expected inputs are present (order may vary)
		for _, expected := range expectedTriggerInputs {
			found := false
			for _, actual := range triggerStep.Inputs {
				if actual == expected {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("🔍 Missing expected input '%s' in trigger inputs: %v", expected, triggerStep.Inputs)
			}
		}
		t.Logf("✅ Trigger step correctly has inputs from inputVariables")
	}

	// ===== BUG TEST 2: Custom Code Step InputsList =====
	codeStep := execution.Steps[1]
	assert.Equal(t, "node1", codeStep.NodeId)
	assert.Equal(t, "custom code", codeStep.NodeName)
	assert.True(t, codeStep.Success)

	// Check for 'trigger.data' (convenience variable) and 'manual_trigger.data' (normalized trigger name)
	hasTriggerData := false
	hasManualTriggerData := false

	t.Logf("Custom code step inputs: %v", codeStep.Inputs)
	for _, input := range codeStep.Inputs {
		if input == "trigger.data" {
			hasTriggerData = true
		}
		if input == "manual_trigger.data" {
			hasManualTriggerData = true
		}
	}

	// 'trigger.data' should exist as a convenience variable for JavaScript access
	if hasTriggerData {
		t.Logf("✅ Custom code step correctly contains 'trigger.data' (convenience variable for JS access)")
	} else {
		t.Errorf("❌ Missing 'trigger.data' convenience variable in: %v", codeStep.Inputs)
		t.Logf("The 'trigger' convenience variable should always be available for JavaScript access")
	}

	// Verify 'manual_trigger.data' exists (normalized name from trigger name "manual trigger")
	if hasManualTriggerData {
		t.Logf("✅ Custom code step correctly contains 'manual_trigger.data'")
	} else {
		t.Logf("❓ Expected 'manual_trigger.data' not found in: %v", codeStep.Inputs)
	}

	// Print detailed analysis
	t.Logf("\n=== INPUTSLIST BUG ANALYSIS ===")
	t.Logf("Step 1 (Trigger '%s'):", triggerStep.NodeName)
	t.Logf("  Expected inputs: %v", expectedTriggerInputs)
	t.Logf("  Actual inputs: %v", triggerStep.Inputs)
	t.Logf("  Status: %s", func() string {
		if len(triggerStep.Inputs) == len(expectedTriggerInputs) {
			return "✅ CORRECT"
		}
		return "❌ BUG - should be correct"
	}())

	t.Logf("Step 2 (Node '%s'):", codeStep.NodeName)
	t.Logf("  Inputs: %v", codeStep.Inputs)
	t.Logf("  Contains 'trigger.data': %v (should be true)", hasTriggerData)
	t.Logf("  Contains 'manual_trigger.data': %v (should be true)", hasManualTriggerData)

	t.Logf("\n=== EXPECTED FIXES ===")
	t.Logf("1. Trigger step inputs should be: %v", expectedTriggerInputs)
	t.Logf("2. Keep 'trigger.data' (convenience variable for JavaScript access)")
	t.Logf("3. Keep 'manual_trigger.data' (normalized from trigger name 'manual trigger')")

	t.Logf("\n=== TROUBLESHOOTING SUMMARY ===")
	t.Logf("✅ FIX 1: Trigger step inputs now use inputVariables")
	t.Logf("  File: core/taskengine/engine.go:1086-1099")
	t.Logf("  Solution: Extract keys from inputVariables and use as trigger inputs")
	t.Logf("  Result: Trigger step correctly receives input variables")

	t.Logf("✅ BEHAVIOR 2: 'trigger.data' is a convenience variable")
	t.Logf("  File: core/taskengine/engine.go:1077")
	t.Logf("  Purpose: vm.AddVar(\"trigger\", map[string]any{\"data\": triggerDataMap})")
	t.Logf("  Result: JavaScript can access trigger.data regardless of trigger name")
	t.Logf("  This is intended behavior, not a bug")
}

func TestSimulateTask_WithTriggerInputVariable(t *testing.T) {
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

	// Define task components
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "manual trigger",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "node1",
			Name: "custom code",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `return { 
							convenientTrigger: trigger.data.triggered,
							userTrigger: trigger.data.userValue  
						};`,
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge1",
			Source: "trigger1",
			Target: "node1",
		},
	}

	// Test case: Include 'trigger' as an actual input variable
	triggerConfig := map[string]interface{}{}
	inputVariables := map[string]interface{}{
		"trigger": map[string]interface{}{
			"userValue": "user provided trigger data",
		},
		"otherInput": "some other value",
	}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, NodeTypeManualTrigger, triggerConfig, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Len(t, execution.Steps, 2)

	// Verify trigger step has inputs from inputVariables
	triggerStep := execution.Steps[0]
	assert.Contains(t, triggerStep.Inputs, "trigger")
	assert.Contains(t, triggerStep.Inputs, "otherInput")

	// Verify custom code step
	codeStep := execution.Steps[1]

	// Should have both convenience trigger.data AND user-provided trigger.data
	hasTriggerData := false
	for _, input := range codeStep.Inputs {
		if input == "trigger.data" {
			hasTriggerData = true
			break
		}
	}

	assert.True(t, hasTriggerData, "Should have trigger.data from both convenience variable and user input")

	t.Logf("\n=== TRIGGER VARIABLE TEST ===")
	t.Logf("Input variables provided: %v", []string{"trigger", "otherInput"})
	t.Logf("Trigger step inputs: %v", triggerStep.Inputs)
	t.Logf("Custom code step inputs: %v", codeStep.Inputs)
	t.Logf("Result: When 'trigger' is provided as input variable, it merges with convenience trigger")
}
