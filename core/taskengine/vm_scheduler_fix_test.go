package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			Manual: &avsproto.ManualTrigger{},
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
	//                                   |-> (condition 0) -> approve -> swap -> email
	//                                   |-> (condition 1) -> email

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "testTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{},
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
	}

	edges := []*avsproto.TaskEdge{
		{Source: "trigger1", Target: "balance"},
		{Source: "balance", Target: "branch"},
		{Source: "branch.0", Target: "approve"}, // IF path
		{Source: "approve", Target: "swap"},
		{Source: "swap", Target: "email"},     // This is the critical edge that was skipped
		{Source: "branch.1", Target: "email"}, // ELSE path
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
