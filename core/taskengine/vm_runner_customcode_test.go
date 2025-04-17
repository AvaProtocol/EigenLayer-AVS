package taskengine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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

// TestRunJavaScriptObjectInTemplate verifies that using an object result
// directly in templating results in Go's map string representation.
func TestRunJavaScriptObjectResultRendering(t *testing.T) {
	// --- Setup ---
	logger := testutil.GetLogger()
	db := testutil.TestMustDB()
	defer db.Close()

	smartWalletConfig := &config.SmartWalletConfig{
		// Populate if needed
	}

	// Mock HTTP Server to capture the request body
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Logf("Error reading request body in mock server: %v", err)
			http.Error(w, "Failed to read body", http.StatusInternalServerError)
			return
		}
		capturedBody = string(bodyBytes)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) // Simple response
	}))
	defer server.Close()

	// --- Define Task Components ---
	customCodeNodeID := "customCodeObjectNode"
	restNodeID := "restNodeUsingObject"
	triggerID := "triggerTemplate"

	jsObjectSource := `
		function execute(context) {
			return { message: "test", id: 123 };
		}
	`

	nodes := []*avsproto.TaskNode{
		{
			Id:   customCodeNodeID,
			Name: "customCodeObjectNode",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Lang:   avsproto.CustomCodeLang_JavaScript,
					Source: jsObjectSource,
				},
			},
		},
		{
			Id:   restNodeID,
			Name: "restNodeUsingObject",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    server.URL, // Point to mock server
					Method: "POST",
					Body:   `{"output_from_js": "{{ customCodeObjectNode.data }}"`,
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:          triggerID,
		Name:        "Template Trigger",
		TriggerType: &avsproto.TaskTrigger_Manual{Manual: true},
	}

	edges := []*avsproto.TaskEdge{
		{Id: "e1-template", Source: trigger.Id, Target: customCodeNodeID},
		{Id: "e2-template", Source: customCodeNodeID, Target: restNodeID},
	}

	taskModel := &model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	vm, err := NewVMWithData(taskModel, nil, smartWalletConfig, nil)
	if err != nil {
		t.Fatalf("NewVMWithData should not error: %v", err)
	}
	vm.WithLogger(logger).WithDb(db)

	//n := NewJSProcessor(vm)

	// --- Compile and Run ---
	err = vm.Compile()
	if err != nil {
		t.Fatalf("VM Compile should not error: %v", err)
	}
	if vm.entrypoint != customCodeNodeID {
		t.Errorf("VM entrypoint incorrect: got %q, want %q", vm.entrypoint, customCodeNodeID)
	}

	err = vm.Run()
	if err != nil {
		t.Fatalf("VM Run should not error: %v", err)
	}

	// --- Verification ---
	if len(vm.ExecutionLogs) != 2 {
		t.Fatalf("Expected 2 execution steps, got %d", len(vm.ExecutionLogs))
	}

	// Verify the request body captured by the mock server
	// It should contain Go's string representation of the map, not "[object Object]"
	// NOTE: The exact order of keys in the map string representation is not guaranteed by Go.
	// We check for the presence of key-value pairs instead of exact string match.
	expectedKeyVal1 := "id:123"
	expectedKeyVal2 := "message:test"
	if !((strings.Contains(capturedBody, expectedKeyVal1) && strings.Contains(capturedBody, expectedKeyVal2)) ||
		(strings.Contains(capturedBody, expectedKeyVal2) && strings.Contains(capturedBody, expectedKeyVal1))) {
		// A more robust check might involve parsing the JSON and the inner map string
		t.Errorf("Request body (%q) does not contain expected Go map key-value pairs like %q and %q", capturedBody, expectedKeyVal1, expectedKeyVal2)
	}

	if capturedBody != `{"output_from_js": [object Object] }` {
		t.Errorf("expected output_from_js to be [object Object] but got %q", capturedBody)
	}

	// Optional: Verify logs for completeness
	step1Log := vm.ExecutionLogs[0]
	if step1Log.NodeId != customCodeNodeID {
		t.Errorf("Step 1 log NodeId mismatch: got %q, want %q", step1Log.NodeId, customCodeNodeID)
	}
	if !step1Log.Success {
		t.Errorf("Step 1 log should be successful")
	}

	step2Log := vm.ExecutionLogs[1]
	if step2Log.NodeId != restNodeID {
		t.Errorf("Step 2 log NodeId mismatch: got %q, want %q", step2Log.NodeId, restNodeID)
	}
	if !step2Log.Success {
		t.Errorf("REST node step log should be successful")
	}
	if step2Log.Error != "" {
		t.Errorf("REST node step log error should be empty, got %q", step2Log.Error)
	}
}
