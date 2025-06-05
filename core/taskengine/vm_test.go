package taskengine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dop251/goja"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestVMCompile(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://webhook.site/15431497-2b59-4000-97ee-245fef272967",
						Method: "POST",
						Body:   "a=123",
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
	if len(vm.plans) < 1 {
		t.Errorf("Expect steps is populated, got nil")
	}
}

func TestRunSimpleTasks(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	}))
	defer ts.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    ts.URL,
						Method: "POST",
						Body:   `{"name":"Alice"}`,
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
	tempHttpNode, exists := vm.vars["httpnode"]
	vm.mu.Unlock()

	if !exists {
		t.Errorf("httpnode variable not found in VM vars")
		return
	}

	if tempHttpNode == nil {
		t.Errorf("httpnode variable is nil")
		return
	}

	// The REST API response is nested under a "data" field
	responseData, ok := tempHttpNode.(map[string]any)
	if !ok {
		t.Errorf("httpnode is not map[string]any, got %T: %v", tempHttpNode, tempHttpNode)
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

	// The test server echoes back the JSON request body
	bodyMap, ok := body.(map[string]any)
	if !ok {
		t.Errorf("body is not map[string]any, got %T: %v", body, body)
		return
	}

	if bodyMap["name"].(string) != "Alice" {
		t.Errorf("step result isn't store properly, expect Alice got %s", bodyMap["name"])
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
						Url:    "https://httpbin.org/post",
						Method: "POST",
						Body:   "post123",
					},
				},
			},
		},
		{
			Id:   "456",
			Name: "graphql",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://httpbin.org/get?query123=abc",
						Method: "GET",
						Headers: map[string]string{
							"content-type": "application/json",
						},
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

	if len(vm.plans) < 2 {
		t.Errorf("incorrect generated plan")
	}

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected run ok, got error %v", err)
	}

	if len(vm.ExecutionLogs) < 2 {
		t.Errorf("Missing an execution")
	}

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Executing REST API Node ID") || !strings.Contains(vm.ExecutionLogs[1].Log, "Executing REST API Node ID") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got: %s and %s", vm.ExecutionLogs[0].Log, vm.ExecutionLogs[1].Log)
	}

	if !vm.ExecutionLogs[0].Success || !vm.ExecutionLogs[1].Success {
		t.Errorf("incorrect success status, expect all success but got failure")
	}

	if vm.ExecutionLogs[0].Id != "123" || vm.ExecutionLogs[1].Id != "456" {
		t.Errorf("incorrect node id in execution log")
	}

	outputData := gow.ValueToMap(vm.ExecutionLogs[0].GetRestApi().Data)
	if body, ok := outputData["body"].(map[string]any); ok {
		if data, ok := body["data"].(string); ok && data != "post123" {
			t.Errorf("rest node result is incorrect, should contains the string post123, got: %v", data)
		}
	} else {
		t.Errorf("rest node result is incorrect, body should be a map, got: %T %v", outputData["body"], outputData["body"])
	}

	outputData = gow.ValueToMap(vm.ExecutionLogs[1].GetRestApi().Data)
	if body, ok := outputData["body"].(map[string]any); ok {
		if args, ok := body["args"].(map[string]any); ok {
			if query123, ok := args["query123"].(string); ok && query123 != "abc" {
				t.Errorf("rest node result is incorrect, should contains the string query123=abc, got: %v", query123)
			}
		} else {
			t.Errorf("rest node result is incorrect, args should be a map, got: %T %v", body["args"], body["args"])
		}
	} else {
		t.Errorf("rest node result is incorrect, body should be a map, got: %T %v", outputData["body"], outputData["body"])
	}
}

