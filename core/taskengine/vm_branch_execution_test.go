package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestRunTaskWithBranchNode(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://httpbin.org/get",
						Method: "GET",
					},
				},
			},
		},
		{
			Id:   "456",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition1",
								Type:       "if",
								Expression: "httpnode.data.body.url == 'https://httpbin.org/get'",
							},
						},
					},
				},
			},
		},
		{
			Id:   "789",
			Name: "httpnode2",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://httpbin.org/get",
						Method: "GET",
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123",
		},
		{
			Id:     "e2",
			Source: "123",
			Target: "456",
		},
		{
			Id:     "e3",
			Source: "456.condition1",
			Target: "789",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	vm.Compile()

	if vm.entrypoint != "triggertest" {
		t.Errorf("Error compute entrypoint. Expected triggertest Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected no error Got error %v", err)
	}

	vm.mu.Lock()
	tempData, exists := vm.vars["httpnode"]
	vm.mu.Unlock()

	if !exists {
		t.Errorf("httpnode variable not found in VM vars")
		return
	}

	if tempData == nil {
		t.Errorf("httpnode variable is nil")
		return
	}

	vm.mu.Lock()
	tempData2, exists2 := vm.vars["httpnode2"]
	vm.mu.Unlock()

	if !exists2 {
		t.Errorf("httpnode2 variable not found in VM vars")
		return
	}

	if tempData2 == nil {
		t.Errorf("httpnode2 variable is nil")
		return
	}

	vm.mu.Lock()
	branchData, branchExists := vm.vars["branch"]
	vm.mu.Unlock()

	if !branchExists {
		t.Errorf("branch variable not found in VM vars")
		return
	}

	if branchData == nil {
		t.Errorf("branch variable is nil")
		return
	}

	branchMap, ok := branchData.(map[string]any)
	if !ok {
		t.Errorf("branch is not map[string]any, got %T: %v", branchData, branchData)
		return
	}

	data, dataExists := branchMap["data"]
	if !dataExists {
		t.Errorf("data field not found in branch response, available fields: %v", branchMap)
		return
	}

	dataMap, ok := data.(map[string]any)
	if !ok {
		t.Errorf("data is not map[string]any, got %T: %v", data, data)
		return
	}

	conditionResults, conditionExists := dataMap["condition_results"]
	if !conditionExists {
		t.Errorf("condition_results field not found in data, available fields: %v", dataMap)
		return
	}

	conditionResultsSlice, ok := conditionResults.([]map[string]interface{})
	if !ok {
		t.Errorf("condition_results is not []map[string]interface{}, got %T: %v", conditionResults, conditionResults)
		return
	}

	if len(conditionResultsSlice) != 1 {
		t.Errorf("Expected 1 condition result, got %d", len(conditionResultsSlice))
		return
	}

	firstCondition := conditionResultsSlice[0]

	if firstCondition["id"] != "condition1" {
		t.Errorf("Expected condition id 'condition1', got %v", firstCondition["id"])
	}

	if firstCondition["result"] != true {
		t.Errorf("Expected condition result true, got %v", firstCondition["result"])
	}

	if firstCondition["next_node_id"] != "789" {
		t.Errorf("Expected next_node_id '789', got %v", firstCondition["next_node_id"])
	}
}
