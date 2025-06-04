package taskengine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
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
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition1",
								Type:       "if",
								Expression: "a > 10",
							},
							{
								Id:         "condition2",
								Type:       "if",
								Expression: "a > 5",
							},
							{
								Id:   "condition3",
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
						Url:    ts.URL,
						Method: "POST",
						Body:   "hit=first_condition",
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
						Url:    ts.URL,
						Method: "POST",
						Body:   "hit=second_condition",
					},
				},
			},
		},
		{
			Id:   "notification3",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    ts.URL,
						Method: "POST",
						Body:   "hit=else_condition",
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
			Id:     "e2",
			Source: "branch1.condition1",
			Target: "notification1",
		},
		{
			Id:     "e3",
			Source: "branch1.condition2",
			Target: "notification2",
		},
		{
			Id:     "e4",
			Source: "branch1.condition3",
			Target: "notification3",
		},
	}

	testTriggerData := testutil.GetTestEventTriggerData()
	triggerData := &TriggerData{
		Type:   testTriggerData.Type,
		Output: testTriggerData.Output,
	}

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

	// Set value to hit the second condition (a > 5 but not > 10)
	vm.AddVar("a", 7)

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
	outputData := gow.ValueToMap(vm.ExecutionLogs[1].GetRestApi().Data)
	bodyData := outputData["body"].(map[string]interface{})
	if bodyData["name"].(string) != "hit=second_condition" {
		t.Errorf("expected second notification to be executed, but got %s", vm.ExecutionLogs[1].OutputData)
	}

	// Test first condition
	vm.Reset()
	vm.AddVar("a", 15)
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
	vm.AddVar("a", 3)
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
	conditions := []*avsproto.BranchNode_Condition{
		{Id: "condition1", Type: "if", Expression: "a > 10"},
		{Id: "condition2", Type: "if", Expression: "a > 3"},
		{Id: "condition3", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)

			vm.AddVar("a", tc.aValue)
			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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
	conditions := []*avsproto.BranchNode_Condition{
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

			vm.AddVar("a", tc.aValue)
			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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
	conditions := []*avsproto.BranchNode_Condition{
		{Id: "condition2", Type: "else"},
		{Id: "condition1", Type: "if", Expression: "a > 10"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)

			vm.AddVar("a", tc.aValue)
			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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

			if stepResult != nil && stepResult.GetBranch() != nil && stepResult.GetBranch().ConditionId != tc.expectedOutput {
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

	conditions := []*avsproto.BranchNode_Condition{
		{Id: "condition1", Type: "if", Expression: `typeof mytrigger.data == "undefined"`},
		{Id: "condition2", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.AddVar("mytrigger", tc.dataValue)

			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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

	conditions := []*avsproto.BranchNode_Condition{
		{Id: "condition1", Type: "if", Expression: "\t\n"},
		{Id: "condition2", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.AddVar("mytrigger", tc.dataValue)

			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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

	conditions := []*avsproto.BranchNode_Condition{
		{Id: "condition1", Type: "if", Expression: ""},
		{Id: "condition2", Type: "else"},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.AddVar("cod1", map[string]interface{}{"data": tc.dataValue})

			conditions[0].Expression = tc.expression
			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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

	conditions := []*avsproto.BranchNode_Condition{
		{Id: "condition1", Type: "if", Expression: ""},
	}

	// Iterate over test cases
	for _, tc := range testCases {
		t.Run(tc.expression, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			vm.AddVar("cod1", map[string]interface{}{"data": tc.dataValue})

			conditions[0].Expression = tc.expression
			stepResult, _, err := processor.Execute("test1", &avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: conditions,
				},
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

func TestBranchNodeMalformedData(t *testing.T) {
	testCases := []struct {
		name        string
		conditions  []*avsproto.BranchNode_Condition
		expectError bool
		errorMsg    string
	}{
		{
			name: "nil condition",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				nil,
			},
			expectError: true,
			errorMsg:    "condition at index 1 is nil",
		},
		{
			name: "empty condition ID",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				{Id: "", Type: "if", Expression: "a > 5"},
			},
			expectError: true,
			errorMsg:    "condition at index 1 has empty ID",
		},
		{
			name: "empty condition type",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				{Id: "condition2", Type: "", Expression: "a > 5"},
			},
			expectError: true,
			errorMsg:    "condition at index 1 has empty type",
		},
		{
			name: "invalid condition type",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				{Id: "condition2", Type: "invalid", Expression: "a > 5"},
			},
			expectError: true,
			errorMsg:    "condition at index 1 has invalid type",
		},
		{
			name: "empty if expression",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				{Id: "condition2", Type: "if", Expression: ""},
			},
			expectError: false,
		},
		{
			name: "whitespace if expression",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				{Id: "condition2", Type: "if", Expression: "   \t\n  "},
			},
			expectError: false,
		},
		{
			name: "valid conditions",
			conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "a > 10"},
				{Id: "condition2", Type: "if", Expression: "a > 5"},
				{Id: "condition3", Type: "else"},
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)

			err := processor.Validate(&avsproto.BranchNode{
				Config: &avsproto.BranchNode_Config{
					Conditions: tc.conditions,
				},
			})

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tc.errorMsg) {
					t.Errorf("expected error message to contain '%s', but got: '%s'", tc.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestBranchProcessor_Execute_NoConditions(t *testing.T) {
	vm := NewVM()
	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "testStep"

	executionLog, nextStep, err := processor.Execute(stepID, node)

	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "there is no condition to evaluate")
	assert.Nil(t, nextStep)
	assert.NotNil(t, executionLog)
	assert.False(t, executionLog.Success)
}

func TestBranchProcessor_Execute_ConditionMet(t *testing.T) {
	vm := NewVM()
	vm.AddVar("myVar", 123)

	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{
					Id:         "cond1",
					Type:       "if",
					Expression: "myVar == 123",
				},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "testStep"

	executionLog, nextStep, err := processor.Execute(stepID, node)

	assert.Nil(t, err)
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, nextStep)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "cond1"), nextStep.NodeID)
	branchOutput := executionLog.GetBranch()
	assert.NotNil(t, branchOutput)
	assert.Equal(t, "testStep.cond1", branchOutput.ConditionId)
}

func TestBranchProcessor_Execute_NoConditionMet(t *testing.T) {
	vm := NewVM()
	vm.AddVar("myVar", 456)

	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{
					Id:         "cond1",
					Type:       "if",
					Expression: "myVar == 123", // This will be false
				},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "testStepNoMatch"

	executionLog, nextStep, err := processor.Execute(stepID, node)

	// Updated expectation: when only if conditions exist and none match, it should be a valid no-op
	assert.Nil(t, err)
	assert.Nil(t, nextStep)
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success)
	assert.Nil(t, executionLog.GetBranch()) // No branch action taken
}

func TestBranchProcessor_Execute_ErrorInExpression(t *testing.T) {
	vm := NewVM()
	vm.AddVar("myVar", 123)

	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{
					Id:         "cond1",
					Type:       "if",
					Expression: "myVar + ", // Invalid expression
				},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "testStepErrorExpr"

	executionLog, nextStep, err := processor.Execute(stepID, node)

	// Now expects silent failure - no error returned, condition treated as false
	assert.Nil(t, err)
	assert.Nil(t, nextStep) // No condition matched, no else condition, so no next step
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success) // Success with no branch action taken
	assert.Empty(t, executionLog.Error)  // No error in execution log
}

func TestBranchProcessor_Execute_MultipleConditions_FirstMatch(t *testing.T) {
	vm := NewVM()
	vm.AddVar("val", 10)

	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{Id: "c1", Type: "if", Expression: "val == 10"}, // Match
				{Id: "c2", Type: "if", Expression: "val == 20"},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "multiCondFirst"
	executionLog, nextStep, err := processor.Execute(stepID, node)

	assert.Nil(t, err)
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, nextStep)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "c1"), nextStep.NodeID)
	branchOutput := executionLog.GetBranch()
	assert.NotNil(t, branchOutput)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "c1"), branchOutput.ConditionId)
}

