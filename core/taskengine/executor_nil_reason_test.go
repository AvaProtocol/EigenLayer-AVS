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
					Config: &avsproto.RestAPINode_Config{
						Url:    server.URL,
						Method: "POST",
						Body:   `{"test": "data"}`,
					},
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
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED,
		TriggerOutput: nil,
		ExecutionID:   "exec_nil_reason",
	})

	if err != nil {
		t.Errorf("Expected no error with unspecified trigger type, but got: %v", err)
	}

	if !execution.Success {
		t.Errorf("Expected success status but got failure")
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node) but got: %d", len(execution.Steps))
	}

	// Check trigger step at index 0
	if execution.Steps[0].Id != "manual_trigger" {
		t.Errorf("Expected trigger step ID to be manual_trigger but got: %s", execution.Steps[0].Id)
	}

	// Check node step at index 1
	if execution.Steps[1].Id != "rest1" {
		t.Errorf("Expected step ID to be rest1 but got: %s", execution.Steps[1].Id)
	}
}
