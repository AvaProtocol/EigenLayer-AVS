package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type MockTokenPrice struct {
	name string  `json:"name"`
	cost float64 `json:"cost"`
}

func TestFilter(t *testing.T) {
	node := &avsproto.FilterNode{
		Expression: "value.cost > 5",
		Input:      "trades",
	}

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "abc123",
			Name: "filter",
			TaskType: &avsproto.TaskNode_Filter{
				Filter: node,
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
			Target: "abc123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialize succesully but failed with error: %v", err)
	}
	vm.vars["trades"] = []map[string]interface{}{
		map[string]interface{}{
			"cost": 12,
			"name": "abc",
		},
		map[string]interface{}{
			"cost": 2,
			"name": "def",
		},
	}
	n := NewFilterProcessor(vm)
	step, err := n.Execute("abc123", node)
	varname := vm.GetNodeNameAsVar("abc123")
	data := vm.vars[varname].(map[string]any)["data"].([]any)

	if len(data) != 1 {
		t.Errorf("expect return only one element with cost > 5 but got 0")
	}

	name := data[0].(map[string]interface{})["name"].(string)
	if name != "abc" {
		t.Errorf("expect return only one element with cost > 5 but got: %s", data[0])
	}

	if !strings.Contains(step.Log, "start filter input trades with expression value.cost > 5") {
		t.Errorf("log doesn't contain execution info")
	}
}

func TestFilterComplexLogic(t *testing.T) {
	node := &avsproto.FilterNode{
		Expression: "if (index<=2) { return value.cost > 13; } else { return value.cost < 21; }",
		Input:      "trades",
	}

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "abc123",
			Name: "filter",
			TaskType: &avsproto.TaskNode_Filter{
				Filter: node,
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
			Target: "abc123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialize succesully but failed with error: %v", err)
	}
	vm.vars["trades"] = []map[string]interface{}{
		map[string]interface{}{
			"cost": 12,
			"name": "first",
		},
		map[string]interface{}{
			"cost": 15,
			"name": "second",
		},
		map[string]interface{}{
			"cost": 20,
			"name": "third",
		},
		map[string]interface{}{
			"cost": 25,
			"name": "fourth",
		},
		map[string]interface{}{
			"cost": 19,
			"name": "sixth",
		},
	}
	n := NewFilterProcessor(vm)
	step, err := n.Execute("abc123", node)

	varname := vm.GetNodeNameAsVar("abc123")
	data := vm.vars[varname].(map[string]any)["data"].([]any)
	if len(data) != 3 {
		t.Errorf("expect return only 3 element but got %v", len(data))
	}

	name1 := data[0].(map[string]interface{})["name"].(string)
	name2 := data[1].(map[string]interface{})["name"].(string)
	name3 := data[2].(map[string]interface{})["name"].(string)
	if name1 != "second" {
		t.Errorf("expect first elemt is second but got: %s", name1)
	}
	if name2 != "third" {
		t.Errorf("expect second elemt is third but got: %s", name2)
	}
	if name3 != "sixth" {
		t.Errorf("expect third elemt is fourth  but got: %s", name3)
	}

	if !strings.Contains(step.OutputData, "second") {
		t.Errorf("invalid finaly render output data")
	}
}
