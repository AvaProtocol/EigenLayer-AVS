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

func TestRunJavaScript(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: "return 3>2",
	}
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "customJs",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: node,
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
			Target: "123abc",
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

	n := NewJSProcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected JavaScript node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected JavaScript node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Start execute user-input JS code at") {
		t.Errorf("expected log contains trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	if gow.AnyToBool(step.GetCustomCode().Data) != true {
		t.Errorf("wrong result, expect true got %s", step.OutputData)
	}

}

func TestRunJavaScriptComplex(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: "const a=[1,2,3]; return a.filter((i) => i >= 2);",
	}
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "customJs",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: node,
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
			Target: "123abc",
		},
	}

	vm, _ := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	output := gow.AnyToSlice(step.GetCustomCode().Data)
	got := []float64{output[0].(float64), output[1].(float64)}
	expect := []float64{2, 3}

	if reflect.DeepEqual(got, expect) != true {
		t.Errorf("wrong JS code evaluation result, expect [2,3] got %s", step.OutputData)
	}
}

func TestRunJavaScriptComplexWithMap(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: `
			const a=[{name: 'alice', age: 10}, {name: 'bob', age: 12}];
			return a.filter((i) => i.age >= 12).map((i) => { return { name: i.name, age: i.age + 3} });
			`,
	}
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "customJs",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: node,
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
			Target: "123abc",
		},
	}

	vm, _ := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	output := gow.AnyToSlice(step.GetCustomCode().Data)
	if len(output) != 1 {
		t.Errorf("expect a single element return form javascript epression but got: %d", len(output))
	}
	got := output[0].(map[string]interface{})
	if got["name"].(string) != "bob" {
		t.Errorf("expect return bob but got: %s", got["name"])
	}

	if got["age"].(float64) != 15 {
		t.Errorf("expect return age 15 but got: %s", got["age"])
	}
}

// Temp disable until we figured out the event loop
// func TestRunJavaScriptHTTP(t *testing.T) {
// 	node := &avsproto.CustomCodeNode{
// 		Source: `
// 		toBigInt("1234442")
// 		`,
// 	}
// 	nodes := []*avsproto.TaskNode{
// 		&avsproto.TaskNode{
// 			Id:   "123abc",
// 			Name: "customJs",
// 			TaskType: &avsproto.TaskNode_CustomCode{
// 				CustomCode: node,
// 			},
// 		},
// 	}
//
// 	edges := []*avsproto.TaskEdge{
// 		&avsproto.TaskEdge{
// 			Id:     "e1",
// 			Source: "__TRIGGER__",
// 			Target: "123abc",
// 		},
// 	}
//
// 	vm, _ := NewVMWithData("123abc", nil, nodes, edges)
// 	n := NewJSProcessor(vm)
//
// 	step, err := n.Execute("123abc", node)
//
// 	if step.OutputData != "[2,3]" {
// 		t.Errorf("wrong JS code evaluation result, expect [2,3] got %s", step.OutputData)
// 	}
// }

func TestRunJavaScriptCanAccessSecretsWithapContext(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: "return 'my name is ' + apContext.configVars.my_awesome_secret",
	}
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "customJs",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: node,
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
			Target: "123abc",
		},
	}

	vm, _ := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), testutil.GetTestSecrets())

	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	if gow.AnyToString(step.GetCustomCode().Data) != "my name is my_awesome_secret_value" {
		t.Errorf("wrong JS code evaluation result, expect: `\"my name is my_awesome_secret_value\"`,  got `%s`", step.OutputData)
	}
}
