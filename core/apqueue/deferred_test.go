package apqueue

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/stretchr/testify/require"
)

type pausedJobError struct{}

func (pausedJobError) Error() string             { return "budget changing" }
func (pausedJobError) RetryAfter() time.Duration { return 10 * time.Millisecond }

type pausedProcessor struct {
	paused           bool
	attempts, spends int
}

func (p *pausedProcessor) Perform(*Job) error {
	p.attempts++
	if p.paused {
		return pausedJobError{}
	}
	p.spends++
	return nil
}

func TestDeferredJobWaitsWithoutFailureOrDuplicateSpend(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q := New(db, testutil.GetLogger(), &QueueOption{Prefix: "pause"})
	require.NoError(t, q.MustStart())
	t.Cleanup(func() { require.NoError(t, q.Stop()) })
	id, err := q.Enqueue("run", "task-1", []byte("unchanged-trigger"))
	require.NoError(t, err)
	<-q.eventCh
	p := &pausedProcessor{paused: true}
	w := NewWorker(q, db)
	require.NoError(t, w.RegisterProcessor("run", p))
	w.ProcessSignal(id)
	require.Zero(t, p.spends)
	deferred, err := db.GetKey(q.getJobKey(jobDeferred, id))
	require.NoError(t, err)
	stored, err := decodeJob(deferred)
	require.NoError(t, err)
	require.Zero(t, stored.Attempt)
	require.Equal(t, "unchanged-trigger", string(stored.Data))
	failed, err := db.GetByPrefix(q.getQueueKeyPrefix(jobFailed))
	require.NoError(t, err)
	require.Empty(t, failed)
	select {
	case got := <-q.eventCh:
		require.Equal(t, id, got)
	case <-time.After(time.Second):
		t.Fatal("paused job never resumed")
	}
	p.paused = false
	w.ProcessSignal(id)
	w.ProcessSignal(id) // a duplicate wakeup finds no pending job
	require.Equal(t, 2, p.attempts)
	require.Equal(t, 1, p.spends)
	_, err = db.GetKey(q.getJobKey(jobComplete, id))
	require.NoError(t, err)
}

func TestDeferredRestartRestoresEveryWakeupBeyondBuffer(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q := New(db, testutil.GetLogger(), &QueueOption{Prefix: "restart"})
	require.NoError(t, q.MustStart())
	// Persist deferred rows as if the process stopped with jobs waiting. The
	// backlog exceeds eventCh's 1,000 entries; no restored wakeup may be lost.
	for id := uint64(1); id <= 1005; id++ {
		raw, err := encodeJob(&Job{ID: id, Type: "run", Name: "task-1"})
		require.NoError(t, err)
		require.NoError(t, db.Set(q.getJobKey(jobDeferred, id), raw))
	}
	require.NoError(t, q.Stop())
	restarted := New(db, testutil.GetLogger(), &QueueOption{Prefix: "restart"})
	require.NoError(t, restarted.MustStart())
	t.Cleanup(func() { require.NoError(t, restarted.Stop()) })
	seen := map[uint64]bool{}
	for len(seen) < 1005 {
		select {
		case id := <-restarted.eventCh:
			require.False(t, seen[id])
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("recovery dropped deferred wakeups")
		}
	}
	rows, err := db.GetByPrefix(restarted.getQueueKeyPrefix(jobDeferred))
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestDeferredStopPreservesJobForRestart(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q := New(db, testutil.GetLogger(), &QueueOption{Prefix: "stop"})
	require.NoError(t, q.MustStart())
	id, err := q.Enqueue("run", "task", nil)
	require.NoError(t, err)
	job, err := q.Dequeue()
	require.NoError(t, err)
	require.NoError(t, q.deferJob(job, time.Hour))
	require.NoError(t, q.Stop())
	_, err = db.GetKey(q.getJobKey(jobDeferred, id))
	require.NoError(t, err)
}

func TestDeferredInterruptedRecoveryRestoresPendingButNotInProgress(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	q := New(db, testutil.GetLogger(), &QueueOption{Prefix: "interrupted"})
	for id := uint64(1); id <= 1005; id++ {
		raw, err := encodeJob(&Job{ID: id, Type: "run", Name: "task"})
		require.NoError(t, err)
		require.NoError(t, db.Set(q.getJobKey(jobDeferred, id), raw))
	}
	running, err := encodeJob(&Job{ID: 1006, Type: "run", Name: "already-spending"})
	require.NoError(t, err)
	require.NoError(t, db.Set(q.getJobKey(jobInProgress, 1006), running))
	require.NoError(t, q.MustStart())
	// Recovery has moved all rows, but its wakeup producer cannot finish
	// without a consumer because the backlog exceeds the channel buffer.
	require.NoError(t, q.Stop())
	q = New(db, testutil.GetLogger(), &QueueOption{Prefix: "interrupted"})
	require.NoError(t, q.MustStart())
	t.Cleanup(func() { require.NoError(t, q.Stop()) })
	seen := map[uint64]bool{}
	for len(seen) < 1005 {
		select {
		case id := <-q.eventCh:
			require.LessOrEqual(t, id, uint64(1005), "in-progress work must never be replayed")
			require.False(t, seen[id])
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("second restart lost a persisted pending job")
		}
	}
	_, err = db.GetKey(q.getJobKey(jobInProgress, 1006))
	require.NoError(t, err)
}