func TestBranchProcessor_Execute_MultipleConditions_SecondMatch(t *testing.T) {
	vm := NewVM()
	vm.AddVar("val", 20)
	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{Id: "c1", Type: "if", Expression: "val == 10"},
				{Id: "c2", Type: "if", Expression: "val == 20"}, // Match
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "multiCondSecond"
	executionLog, nextStep, err := processor.Execute(stepID, node)

	assert.Nil(t, err)
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, nextStep)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "c2"), nextStep.NodeID)
	branchOutput := executionLog.GetBranch()
	assert.NotNil(t, branchOutput)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "c2"), branchOutput.ConditionId)
}

func TestBranchProcessor_Execute_ComplexVariableAccess(t *testing.T) {
	vm := NewVM()
	data := map[string]interface{}{"level1": map[string]interface{}{"level2": "found_me"}}
	vm.AddVar("complexVar", data)

	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{Id: "c1", Type: "if", Expression: "complexVar.level1.level2 == 'found_me'"},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "complexVarAccess"
	executionLog, nextStep, err := processor.Execute(stepID, node)

	assert.Nil(t, err)
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, nextStep)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "c1"), nextStep.NodeID)
	branchOutput := executionLog.GetBranch()
	assert.NotNil(t, branchOutput)
	assert.Equal(t, fmt.Sprintf("%s.%s", stepID, "c1"), branchOutput.ConditionId)
}

func TestBranchProcessor_Execute_NonExistentVarInExpression(t *testing.T) {
	vm := NewVM()
	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "nonExistentVar == 123"},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "testStepNonExistentVar"

	executionLog, nextStep, err := processor.Execute(stepID, node)

	// Now expects silent failure - undefined variables are treated as false
	assert.Nil(t, err)
	assert.Nil(t, nextStep) // No condition matched, no else condition, so no next step
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success) // Success with no branch action taken
}

func TestBranchProcessor_Execute_InvalidScriptSyntax(t *testing.T) {
	vm := NewVM()
	node := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{Id: "condition1", Type: "if", Expression: "invalid script (((("},
			},
		},
	}
	processor := NewBranchProcessor(vm)
	stepID := "testStepInvalidScript"

	executionLog, nextStep, err := processor.Execute(stepID, node)

	// Now expects silent failure - invalid syntax is treated as false
	assert.Nil(t, err)
	assert.Nil(t, nextStep) // No condition matched, no else condition, so no next step
	assert.NotNil(t, executionLog)
	assert.True(t, executionLog.Success) // Success with no branch action taken
	assert.Empty(t, executionLog.Error)  // No error in execution log
}
