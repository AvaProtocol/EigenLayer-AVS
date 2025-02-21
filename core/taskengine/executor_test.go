package taskengine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

func TestExecutorRunTaskSucess(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Set up a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a successful response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "success"}`))
	}))
	defer server.Close()

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:   "a1",
							Type: "if",
							// The test data is of this transaction https://sepolia.etherscan.io/tx/0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8 which is 3.45 token
							Expression: "Number(triggertest.data.value_formatted) >= 3",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    server.URL, // Use the test server URL
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if err != nil {
		t.Errorf("expect no error but got %v", err)
	}

	if !execution.Success {
		t.Errorf("Expect success status but got failure")
	}

	if execution.Error != "" {
		t.Errorf("Expect no error but got: %s", execution.Error)
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expect evaluate 2 steps only but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("step id doesn't match, expect branch1.a1 but got: %s", execution.Steps[0].NodeId)
	}

	if execution.Steps[0].OutputData != "branch1.a1" {
		t.Errorf("expect branch output data is `branch1.a1` but got %s", execution.Steps[0].OutputData)
	}

	if execution.Steps[1].NodeId != "notification1" {
		t.Errorf("step id doesn't match, expect notification1 but got: %s", execution.Steps[1].NodeId)
	}

	if execution.Steps[1].OutputData != "{\"status\": \"success\"}" {
		t.Errorf("expect branch output data is {\"status\": \"success\"} but got %s", execution.Steps[1].OutputData)
	}
}

func TestExecutorRunTaskStopAndReturnErrorWhenANodeFailed(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:         "a1",
							Type:       "if",
							Expression: "a >= 5",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if err == nil {
		t.Errorf("expect an error but the execution return no error")
	}

	if execution.Success {
		t.Errorf("Expect failure status but got success")
	}

	if len(execution.Steps) != 1 {
		t.Errorf("expect a single step but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("expect evaluate branch node but got: %s", execution.Steps[0].NodeId)
	}

	if execution.Steps[0].OutputData != "" {
		t.Errorf("expect evaluate branch output data empty but got: %s", execution.Steps[0].OutputData)
	}
}

func TestExecutorRunTaskComputeSuccessFalseWhenANodeFailedToRun(t *testing.T) {
	// Set up a test HTTP server that returns a 503 status code
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:         "condition1",
							Type:       "if",
							Expression: "true",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "rest1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    server.URL, // Use the test server URL
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.condition1",
			Target: "rest1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if err == nil {
		t.Errorf("expected error due to 503 response but got nil")
	}

	if execution.Success {
		t.Error("Expected failure status but got success")
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expect evaluate 2 steps but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("step id doesn't match, expect branch1 but got: %s", execution.Steps[0].NodeId)
	}
	if execution.Steps[1].NodeId != "rest1" {
		t.Errorf("step id doesn't match, expect branch1 but got: %s", execution.Steps[0].NodeId)
	}

}
