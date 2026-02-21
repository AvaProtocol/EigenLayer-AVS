package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEngineWithQueue creates an Engine with a real queue for tests that
// exercise the full AggregateChecksResultWithState path (which enqueues work).
func newTestEngineWithQueue(t *testing.T) (*Engine, storage.Storage) {
	t.Helper()
	db := testutil.TestMustDB()
	logger := testutil.GetLogger()
	cfg := testutil.GetAggregatorConfig()

	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})
	require.NoError(t, queue.MustStart())

	engine := New(db, cfg, queue, logger)
	require.NoError(t, engine.MustStart())

	return engine, db
}

// TestTriggerRequestIdDedup verifies that two NotifyTriggers RPCs carrying
// the same trigger_request_id are collapsed: the first is executed normally,
// the second returns status "deduplicated" without running the task again.
func TestTriggerRequestIdDedup(t *testing.T) {
	engine, db := newTestEngineWithQueue(t)
	defer storage.Destroy(db.(*storage.BadgerStorage))

	user := testutil.TestUser1()

	// Create a task with high max execution so it stays runnable
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	taskReq.MaxExecution = 100
	task, err := engine.CreateTask(user, taskReq)
	require.NoError(t, err)

	operatorAddr := "0x1234567890123456789012345678901234567890"

	// Set up operator tracking so AggregateChecksResultWithState finds the operator
	engine.lock.Lock()
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: map[string]bool{task.Id: true},
	}
	engine.lock.Unlock()

	triggerRequestID := task.Id + ":1771641900003"

	payload := &avsproto.NotifyTriggersReq{
		Address:          operatorAddr,
		Signature:        "pending",
		TaskId:           task.Id,
		TriggerType:      avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerRequestId: triggerRequestID,
	}

	// First call should succeed normally (queued for execution)
	state1, err := engine.AggregateChecksResultWithState(operatorAddr, payload)
	require.NoError(t, err)
	assert.NotEqual(t, "deduplicated", state1.Status,
		"First call must not be deduplicated")

	// Second call with the SAME trigger_request_id should be deduplicated
	state2, err := engine.AggregateChecksResultWithState(operatorAddr, payload)
	require.NoError(t, err)
	assert.Equal(t, "deduplicated", state2.Status,
		"Second call with same trigger_request_id must be deduplicated")
	assert.True(t, state2.TaskStillEnabled,
		"Deduplicated response should still indicate task is enabled")
}

// TestTriggerRequestIdDedup_DifferentIdsNotDeduplicated verifies that two
// notifications with different trigger_request_ids are both processed
// normally (not deduplicated).
func TestTriggerRequestIdDedup_DifferentIdsNotDeduplicated(t *testing.T) {
	engine, db := newTestEngineWithQueue(t)
	defer storage.Destroy(db.(*storage.BadgerStorage))

	user := testutil.TestUser1()

	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	taskReq.MaxExecution = 100
	task, err := engine.CreateTask(user, taskReq)
	require.NoError(t, err)

	operatorAddr := "0x1234567890123456789012345678901234567890"

	engine.lock.Lock()
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: map[string]bool{task.Id: true},
	}
	engine.lock.Unlock()

	// Two payloads with different trigger_request_ids (different cron ticks)
	payload1 := &avsproto.NotifyTriggersReq{
		Address:          operatorAddr,
		Signature:        "pending",
		TaskId:           task.Id,
		TriggerType:      avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerRequestId: task.Id + ":1771641900003",
	}
	payload2 := &avsproto.NotifyTriggersReq{
		Address:          operatorAddr,
		Signature:        "pending",
		TaskId:           task.Id,
		TriggerType:      avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerRequestId: task.Id + ":1771642080003", // 3 minutes later
	}

	state1, err := engine.AggregateChecksResultWithState(operatorAddr, payload1)
	require.NoError(t, err)
	assert.NotEqual(t, "deduplicated", state1.Status,
		"First trigger event should not be deduplicated")

	state2, err := engine.AggregateChecksResultWithState(operatorAddr, payload2)
	require.NoError(t, err)
	assert.NotEqual(t, "deduplicated", state2.Status,
		"Second trigger event with different ID should not be deduplicated")
}

// TestTriggerRequestIdDedup_EmptyIdSkipsDedup verifies that a notification
// without a trigger_request_id (backward compatibility) is never deduplicated.
func TestTriggerRequestIdDedup_EmptyIdSkipsDedup(t *testing.T) {
	engine, db := newTestEngineWithQueue(t)
	defer storage.Destroy(db.(*storage.BadgerStorage))

	user := testutil.TestUser1()

	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	taskReq.MaxExecution = 100
	task, err := engine.CreateTask(user, taskReq)
	require.NoError(t, err)

	operatorAddr := "0x1234567890123456789012345678901234567890"

	engine.lock.Lock()
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: map[string]bool{task.Id: true},
	}
	engine.lock.Unlock()

	// Payload without TriggerRequestId (old operator that hasn't upgraded)
	payload := &avsproto.NotifyTriggersReq{
		Address:     operatorAddr,
		Signature:   "pending",
		TaskId:      task.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		// TriggerRequestId intentionally empty
	}

	state1, err := engine.AggregateChecksResultWithState(operatorAddr, payload)
	require.NoError(t, err)
	assert.NotEqual(t, "deduplicated", state1.Status)

	// Second call also should not be deduplicated when ID is empty
	state2, err := engine.AggregateChecksResultWithState(operatorAddr, payload)
	require.NoError(t, err)
	assert.NotEqual(t, "deduplicated", state2.Status,
		"Empty trigger_request_id should bypass dedup entirely")
}
