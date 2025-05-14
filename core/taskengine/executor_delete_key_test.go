package taskengine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestExecutorDeletesTriggerKey(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "success"}`))
	}))
	defer server.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "rest1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    server.URL,
					Method: "GET",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "manual_trigger",
		Name: "manual_trigger",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "rest1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "DeleteKeyTestTask",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	mockDB := &mockStorage{db: db}
	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), mockDB, testutil.GetLogger())
	
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      nil,
		ExecutionID: "exec_delete_key_test",
	})

	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	if !execution.Success {
		t.Errorf("Expected success status but got failure")
	}

	if !mockDB.deleteWasCalled {
		t.Errorf("Expected Delete to be called, but it wasn't")
	}

	expectedKey := string(TaskTriggerKey(task, "exec_delete_key_test"))
	if mockDB.lastDeletedKey != expectedKey {
		t.Errorf("Expected Delete to be called with key %s, but got %s", expectedKey, mockDB.lastDeletedKey)
	}
}

type mockStorage struct {
	db              storage.Storage
	deleteWasCalled bool
	lastDeletedKey  string
}

func (m *mockStorage) Setup() error {
	return m.db.Setup()
}

func (m *mockStorage) Close() error {
	return m.db.Close()
}

func (m *mockStorage) GetSequence(prefix []byte, inflightItem uint64) (storage.Sequence, error) {
	return m.db.GetSequence(prefix, inflightItem)
}

func (m *mockStorage) Exist(key []byte) (bool, error) {
	return m.db.Exist(key)
}

func (m *mockStorage) GetKey(key []byte) ([]byte, error) {
	return m.db.GetKey(key)
}

func (m *mockStorage) GetByPrefix(prefix []byte) ([]*storage.KeyValueItem, error) {
	return m.db.GetByPrefix(prefix)
}

func (m *mockStorage) GetKeyHasPrefix(prefix []byte) ([][]byte, error) {
	return m.db.GetKeyHasPrefix(prefix)
}

func (m *mockStorage) ListKeys(prefix string) ([]string, error) {
	return m.db.ListKeys(prefix)
}

func (m *mockStorage) ListKeysMulti(prefixes []string) ([]string, error) {
	return m.db.ListKeysMulti(prefixes)
}

func (m *mockStorage) CountKeysByPrefix(prefix []byte) (int64, error) {
	return m.db.CountKeysByPrefix(prefix)
}

func (m *mockStorage) CountKeysByPrefixes(prefixes [][]byte) (int64, error) {
	return m.db.CountKeysByPrefixes(prefixes)
}

func (m *mockStorage) BatchWrite(updates map[string][]byte) error {
	return m.db.BatchWrite(updates)
}

func (m *mockStorage) Move(src, dest []byte) error {
	return m.db.Move(src, dest)
}

func (m *mockStorage) Set(key, value []byte) error {
	return m.db.Set(key, value)
}

func (m *mockStorage) Delete(key []byte) error {
	m.deleteWasCalled = true
	m.lastDeletedKey = string(key)
	return m.db.Delete(key)
}

func (m *mockStorage) GetCounter(key []byte, defaultValue ...uint64) (uint64, error) {
	return m.db.GetCounter(key, defaultValue...)
}

func (m *mockStorage) IncCounter(key []byte, defaultValue ...uint64) (uint64, error) {
	return m.db.IncCounter(key, defaultValue...)
}

func (m *mockStorage) SetCounter(key []byte, value uint64) error {
	return m.db.SetCounter(key, value)
}

func (m *mockStorage) Vacuum() error {
	return m.db.Vacuum()
}

func (m *mockStorage) DbPath() string {
	return m.db.DbPath()
}

func (m *mockStorage) FirstKVHasPrefix(prefix []byte) ([]byte, []byte, error) {
	return m.db.FirstKVHasPrefix(prefix)
}

func (m *mockStorage) Backup(ctx context.Context, w io.Writer, since uint64) (uint64, error) {
	return m.db.Backup(ctx, w, since)
}

func (m *mockStorage) Load(ctx context.Context, r io.Reader) error {
	return m.db.Load(ctx, r)
}
