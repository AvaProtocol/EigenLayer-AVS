package taskengine

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// Helper functions for structpb.Value assertions

func assertStructpbValueIsBool(t *testing.T, val *structpb.Value, expectedBool bool, msgAndArgs ...interface{}) {
	t.Helper()
	if val == nil {
		t.Fatalf("assertStructpbValueIsBool: value is nil. Expected bool %v. %s", expectedBool, fmt.Sprint(msgAndArgs...))
	}

	var actualBool bool
	validKind := false
	switch kind := val.GetKind().(type) {
	case *structpb.Value_BoolValue:
		actualBool = kind.BoolValue
		validKind = true
	case *structpb.Value_NumberValue: // JavaScript `true` might come as number 1
		actualBool = (kind.NumberValue != 0)
		validKind = true
	case *structpb.Value_StringValue: // JavaScript "true" might come as string
		actualBool = (strings.ToLower(kind.StringValue) == "true")
		validKind = true
	}

	if !validKind {
		t.Fatalf("assertStructpbValueIsBool: Expected bool (or convertible type), got %T: %v. %s", val.GetKind(), val.String(), fmt.Sprint(msgAndArgs...))
	}

	if actualBool != expectedBool {
		t.Errorf("assertStructpbValueIsBool: expected %v, got %v. Original value: %s. %s", expectedBool, actualBool, val.String(), fmt.Sprint(msgAndArgs...))
	}
}

func assertStructpbValueIsFloat64Slice(t *testing.T, val *structpb.Value, expectedSlice []float64, msgAndArgs ...interface{}) {
	t.Helper()
	if val == nil {
		t.Fatalf("assertStructpbValueIsFloat64Slice: value is nil. Expected slice %v. %s", expectedSlice, fmt.Sprint(msgAndArgs...))
	}
	listValue, ok := val.GetKind().(*structpb.Value_ListValue)
	if !ok {
		t.Fatalf("assertStructpbValueIsFloat64Slice: expected list, got %T. %s", val.GetKind(), fmt.Sprint(msgAndArgs...))
	}

	values := listValue.ListValue.GetValues()
	if len(values) != len(expectedSlice) {
		t.Fatalf("assertStructpbValueIsFloat64Slice: expected list of %d values, got %d. %s", len(expectedSlice), len(values), fmt.Sprint(msgAndArgs...))
	}

	var got []float64
	for i, v := range values {
		numVal, okNum := v.GetKind().(*structpb.Value_NumberValue)
		if !okNum {
			t.Fatalf("assertStructpbValueIsFloat64Slice: expected list item %d to be number, got %T for value %v. %s", i, v.GetKind(), v, fmt.Sprint(msgAndArgs...))
		}
		got = append(got, numVal.NumberValue)
	}

	if !reflect.DeepEqual(got, expectedSlice) {
		t.Errorf("assertStructpbValueIsFloat64Slice: expected %v, got %v. Original output: %s. %s", expectedSlice, got, val.String(), fmt.Sprint(msgAndArgs...))
	}
}

func assertStructpbValueIsString(t *testing.T, val *structpb.Value, expectedString string, msgAndArgs ...interface{}) {
	t.Helper()
	if val == nil {
		t.Fatalf("assertStructpbValueIsString: value is nil. Expected string %q. %s", expectedString, fmt.Sprint(msgAndArgs...))
	}
	stringValue, ok := val.GetKind().(*structpb.Value_StringValue)
	if !ok {
		t.Fatalf("assertStructpbValueIsString: expected string, got %T. %s", val.GetKind(), fmt.Sprint(msgAndArgs...))
	}
	if stringValue.StringValue != expectedString {
		t.Errorf("assertStructpbValueIsString: expected %q, got %q. Original output: %s. %s", expectedString, stringValue.StringValue, val.String(), fmt.Sprint(msgAndArgs...))
	}
}

func TestRunJavaScript(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return 3>2",
		},
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
		Task: &avsproto.Task{
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
		t.Errorf("expected JavaScript node run successfully but failed")
	}

	if !strings.Contains(step.Log, "Execute Custom Code:") {
		t.Errorf("expected log contains trace data but found no. Actual log: %s", step.Log)
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	customCodeOutput := step.GetCustomCode().Data
	assertStructpbValueIsBool(t, customCodeOutput, true, "TestRunJavaScript: boolean check failed")
}

func TestRunJavaScriptComplex(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "const a=[1,2,3]; return a.filter((i) => i >= 2);",
		},
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
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	customCodeOutput := step.GetCustomCode().Data
	assertStructpbValueIsFloat64Slice(t, customCodeOutput, []float64{2, 3}, "TestRunJavaScriptComplex: slice check failed")
}

