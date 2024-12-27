package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestRunJavaScript(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: "3>2",
	}
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123abc",
			Name: "customJs",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: node,
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData("123abc", nil, nodes, edges)
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

	if step.OutputData != "true" {
		t.Errorf("wrong result, expect true got %s", step.OutputData)
	}

}

func TestRunJavaScriptComplex(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: "const a=[1,2,3]; a.filter((i) => i >= 2);",
	}
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123abc",
			Name: "customJs",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: node,
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "__TRIGGER__",
			Target: "123abc",
		},
	}

	vm, _ := NewVMWithData("123abc", nil, nodes, edges)
	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	if step.OutputData != "[2,3]" {
		t.Errorf("wrong JS code evaluation result, expect [2,3] got %s", step.OutputData)
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
// 	fmt.Println("error", err, step.OutputData)
//
// 	if step.OutputData != "[2,3]" {
// 		t.Errorf("wrong JS code evaluation result, expect [2,3] got %s", step.OutputData)
// 	}
// }
