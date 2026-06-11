package apqueue

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

// TestMarkJobDoneTreatsKeyNotFoundAsNoOp covers the race between the worker
// and CleanupOrphanedJobs (cleanup.go): cleanup can delete an in-progress
// job's key when the underlying task is removed mid-execution, so the
// worker's eventual markJobDone(complete) hits a missing src key. That
// must NOT bubble up — bubbling logs Error at worker.go and floods Sentry
// (EIGENLAYER-AVS-1T).
func TestMarkJobDoneTreatsKeyNotFoundAsNoOp(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	q := New(db, testutil.GetLogger(), &QueueOption{Prefix: "test"})
	if err := q.MustStart(); err != nil {
		t.Fatalf("queue start failed: %v", err)
	}
	defer func() { _ = q.Stop() }()

	id, err := q.Enqueue("test_job", "task-1", []byte("payload"))
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	job, err := q.Dequeue()
	if err != nil || job == nil {
		t.Fatalf("dequeue failed: job=%v err=%v", job, err)
	}
	if job.ID != id {
		t.Fatalf("dequeued unexpected job id: want %d got %d", id, job.ID)
	}

	// Simulate CleanupOrphanedJobs deleting the in-progress key while the
	// worker still holds the job in memory.
	inProgressKey := q.getJobKey(jobInProgress, job.ID)
	if err := db.Delete(inProgressKey); err != nil {
		t.Fatalf("simulating cleanup delete failed: %v", err)
	}

	if err := q.markJobDone(job, jobComplete); err != nil {
		t.Fatalf("markJobDone(jobComplete) should swallow Key-not-found, got: %v", err)
	}
	if err := q.markJobDone(job, jobFailed); err != nil {
		t.Fatalf("markJobDone(jobFailed) should swallow Key-not-found, got: %v", err)
	}
}

// TestMarkJobDoneHappyPath asserts the normal Move(inprogress → complete)
// still works after the Key-not-found short-circuit landed.
func TestMarkJobDoneHappyPath(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	q := New(db, testutil.GetLogger(), &QueueOption{Prefix: "test"})
	if err := q.MustStart(); err != nil {
		t.Fatalf("queue start failed: %v", err)
	}
	defer func() { _ = q.Stop() }()

	if _, err := q.Enqueue("test_job", "task-1", []byte("payload")); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	job, err := q.Dequeue()
	if err != nil || job == nil {
		t.Fatalf("dequeue failed: job=%v err=%v", job, err)
	}

	if err := q.markJobDone(job, jobComplete); err != nil {
		t.Fatalf("markJobDone(jobComplete) failed: %v", err)
	}

	completeKey := q.getJobKey(jobComplete, job.ID)
	if _, err := db.GetKey(completeKey); err != nil {
		t.Fatalf("expected complete key to exist after markJobDone, got: %v", err)
	}
	inProgressKey := q.getJobKey(jobInProgress, job.ID)
	if _, err := db.GetKey(inProgressKey); err == nil {
		t.Fatalf("expected in-progress key to be gone after markJobDone")
	}
}
