package taskengine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/pkg/gow"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestRunTaskWithMultipleConditions(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		response := map[string]interface{}{
			"name": string(body),
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:         "condition1",
							Type:       "if",
							Expression: "a > 10",
						},
						&avsproto.Condition{
							Id:         "condition2",
							Type:       "if",
							Expression: "a > 5",
						},
						&avsproto.Condition{
							Id:   "condition3",
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
					Url:    ts.URL,
					Method: "POST",
					Body:   "hit=first_condition",
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification2",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    ts.URL,
					Method: "POST",
					Body:   "hit=second_condition",
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification3",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    ts.URL,
					Method: "POST",
					Body:   "hit=else_condition",
				},
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
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e2",
			Source: "branch1.condition1",
			Target: "notification1",
		},
		&avsproto.TaskEdge{
			Id:     "e3",
			Source: "branch1.condition2",
			Target: "notification2",
		},
		&avsproto.TaskEdge{
			Id:     "e4",
			Source: "branch1.condition3",
			Target: "notification3",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	// Set value to hit the second condition (a > 5 but not > 10)
	vm.vars["a"] = 7
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

	if vm.instructionCount != 2 {
		t.Errorf("incorrect steps, expect 2 got %d", vm.instructionCount)
	}
	if len(vm.ExecutionLogs) != 2 {
		t.Errorf("incorrect log, expect 2 got %d", len(vm.ExecutionLogs))
	}

	// Verify that the second condition was hit
	if !strings.Contains(vm.ExecutionLogs[0].GetBranch().ConditionId, "branch1.condition2") {
		t.Errorf("expected second condition to be hit, but got %s", vm.ExecutionLogs[0].OutputData)
	}
	outputData := gow.AnyToMap(vm.ExecutionLogs[1].GetRestApi().Data)
	if outputData["name"].(string) != "hit=second_condition" {
		t.Errorf("expected second notification to be executed, but got %s", vm.ExecutionLogs[1].OutputData)
	}

	// Test first condition
	vm.Reset()
	vm.vars["a"] = 15
	vm.Compile()
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
	}
	if !strings.Contains(vm.ExecutionLogs[0].GetBranch().ConditionId, "branch1.condition1") {
		t.Errorf("expected first condition to be hit, but got %s", vm.ExecutionLogs[0].OutputData)
	}

	// Test else condition
	vm.Reset()
	vm.vars["a"] = 3
	vm.Compile()
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
	}
	if !strings.Contains(vm.ExecutionLogs[0].GetBranch().ConditionId, "branch1.condition3") {
		t.Errorf("expected else condition to be hit, but got %s", vm.ExecutionLogs[0].OutputData)
	}
}

/*
1. Types of Conditions
A Branch Node supports two types of conditions:

If: The primary condition, which is always required.
Else: A fallback condition executed when none of the previous conditions are met. It's optional, but should always be the last. Any condtions after else will be discarded.
Therefore, examples of the correct conditions list are: (⬇ need to be unit tested.)

Successful cases: if, if, if, if, if, else
Discarded cases: if, else, if, if (the last two ifs will be discarded)
Invalid cases: else (starting with an else is not allowed)
*/

func TestBranchConditionSuccesfulCase(t *testing.T) {
	// Successful cases: if if if else
	testCases := []struct {
		name           string
		aValue         int
		expectedOutput string
		expectError    bool
	}{
		{"a = 12", 12, "test1.condition1", false},
		{"a = 5", 5, "test1.condition2", false},
		{"a = 1", 1, "test1.condition3", false},
	}

	// Define conditions
	conditions := []*avsproto.Condition{
		{Id: "condition1", Type: "if", Expression: "a > 10"},
		{Id: "condition2", Type: "if", Expression: "a > 3"},
		{Id: "condition3", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)

			vm.vars["a"] = tc.aValue
			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch().ConditionId != tc.expectedOutput {
				t.Errorf("expected output data %s but got %s", tc.expectedOutput, stepResult.OutputData)
			}
		})
	}
}

func TestBranchConditionDiscardAnythingAfterElse(t *testing.T) {
	//Discarded cases: if, else, if, if (the last two ifs will be discarded)
	// We stop as soon as we reach else, even if the next if match
	testCases := []struct {
		name           string
		aValue         int
		expectedOutput string
		expectError    bool
	}{
		{"a = 12", 12, "test1.condition1", false},
		{"a = 13", 13, "test1.condition1", false},
		{"a = 5", 5, "test1.condition2", false},
		{"a = 2", 2, "test1.condition2", false},
		{"a = 2", 2, "test1.condition2", false},
		{"a = 1", 1, "test1.condition2", false},
		{"a = 4", 1, "test1.condition2", false},
	}

	// Define conditions
	conditions := []*avsproto.Condition{
		{Id: "condition1", Type: "if", Expression: "a > 10"},
		{Id: "condition2", Type: "else"},
		{Id: "discard1", Type: "if", Expression: "a>3"},
		{Id: "discard2", Type: "if", Expression: "a>3"},
		{Id: "discard3", Type: "if", Expression: "a>4"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)

			vm.vars["a"] = tc.aValue
			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch().ConditionId != tc.expectedOutput {
				t.Errorf("expected output data %s but got %s", tc.expectedOutput, stepResult.OutputData)
			}
		})
	}
}

