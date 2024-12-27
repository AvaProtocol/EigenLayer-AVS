package taskengine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/k0kubun/pp/v3"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestVMCompile(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://webhook.site/15431497-2b59-4000-97ee-245fef272967",
					Method: "POST",
					Body:   "a=123",
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "123",
		},
	}

	vm, err := NewVMWithData("123", nil, nodes, edges)
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
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "a=123",
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "123",
		},
	}

	vm, err := NewVMWithData("123", nil, nodes, edges)
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

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Execute") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got nothing")
	}

	data := vm.vars["httpnode"].(map[string]any)
	if data["data"].(string) != "a=123" {
		t.Errorf("step result isn't store properly, expect 123 got %s", data["data"])
	}
}

func TestRunSequentialTasks(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "post123",
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "456",
			Name: "graphql",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/get?query123",
					Method: "GET",
					Headers: map[string]string{
						"content-type": "application/json",
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "123",
		},
		&avsproto.TaskEdge{
			Id:     "e2",
			Source: "123",
			Target: "456",
		},
	}

	vm, err := NewVMWithData("123", nil, nodes, edges)
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

	pp.Print(vm.ExecutionLogs)

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Execute POST httpbin.org at") || !strings.Contains(vm.ExecutionLogs[1].Log, "Execute GET httpbin.org") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got nothing")
	}

	if !vm.ExecutionLogs[0].Success || !vm.ExecutionLogs[1].Success {
		t.Errorf("incorrect success status, expect all success but got failure")
	}

	if vm.ExecutionLogs[0].NodeId != "123" || vm.ExecutionLogs[1].NodeId != "456" {
		t.Errorf("incorrect node id in execution log")
	}

	if !strings.Contains(vm.ExecutionLogs[0].OutputData, "post123") {
		t.Errorf("rest node result is incorrect, should contains the string post123")
	}
	if !strings.Contains(vm.ExecutionLogs[1].OutputData, "query123") {
		t.Errorf("rest node result is incorrect, should contains the string query123")
	}
}

func TestRunTaskWithBranchNode(t *testing.T) {
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
						&avsproto.Condition{
							Id:   "a2",
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
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification2",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/get?hit=notification2",
					Method: "GET",
					Headers: map[string]string{
						"content-type": "application/json",
					},
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a2",
			Target: "notification2",
		},
	}

	vm, err := NewVMWithData("123", nil, nodes, edges)
	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.vars["a"] = 10
	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	pp.Print(vm.plans)

	if len(vm.plans) != 3 {
		t.Errorf("Invalid plan generation. Expect one step, got %d", len(vm.plans))
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
	pp.Print(vm.ExecutionLogs[0])
	pp.Print(vm.ExecutionLogs[1])
	fmt.Println(vm.ExecutionLogs[1].OutputData)
	if !strings.Contains(vm.ExecutionLogs[1].OutputData, `notification1`) {
		t.Errorf("expect executing notification1 step but not it didn't run")
	}

	vm.Reset()
	vm.vars["a"] = 1
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
	pp.Print(vm.ExecutionLogs[0])
	pp.Print(vm.ExecutionLogs[1])
	fmt.Println(vm.ExecutionLogs[1].OutputData)
	if !strings.Contains(vm.ExecutionLogs[1].OutputData, `notification2`) {
		t.Errorf("expect executing notification1 step but not it didn't run")
	}
}

func TestRenderString(t *testing.T) {
	vm := goja.New()
	vm.Set("trigger1", map[string]any{
		"data": map[string]any{
			"token_symbol": "0xtoken",
			"amount":       123456,
			"tx_hash":      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		},
	})
	vm.Set("target", "123")

	result, err := vm.RunString(`JSON.stringify({
      chat_id:-4609037622,
	  text: ` + "`Congrat, your walllet ${target} received ${trigger1.data.amount} ${trigger1.data.token_symbol} at [${trigger1.data.tx_hash}](sepolia.etherscan.io/tx/${trigger1.data.tx_hash}`" + `
	  })`)
	v := result.Export().(string)
	if err != nil || !strings.Contains(v, "123456") || !strings.Contains(v, "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8") {
		t.Errorf("text not render correctly")
	}
}

func TestEvaluateEvent(t *testing.T) {
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
							Expression: `trigger1.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" && bigGt(toBigInt(trigger1.data.data), toBigInt("1200000"))`},
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

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	mark := avsproto.TriggerMetadata{
		BlockNumber: 7212417,
		TxHash:      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		LogIndex:    98,
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData("sampletaskid1", &mark, nodes, edges)
	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	pp.Print(vm.plans)

	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
		return
	}

	pp.Print(vm.ExecutionLogs)
	if vm.ExecutionLogs[0].OutputData != "branch1.a1" {
		t.Errorf("expression evaluate incorrect")
	}
}
