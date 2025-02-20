package taskengine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunTaskWithMultipleConditions(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		response := map[string]interface{}{
			"data": string(body),
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

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
							Expression: "a > 10",
						},
						&avsproto.Condition{
							Id:         "condition2",
							Type:       "if",
							Expression: "a > 5",
						},
						&avsproto.Condition{
							Id:   "condition3",
							Type: "else",
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
					Url:    ts.URL,
					Method: "POST",
					Body:   "hit=first_condition",
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification2",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    ts.URL,
					Method: "POST",
					Body:   "hit=second_condition",
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification3",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    ts.URL,
					Method: "POST",
					Body:   "hit=else_condition",
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
			Id:     "e2",
			Source: "branch1.condition1",
			Target: "notification1",
		},
		&avsproto.TaskEdge{
			Id:     "e3",
			Source: "branch1.condition2",
			Target: "notification2",
		},
		&avsproto.TaskEdge{
			Id:     "e4",
			Source: "branch1.condition3",
			Target: "notification3",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	// Set value to hit the second condition (a > 5 but not > 10)
	vm.vars["a"] = 7
	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
		return
	}

	if vm.instructionCount != 2 {
		t.Errorf("incorrect steps, expect 2 got %d", vm.instructionCount)
	}
	if len(vm.ExecutionLogs) != 2 {
		t.Errorf("incorrect log, expect 2 got %d", len(vm.ExecutionLogs))
	}

	// Verify that the second condition was hit
	if !strings.Contains(vm.ExecutionLogs[0].OutputData, "branch1.condition2") {
		t.Errorf("expected second condition to be hit, but got %s", vm.ExecutionLogs[0].OutputData)
	}
	if !strings.Contains(vm.ExecutionLogs[1].OutputData, "second_condition") {
		t.Errorf("expected second notification to be executed, but got %s", vm.ExecutionLogs[1].OutputData)
	}

	// Test first condition
	vm.Reset()
	vm.vars["a"] = 15
	vm.Compile()
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
	}
	if !strings.Contains(vm.ExecutionLogs[0].OutputData, "branch1.condition1") {
		t.Errorf("expected first condition to be hit, but got %s", vm.ExecutionLogs[0].OutputData)
	}

	// Test else condition
	vm.Reset()
	vm.vars["a"] = 3
	vm.Compile()
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
	}
	if !strings.Contains(vm.ExecutionLogs[0].OutputData, "branch1.condition3") {
		t.Errorf("expected else condition to be hit, but got %s", vm.ExecutionLogs[0].OutputData)
	}
}
