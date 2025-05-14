package taskengine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestExecutorRunTaskWithNilReason(t *testing.T) {
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
		Task: &avsproto.Task{
			Id:      "ManualTaskID",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      nil, // Explicitly set to nil to test our fix
		ExecutionID: "exec_nil_reason",
	})

	if err != nil {
		t.Errorf("Expected no error with nil reason, but got: %v", err)
	}

	if !execution.Success {
		t.Errorf("Expected success status but got failure")
	}

	if execution.Reason == nil {
		t.Errorf("Expected a default reason to be created, but it's still nil")
	}

	if execution.Reason.Type != avsproto.TriggerReason_Unset {
		t.Errorf("Expected default reason type to be Unset, but got: %v", execution.Reason.Type)
	}

	if len(execution.Steps) != 1 {
		t.Errorf("Expected 1 step but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "rest1" {
		t.Errorf("Expected step ID to be rest1 but got: %s", execution.Steps[0].NodeId)
	}
}
