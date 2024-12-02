package taskengine

import (
	"log"
	"strings"
	"testing"

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

	vm, err := NewVMWithData("123", nodes, edges)
	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()
	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	if len(vm.steps) < 1 {
		t.Errorf("Expect steps is populated, got nil")
	}
}

func TestVMRunOneNodeTask(t *testing.T) {
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

	vm, err := NewVMWithData("123", nodes, edges)
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

	if !strings.Contains(vm.ExecutionLogs[0].Logs[0], "Request ") {
		t.Errorf("error generating log. expected including log line, got nothing")
	}
	log.Println(string(vm.ExecutionLogs[0].Result.([]byte)))
}
