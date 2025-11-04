package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestSchedulerExecutesAllNodesInSequence verifies that the Kahn scheduler
// doesn't prematurely terminate execution when nodes are in a linear chain.
// This is a regression test for the bug where email_report_success was skipped
// because the scheduler closed the ready channel before run_swap's successor was scheduled.
func TestSchedulerExecutesAllNodesInSequence(t *testing.T) {
	// Create a simple linear workflow: node1 -> node2 -> node3
	// This simulates: approve_token1 -> run_swap -> email_report_success

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "testTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: func() *structpb.Value {
						data, _ := structpb.NewValue(map[string]interface{}{"test": "data"})
						return data
					}(),
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "node1",
			Name: "first_node",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { result: 'node1 complete' };",
					},
				},
			},
		},
		{
			Id:   "node2",
			Name: "second_node",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { result: 'node2 complete' };",
					},
				},
			},
		},
		{
			Id:   "node3",
			Name: "third_node",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { result: 'node3 complete' };",
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{Source: "trigger1", Target: "node1"},
		{Source: "node1", Target: "node2"},
		{Source: "node2", Target: "node3"},
	}

	// Setup test infrastructure
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Execute simulation
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, map[string]interface{}{})
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)

	// Verify all three nodes + trigger executed
	assert.Len(t, execution.Steps, 4, "All 4 steps should have executed (trigger + 3 nodes)")

	// Verify execution order (skip trigger step)
	assert.Equal(t, "node1", execution.Steps[1].Id, "First node should be node1")
	assert.Equal(t, "node2", execution.Steps[2].Id, "Second node should be node2")
	assert.Equal(t, "node3", execution.Steps[3].Id, "Third node should be node3")

	// Verify all nodes succeeded
	for i := 1; i < len(execution.Steps); i++ {
		assert.True(t, execution.Steps[i].Success, "Node %d should succeed", i)
	}
}

// TestSchedulerExecutesNodeAfterBranch verifies that nodes after a branch
// are executed correctly, even when the branch path has multiple steps.
func TestSchedulerExecutesNodeAfterBranch(t *testing.T) {
	// Workflow: trigger -> balance -> branch
	//                                   |-> (condition 0 /IF) -> approve -> swap -> email
	//                                   |-> (condition 1/ELSE) -> error_email

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "testTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: func() *structpb.Value {
						data, _ := structpb.NewValue(map[string]interface{}{"test": "data"})
						return data
					}(),
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "balance",
			Name: "check_balance",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { hasBalance: true };",
					},
				},
			},
		},
		{
			Id:   "branch",
			Name: "branch_node",
			Type: avsproto.NodeType_NODE_TYPE_BRANCH,
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{Id: "0", Type: "if", Expression: "{{check_balance.data.hasBalance === true}}"},
							{Id: "1", Type: "else", Expression: ""},
						},
					},
				},
			},
		},
		{
			Id:   "approve",
			Name: "approve_token",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { approved: true };",
					},
				},
			},
		},
		{
			Id:   "swap",
			Name: "run_swap",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { swapped: true };",
					},
				},
			},
		},
		{
			Id:   "email",
			Name: "email_report",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { sent: true };",
					},
				},
			},
		},
		{
			Id:   "error_email",
			Name: "email_error",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { error: true };",
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{Source: "trigger1", Target: "balance"},
		{Source: "balance", Target: "branch"},
		{Source: "branch.0", Target: "approve"}, // IF path
		{Source: "approve", Target: "swap"},
		{Source: "swap", Target: "email"},           // This is the critical edge that was skipped
		{Source: "branch.1", Target: "error_email"}, // ELSE path (different endpoint)
	}

	// Setup test infrastructure
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Execute simulation
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, map[string]interface{}{})
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was partially successful (branch path means not all nodes executed)
	// When a branch workflow executes, not all configured nodes run (only one branch path),
	// which correctly results in PARTIAL_SUCCESS status
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_PARTIAL_SUCCESS, execution.Status,
		"Branch workflows should report PARTIAL_SUCCESS when not all nodes execute")
	assert.Contains(t, execution.Error, "Partial execution", "Should report partial execution due to branch path")
	assert.Contains(t, execution.Error, "6 out of 7 steps executed", "Should show correct step counts")

	// Debug: print what executed
	t.Logf("Executed %d steps:", len(execution.Steps))
	for i, step := range execution.Steps {
		t.Logf("  Step %d: %s (name: %s, success: %v)", i, step.Id, step.Name, step.Success)
	}

	// Verify all nodes in the IF path executed (trigger, balance, branch, approve, swap, email)
	assert.GreaterOrEqual(t, len(execution.Steps), 6, "At least 6 steps should execute (trigger, balance, branch, approve, swap, email)")

	// Find the email node in execution logs
	emailExecuted := false
	swapExecuted := false
	for _, step := range execution.Steps {
		if step.Id == "email" {
			emailExecuted = true
			assert.True(t, step.Success, "Email node should succeed")
		}
		if step.Id == "swap" {
			swapExecuted = true
		}
	}

	assert.True(t, swapExecuted, "Swap node should have executed")
	assert.True(t, emailExecuted, "Email node should have executed after swap")
}

