package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestBranchNodePreprocessing(t *testing.T) {
	testCases := []struct {
		name           string
		expression     string
		triggerData    map[string]interface{}
		expectedResult bool
		expectError    bool
	}{
		{
			name:       "timestamp comparison with preprocessing",
			expression: "{{ trigger.data.timestamp > 0 }}",
			triggerData: map[string]interface{}{
				"timestamp": 1234567890,
			},
			expectedResult: true,
		},
		{
			name:       "status string comparison with preprocessing",
			expression: "{{ trigger.data.status === \"ready\" }}",
			triggerData: map[string]interface{}{
				"status": "ready",
			},
			expectedResult: true,
		},
		{
			name:       "nested object property access with preprocessing",
			expression: "{{ trigger.data.user.role === \"admin\" }}",
			triggerData: map[string]interface{}{
				"user": map[string]interface{}{
					"role": "admin",
				},
			},
			expectedResult: true,
		},
		{
			name:       "age comparison with preprocessing",
			expression: "{{ trigger.data.user.age >= 18 }}",
			triggerData: map[string]interface{}{
				"user": map[string]interface{}{
					"age": 25,
				},
			},
			expectedResult: true,
		},
		{
			name:       "complex logical expression with preprocessing",
			expression: "{{ trigger.data.status === \"ready\" && trigger.data.user.age >= 18 }}",
			triggerData: map[string]interface{}{
				"status": "ready",
				"user": map[string]interface{}{
					"age": 25,
				},
			},
			expectedResult: true,
		},
		{
			name:       "false condition with preprocessing",
			expression: "{{ trigger.data.status === \"pending\" }}",
			triggerData: map[string]interface{}{
				"status": "ready",
			},
			expectedResult: false,
		},
		{
			name:       "age below threshold with preprocessing",
			expression: "{{ trigger.data.user.age >= 18 }}",
			triggerData: map[string]interface{}{
				"user": map[string]interface{}{
					"age": 16,
				},
			},
			expectedResult: false,
		},
		{
			name:       "mixed expression without preprocessing",
			expression: "trigger.data.timestamp > 0",
			triggerData: map[string]interface{}{
				"timestamp": 1234567890,
			},
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			taskNode := &avsproto.TaskNode{
				Id:   "test1",
				Name: "branch",
				TaskType: &avsproto.TaskNode_Branch{
					Branch: &avsproto.BranchNode{
						Config: &avsproto.BranchNode_Config{
							Conditions: []*avsproto.BranchNode_Condition{
								{Id: "condition1", Type: "if", Expression: tc.expression},
								{Id: "condition2", Type: "else"},
							},
						},
					},
				},
			}

			vm := NewVM()
			step, err := vm.RunNodeWithInputs(taskNode, map[string]interface{}{
				"trigger": map[string]interface{}{
					"data": tc.triggerData,
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

			if step == nil {
				t.Errorf("expected step but got none")
				return
			}

			branchOutput := step.GetBranch()

			if tc.expectedResult {
				if branchOutput == nil {
					t.Errorf("expected branch output but got none")
					return
				}
				if branchOutput.Data == nil {
					t.Errorf("expected branch data but got none")
					return
				}
				// Check that condition1 (the "if" branch) was matched
				dataMap := branchOutput.Data.AsInterface()
				if m, ok := dataMap.(map[string]interface{}); ok {
					if conditionId, exists := m["conditionId"]; exists {
						if conditionId != "test1.condition1" {
							t.Errorf("expected condition1 path but got %s", conditionId)
						}
					} else {
						t.Errorf("expected conditionId in branch data but not found")
					}
				} else {
					t.Errorf("expected branch data to be a map")
				}
			} else {
				// When the "if" condition is false, the "else" branch should be taken
				if branchOutput == nil {
					t.Errorf("expected branch output but got none")
					return
				}
				if branchOutput.Data == nil {
					t.Errorf("expected branch data but got none")
					return
				}
				// Check that condition2 (the "else" branch) was matched
				dataMap := branchOutput.Data.AsInterface()
				if m, ok := dataMap.(map[string]interface{}); ok {
					if conditionId, exists := m["conditionId"]; exists {
						if conditionId != "test1.condition2" {
							t.Errorf("expected condition2 (else) path but got %s", conditionId)
						}
					} else {
						t.Errorf("expected conditionId in branch data but not found")
					}
				} else {
					t.Errorf("expected branch data to be a map")
				}
			}
		})
	}
}

func TestBranchNodePreprocessingEdgeCases(t *testing.T) {
	testCases := []struct {
		name        string
		expression  string
		triggerData map[string]interface{}
		expectError bool
	}{
		{
			name:       "empty preprocessing expression",
			expression: "{{ }}",
			triggerData: map[string]interface{}{
				"status": "ready",
			},
			expectError: false, // Now fails silently - goes to else condition
		},
		{
			name:       "string with braces in content",
			expression: "{{ trigger.data.status === \"ready\" }}",
			triggerData: map[string]interface{}{
				"status": "ready",
			},
			expectError: false,
		},
		{
			name:       "invalid property access",
			expression: "{{ trigger.data.nonexistent.property }}",
			triggerData: map[string]interface{}{
				"status": "ready",
			},
			expectError: false, // Now fails silently - goes to else condition
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			taskNode := &avsproto.TaskNode{
				Id:   "test1",
				Name: "branch",
				TaskType: &avsproto.TaskNode_Branch{
					Branch: &avsproto.BranchNode{
						Config: &avsproto.BranchNode_Config{
							Conditions: []*avsproto.BranchNode_Condition{
								{Id: "condition1", Type: "if", Expression: tc.expression},
								{Id: "condition2", Type: "else"},
							},
						},
					},
				},
			}

			vm := NewVM()
			step, err := vm.RunNodeWithInputs(taskNode, map[string]interface{}{
				"trigger": map[string]interface{}{
					"data": tc.triggerData,
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

			if step == nil {
				t.Errorf("expected step but got none")
			}
			if step.GetBranch() == nil {
				t.Errorf("expected branch output but got none")
			}
		})
	}
}
