package taskengine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type MockTokenPrice struct {
	Name string  `json:"name"`
	Cost float64 `json:"cost"`
}

func TestFilter(t *testing.T) {
	node := &avsproto.FilterNode{
		Config: &avsproto.FilterNode_Config{
			Expression:    "value.cost > 5",
			InputNodeName: "trades",
		},
	}

	nodes := []*avsproto.TaskNode{
		{
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
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "abc123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialize succesully but failed with error: %v", err)
	}
	vm.AddVar("trades", []map[string]interface{}{
		{
			"cost": 12,
			"name": "abc",
		},
		{
			"cost": 2,
			"name": "def",
		},
	})

	n := NewFilterProcessor(vm)
	step, err := n.Execute("abc123", node)
	if err != nil {
		t.Errorf("Filter execution failed with error: %v", err)
		return
	}

	varname := vm.GetNodeNameAsVar("abc123")
	vm.mu.Lock()
	tempData, _ := vm.vars[varname]
	vm.mu.Unlock()
	data := tempData.(map[string]any)["data"].([]any)

	if len(data) != 1 {
		t.Errorf("expect return only one element with cost > 5 but got 0")
	}

	name := data[0].(map[string]interface{})["name"].(string)
	if name != "abc" {
		t.Errorf("expect return only one element with cost > 5 but got: %s", data[0])
	}

	if !strings.Contains(step.Log, "Input node name: 'trades', Variable name: 'trades', Original Expression: 'value.cost > 5', Clean Expression: 'value.cost > 5'") {
		t.Errorf("log doesn't contain execution info")
	}
}

func TestFilterComplexLogic(t *testing.T) {
	node := &avsproto.FilterNode{
		Config: &avsproto.FilterNode_Config{
			Expression:    "if (index<=2) { return value.cost > 13; } else { return value.cost < 21; }",
			InputNodeName: "trades",
		},
	}

	nodes := []*avsproto.TaskNode{
		{
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
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "abc123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialize succesully but failed with error: %v", err)
	}
	vm.AddVar("trades", []map[string]interface{}{
		{
			"cost": 12,
			"name": "first",
		},
		{
			"cost": 15,
			"name": "second",
		},
		{
			"cost": 20,
			"name": "third",
		},
		{
			"cost": 25,
			"name": "fourth",
		},
		{
			"cost": 19,
			"name": "sixth",
		},
	})

	n := NewFilterProcessor(vm)
	step, err := n.Execute("abc123", node)
	if err != nil {
		t.Errorf("Filter execution failed with error: %v", err)
		return
	}

	varname := vm.GetNodeNameAsVar("abc123")
	vm.mu.Lock()
	tempData, exists := vm.vars[varname]
	vm.mu.Unlock()

	if !exists {
		t.Errorf("Variable %s does not exist", varname)
		return
	}

	dataMap, ok := tempData.(map[string]any)
	if !ok {
		t.Errorf("Variable %s is not a map[string]any, it's %T", varname, tempData)
		return
	}

	dataValue, exists := dataMap["data"]
	if !exists {
		t.Errorf("Variable %s does not have a 'data' key", varname)
		return
	}

	data, ok := dataValue.([]any)
	if !ok {
		t.Errorf("Data value is not []any, it's %T: %v", dataValue, dataValue)
		return
	}

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

	data2 := gow.AnyToSlice(step.GetFilter().Data)
	if !reflect.DeepEqual(
		[]float64{15, 20, 19},
		[]float64{
			data2[0].(map[string]any)["cost"].(float64),
			data2[1].(map[string]any)["cost"].(float64),
			data2[2].(map[string]any)["cost"].(float64),
		}) {
		t.Errorf("exepect cost array is 15, 20, 19 but got: %v", data2)
	}

	if !reflect.DeepEqual(
		[]string{"second", "third", "sixth"},
		[]string{
			data2[0].(map[string]any)["name"].(string),
			data2[1].(map[string]any)["name"].(string),
			data2[2].(map[string]any)["name"].(string),
		}) {
		t.Errorf("exepect name array is second, third, sixth but got: %v", data2)
	}
}
