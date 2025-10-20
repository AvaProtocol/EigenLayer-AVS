package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNonBlockingExecutionFullLifecycle verifies that execution indexes remain stable
// throughout the complete execution lifecycle (pending -> completed).
//
// This test validates the specific issue where:
// 1. Non-blocking trigger assigns atomic index (e.g., 0) during pending state
// 2. When execution completes, the index should remain the same (0), not recalculated
func TestNonBlockingExecutionFullLifecycle(t *testing.T) {
	// Set up test database and engine for proper atomic indexing
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create engine with proper configuration for atomic indexing
	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), engine)

	// Create a simple task with manual trigger and custom code node
	task := &model.Task{
		Task: &avsproto.Task{
			Id:             "test-task-lifecycle",
			Owner:          "", // Empty owner to skip wallet validation
			Status:         avsproto.TaskStatus_Active,
			Name:           "Test Full Lifecycle Index",
			ExecutionCount: 0, // Start with 0 executions
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "manualTrigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{},
					},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "node1",
					Name: "testNode",
					Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								Lang:   avsproto.Lang_LANG_JAVASCRIPT,
								Source: "return 'test result'", // Simple JavaScript that returns a string
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{
				{
					Id:     "edge1",
					Source: "trigger1",
					Target: "node1",
				},
			},
		},
	}

	t.Logf("Created task with ID: %s", task.Id)

	// Step 1: Execute the task directly using executor and check index
	executionId := ulid.Make().String()
	queueData := &QueueExecutionData{
		ExecutionID:   executionId,
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: &avsproto.ManualTrigger_Output{},
	}

	// Store a pre-assigned index in pending storage to simulate non-blocking behavior
	preAssignedIndex, indexErr := engine.AssignNextExecutionIndex(task)
	require.NoError(t, indexErr, "Should be able to assign execution index")
	pendingKey := PendingExecutionKey(task, executionId)
	err := engine.db.Set(pendingKey, []byte(fmt.Sprintf("%d", preAssignedIndex)))
	require.NoError(t, err, "Should be able to store pending execution index")

	t.Logf("Pre-assigned execution index: %d", preAssignedIndex)

	// Step 2: Execute the task - this is where the bug might occur
	execution, err := executor.RunTask(task, queueData)
	require.NoError(t, err, "RunTask should not return error")
	require.NotNil(t, execution, "Execution should not be nil")

	t.Logf("✅ Execution completed:")
	t.Logf("   - ExecutionId: %s", execution.Id)
	t.Logf("   - Pre-assigned index: %d", preAssignedIndex)
	t.Logf("   - Final execution index: %d", execution.Index)
	t.Logf("   - Status: %v", execution.Status)

	// Step 3: Critical check - the execution index should match the pre-assigned index
	if execution.Index != preAssignedIndex {
		t.Errorf("❌ CRITICAL BUG DETECTED: Execution index changed during lifecycle!")
		t.Errorf("   - Expected index (pre-assigned): %d", preAssignedIndex)
		t.Errorf("   - Actual index (after execution): %d", execution.Index)
		t.Errorf("   - This breaks the atomic execution index assignment fix!")

		// This is the bug we need to fix - the executor is recalculating the index
		// instead of using the pre-assigned atomic index stored during non-blocking trigger
		assert.Equal(t, preAssignedIndex, execution.Index, "Execution index must remain stable throughout lifecycle")
	} else {
		t.Logf("✅ Index stability verified: execution index remained %d throughout lifecycle", execution.Index)
	}
}