func TestRunTaskWithBranchNode(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/post" {
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": string(body),
			})
			return
		}

		if r.Method == "GET" && r.URL.Path == "/get" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"args": map[string]interface{}{
					"hit": r.URL.Query().Get("hit"),
				},
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "a1",
								Type:       "if",
								Expression: "a >= 5",
							},
							{
								Id:   "a2",
								Type: "else",
							},
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    mockServer.URL + "/post",
						Method: "POST",
						Body:   "hit=notification1",
					},
				},
			},
		},
		{
			Id:   "notification2",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    mockServer.URL + "/get?hit=notification2",
						Method: "GET",
						Headers: map[string]string{
							"content-type": "application/json",
						},
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
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
		{
			Id:     "e1",
			Source: "branch1.a2",
			Target: "notification2",
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

	vm.AddVar("a", 10)
	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	if len(vm.plans) != 5 {
		t.Errorf("Invalid plan generation. Expect 5 steps, got %d", len(vm.plans))
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

	vm.mu.Lock()
	outputVar, _ := vm.vars["httpnode"]
	vm.mu.Unlock()
	outputMap := outputVar.(map[string]any)
	actualData := outputMap["data"].(map[string]any)["body"].(map[string]any)["data"].(string)
	if actualData != `hit=notification1` {
		t.Errorf("expect executing notification1 and set output data to notification1, got: %s", actualData)
	}

	vm.Reset()

	// Now test the else branch
	vm.AddVar("a", 1)
	vm.Compile()
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
	vm.mu.Lock()
	outputVarElse, _ := vm.vars["httpnode"]
	vm.mu.Unlock()
	outputMapElse := outputVarElse.(map[string]any)["data"].(map[string]any)["body"].(map[string]any)
	if outputMapElse["args"].(map[string]any)["hit"].(string) != `notification2` {
		t.Errorf("expect executing notification2 to be run but it didn't run, got %v", outputMapElse)
	}
}

func TestRenderString(t *testing.T) {
	vm := goja.New()
	vm.Set("myTrigger", map[string]any{
		"data": map[string]any{
			"token_symbol": "0xtoken",
			"amount":       123456,
			"tx_hash":      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		},
	})
	vm.Set("target", "123")

	result, err := vm.RunString(`JSON.stringify({
      chat_id:-4609037622,
	  text: ` + "`Congrat, your walllet ${target} received ${myTrigger.data.amount} ${myTrigger.data.token_symbol} at [${myTrigger.data.tx_hash}](sepolia.etherscan.io/tx/${myTrigger.data.tx_hash}`" + `
	  })`)
	v := result.Export().(string)
	if err != nil || !strings.Contains(v, "123456") || !strings.Contains(v, "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8") {
		t.Errorf("text not render correctly")
	}
}

func TestEvaluateEvent(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "a1",
								Type:       "if",
								Expression: `triggertest.data.address == "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" && bigGt(toBigInt(triggertest.data.value), toBigInt("1200000"))`},
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://httpbin.org/post",
						Method: "POST",
						Body:   "hit=notification1",
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
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			// Use oneof structure with TransferLog for enriched transfer data
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000", // Greater than 1200000 to make condition true
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

	if len(vm.ExecutionLogs) == 0 {
		t.Errorf("no execution logs found")
		return
	}

	// Look for branch execution log
	var branchLog *avsproto.Execution_Step
	for _, log := range vm.ExecutionLogs {
		if log.GetBranch() != nil {
			branchLog = log
			break
		}
	}

	if branchLog == nil {
		t.Errorf("no branch execution log found, logs: %v", vm.ExecutionLogs)
		return
	}

	if branchLog.GetBranch().ConditionId != "branch1.a1" {
		t.Errorf("expression evaluate incorrect, got: %s", branchLog.GetBranch().ConditionId)
	}
}

func TestEvaluateEventEvmLog(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "a1",
								Type:       "if",
								Expression: `triggertest.data.address == "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" && triggertest.data.block_number > 7000000`},
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://httpbin.org/post",
						Method: "POST",
						Body:   "hit=notification1",
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
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			// Use oneof structure with EvmLog for raw event data
			OutputType: &avsproto.EventTrigger_Output_EvmLog{
				EvmLog: &avsproto.Evm_Log{
					BlockNumber:      7212417, // Greater than 7000000 to make condition true
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					Index:            98,
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Topics:           []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x000000000000000000000000d8da6bf26964af9d7eed9e03e53415d37aa96045", "0x0000000000000000000000001c7d4b196cb0c7b01d743fbc6116a902379c7238"},
					Data:             "0x0000000000000000000000000000000000000000000000000000000000200000",
					TransactionIndex: 42,
					BlockHash:        "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					Removed:          false,
				},
			},
		},
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "sampletaskid2",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, triggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	// Verify the oneof structure is correctly set up
	eventOutput := triggerData.Output.(*avsproto.EventTrigger_Output)
	evmLog := eventOutput.GetEvmLog()
	if evmLog == nil {
		t.Errorf("Expected EvmLog to be populated in oneof structure")
		return
	}

	transferLog := eventOutput.GetTransferLog()
	if transferLog != nil {
		t.Errorf("Expected TransferLog to be nil when EvmLog is used in oneof structure")
		return
	}

	// Verify EvmLog fields are accessible
	if evmLog.Address != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" {
		t.Errorf("Expected EvmLog address to be accessible, got: %s", evmLog.Address)
		return
	}

	if evmLog.BlockNumber != 7212417 {
		t.Errorf("Expected EvmLog block number to be accessible, got: %d", evmLog.BlockNumber)
		return
	}

	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
		return
	}

	if len(vm.ExecutionLogs) == 0 {
		t.Errorf("no execution logs found")
		return
	}

	// Look for branch execution log
	var branchLog *avsproto.Execution_Step
	for _, log := range vm.ExecutionLogs {
		if log.GetBranch() != nil {
			branchLog = log
			break
		}
	}

	if branchLog == nil {
		t.Errorf("no branch execution log found, logs: %v", vm.ExecutionLogs)
		return
	}

	if branchLog.GetBranch().ConditionId != "branch1.a1" {
		t.Errorf("expression evaluate incorrect for EvmLog branch, got: %s", branchLog.GetBranch().ConditionId)
	}
}

