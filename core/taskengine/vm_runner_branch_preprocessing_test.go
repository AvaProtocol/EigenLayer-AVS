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
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			
			vm.vars["trigger"] = map[string]interface{}{
				"data": tc.triggerData,
			}

			conditions := []*avsproto.Condition{
				{Id: "condition1", Type: "if", Expression: tc.expression},
				{Id: "condition2", Type: "else"},
			}

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

			if tc.expectedResult {
				if stepResult.GetBranch() == nil {
					t.Errorf("expected branch output but got none")
					return
				}
				if stepResult.GetBranch().ConditionId != "test1.condition1" {
					t.Errorf("expected condition1 path but got %s", stepResult.GetBranch().ConditionId)
				}
			} else {
				if stepResult.GetBranch() == nil {
					t.Errorf("expected branch output but got none")
					return
				}
				if stepResult.GetBranch().ConditionId != "test1.condition2" {
					t.Errorf("expected condition2 (else) path but got %s", stepResult.GetBranch().ConditionId)
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
			expectError: false, // Should be treated as empty and return false
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
			expectError: false, // Should handle gracefully and return false
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewBranchProcessor(vm)
			
			vm.vars["trigger"] = map[string]interface{}{
				"data": tc.triggerData,
			}

			conditions := []*avsproto.Condition{
				{Id: "condition1", Type: "if", Expression: tc.expression},
				{Id: "condition2", Type: "else"},
			}

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

			if stepResult == nil {
				t.Errorf("expected step result but got none")
			}
		})
	}
}