// TestSchedulerNodeWithMixedEdges tests the specific bug scenario where a node
// receives BOTH regular edges AND branch condition edges.
// This is a regression test for: email node had edge from run_swap (regular)
// AND edge from branch.1 (branch condition). The bug was that the node would
// get marked as a branch target and remain gated even though run_swap completed.
func TestSchedulerNodeWithMixedEdges(t *testing.T) {
	// Workflow structure mimicking the real bug scenario:
	//   trigger -> branch
	//              |-> (condition 0) -> node_a -> shared_node
	//              |-> (condition 1) ------------> shared_node
	//
	// shared_node has TWO incoming edges:
	//   1. Regular edge from node_a (when condition 0 is selected)
	//   2. Branch edge from branch.1 (when condition 1 would be selected)
	//
	// When condition 0 is selected, node_a should execute, then shared_node should execute.
	// The bug was that shared_node remained gated by the unselected branch.1 edge.

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "testTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: func() *structpb.Value {
						data, _ := structpb.NewValue(map[string]interface{}{
							"shouldTakePath0": true,
						})
						return data
					}(),
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch",
			Name: "branch_node",
			Type: avsproto.NodeType_NODE_TYPE_BRANCH,
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{Id: "0", Type: "if", Expression: "{{testTrigger.data.shouldTakePath0 === true}}"},
							{Id: "1", Type: "else", Expression: ""},
						},
					},
				},
			},
		},
		{
			Id:   "node_a",
			Name: "intermediate_node",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { result: 'node_a executed' };",
					},
				},
			},
		},
		{
			Id:   "shared_node",
			Name: "convergence_point",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return { result: 'shared_node executed' };",
					},
				},
			},
		},
	}

	// Critical edge structure:
	// - branch.0 -> node_a (branch condition edge)
	// - node_a -> shared_node (regular edge)
	// - branch.1 -> shared_node (branch condition edge) <-- THIS is the problematic edge
	edges := []*avsproto.TaskEdge{
		{Source: "trigger1", Target: "branch"},
		{Source: "branch.0", Target: "node_a"},      // Branch condition 0 leads to node_a
		{Source: "node_a", Target: "shared_node"},   // Regular edge: node_a -> shared_node
		{Source: "branch.1", Target: "shared_node"}, // Branch condition 1 ALSO leads to shared_node
	}

	// Setup test infrastructure
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Execute simulation
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, map[string]interface{}{})
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)

	// Debug output
	t.Logf("Executed %d steps:", len(execution.Steps))
	for i, step := range execution.Steps {
		t.Logf("  Step %d: %s (name: %s, success: %v)", i, step.Id, step.Name, step.Success)
	}

	// Verify all expected nodes executed: trigger, branch, node_a, shared_node
	assert.GreaterOrEqual(t, len(execution.Steps), 4, "Should execute trigger, branch, node_a, and shared_node")

	// Verify specific nodes executed
	branchExecuted := false
	nodeAExecuted := false
	sharedNodeExecuted := false

	for _, step := range execution.Steps {
		switch step.Id {
		case "branch":
			branchExecuted = true
			assert.True(t, step.Success, "Branch node should succeed")
		case "node_a":
			nodeAExecuted = true
			assert.True(t, step.Success, "node_a should succeed")
		case "shared_node":
			sharedNodeExecuted = true
			assert.True(t, step.Success, "shared_node should succeed")
		}
	}

	assert.True(t, branchExecuted, "Branch node should have executed")
	assert.True(t, nodeAExecuted, "node_a should have executed (condition 0 selected)")
	assert.True(t, sharedNodeExecuted, "shared_node MUST execute - this is the bug we're testing for!")

	// Verify execution order: branch should come before node_a, node_a before shared_node
	branchIdx, nodeAIdx, sharedIdx := -1, -1, -1
	for i, step := range execution.Steps {
		switch step.Id {
		case "branch":
			branchIdx = i
		case "node_a":
			nodeAIdx = i
		case "shared_node":
			sharedIdx = i
		}
	}

	assert.Less(t, branchIdx, nodeAIdx, "branch should execute before node_a")
	assert.Less(t, nodeAIdx, sharedIdx, "node_a should execute before shared_node")
}
