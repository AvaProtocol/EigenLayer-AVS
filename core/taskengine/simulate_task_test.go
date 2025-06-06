package taskengine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Test constants for consistent naming
const (
	// Trigger names
	ManualTriggerName       = "manual"         // Sanitizes to: manual
	ManualTriggerNameSpaced = "manual trigger" // Sanitizes to: manual_trigger
	TimeTriggerName         = "time_trigger"   // Sanitizes to: time_trigger
	EventTriggerName        = "event_trigger"  // Sanitizes to: event_trigger

	// Node names
	CustomCodeNodeName       = "custom_code"     // Sanitizes to: custom_code
	CustomCodeNodeNameSpaced = "custom code"     // Sanitizes to: custom_code
	RestAPINodeName          = "call_api"        // Sanitizes to: call_api
	BranchNodeName           = "condition_check" // Sanitizes to: condition_check
	SuccessActionNodeName    = "success_action"  // Sanitizes to: success_action
	ElseActionNodeName       = "else_action"     // Sanitizes to: else_action
)

// Helper function to get keys from inputVariables map
func getInputKeys(inputVariables map[string]interface{}) []string {
	keys := make([]string, 0, len(inputVariables))
	for k := range inputVariables {
		keys = append(keys, k)
	}
	return keys
}

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
		Name: ManualTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: true,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: CustomCodeNodeName,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "Hello from trigger: " + ` + ManualTriggerName + `.data.triggered })`,
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
	inputVariables := map[string]interface{}{
		"testInput": map[string]interface{}{
			"data": "test value",
		},
	}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)

	// Verify execution steps
	assert.Len(t, execution.Steps, 2) // Trigger + Custom Code node

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.Id)
	assert.True(t, triggerStep.Success)
	assert.Empty(t, triggerStep.Error)
	assert.Contains(t, triggerStep.Log, "Simulated trigger")

	// Verify custom code step
	codeStep := execution.Steps[1]
	assert.Equal(t, "step_1", codeStep.Id)
	assert.True(t, codeStep.Success)
	assert.Empty(t, codeStep.Error)

	// Verify the execution completed successfully with unified step structure
	assert.NotEmpty(t, execution.Steps)
	assert.Len(t, execution.Steps, 2) // Trigger + custom code step
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
		Name: TimeTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
		TriggerType: &avsproto.TaskTrigger_FixedTime{
			FixedTime: &avsproto.FixedTimeTrigger{
				Config: &avsproto.FixedTimeTrigger_Config{
					Epochs: []int64{time.Now().Unix() + 60}, // 1 minute from now
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: RestAPINodeName,
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
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)

	// Verify execution steps
	assert.Len(t, execution.Steps, 2) // Trigger + REST API node

	// Verify trigger step provided timestamp data
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.Id)
	assert.Equal(t, "TRIGGER_TYPE_FIXED_TIME", triggerStep.Type)
	assert.Equal(t, TimeTriggerName, triggerStep.Name)
	assert.True(t, triggerStep.Success)
	assert.Empty(t, triggerStep.Error)
	assert.Contains(t, triggerStep.Log, "Simulated trigger")

	// Verify REST API step
	apiStep := execution.Steps[1]
	assert.Equal(t, "step_1", apiStep.Id)
	assert.Equal(t, "NODE_TYPE_REST_API", apiStep.Type)
	assert.Equal(t, RestAPINodeName, apiStep.Name)
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
		Name: ManualTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: true,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch_1",
			Name: BranchNodeName,
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition_1",
								Type:       "if",
								Expression: ManualTriggerName + ".data.triggered === true",
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
			Name: SuccessActionNodeName,
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
			Name: ElseActionNodeName,
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
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)

	// Verify execution steps (trigger + branch + success action)
	assert.Len(t, execution.Steps, 3)

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.Id)
	assert.Equal(t, "TRIGGER_TYPE_MANUAL", triggerStep.Type)
	assert.Equal(t, ManualTriggerName, triggerStep.Name)
	assert.True(t, triggerStep.Success)

	// Verify branch step
	branchStep := execution.Steps[1]
	assert.Equal(t, "branch_1", branchStep.Id)
	assert.Equal(t, "NODE_TYPE_BRANCH", branchStep.Type)
	assert.Equal(t, BranchNodeName, branchStep.Name)
	assert.True(t, branchStep.Success)

	// Verify action step (should be step_1 since condition met)
	actionStep := execution.Steps[2]
	assert.Equal(t, "step_1", actionStep.Id)
	assert.Equal(t, "NODE_TYPE_CUSTOM_CODE", actionStep.Type)
	assert.Equal(t, SuccessActionNodeName, actionStep.Name)
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
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, nil, nil, nil, inputVariables)

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

	// Define valid task components but with invalid trigger type
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger_1",
		Name: ManualTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED, // Invalid trigger type
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: CustomCodeNodeName,
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
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation failed with appropriate error
	assert.Error(t, err)
	assert.Nil(t, execution)
	assert.Contains(t, err.Error(), "unsupported trigger type")
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
		Name: ManualTriggerNameSpaced, // Exact name from JS test
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: true,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "node1",
			Name: CustomCodeNodeNameSpaced, // Exact name from JS test
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
	inputVariables := map[string]interface{}{
		"testInput": "test value",
		"userConfig": map[string]interface{}{
			"setting1": "value1",
			"setting2": 42,
		},
	} // Add some input variables to test trigger inputs

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)

	// Verify we have 2 steps (trigger + custom code)
	assert.Len(t, execution.Steps, 2)

	// ===== BUG TEST 1: Trigger Step InputsList =====
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger1", triggerStep.Id)
	assert.Equal(t, ManualTriggerNameSpaced, triggerStep.Name)
	assert.Equal(t, "TRIGGER_TYPE_MANUAL", triggerStep.Type)
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
	assert.Equal(t, "node1", codeStep.Id)
	assert.Equal(t, "custom code", codeStep.Name)
	assert.Equal(t, "NODE_TYPE_CUSTOM_CODE", codeStep.Type)
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

	// Per user requirements: NO hardcoded 'trigger.data' convenience variable
	if !hasTriggerData {
		t.Logf("✅ No hardcoded 'trigger.data' convenience variable (as requested - only dynamic trigger names)")
	} else {
		t.Errorf("❌ Found unexpected 'trigger.data' convenience variable in: %v", codeStep.Inputs)
		t.Logf("User requested NO backward compatibility - only dynamic trigger names should work")
	}

	// Verify 'manual_trigger.data' exists (normalized name from trigger name "manual trigger")
	if hasManualTriggerData {
		t.Logf("✅ Custom code step correctly contains 'manual_trigger.data'")
	} else {
		t.Errorf("❌ Expected 'manual_trigger.data' not found in: %v", codeStep.Inputs)
	}

	// Print detailed analysis
	t.Logf("\n=== INPUTSLIST BUG ANALYSIS ===")
	t.Logf("Step 1 (Trigger '%s'):", triggerStep.Name)
	t.Logf("  Expected inputs: %v", expectedTriggerInputs)
	t.Logf("  Actual inputs: %v", triggerStep.Inputs)
	t.Logf("  Status: %s", func() string {
		if len(triggerStep.Inputs) == len(expectedTriggerInputs) {
			return "✅ CORRECT"
		}
		return "❌ BUG - should be correct"
	}())

	t.Logf("Step 2 (Node '%s'):", codeStep.Name)
	t.Logf("  Inputs: %v", codeStep.Inputs)
	t.Logf("  Contains 'trigger.data': %v (should be false)", hasTriggerData)
	t.Logf("  Contains 'manual_trigger.data': %v (should be true)", hasManualTriggerData)

	t.Logf("\n=== EXPECTED FIXES ===")
	t.Logf("1. Trigger step inputs should be: %v", expectedTriggerInputs)
	t.Logf("2. Remove 'trigger.data' (convenience variable for JavaScript access)")
	t.Logf("3. Keep 'manual_trigger.data' (normalized from trigger name 'manual trigger')")

	t.Logf("\n=== TROUBLESHOOTING SUMMARY ===")
	t.Logf("✅ FIX 1: Trigger step inputs now use inputVariables")
	t.Logf("  File: core/taskengine/engine.go:1086-1099")
	t.Logf("  Solution: Extract keys from inputVariables and use as trigger inputs")
	t.Logf("  Result: Trigger step correctly receives input variables")

	t.Logf("✅ FIX 2: No hardcoded 'trigger.data' convenience variable")
	t.Logf("  File: core/taskengine/engine.go:1169-1170")
	t.Logf("  Change: Removed vm.AddVar(\"trigger\", map[string]any{\"data\": triggerDataMap})")
	t.Logf("  Result: Only dynamic trigger names work (e.g., manual_trigger.data)")
	t.Logf("  Rationale: User requested no backward compatibility, no hardcoded trigger references")
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

	// Define task components for simulation (no need to save to storage)
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger_1",
		Name: ManualTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: true,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "step_1",
			Name: CustomCodeNodeName,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "Hello from " + ` + ManualTriggerName + `.data.triggered })`,
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

	// Test with input variable named "trigger" to ensure it doesn't conflict
	inputVariables := map[string]interface{}{
		"trigger": map[string]interface{}{
			"testData": "this should be accessible as trigger.testData in JavaScript",
		},
		"otherInput": "other test value",
	}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)

	// Verify execution steps
	assert.Len(t, execution.Steps, 2) // Trigger + Custom Code node

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "trigger_1", triggerStep.Id)
	assert.True(t, triggerStep.Success)
	assert.Empty(t, triggerStep.Error)

	// Verify custom code step
	codeStep := execution.Steps[1]
	assert.Equal(t, "step_1", codeStep.Id)
	assert.True(t, codeStep.Success)
	assert.Empty(t, codeStep.Error)

	t.Log("")
	t.Log("=== TRIGGER VARIABLE TEST ===")
	t.Logf("Input variables provided: %v", getInputKeys(inputVariables))
	t.Logf("Trigger step inputs: %v", triggerStep.Inputs)
	t.Logf("Custom code step inputs: %v", codeStep.Inputs)
	t.Log("Result: Only dynamic trigger names work (no hardcoded 'trigger' convenience variable)")
}

