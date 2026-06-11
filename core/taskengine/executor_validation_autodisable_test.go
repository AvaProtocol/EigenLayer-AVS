package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func newTaskForAutoDisable(id string) *model.Workflow {
	return &model.Workflow{
		Task: &avsproto.Task{
			Id:                 id,
			Owner:              "0x0000000000000000000000000000000000000001",
			SmartWalletAddress: "0x0000000000000000000000000000000000000002",
			Status:             avsproto.TaskStatus_Enabled,
		},
	}
}

func newFailedExecution(id, errMsg string) *avsproto.Execution {
	return &avsproto.Execution{
		Id:     id,
		Error:  errMsg,
		Status: avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED,
	}
}

// TestPermanentValidationErrorIncrementsCounter covers the three permanent
// rejection prefixes that should count toward auto-disable.
func TestPermanentValidationErrorIncrementsCounter(t *testing.T) {
	cases := []string{
		"invalid or missing task smart wallet address for deployed run",
		"task smart wallet address does not belong to owner",
		"failed to create VM: some compile error",
	}
	for _, errMsg := range cases {
		t.Run(errMsg, func(t *testing.T) {
			db := testutil.TestMustDB()
			defer storage.Destroy(db.(*storage.BadgerStorage))

			executor := NewExecutorForTesting(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
			task := newTaskForAutoDisable("task-perm-" + errMsg)

			executor.persistFailedExecution(task, newFailedExecution("e1", errMsg), avsproto.TaskStatus_Enabled)

			if task.ConsecutiveValidationFailures != 1 {
				t.Fatalf("counter should be 1 after one permanent failure, got %d", task.ConsecutiveValidationFailures)
			}
			if task.LastValidationError != errMsg {
				t.Fatalf("LastValidationError = %q, want %q", task.LastValidationError, errMsg)
			}
			if task.Status != avsproto.TaskStatus_Enabled {
				t.Fatalf("task should still be Enabled after one failure, got %v", task.Status)
			}
		})
	}
}

// TestTransientValidationErrorDoesNotIncrementCounter — RPC errors and
// credit-limit blocks are recoverable and must not push toward auto-disable.
func TestTransientValidationErrorDoesNotIncrementCounter(t *testing.T) {
	cases := []string{
		"failed to validate wallet ownership for owner 0xabc: dial tcp: i/o timeout",
		"[INSUFFICIENT_CREDIT] outstanding value fees (1000 wei) exceed credit limit",
	}
	for _, errMsg := range cases {
		t.Run(errMsg, func(t *testing.T) {
			db := testutil.TestMustDB()
			defer storage.Destroy(db.(*storage.BadgerStorage))

			executor := NewExecutorForTesting(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
			task := newTaskForAutoDisable("task-trans-" + errMsg)

			executor.persistFailedExecution(task, newFailedExecution("e1", errMsg), avsproto.TaskStatus_Enabled)

			if task.ConsecutiveValidationFailures != 0 {
				t.Fatalf("transient error should not increment counter, got %d", task.ConsecutiveValidationFailures)
			}
			if task.LastValidationError != errMsg {
				t.Fatalf("LastValidationError should still be recorded for visibility, got %q", task.LastValidationError)
			}
			if task.Status != avsproto.TaskStatus_Enabled {
				t.Fatalf("transient errors must never auto-disable, got status %v", task.Status)
			}
		})
	}
}

// TestAutoDisableOnThreshold drives the counter to validationFailureDisableThreshold
// and asserts the task flips to Disabled exactly once.
func TestAutoDisableOnThreshold(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	executor := NewExecutorForTesting(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	task := newTaskForAutoDisable("task-threshold")

	const errMsg = "task smart wallet address does not belong to owner"
	for i := uint32(1); i <= validationFailureDisableThreshold; i++ {
		executor.persistFailedExecution(task, newFailedExecution("e", errMsg), avsproto.TaskStatus_Enabled)
		if i < validationFailureDisableThreshold {
			if task.Status != avsproto.TaskStatus_Enabled {
				t.Fatalf("status flipped early at i=%d, want Enabled got %v", i, task.Status)
			}
		}
	}
	if task.ConsecutiveValidationFailures != validationFailureDisableThreshold {
		t.Fatalf("counter at threshold tick = %d, want %d", task.ConsecutiveValidationFailures, validationFailureDisableThreshold)
	}
	if task.Status != avsproto.TaskStatus_Disabled {
		t.Fatalf("expected Disabled after %d permanent failures, got %v", validationFailureDisableThreshold, task.Status)
	}

	// One more failure after disable should not panic, change status, or
	// double-trigger the Sentry capture (status is no longer Enabled).
	executor.persistFailedExecution(task, newFailedExecution("e", errMsg), avsproto.TaskStatus_Disabled)
	if task.Status != avsproto.TaskStatus_Disabled {
		t.Fatalf("status should remain Disabled, got %v", task.Status)
	}
}

// TestIsPermanentValidationErrorClassification protects the classifier from
// drift when error strings get edited in executor.go.
func TestIsPermanentValidationErrorClassification(t *testing.T) {
	permanent := []string{
		"invalid or missing task smart wallet address for deployed run",
		"task smart wallet address does not belong to owner",
		"failed to create VM: cycle detected",
	}
	for _, m := range permanent {
		if !isPermanentValidationError(m) {
			t.Errorf("expected permanent: %q", m)
		}
	}
	transient := []string{
		"failed to validate wallet ownership for owner 0xabc: rpc unreachable",
		"[INSUFFICIENT_CREDIT] outstanding value fees (1 wei) exceed credit limit",
		"",
		"some unrelated error",
	}
	for _, m := range transient {
		if isPermanentValidationError(m) {
			t.Errorf("expected transient: %q", m)
		}
	}
}
