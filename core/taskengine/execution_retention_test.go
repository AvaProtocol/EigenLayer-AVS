package taskengine

import (
	"context"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeExecutions stores n execution records for task, oldest first, and
// returns their ids in that order.
func writeExecutions(t *testing.T, db storage.Storage, task *model.Workflow, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = model.GenerateID() // ULIDs: ascending
		require.NoError(t, db.Set(TaskExecutionKey(task, ids[i]), []byte("{}")))
	}
	return ids
}

func TestPruneExecutionHistory(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	const keep = 3
	busy := &model.Workflow{Task: &avsproto.Task{Id: model.GenerateID()}}
	quiet := &model.Workflow{Task: &avsproto.Task{Id: model.GenerateID()}}
	busyIDs := writeExecutions(t, db, busy, 8)
	quietIDs := writeExecutions(t, db, quiet, keep)

	// busyIDs[0] is a durable execution still waiting on a wake; busyIDs[1]
	// failed and left its PENDING status key behind.
	require.NoError(t, db.Set(wakeSubscriptionKey(busyIDs[0]), []byte("{}")))
	require.NoError(t, db.Set(TaskTriggerKey(busy, busyIDs[1]), []byte("0")))
	// Under the prefix but not an execution record.
	require.NoError(t, db.Set([]byte("history:not-a-record"), []byte("x")))

	deleted, err := pruneExecutionHistory(context.Background(), db, keep, 100)
	require.NoError(t, err)

	// 8 records over a cap of 3 leaves 5 to delete, one of them in flight.
	assert.Equal(t, 4, deleted)
	for i, id := range busyIDs {
		want := i == 0 || i >= len(busyIDs)-keep
		assert.Equal(t, want, existsKey(db, TaskExecutionKey(busy, id)), "busy record %d", i)
	}
	assert.False(t, existsKey(db, TaskTriggerKey(busy, busyIDs[1])),
		"a pruned run's status key must go too, or GetExecutionStatus reports it PENDING")
	for _, id := range quietIDs {
		assert.True(t, existsKey(db, TaskExecutionKey(quiet, id)), "a task at the cap keeps everything")
	}
	assert.True(t, existsKey(db, []byte("history:not-a-record")))

	// Nothing left over the cap: a second sweep is a no-op.
	deleted, err = pruneExecutionHistory(context.Background(), db, keep, 100)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}

// A backlog drains over several sweeps, oldest first.
func TestPruneExecutionHistoryStopsAtLimit(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	task := &model.Workflow{Task: &avsproto.Task{Id: model.GenerateID()}}
	ids := writeExecutions(t, db, task, 10)

	deleted, err := pruneExecutionHistory(context.Background(), db, 2, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, deleted)
	for i, id := range ids {
		assert.Equal(t, i >= 3, existsKey(db, TaskExecutionKey(task, id)), "record %d after one sweep", i)
	}

	deleted, err = pruneExecutionHistory(context.Background(), db, 2, 100)
	require.NoError(t, err)
	assert.Equal(t, 5, deleted)
	for i, id := range ids {
		assert.Equal(t, i >= 8, existsKey(db, TaskExecutionKey(task, id)), "record %d after two sweeps", i)
	}
}
