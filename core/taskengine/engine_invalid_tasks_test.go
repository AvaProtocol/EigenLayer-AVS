package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedInvalidEnabledTask stores an invalid task (block trigger with no config)
// under the Enabled status key, mirroring a task created before validation
// existed, and returns it.
func seedInvalidEnabledTask(t *testing.T, db interface {
	Set(key, value []byte) error
}, chainID int64, id string) *model.Workflow {
	t.Helper()
	task := &model.Workflow{Task: &avsproto.Task{
		Id:      id,
		ChainId: chainID,
		Status:  avsproto.TaskStatus_Enabled,
		Trigger: &avsproto.TaskTrigger{
			Name: "trigger1",
			// Block trigger with a nil Config → fails ValidateWithError with
			// "block trigger config is required but missing".
			TriggerType: &avsproto.TaskTrigger_Block{Block: &avsproto.BlockTrigger{}},
		},
	}}
	require.Error(t, task.ValidateWithError(), "fixture must be invalid")

	taskJSON, err := task.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set(ChainWorkflowStorageKey(chainID, id, avsproto.TaskStatus_Enabled), taskJSON))
	return task
}

// TestDetectAndHandleInvalidTasks_DeletesStaleEnabledKey is the regression test
// for the every-boot re-fail loop: marking a task Failed must also remove its
// stale Enabled-status key, so a subsequent boot's Enabled-prefix scan does not
// reload and re-fail it.
func TestDetectAndHandleInvalidTasks_DeletesStaleEnabledKey(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	n := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	const chainID = int64(11155111)
	task := seedInvalidEnabledTask(t, db, chainID, "invalid-task-1")
	n.AddWorkflowForTesting(task)

	// First scan: marks it failed.
	require.NoError(t, n.DetectAndHandleInvalidTasks())

	// The stale Enabled key must be gone...
	enabledItems, err := db.GetByPrefix(ChainWorkflowByStatusStoragePrefix(chainID, avsproto.TaskStatus_Enabled))
	require.NoError(t, err)
	assert.Empty(t, enabledItems, "stale Enabled-status key must be deleted so the next boot can't reload it")

	// ...and the task must now live under the Failed key.
	failedExists, err := db.Exist(ChainWorkflowStorageKey(chainID, task.Id, avsproto.TaskStatus_Failed))
	require.NoError(t, err)
	assert.True(t, failedExists, "task should now be stored under the Failed status key")

	// It must be dropped from the in-memory active set.
	n.lock.Lock()
	_, stillActive := n.tasks[task.Id]
	n.lock.Unlock()
	assert.False(t, stillActive, "failed task should be dropped from the active set")

	// Simulate the next boot: reloading from the Enabled prefix must NOT bring
	// it back. This is the assertion that would have caught the loop.
	reloaded, err := db.GetByPrefix(ChainWorkflowByStatusStoragePrefix(chainID, avsproto.TaskStatus_Enabled))
	require.NoError(t, err)
	assert.Empty(t, reloaded, "invalid task must not reappear under Enabled on the next boot")
}

// TestDetectAndHandleInvalidTasks_LeavesValidTasksAlone ensures the scan is a
// no-op for valid tasks (no spurious failures, no key churn).
func TestDetectAndHandleInvalidTasks_LeavesValidTasksAlone(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	n := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	const chainID = int64(11155111)
	valid := &model.Workflow{Task: &avsproto.Task{
		Id:      "valid-task-1",
		ChainId: chainID,
		Status:  avsproto.TaskStatus_Enabled,
		Trigger: &avsproto.TaskTrigger{
			Name: "trigger1",
			TriggerType: &avsproto.TaskTrigger_Block{Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{Interval: 1},
			}},
		},
	}}
	require.NoError(t, valid.ValidateWithError(), "fixture must be valid")
	n.AddWorkflowForTesting(valid)

	require.NoError(t, n.DetectAndHandleInvalidTasks())

	n.lock.Lock()
	_, stillActive := n.tasks[valid.Id]
	n.lock.Unlock()
	assert.True(t, stillActive, "valid task must remain active")

	failedExists, err := db.Exist(ChainWorkflowStorageKey(chainID, valid.Id, avsproto.TaskStatus_Failed))
	require.NoError(t, err)
	assert.False(t, failedExists, "valid task must not be written under the Failed key")
}