func TestEventTriggerOneofExclusivity(t *testing.T) {
	t.Run("EvmLog branch populated, TransferLog nil", func(t *testing.T) {
		eventOutput := &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_EvmLog{
				EvmLog: &avsproto.Evm_Log{
					BlockNumber: 7212417,
					Address:     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Topics:      []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
					Data:        "0x0000000000000000000000000000000000000000000000000000000000200000",
				},
			},
		}

		// Verify EvmLog is populated
		evmLog := eventOutput.GetEvmLog()
		if evmLog == nil {
			t.Errorf("Expected EvmLog to be populated")
		}
		if evmLog.Address != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" {
			t.Errorf("Expected EvmLog address to match, got: %s", evmLog.Address)
		}

		// Verify TransferLog is nil (oneof exclusivity)
		transferLog := eventOutput.GetTransferLog()
		if transferLog != nil {
			t.Errorf("Expected TransferLog to be nil when EvmLog is set")
		}
	})

	t.Run("TransferLog branch populated, EvmLog nil", func(t *testing.T) {
		eventOutput := &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:       "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:         "1500000",
					TokenName:     "TestToken",
					TokenSymbol:   "TEST",
					TokenDecimals: 18,
				},
			},
		}

		// Verify TransferLog is populated
		transferLog := eventOutput.GetTransferLog()
		if transferLog == nil {
			t.Errorf("Expected TransferLog to be populated")
		}
		if transferLog.Address != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" {
			t.Errorf("Expected TransferLog address to match, got: %s", transferLog.Address)
		}
		if transferLog.TokenName != "TestToken" {
			t.Errorf("Expected TransferLog token name to match, got: %s", transferLog.TokenName)
		}

		// Verify EvmLog is nil (oneof exclusivity)
		evmLog := eventOutput.GetEvmLog()
		if evmLog != nil {
			t.Errorf("Expected EvmLog to be nil when TransferLog is set")
		}
	})

	t.Run("Empty output returns nil for both", func(t *testing.T) {
		eventOutput := &avsproto.EventTrigger_Output{}

		// Both should return nil when no oneof branch is set
		evmLog := eventOutput.GetEvmLog()
		if evmLog != nil {
			t.Errorf("Expected EvmLog to be nil when no oneof branch is set")
		}

		transferLog := eventOutput.GetTransferLog()
		if transferLog != nil {
			t.Errorf("Expected TransferLog to be nil when no oneof branch is set")
		}
	})
}

func TestReturnErrorWhenMissingEntrypoint(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "a1",
								Type:       "if",
								Expression: `triggertest.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" && bigGt(toBigInt(triggertest.data.data), toBigInt("1200000"))`},
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://httpbin.org/post",
						Method: "POST",
						Body:   "hit=notification1",
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
			Source: "foo",
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			// Use oneof structure with TransferLog for enriched transfer data
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000", // Greater than 1200000 to make condition true
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
			// Use oneof structure with TransferLog for enriched transfer data
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000", // Greater than 1200000 to make condition true
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