func TestBranchConditionInvalidCase(t *testing.T) {
	testCases := []struct {
		name           string
		aValue         int
		expectedOutput string
		expectError    bool
	}{
		{"a = 12", 12, "", true},
		{"a = 5", 5, "", true},
	}

	// Define conditions
	conditions := []*avsproto.Condition{
		{Id: "condition2", Type: "else"},
		{Id: "condition1", Type: "if", Expression: "a > 10"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)

			vm.vars["a"] = tc.aValue
			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch().ConditionId != tc.expectedOutput {
				t.Errorf("expected output data %s but got %s", tc.expectedOutput, stepResult.OutputData)
			}
		})
	}
}

/*
Issue: https://github.com/AvaProtocol/EigenLayer-AVS/issues/142

Conditions should accept JavaScript-like expressions for evaluation.
Currently, expression such as typeof trigger.data === "undefined" is failing
*/
func TestBranchNodeEvaluateTypeof(t *testing.T) {
	testCases := []struct {
		name           string
		dataValue      interface{}
		expectedOutput string
		expectError    bool
	}{
		{"data is undefined", map[string]interface{}{}, "test1.condition1", false},
		{"data is defined", map[string]interface{}{"data": "name"}, "test1.condition2", false},
	}

	conditions := []*avsproto.Condition{
		{Id: "condition1", Type: "if", Expression: `typeof mytrigger.data == "undefined"`},
		{Id: "condition2", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.vars["mytrigger"] = tc.dataValue

			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch().ConditionId != tc.expectedOutput {
				t.Errorf("expected output data %s but got %s", tc.expectedOutput, stepResult.OutputData)
			}
		})
	}
}

func TestBranchNodeEmptyConditionIsAPass(t *testing.T) {
	testCases := []struct {
		name           string
		dataValue      interface{}
		expectedOutput string
		expectError    bool
	}{
		{"expression is empty", "", "test1.condition2", false},
		{"expression is many space", "   ", "test1.condition2", false},
		{"expression is many tab", "\t\t", "test1.condition2", false},
		{"expression is many newline", "\t\t\n\n", "test1.condition2", false},
	}

	conditions := []*avsproto.Condition{
		{Id: "condition1", Type: "if", Expression: "\t\n"},
		{Id: "condition2", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.vars["mytrigger"] = tc.dataValue

			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch().ConditionId != tc.expectedOutput {
				t.Errorf("expected output data %s but got %s", tc.expectedOutput, stepResult.OutputData)
			}
		})
	}
}

func TestBranchNodeExpressionWithJavaScript(t *testing.T) {
	testCases := []struct {
		name           string
		expression     string
		dataValue      interface{}
		expectedOutput string
		expectError    bool
	}{
		{"Numeric compare matched", "Number(cod1.data) > 10", 12, "test1.condition1", false},
		{"Numeric compare not matched", "Number(cod1.data) > 10", 5, "test1.condition2", false},
		{"Logical operator match", "Number(cod1.data) >= 2 && Number(cod1.data) <=3", 3, "test1.condition1", false},
		{"Logical operator not matched", "Number(cod1.data) >= 2 && Number(cod1.data) <=3", 1, "test1.condition2", false},
		{"Logical operator negative match", "!(Number(cod1.data) >= 2 && Number(cod1.data) <=3)", 1, "test1.condition1", false},
		{"Logical operator negative not match", "!(Number(cod1.data) >= 2 && Number(cod1.data) <=3)", 3, "test1.condition2", false},
		{"String comparison matched", "cod1.data == 'alice'", "alice", "test1.condition1", false},
		{"String comparison not matched", "cod1.data == 'alice'", "bob", "test1.condition2", false},
	}

	conditions := []*avsproto.Condition{
		{Id: "condition1", Type: "if", Expression: ""},
		{Id: "condition2", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.vars["cod1"] = map[string]interface{}{"data": tc.dataValue}

			conditions[0].Expression = tc.expression
			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch().ConditionId != tc.expectedOutput {
				t.Errorf("expected output data %s but got %s", tc.expectedOutput, stepResult.OutputData)
			}
		})
	}
}

func TestBranchNodeNoElseSkip(t *testing.T) {
	testCases := []struct {
		expression  string
		dataValue   interface{}
		expectError bool
	}{
		{"Number(cod1.data) > 10", 5, false},
		{"Number(cod1.data) >= 2 && Number(cod1.data) <=3", 1, false},
		{"!(Number(cod1.data) >= 2 && Number(cod1.data) <=3)", 3, false},
		{"cod1.data == 'alice'", "bob", false},
	}

	conditions := []*avsproto.Condition{
		{Id: "condition1", Type: "if", Expression: ""},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.expression, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.vars["cod1"] = map[string]interface{}{"data": tc.dataValue}

			conditions[0].Expression = tc.expression
			stepResult, err := processor.Execute("test1", &avsproto.BranchNode{
				Conditions: conditions,
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if stepResult.GetBranch() != nil {
				t.Errorf("expected no action but got %s", stepResult.OutputData)
			}
		})
	}
}
