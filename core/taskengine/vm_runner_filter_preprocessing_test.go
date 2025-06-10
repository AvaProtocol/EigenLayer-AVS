package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestFilterNodePreprocessing(t *testing.T) {
	testCases := []struct {
		name          string
		expression    string
		inputData     []interface{}
		expectedCount int
		expectError   bool
	}{
		{
			name:       "age filter with preprocessing using trigger data",
			expression: "{{ trigger.data.minAge }} <= current.age",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 2, // Alice and Charlie (assuming minAge = 18)
		},
		{
			name:       "age filter under 18 with preprocessing",
			expression: "current.age < 18",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 1, // Bob
		},
		{
			name:       "name starts with filter with preprocessing",
			expression: "current.name.startsWith(\"A\")",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Anna", "age": 30},
			},
			expectedCount: 2, // Alice and Anna
		},
		{
			name:       "complex age range filter with preprocessing",
			expression: "current.age >= 20 && current.age <= 22",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 21},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
				map[string]interface{}{"name": "David", "age": 22},
			},
			expectedCount: 2, // Alice and David
		},
		{
			name:       "filter without preprocessing",
			expression: "current.age >= 18",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 16},
				map[string]interface{}{"name": "Charlie", "age": 30},
			},
			expectedCount: 2, // Alice and Charlie
		},
		{
			name:       "numeric property filter with preprocessing",
			expression: "current.score > 80",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "score": 85},
				map[string]interface{}{"name": "Bob", "score": 75},
				map[string]interface{}{"name": "Charlie", "score": 90},
			},
			expectedCount: 2, // Alice and Charlie
		},
		{
			name:       "boolean property filter with preprocessing",
			expression: "current.active === true",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "active": true},
				map[string]interface{}{"name": "Bob", "active": false},
				map[string]interface{}{"name": "Charlie", "active": true},
			},
			expectedCount: 2, // Alice and Charlie
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewFilterProcessor(vm)

			vm.AddVar("testInput", tc.inputData)
			vm.AddVar("trigger", map[string]interface{}{
				"data": map[string]interface{}{
					"minAge": 18,
				},
			})

			node := &avsproto.FilterNode{
				Config: &avsproto.FilterNode_Config{
					Expression: tc.expression,
					SourceId:   "testInput",
				},
			}

			stepResult, err := processor.Execute("filter1", node)

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
				return
			}

			varname := vm.GetNodeNameAsVar("filter1")
			if filteredData, exists := vm.vars[varname]; exists {
				if dataMap, ok := filteredData.(map[string]interface{}); ok {
					if data, exists := dataMap["data"]; exists {
						if filteredArray, ok := data.([]interface{}); ok {
							if len(filteredArray) != tc.expectedCount {
								t.Errorf("expected %d filtered items but got %d", tc.expectedCount, len(filteredArray))
							}
						} else {
							t.Errorf("expected filtered data to be an array but got %T", data)
						}
					} else {
						t.Errorf("expected data field in filtered result but not found")
					}
				} else {
					t.Errorf("expected filtered data to be a map but got %T", filteredData)
				}
			} else {
				t.Errorf("expected filtered data to be stored in vm.vars but not found")
			}
		})
	}
}

func TestFilterNodePreprocessingEdgeCases(t *testing.T) {
	testCases := []struct {
		name        string
		expression  string
		inputData   []interface{}
		expectError bool
	}{
		{
			name:       "empty preprocessing expression",
			expression: "{{ }}",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
			},
			expectError: false, // Should handle gracefully
		},
		{
			name:       "invalid property access",
			expression: "{{ current.nonexistent.property }}",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
			},
			expectError: false, // Should handle gracefully
		},
		{
			name:       "complex expression with index",
			expression: "{{ index <= 1 && current.age >= 18 }}",
			inputData: []interface{}{
				map[string]interface{}{"name": "Alice", "age": 25},
				map[string]interface{}{"name": "Bob", "age": 30},
				map[string]interface{}{"name": "Charlie", "age": 20},
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := NewVM()
			processor := NewFilterProcessor(vm)

			vm.AddVar("testInput", tc.inputData)

			node := &avsproto.FilterNode{
				Config: &avsproto.FilterNode_Config{
					Expression: tc.expression,
					SourceId:   "testInput",
				},
			}

			stepResult, err := processor.Execute("filter1", node)

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
