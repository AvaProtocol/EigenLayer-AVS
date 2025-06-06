package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestVMCompile(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
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
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
}

func TestRunSimpleTasks(t *testing.T) {
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
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected no error Got error %v", err)
	}

	if len(vm.ExecutionLogs) == 0 {
		t.Errorf("no execution logs found")
		return
	}

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Executing REST API Node ID") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got: %s", vm.ExecutionLogs[0].Log)
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

	responseData, ok := tempData.(map[string]any)
	if !ok {
		t.Errorf("httpnode is not map[string]any, got %T: %v", tempData, tempData)
		return
	}

	data, dataExists := responseData["data"]
	if !dataExists {
		t.Errorf("data field not found in response, available fields: %v", responseData)
		return
	}

	dataMap, ok := data.(map[string]any)
	if !ok {
		t.Errorf("data is not map[string]any, got %T: %v", data, data)
		return
	}

	body, bodyExists := dataMap["body"]
	if !bodyExists {
		t.Errorf("body field not found in data, available fields: %v", dataMap)
		return
	}

	bodyMap, ok := body.(map[string]any)
	if !ok {
		t.Errorf("body is not map[string]any, got %T: %v", body, body)
		return
	}

	if bodyMap["url"].(string) != "https://httpbin.org/get" {
		t.Errorf("invalid output data. expect https://httpbin.org/get but got %s", bodyMap["url"])
	}
}

func TestRunSequentialTasks(t *testing.T) {
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
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected no error Got error %v", err)
	}

	if len(vm.ExecutionLogs) == 0 {
		t.Errorf("no execution logs found")
		return
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
}

func TestReturnErrorWhenMissingEntrypoint(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
		},
		{
			Id:   "notification1",
			Name: "httpnode",
		},
		{
			Id:   "rest1",
			Name: "httpnode",
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: "foo",
			Target: "notification1",
		},
		{
			Id:     "e2",
			Source: "notification1",
			Target: "rest1",
		},
		{
			Id:     "e3",
			Source: "rest1",
			Target: "branch1",
		},
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000",
					TokenName:        "TestToken",
					TokenSymbol:      "TEST",
					TokenDecimals:    18,
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					BlockNumber:      7212417,
					BlockTimestamp:   1625097600000,
					FromAddress:      "0x0000000000000000000000000000000000000000",
					ToAddress:        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					ValueFormatted:   "1.5",
					TransactionIndex: 0,
					LogIndex:         98,
				},
			},
		},
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "sampletaskid1",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, triggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	err = vm.Compile()
	if err == nil || (!strings.Contains(err.Error(), "invalid entrypoint") && !strings.Contains(err.Error(), "source node 'foo' in edge")) {
		t.Errorf("Expect return error due to invalid data, got: %v", err)
	}
}

func TestParseEntrypointRegardlessOfOrdering(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
		},
		{
			Id:   "notification1",
			Name: "httpnode",
		},
		{
			Id:   "rest1",
			Name: "httpnode",
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
			Target: "notification1",
		},
		{
			Id:     "e2",
			Source: "notification1",
			Target: "rest1",
		},
		{
			Id:     "e3",
			Source: "rest1",
			Target: "branch1",
		},
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000",
					TokenName:        "TestToken",
					TokenSymbol:      "TEST",
					TokenDecimals:    18,
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					BlockNumber:      7212417,
					BlockTimestamp:   1625097600000,
					FromAddress:      "0x0000000000000000000000000000000000000000",
					ToAddress:        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					ValueFormatted:   "1.5",
					TransactionIndex: 0,
					LogIndex:         98,
				},
			},
		},
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "sampletaskid1",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, triggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	err = vm.Compile()
	if err != nil {
		t.Errorf("Expect compile successfully but got error: %v", err)
	}

	if vm.entrypoint != "notification1" {
		t.Errorf("expect entrypoint is notification1 but got %v", vm.entrypoint)
	}
}