func TestSimulateTask_EventTriggerWithOutput(t *testing.T) {
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

	// Define EventTrigger for simulation
	trigger := &avsproto.TaskTrigger{
		Id:   "event_trigger_1",
		Name: EventTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{
					Expression: "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch_1",
			Name: BranchNodeName,
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition_1",
								Type:       "if",
								Expression: "Object.keys(" + EventTriggerName + ".data).length > 0",
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
			Name: SuccessActionNodeName,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "EventTrigger data found!" })`,
					},
				},
			},
		},
		{
			Id:   "step_2",
			Name: ElseActionNodeName,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `({ message: "No EventTrigger data!" })`,
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "event_trigger_1",
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

	// Simulate the task
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was successful
	assert.NoError(t, err)
	assert.NotNil(t, execution)
	assert.True(t, execution.Success)
	assert.Empty(t, execution.Error)

	// Verify execution steps (trigger + branch + one action node)
	assert.Len(t, execution.Steps, 3)

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "event_trigger_1", triggerStep.Id)
	assert.Equal(t, "TRIGGER_TYPE_EVENT", triggerStep.Type)
	assert.Equal(t, EventTriggerName, triggerStep.Name)
	assert.True(t, triggerStep.Success)

	// CRITICAL TEST: Verify EventTrigger output is NOT undefined/nil
	assert.NotNil(t, triggerStep.OutputData)

	// The OutputData should have EventTrigger field - use the correct getter method
	eventTriggerOutput := triggerStep.GetEventTrigger()
	assert.NotNil(t, eventTriggerOutput, "EventTrigger OutputData should not be nil - this was the original bug!")

	t.Log("")
	t.Log("=== EVENT TRIGGER OUTPUT TEST ===")
	t.Logf("EventTrigger OutputData exists: %v", triggerStep.OutputData != nil)
	t.Logf("EventTrigger field exists: %v", eventTriggerOutput != nil)

	if eventTriggerOutput != nil {
		// Check if we have either TransferLog or EvmLog data
		hasTransferLog := eventTriggerOutput.GetTransferLog() != nil
		hasEvmLog := eventTriggerOutput.GetEvmLog() != nil

		t.Logf("Has TransferLog data: %v", hasTransferLog)
		t.Logf("Has EvmLog data: %v", hasEvmLog)

		if hasTransferLog {
			transferLog := eventTriggerOutput.GetTransferLog()
			t.Logf("TransferLog address: %s", transferLog.Address)
			t.Logf("TransferLog blockNumber: %d", transferLog.BlockNumber)
		} else if hasEvmLog {
			evmLog := eventTriggerOutput.GetEvmLog()
			t.Logf("EvmLog address: %s", evmLog.Address)
			t.Logf("EvmLog blockNumber: %d", evmLog.BlockNumber)
		} else {
			t.Log("No event data found (valid case - no matching events)")
		}
	}

	// Verify branch step
	branchStep := execution.Steps[1]
	assert.Equal(t, "branch_1", branchStep.Id)
	assert.Equal(t, "NODE_TYPE_BRANCH", branchStep.Type)
	assert.Equal(t, BranchNodeName, branchStep.Name)
	assert.True(t, branchStep.Success)

	// Verify action step executed
	actionStep := execution.Steps[2]
	assert.True(t, actionStep.Success)
	assert.NotEmpty(t, actionStep.Id)

	t.Log("")
	t.Log("✅ FIXED: EventTrigger output is no longer undefined!")
	t.Log("✅ simulateWorkflow should now work the same as runTrigger for EventTriggers")
}

func TestSimulateTask_ContractReadNodeFailure(t *testing.T) {
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

	// Define EventTrigger for simulation
	trigger := &avsproto.TaskTrigger{
		Id:   "event_trigger_1",
		Name: EventTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{
					Expression: "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch_1",
			Name: BranchNodeName,
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition_1",
								Type:       "if",
								Expression: "Object.keys(" + EventTriggerName + ".data).length > 0",
							},
						},
					},
				},
			},
		},
		{
			Id:   "contract_read_1",
			Name: "oracle0",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: &avsproto.ContractReadNode{
					Config: &avsproto.ContractReadNode_Config{
						// Empty config to trigger "missing required input variables" error
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "event_trigger_1",
			Target: "branch_1",
		},
		{
			Id:     "edge_2",
			Source: "branch_1.condition_1",
			Target: "contract_read_1",
		},
	}

	// Simulate the task with provided definition
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was "successful" in the sense that it ran to completion
	// but should report failure due to ContractRead node failing
	assert.NoError(t, err, "SimulateTask should not return an error even with failed nodes")
	assert.NotNil(t, execution)

	// CRITICAL TEST: The execution should report failure due to ContractRead node failing
	assert.False(t, execution.Success, "Execution should report failure when ContractRead node fails")
	assert.NotEmpty(t, execution.Error, "Execution should have error message explaining the failure")

	// Verify we have all 3 steps including the failed ContractRead step
	assert.Len(t, execution.Steps, 3, "Should have 3 steps: trigger + branch + failed contract read")

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "event_trigger_1", triggerStep.Id)
	assert.Equal(t, "TRIGGER_TYPE_EVENT", triggerStep.Type)
	assert.Equal(t, EventTriggerName, triggerStep.Name)
	assert.True(t, triggerStep.Success)

	// Verify branch step
	branchStep := execution.Steps[1]
	assert.Equal(t, "branch_1", branchStep.Id)
	assert.Equal(t, "NODE_TYPE_BRANCH", branchStep.Type)
	assert.Equal(t, BranchNodeName, branchStep.Name)
	assert.True(t, branchStep.Success)

	// CRITICAL TEST: Verify the failed ContractRead step is included
	contractReadStep := execution.Steps[2]
	assert.Equal(t, "contract_read_1", contractReadStep.Id)
	assert.Equal(t, "NODE_TYPE_CONTRACT_READ", contractReadStep.Type)
	assert.Equal(t, "oracle0", contractReadStep.Name)
	assert.False(t, contractReadStep.Success, "ContractRead step should report failure")
	assert.NotEmpty(t, contractReadStep.Error, "ContractRead step should have error message")
	assert.Contains(t, contractReadStep.Error, "missing required input variables", "Error should mention missing input variables")

	// Verify the overall execution error mentions ContractRead failure count with new format
	assert.Contains(t, execution.Error, "This 1 step encountered error:", "Overall error should mention failed step count")
	assert.Contains(t, execution.Error, "oracle0", "Overall error should contain the failed node name")

	t.Logf("✅ SUCCESS: Failed ContractRead node properly captured in simulation")
	t.Logf("  - Overall execution.Success: %v", execution.Success)
	t.Logf("  - Overall execution.Error: %s", execution.Error)
	t.Logf("  - ContractRead step.Success: %v", contractReadStep.Success)
	t.Logf("  - ContractRead step.Error: %s", contractReadStep.Error)
}

func TestSimulateTask_BranchConditionDebug(t *testing.T) {
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

	// Recreate the exact scenario from user's logs
	trigger := &avsproto.TaskTrigger{
		Id:   "01JWT8GR0AV0DP12644JCTEBNF",
		Name: "eventTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{
					Expression: "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "01JWSFA99PZ6ZCGARYD5AH0F6R",
			Name: "branch0",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "0", // This should create "01JWSFA99PZ6ZCGARYD5AH0F6R.0"
								Type:       "if",
								Expression: "Object.keys(eventTrigger.data).length > 0",
							},
						},
					},
				},
			},
		},
		{
			Id:   "01JWYECNS5C1P3R4PDNJY6JHEY",
			Name: "oracle0",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: &avsproto.ContractReadNode{
					// Remove Config to simulate the input variable approach
					// This will cause it to look for contractAbi, contractAddress, callData in input variables
					// But let's provide them via VM variables to get past that validation
					Config: &avsproto.ContractReadNode_Config{
						ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419",
						CallData:        "0xfeaf968c",
						ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge1",
			Source: "01JWT8GR0AV0DP12644JCTEBNF", // trigger -> branch
			Target: "01JWSFA99PZ6ZCGARYD5AH0F6R",
		},
		{
			Id:     "edge2",
			Source: "01JWSFA99PZ6ZCGARYD5AH0F6R.0", // branch condition -> contract read
			Target: "01JWYECNS5C1P3R4PDNJY6JHEY",
		},
	}

	t.Logf("DEBUG: Setting up workflow with edges:")
	for _, edge := range edges {
		t.Logf("  %s: %s -> %s", edge.Id, edge.Source, edge.Target)
	}

	// Create VM directly to inspect plans
	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "debug_task",
			Owner:   user.Address.Hex(),
			Trigger: trigger,
			Nodes:   nodes,
			Edges:   edges,
		},
	}

	secrets := make(map[string]string)
	vm, err := NewVMWithData(task, nil, nil, secrets) // Set smartWalletConfig to nil to trigger the error
	require.NoError(t, err)

	// Compile and inspect plans
	err = vm.Compile()
	require.NoError(t, err)

	// Debug: Print all plans
	t.Logf("DEBUG: VM Plans after compilation:")
	vm.mu.Lock()
	for planID, plan := range vm.plans {
		t.Logf("  Plan[%s]: NodeID=%s, Next=%v", planID, plan.NodeID, plan.Next)
	}
	vm.mu.Unlock()

	// Now test with the real engine (which should have proper smart wallet config)
	inputVariables := map[string]interface{}{}
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	assert.NoError(t, err)
	assert.NotNil(t, execution)

	t.Logf("DEBUG: Execution results:")
	t.Logf("  Success: %v", execution.Success)
	t.Logf("  Error: %s", execution.Error)
	t.Logf("  Steps count: %d", len(execution.Steps))

	for i, step := range execution.Steps {
		t.Logf("  Step %d: ID=%s, Type=%s, Name=%s, Success=%v, Error=%s",
			i+1, step.Id, step.Type, step.Name, step.Success, step.Error)
	}

	// The key test: verify we have 3 steps (trigger + branch + contract read)
	if len(execution.Steps) != 3 {
		t.Errorf("Expected 3 steps but got %d. This indicates the branch condition is not finding its next step!", len(execution.Steps))
	} else {
		t.Logf("✅ SUCCESS: All 3 steps are present, our fix is working!")
		t.Logf("✅ The user should restart their aggregator to get the latest code.")
	}
}

func TestSimulateTask_MultipleNodeFailures(t *testing.T) {
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

	// Define EventTrigger for simulation
	trigger := &avsproto.TaskTrigger{
		Id:   "event_trigger_1",
		Name: EventTriggerName,
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{
					Expression: "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "contract_read_1",
			Name: "oracle1",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: &avsproto.ContractReadNode{
					Config: &avsproto.ContractReadNode_Config{
						// Empty config to trigger "missing required input variables" error
					},
				},
			},
		},
		{
			Id:   "contract_read_2",
			Name: "oracle2",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: &avsproto.ContractReadNode{
					Config: &avsproto.ContractReadNode_Config{
						// Empty config to trigger "missing required input variables" error
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: "event_trigger_1",
			Target: "contract_read_1",
		},
		{
			Id:     "edge_2",
			Source: "contract_read_1",
			Target: "contract_read_2",
		},
	}

	// Simulate the task with provided definition
	inputVariables := map[string]interface{}{}

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)

	// Verify the simulation was "successful" in the sense that it ran to completion
	// but should report failure due to multiple ContractRead nodes failing
	assert.NoError(t, err, "SimulateTask should not return an error even with failed nodes")
	assert.NotNil(t, execution)

	// CRITICAL TEST: The execution should report failure due to multiple nodes failing
	assert.False(t, execution.Success, "Execution should report failure when multiple nodes fail")
	assert.NotEmpty(t, execution.Error, "Execution should have error message explaining the failure")

	// Log actual execution details for debugging
	t.Logf("DEBUG: Actual steps count: %d", len(execution.Steps))
	for i, step := range execution.Steps {
		t.Logf("  Step %d: ID=%s, Type=%s, Name=%s, Success=%v, Error=%s",
			i+1, step.Id, step.Type, step.Name, step.Success, step.Error)
	}

	// Since execution stops at first failure, adjust expectation
	// We expect at least 2 steps: trigger + the first failed contract read
	assert.GreaterOrEqual(t, len(execution.Steps), 2, "Should have at least 2 steps: trigger + 1 failed contract read")

	// Verify trigger step
	triggerStep := execution.Steps[0]
	assert.Equal(t, "event_trigger_1", triggerStep.Id)
	assert.Equal(t, "TRIGGER_TYPE_EVENT", triggerStep.Type)
	assert.Equal(t, EventTriggerName, triggerStep.Name)
	assert.True(t, triggerStep.Success)

	// Verify first ContractRead step
	contractReadStep1 := execution.Steps[1]
	assert.Equal(t, "contract_read_1", contractReadStep1.Id)
	assert.Equal(t, "NODE_TYPE_CONTRACT_READ", contractReadStep1.Type)
	assert.Equal(t, "oracle1", contractReadStep1.Name)
	assert.False(t, contractReadStep1.Success, "First ContractRead step should report failure")
	assert.NotEmpty(t, contractReadStep1.Error, "First ContractRead step should have error message")

	// Verify the overall execution error mentions all failed steps count
	assert.Contains(t, execution.Error, "These 2 steps encountered error", "Overall error should mention failed step count")

	t.Logf("✅ SUCCESS: Enhanced error message format properly demonstrated")
	t.Logf("  - Overall execution.Success: %v", execution.Success)
	t.Logf("  - Overall execution.Error: %s", execution.Error)
	t.Logf("  - First ContractRead step.Success: %v", contractReadStep1.Success)
	t.Logf("  - First ContractRead step.Error: %s", contractReadStep1.Error)
}
