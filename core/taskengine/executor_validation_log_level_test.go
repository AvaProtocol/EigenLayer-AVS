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
	"github.com/oklog/ulid/v2"
)

// captureLogger spies on which level a message was logged at and (for Warn)
// also retains the key/value pairs so tests can assert on structured fields.
// Other levels delegate to no-op.
type captureLogger struct {
	mu       sync.Mutex
	errorMsg []string
	warnMsg  []string
	warnKVs  [][]interface{}
}

func (l *captureLogger) record(bucket *[]string, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*bucket = append(*bucket, msg)
}

func (l *captureLogger) Debug(msg string, _ ...interface{}) {}
func (l *captureLogger) Debugf(string, ...interface{})      {}
func (l *captureLogger) Info(msg string, _ ...interface{})  {}
func (l *captureLogger) Infof(string, ...interface{})       {}
func (l *captureLogger) Warn(msg string, kvs ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnMsg = append(l.warnMsg, msg)
	captured := make([]interface{}, len(kvs))
	copy(captured, kvs)
	l.warnKVs = append(l.warnKVs, captured)
}
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

// findWarnKVs locates the first warnKVs entry whose paired Warn message
// contains substr, returning the key/value slice and ok=true.
func (l *captureLogger) findWarnKVs(substr string) ([]interface{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, m := range l.warnMsg {
		if strings.Contains(m, substr) && i < len(l.warnKVs) {
			return l.warnKVs[i], true
		}
	}
	return nil, false
}

func kvLookup(kvs []interface{}, key string) (interface{}, bool) {
	for i := 0; i+1 < len(kvs); i += 2 {
		k, ok := kvs[i].(string)
		if ok && k == key {
			return kvs[i+1], true
		}
	}
	return nil, false
}

// TestReportTaskAutoDisabledIncludesOwnerContext is the regression for the
// EIGENLAYER-AVS-1Y..28 burst: the auto-disable Warn log (and the matching
// Sentry scope, which is populated from the same data) must carry owner,
// smart_wallet, factory_address and created_at so an operator can triage
// without dropping into BadgerDB.
func TestReportTaskAutoDisabledIncludesOwnerContext(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	spy := &captureLogger{}
	executor := NewExecutorForTesting(testutil.GetTestSmartWalletConfig(), db, spy)

	taskID := ulid.Make().String()
	task := &model.Workflow{
		Task: &avsproto.Task{
			Id:                 taskID,
			Owner:              "0x000000000000000000000000000000000000beef",
			SmartWalletAddress: "0x000000000000000000000000000000000000dead",
			ChainId:            11155111,
			Status:             avsproto.TaskStatus_Enabled,
		},
	}

	const errMsg = "task smart wallet address does not belong to owner"
	for i := uint32(1); i <= validationFailureDisableThreshold; i++ {
		executor.persistFailedExecution(task, &avsproto.Execution{
			Id:     "e",
			Error:  errMsg,
			Status: avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED,
		}, avsproto.TaskStatus_Enabled)
	}
	if task.Status != avsproto.TaskStatus_Disabled {
		t.Fatalf("precondition: task must be Disabled after threshold, got %v", task.Status)
	}

	kvs, ok := spy.findWarnKVs("task auto-disabled after consecutive validation failures")
	if !ok {
		t.Fatalf("expected the auto-disable Warn line to be logged; got warn=%v", spy.warnMsg)
	}
	wantFields := map[string]interface{}{
		"task_id":      taskID,
		"owner":        task.Owner,
		"smart_wallet": task.SmartWalletAddress,
		"chain_id":     task.ChainId,
		"reason":       errMsg,
	}
	for k, want := range wantFields {
		got, found := kvLookup(kvs, k)
		if !found {
			t.Fatalf("auto-disable Warn missing %q; got kvs=%v", k, kvs)
		}
		if got != want {
			t.Fatalf("auto-disable Warn %q = %v, want %v", k, got, want)
		}
	}
	createdAt, found := kvLookup(kvs, "created_at")
	if !found {
		t.Fatalf("auto-disable Warn missing created_at; got kvs=%v", kvs)
	}
	if s, _ := createdAt.(string); s == "" || s == "unknown" {
		t.Fatalf("created_at should derive from the ULID id, got %v", createdAt)
	}
	factoryAddress, found := kvLookup(kvs, "factory_address")
	if !found {
		t.Fatalf("auto-disable Warn missing factory_address; got kvs=%v", kvs)
	}
	if s, _ := factoryAddress.(string); s == "" {
		t.Fatalf("factory_address should resolve from smartWalletConfig in test env, got empty string")
	}
}