func TestRunTaskWithCustomUserSecret(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		response := map[string]interface{}{
			"data": string(body),
			"args": map[string]interface{}{
				"apikey": r.URL.Query().Get("apikey"),
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    ts.URL + "?apikey={{apContext.configVars.apikey}}",
						Method: "POST",
						Body:   "my key is {{apContext.configVars.apikey}} in body",
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
	}, nil, testutil.GetTestSmartWalletConfig(), map[string]string{
		"apikey": "secretapikey",
	})

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

	// The REST API response structure: {data: {statusCode, status, headers, body}}
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

	if bodyMap["data"].(string) != "my key is secretapikey in body" {
		t.Errorf("secret doesn't render correctly in body, expect secretapikey but got %s", bodyMap["data"])
	}

	if bodyMap["args"].(map[string]interface{})["apikey"].(string) != "secretapikey" {
		t.Errorf("secret doesn't render correctly in uri, expect secretapikey but got %s", bodyMap["args"].(map[string]interface{})["apikey"])
	}
}

func TestPreprocessText(t *testing.T) {
	// Setup a VM with some test variables
	vm := NewVM()
	vm.AddVar("user", map[string]any{
		"data": map[string]any{
			"name":    "Alice",
			"balance": 100,
			"items":   []string{"apple", "banana"},
			"address": "0x123",
			"active":  true,
		},
	})
	vm.AddVar("token", map[string]any{
		"data": map[string]any{
			"symbol":  "ETH",
			"decimal": 18,
			"address": "0x123",
			"pairs": []map[string]any{
				{"symbol": "ETH/USD", "price": 2000},
				{"symbol": "ETH/EUR", "price": 1800},
			},
		},
	})
	vm.AddVar(APContextVarName, map[string]map[string]string{
		ConfigVarsPath: {
			"my_awesome_secret": "my_awesome_secret_value",
		},
	})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple variable",
			input:    "Hello {{ user.data.name }}!",
			expected: "Hello Alice!",
		},
		{
			name:     "multiple variables",
			input:    "{{ user.data.name }} has {{ user.data.balance }} {{ token.data.symbol }}",
			expected: "Alice has 100 ETH",
		},
		{
			name:     "apContext variable",
			input:    "my secret is {{ apContext.configVars.my_awesome_secret}}",
			expected: "my secret is my_awesome_secret_value",
		},
		{
			name:     "invalid syntax - unclosed",
			input:    "Hello {{ user.data.name !",
			expected: "Hello {{ user.data.name !",
		},
		{
			name:     "invalid syntax - nested braces",
			input:    "Hello {{ user.data{{ .name }} }}",
			expected: "Hello  }}",
		},
		{
			name:     "multiple nested braces",
			input:    "Hello {{ user.data.name }} {{ {{ token.data.symbol }} }}",
			expected: "Hello Alice  }}",
		},
		{
			name:     "expression with calculation",
			input:    "Total: {{ user.data.balance * 2 }} {{ token.data.symbol }}",
			expected: "Total: 200 ETH",
		},
		{
			name:     "text without expressions",
			input:    "Hello World!",
			expected: "Hello World!",
		},
		{
			name:     "empty expression",
			input:    "Hello {{  }} World",
			expected: "Hello  World",
		},
		{
			name:     "multiple expressions in one line",
			input:    "{{ user.data.name }} owns {{ token.data.symbol }} at {{ token.data.address }}",
			expected: "Alice owns ETH at 0x123",
		},
		{
			name:     "javascript expression - array access",
			input:    "First item: {{ user.data.items[0] }}",
			expected: "First item: apple",
		},
		{
			name:     "javascript expression - string manipulation",
			input:    "Address: {{ user.data.address.toLowerCase() }}",
			expected: "Address: 0x123",
		},
		{
			name:     "javascript expression - conditional",
			input:    "Status: {{ user.data.active ? 'Online' : 'Offline' }}",
			expected: "Status: Online",
		},
		{
			name:     "javascript expression - template literal",
			input:    "{{ `${user.data.name}'s balance is ${user.data.balance}` }}",
			expected: "Alice's balance is 100",
		},
		{
			name:     "complex object access",
			input:    "ETH/USD Price: {{ token.data.pairs[0].price }}",
			expected: "ETH/USD Price: 2000",
		},
		{
			name:     "multiple nested properties",
			input:    "{{ token.data.pairs[0].symbol }} at {{ token.data.pairs[0].price }}",
			expected: "ETH/USD at 2000",
		},
		{
			name:     "invalid property access",
			input:    "{{ user.data.nonexistent.property }}",
			expected: "",
		},
		{
			name:     "invalid method call",
			input:    "{{ user.data.name.nonexistentMethod() }}",
			expected: "",
		},
		{
			name:     "mixed valid and invalid expressions",
			input:    "{{ user.data.name }} has {{ user.data.nonexistent }} {{ token.data.symbol }}",
			expected: "Alice has <nil> ETH",
		},
		{
			name:     "javascript expression - arithmetic",
			input:    "Total in USD: {{ token.data.pairs[0].price * user.data.balance }}",
			expected: "Total in USD: 200000",
		},
		{
			name:     "expression with spaces and newlines",
			input:    "{{ \n  user.data.name  \n }}",
			expected: "Alice",
		},
		{
			name:     "expression with comments",
			input:    "{{ /* comment */ user.data.name }}",
			expected: "Alice", // JavaScript comments in expressions are not supported
		},
		{
			name:     "max iterations test",
			input:    strings.Repeat("{{ user.data.name }}", VMMaxPreprocessIterations+1),
			expected: strings.Repeat("Alice", VMMaxPreprocessIterations) + "{{ user.data.name }}",
		},
		{
			name:     "javascript object representation value",
			input:    `{{ {a: 1, b: 2} }}`,
			expected: "[object Object]",
		},
		{
			name:     "javascript object representation var",
			input:    `{{ user }}`,
			expected: "[object Object]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vm.preprocessText(tt.input)
			if result != tt.expected {
				t.Errorf("preprocessText(%q) = got %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestPreprocessTextDate(t *testing.T) {
	// Setup a VM
	vm := NewVM()
	// Date tests don't usually need complex global vars,
	// NewVM already initializes vm.vars as an empty sync.Map

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "javascript expression - date with Z (UTC)",
			input:    `{{ new Date("2014-04-07T13:58:10.104Z")}}`,
			expected: "2014-04-07 13:58:10.104 +0000 UTC",
		},
		{
			name:     "javascript expression - date toISOString",
			input:    `{{ (new Date("2014-04-07T13:58:10.104Z")).toISOString() }}`,
			expected: "2014-04-07T13:58:10.104Z",
		},
		// NOTE: This test case is commented out because we decided not to manipulate timezone behavior
		// in the JavaScript environment. Date strings without timezone indicators (like "2014-04-07T13:58:10.104")
		// will be interpreted according to the Go runtime's timezone settings, which provides more predictable
		// behavior than trying to force UTC interpretation through complex JavaScript overrides.
		// The complexity of hardcoded timezone conversions was deemed not worth the maintenance burden.
		//
		// {
		// 	name: "javascript expression - date no Z (uses Go runtime timezone)",
		// 	input:    `{{ new Date("2014-04-07T13:58:10.104") }}`,
		// 	expected: "2014-04-07 13:58:10.104 +0000 UTC", // This would vary based on system timezone
		// },
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vm.preprocessText(tt.input)
			if result != tt.expected {
				t.Errorf("preprocessText(%q) = got %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMissingTriggerName(t *testing.T) {
	// Test case 1: Task with trigger but missing name
	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "", // Empty name should cause error
	}

	_, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123",
			Nodes:   []*avsproto.TaskNode{},
			Edges:   []*avsproto.TaskEdge{},
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err == nil {
		t.Errorf("Expected error for missing trigger name, but got nil")
	}
	if !strings.Contains(err.Error(), "trigger name is required") {
		t.Errorf("Expected error message about missing trigger name, got: %s", err.Error())
	}

	// Test case 2: Task with nil trigger should also cause error
	_, err = NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123",
			Nodes:   []*avsproto.TaskNode{},
			Edges:   []*avsproto.TaskEdge{},
			Trigger: nil, // Nil trigger should cause error
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err == nil {
		t.Errorf("Expected error for nil trigger, but got nil")
	}
	if !strings.Contains(err.Error(), "trigger is required") {
		t.Errorf("Expected error message about missing trigger, got: %s", err.Error())
	}

	// Test case 3: Nil task should work (for single node execution)
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, nil)
	if err != nil {
		t.Errorf("Expected nil task to work, but got error: %s", err.Error())
	}
	if vm == nil {
		t.Errorf("Expected VM to be created for nil task")
	}
}