func TestRunJavaScriptComplexWithMap(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang: avsproto.Lang_JavaScript,
			Source: `
			const a=[{name: 'alice', age: 10}, {name: 'bob', age: 12}];
			return a.filter((i) => i.age >= 12).map((i) => { return { name: i.name, age: i.age + 3} });
			`,
		},
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
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	customCodeOutput := step.GetCustomCode().Data
	if customCodeOutput == nil {
		t.Fatalf("Custom code output data is nil for TestRunJavaScriptComplexWithMap")
	}
	listValue, ok := customCodeOutput.GetKind().(*structpb.Value_ListValue)
	if !ok {
		t.Fatalf("Expected custom code output to be a list, got %T", customCodeOutput.GetKind())
	}

	values := listValue.ListValue.GetValues()
	if len(values) != 1 {
		t.Fatalf("expect a single element return from javascript expression but got: %d", len(values))
	}

	structVal, okStruct := values[0].GetKind().(*structpb.Value_StructValue)
	if !okStruct {
		t.Fatalf("Expected list item to be a struct (map), got %T", values[0].GetKind())
	}

	fields := structVal.StructValue.GetFields()

	nameFieldVal, nameExists := fields["name"]
	if !nameExists {
		t.Fatalf("Expected 'name' field in struct")
	}
	assertStructpbValueIsString(t, nameFieldVal, "bob", "TestRunJavaScriptComplexWithMap: name check failed")

	ageFieldVal, ageExists := fields["age"]
	if !ageExists {
		t.Fatalf("Expected 'age' field in struct")
	}
	ageNumVal, okAgeNum := ageFieldVal.GetKind().(*structpb.Value_NumberValue)
	if !okAgeNum {
		t.Fatalf("Expected 'age' field to be a number, got %T for value %v", ageFieldVal.GetKind(), ageFieldVal)
	}
	if ageNumVal.NumberValue != 15 {
		t.Errorf("Expected age to be 15, got %f. Original output: %s", ageNumVal.NumberValue, customCodeOutput.String())
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
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return 'my name is ' + apContext.configVars.my_awesome_secret",
		},
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
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), testutil.GetTestSecrets())

	n := NewJSProcessor(vm)

	step, _ := n.Execute("123abc", node)

	customCodeOutput := step.GetCustomCode().Data
	assertStructpbValueIsString(t, customCodeOutput, "my name is my_awesome_secret_value", "TestRunJavaScriptCanAccessSecretsWithapContext: string check failed")
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
		function execute() {
			return { message: "test", id: 123 };
		};
		return execute();
	`

	nodes := []*avsproto.TaskNode{
		{
			Id:   customCodeNodeID,
			Name: "customCodeObjectNode",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Lang:   avsproto.Lang_JavaScript,
						Source: jsObjectSource,
					},
				},
			},
		},
		{
			Id:   restNodeID,
			Name: "restNodeUsingObject",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    server.URL, // Point to mock server
						Method: "POST",
						Body:   `{"output_from_js": "{{ customCodeObjectNode.data }}"}`,
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:          triggerID,
		Name:        "Template Trigger",
		TriggerType: &avsproto.TaskTrigger_Manual{},
	}

	edges := []*avsproto.TaskEdge{
		{Id: "e1-template", Source: trigger.Id, Target: customCodeNodeID},
		{Id: "e2-template", Source: customCodeNodeID, Target: restNodeID},
	}

	taskModel := &model.Task{
		Task: &avsproto.Task{
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

	if capturedBody != "{\"output_from_js\": \"[object Object]\"}" {
		t.Errorf("expected output_from_js to be [object Object] but got %q", capturedBody)
	}

	// Optional: Verify logs for completeness
	step1Log := vm.ExecutionLogs[0]
	if step1Log.Id != customCodeNodeID {
		t.Errorf("Step 1 log Id mismatch: got %q, want %q", step1Log.Id, customCodeNodeID)
	}
	if !step1Log.Success {
		t.Errorf("Step 1 log should be successful")
	}

	step2Log := vm.ExecutionLogs[1]
	if step2Log.Id != restNodeID {
		t.Errorf("Step 2 log Id mismatch: got %q, want %q", step2Log.Id, restNodeID)
	}
	if !step2Log.Success {
		t.Errorf("REST node step log should be successful")
	}
	if step2Log.Error != "" {
		t.Errorf("REST node step log error should be empty, got %q", step2Log.Error)
	}
}
