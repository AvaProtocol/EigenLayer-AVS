package taskengine

import (
	"strings"
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// captureLogger spies on which level a message was logged at. We only need
// Error vs Warn for this fix — everything else delegates to no-op.
type captureLogger struct {
	mu       sync.Mutex
	errorMsg []string
	warnMsg  []string
}

func (l *captureLogger) record(bucket *[]string, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*bucket = append(*bucket, msg)
}

func (l *captureLogger) Debug(msg string, _ ...interface{})    {}
func (l *captureLogger) Debugf(string, ...interface{})         {}
func (l *captureLogger) Info(msg string, _ ...interface{})     {}
func (l *captureLogger) Infof(string, ...interface{})          {}
func (l *captureLogger) Warn(msg string, _ ...interface{})     { l.record(&l.warnMsg, msg) }
func (l *captureLogger) Warnf(string, ...interface{})          {}
func (l *captureLogger) Error(msg string, _ ...interface{})    { l.record(&l.errorMsg, msg) }
func (l *captureLogger) Errorf(string, ...interface{})         {}
func (l *captureLogger) Fatal(string, ...interface{})          {}
func (l *captureLogger) Fatalf(string, ...interface{})         {}
func (l *captureLogger) With(...interface{}) sdklogging.Logger { return l }

// TestPersistFailedExecutionLogsAtWarnNotError protects EIGENLAYER-AVS-1V:
// validation failures (e.g. smart wallet does not belong to owner) recur on
// every trigger tick until the task is fixed. They must log at Warn so
// SentryLogger.Error -> sentry.CaptureException is not invoked per tick.
func TestPersistFailedExecutionLogsAtWarnNotError(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	spy := &captureLogger{}
	executor := NewExecutorForTesting(testutil.GetTestSmartWalletConfig(), db, spy)

	task := &model.Workflow{
		Task: &avsproto.Task{
			Id:                 "task-validation-test",
			Owner:              "0x0000000000000000000000000000000000000001",
			SmartWalletAddress: "0x0000000000000000000000000000000000000002",
		},
	}
	exec := &avsproto.Execution{
		Id:     "exec-validation-test",
		Error:  "task smart wallet address does not belong to owner",
		Status: avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED,
	}

	executor.persistFailedExecution(task, exec, avsproto.TaskStatus_Enabled)

	const wantMsg = "task execution failed during validation"
	foundWarn := false
	for _, m := range spy.warnMsg {
		if strings.Contains(m, wantMsg) {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Fatalf("expected Warn log containing %q, got warn=%v error=%v", wantMsg, spy.warnMsg, spy.errorMsg)
	}
	for _, m := range spy.errorMsg {
		if strings.Contains(m, wantMsg) {
			t.Fatalf("validation-failure message must not log at Error (would trigger Sentry capture every tick); got: %q", m)
		}
	}
}
