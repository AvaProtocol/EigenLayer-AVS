package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// Test suite for JavaScript module imports in CustomCode nodes
// These tests verify that the VM can properly handle both CommonJS (require) and ES6 (import) module syntax
// and that pre-bundled libraries (lodash, dayjs, uuid) work as expected.

// TestCommonJSRequireSyntax verifies that CommonJS-style require() statements work correctly
// with pre-bundled libraries. This is the traditional Node.js module system.
func TestCommonJSRequireSyntax(t *testing.T) {
	testCases := []struct {
		name           string
		code           string
		expectedResult interface{}
		checkFunction  func(t *testing.T, output interface{}, expected interface{})
	}{
		{
			name: "Lodash Array Operations",
			code: `
			const _ = require('lodash');
			return _.map([1, 2, 3], n => n * 2);
		`,
			expectedResult: []float64{2, 4, 6},
			checkFunction:  wrapAssertFloat64Slice,
		},
		{
			name: "Lodash Object Operations",
			code: `
				const _ = require('lodash');
				const obj = { a: 1, b: 2, c: 3 };
				return _.pick(obj, ['a']);
			`,
			expectedResult: map[string]interface{}{"a": float64(1)},
			checkFunction:  assertStructpbValueIsMap,
		},
		{
			name: "Day.js Date Formatting",
			code: `
				const dayjs = require('dayjs');
				const date = dayjs('2023-01-01');
				return date.format('YYYY-MM-DD');
			`,
			expectedResult: "2023-01-01",
			checkFunction:  wrapAssertString,
		},
		{
			name: "Day.js Date Manipulation",
			code: `
				const dayjs = require('dayjs');
				const date = dayjs('2023-01-01').add(1, 'day');
				return date.format('YYYY-MM-DD');
			`,
			expectedResult: "2023-01-02",
			checkFunction:  wrapAssertString,
		},
		{
			name: "UUID Generation",
			code: `
				const { v4: uuidv4 } = require('uuid');
				const id = uuidv4();
				return typeof id === 'string' && id.length === 36;
			`,
			expectedResult: true,
			checkFunction:  wrapAssertBool,
		},
		{
			name: "Multiple Library Usage",
			code: `
				const _ = require('lodash');
				const dayjs = require('dayjs');
				const { v4: uuidv4 } = require('uuid');
				
				const data = {
					id: uuidv4(),
					dates: _.map([1, 2, 3], n => dayjs().add(n, 'day').format('YYYY-MM-DD')),
					isValid: true
				};
				return data;
			`,
			expectedResult: map[string]interface{}{
				"dates":   []interface{}{"2024-03-21", "2024-03-22", "2024-03-23"}, // Note: These will be dynamic
				"isValid": true,
			},
			checkFunction: assertStructpbValueIsMapWithDynamicDates,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := &avsproto.CustomCodeNode{
				Source: tc.code,
			}

			trigger := &avsproto.TaskTrigger{
				Id:   "triggertest",
				Name: "triggertest",
			}

			vm, err := NewVMWithData(&model.Task{
				Task: &avsproto.Task{
					Id:      "test_commonjs_" + tc.name,
					Trigger: trigger,
				},
			}, nil, testutil.GetTestSmartWalletConfig(), nil)

			if err != nil {
				t.Fatalf("Failed to create VM: %v", err)
			}

			n := NewJSProcessor(vm)
			step, err := n.Execute("test_commonjs_"+tc.name, node)

			if err != nil {
				t.Errorf("Expected JavaScript to run successfully but got error: %v", err)
			}

			if !step.Success {
				t.Errorf("Expected JavaScript to run successfully but failed")
			}

			tc.checkFunction(t, step.GetCustomCode().Data, tc.expectedResult)
		})
	}
}

// TestES6ImportSyntax verifies that ES6-style import statements work correctly
// with pre-bundled libraries. This is the modern JavaScript module system.
func TestES6ImportSyntax(t *testing.T) {
	testCases := []struct {
		name           string
		code           string
		expectedResult interface{}
		checkFunction  func(t *testing.T, output interface{}, expected interface{})
	}{
		{
			name: "Lodash Default Import",
			code: `
				import _ from 'lodash';
				return _.map([1, 2, 3], n => n * 2);
			`,
			expectedResult: []float64{2, 4, 6},
			checkFunction:  wrapAssertFloat64Slice,
		},
		{
			name: "Lodash Named Import",
			code: `
				import { map, pick } from 'lodash';
				const obj = { a: 1, b: 2 };
				return pick(map([1, 2, 3], n => n * 2), [0, 1]);
			`,
			expectedResult: map[string]interface{}{"0": float64(2), "1": float64(4)},
			checkFunction:  assertStructpbValueIsMap,
		},
		{
			name: "Day.js Date Formatting",
			code: `
				import dayjs from 'dayjs';
				return dayjs('2023-01-01').format('YYYY-MM-DD');
			`,
			expectedResult: "2023-01-01",
			checkFunction:  wrapAssertString,
		},
		{
			name: "Day.js Date Manipulation",
			code: `
				import dayjs from 'dayjs';
				return dayjs('2023-01-01').add(1, 'day').format('YYYY-MM-DD');
			`,
			expectedResult: "2023-01-02",
			checkFunction:  wrapAssertString,
		},
		{
			name: "UUID Named Import",
			code: `
				import { v4 as uuidv4 } from 'uuid';
			const id = uuidv4();
				return typeof id === 'string' && id.length === 36;
		`,
			expectedResult: true,
			checkFunction:  wrapAssertBool,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := &avsproto.CustomCodeNode{
				Source: tc.code,
			}

			trigger := &avsproto.TaskTrigger{
				Id:   "triggertest",
				Name: "triggertest",
			}

			vm, err := NewVMWithData(&model.Task{
				Task: &avsproto.Task{
					Id:      "test_es6_" + tc.name,
					Trigger: trigger,
				},
			}, nil, testutil.GetTestSmartWalletConfig(), nil)

			if err != nil {
				t.Fatalf("Failed to create VM: %v", err)
			}

			n := NewJSProcessor(vm)
			step, err := n.Execute("test_es6_"+tc.name, node)

			if err != nil {
				t.Errorf("Expected JavaScript to run successfully but got error: %v", err)
			}

			if !step.Success {
				t.Errorf("Expected JavaScript to run successfully but failed")
			}

			tc.checkFunction(t, step.GetCustomCode().Data, tc.expectedResult)
		})
	}
}

// TestModuleSyntaxDetection verifies that the module syntax detection works correctly
// for both CommonJS and ES6 module systems, including edge cases.
func TestModuleSyntaxDetection(t *testing.T) {
	testCases := []struct {
		name           string
		code           string
		expectedResult bool
		description    string
	}{
		{
			name:           "No Module Syntax",
			code:           "const a = 1; return a + 2;",
			expectedResult: false,
			description:    "Simple JavaScript without any module syntax",
		},
		{
			name:           "CommonJS Require",
			code:           "const _ = require('lodash'); return _.map([1,2,3], n => n * 2);",
			expectedResult: true,
			description:    "CommonJS require() statement",
		},
		{
			name:           "ES6 Import Statement",
			code:           "import _ from 'lodash'; return _.map([1,2,3], n => n * 2);",
			expectedResult: true,
			description:    "ES6 import statement",
		},
		{
			name:           "ES6 Export Statement",
			code:           "const a = 1; export default a;",
			expectedResult: true,
			description:    "ES6 export statement",
		},
		{
			name:           "Import with Whitespace",
			code:           "  import   _   from   'lodash';  ",
			expectedResult: true,
			description:    "Import statement with extra whitespace",
		},
		{
			name:           "Import in Comment",
			code:           "// import _ from 'lodash';\nconst a = 1; return a;",
			expectedResult: false,
			description:    "Import statement in comment should not be detected",
		},
		{
			name:           "Dynamic Import",
			code:           "const module = await import('lodash');",
			expectedResult: true,
			description:    "Dynamic import() expression",
		},
		{
			name:           "Multiple Imports",
			code:           "import _ from 'lodash';\nimport dayjs from 'dayjs';",
			expectedResult: true,
			description:    "Multiple import statements",
		},
		{
			name:           "Import with String Template",
			code:           "import _ from `lodash`;",
			expectedResult: true,
			description:    "Import with template literal",
		},
		{
			name:           "Require with Variable",
			code:           "const lib = 'lodash';\nconst _ = require(lib);",
			expectedResult: true,
			description:    "Dynamic require with variable",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := containsModuleSyntax(tc.code)
			if result != tc.expectedResult {
				t.Errorf("Test case '%s' failed: %s\nExpected containsModuleSyntax(%q) to be %v, got %v",
					tc.name, tc.description, tc.code, tc.expectedResult, result)
			}
		})
	}
}

// TestErrorHandling verifies that the VM properly handles various error cases
// related to module imports and usage.
func TestErrorHandling(t *testing.T) {
	testCases := []struct {
		name        string
		code        string
		expectError bool
		description string
	}{
		{
			name: "Non-existent Module",
			code: `
				const nonexistent = require('nonexistent-module');
				return nonexistent.something();
			`,
			expectError: true,
			description: "Should fail when requiring a non-existent module",
		},
		{
			name: "Invalid Import Syntax",
			code: `
				import { from 'lodash';
				return _.map([1, 2, 3], n => n * 2);
			`,
			expectError: true,
			description: "Should fail with invalid import syntax",
		},
		{
			name: "Circular Dependency",
			code: `
				const _ = require('lodash');
				const dayjs = require('dayjs');
				// Simulate circular dependency
				const circular = require('./circular');
				return circular.value;
			`,
			expectError: true,
			description: "Should handle circular dependency errors",
		},
		{
			name: "Invalid Module Usage",
			code: `
				const dayjs = require('dayjs');
				return dayjs.invalidMethod();
			`,
			expectError: true,
			description: "Should fail when using non-existent module methods",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := &avsproto.CustomCodeNode{
				Source: tc.code,
			}

			trigger := &avsproto.TaskTrigger{
				Id:   "triggertest",
				Name: "triggertest",
			}

			vm, err := NewVMWithData(&model.Task{
				Task: &avsproto.Task{
					Id:      "test_error_" + tc.name,
					Trigger: trigger,
				},
			}, nil, testutil.GetTestSmartWalletConfig(), nil)

			if err != nil {
				t.Fatalf("Failed to create VM: %v", err)
			}

			n := NewJSProcessor(vm)
			step, err := n.Execute("test_error_"+tc.name, node)

			if tc.expectError {
				if err == nil && step.Success {
					t.Errorf("Test case '%s' failed: %s\nExpected an error but got success",
						tc.name, tc.description)
				}
			} else {
				if err != nil || !step.Success {
					t.Errorf("Test case '%s' failed: %s\nExpected success but got error: %v",
						tc.name, tc.description, err)
				}
			}
		})
	}
}

// Helper functions for assertions
// These are wrapper functions that convert between the interface{} based test cases
// and the structpb.Value based assertion functions from vm_runner_customcode_test.go

func wrapAssertFloat64Slice(t *testing.T, output interface{}, expected interface{}) {
	expectedSlice, ok := expected.([]float64)
	if !ok {
		t.Errorf("Expected []float64 in expected value, got %T", expected)
		return
	}

	val, ok := output.(*structpb.Value)
	if !ok {
		t.Errorf("Expected *structpb.Value output, got %T", output)
		return
	}

	assertStructpbValueIsFloat64Slice(t, val, expectedSlice, "wrapAssertFloat64Slice")
}

func wrapAssertString(t *testing.T, output interface{}, expected interface{}) {
	expectedStr, ok := expected.(string)
	if !ok {
		t.Errorf("Expected string in expected value, got %T", expected)
		return
	}

	val, ok := output.(*structpb.Value)
	if !ok {
		t.Errorf("Expected *structpb.Value output, got %T", output)
		return
	}

	assertStructpbValueIsString(t, val, expectedStr, "wrapAssertString")
}

func wrapAssertBool(t *testing.T, output interface{}, expected interface{}) {
	expectedBool, ok := expected.(bool)
	if !ok {
		t.Errorf("Expected bool in expected value, got %T", expected)
		return
	}

	val, ok := output.(*structpb.Value)
	if !ok {
		t.Errorf("Expected *structpb.Value output, got %T", output)
		return
	}

	assertStructpbValueIsBool(t, val, expectedBool, "wrapAssertBool")
}

func assertStructpbValueIsMap(t *testing.T, output interface{}, expected interface{}) {
	val, ok := output.(*structpb.Value)
	if !ok {
		t.Errorf("Expected *structpb.Value output, got %T", output)
		return
	}

	structValue, ok := val.GetKind().(*structpb.Value_StructValue)
	if !ok {
		t.Errorf("Expected struct value, got %T", val.GetKind())
		return
	}

	result := structValue.StructValue.GetFields()
	expectedMap, ok := expected.(map[string]interface{})
	if !ok {
		t.Errorf("Expected map in expected value, got %T", expected)
		return
	}

	// Check all expected fields
	for k, v := range expectedMap {
		fieldVal, exists := result[k]
		if !exists {
			t.Errorf("Expected field %s to exist in result", k)
			continue
		}

		switch expectedVal := v.(type) {
		case float64:
			numVal, ok := fieldVal.GetKind().(*structpb.Value_NumberValue)
			if !ok {
				t.Errorf("Expected number value for field %s, got %T", k, fieldVal.GetKind())
				continue
			}
			if numVal.NumberValue != expectedVal {
				t.Errorf("Expected %v for key %s, got %v", expectedVal, k, numVal.NumberValue)
			}
		case bool:
			boolVal, ok := fieldVal.GetKind().(*structpb.Value_BoolValue)
			if !ok {
				t.Errorf("Expected bool value for field %s, got %T", k, fieldVal.GetKind())
				continue
			}
			if boolVal.BoolValue != expectedVal {
				t.Errorf("Expected %v for key %s, got %v", expectedVal, k, boolVal.BoolValue)
			}
		case string:
			strVal, ok := fieldVal.GetKind().(*structpb.Value_StringValue)
			if !ok {
				t.Errorf("Expected string value for field %s, got %T", k, fieldVal.GetKind())
				continue
			}
			if strVal.StringValue != expectedVal {
				t.Errorf("Expected %v for key %s, got %v", expectedVal, k, strVal.StringValue)
			}
		}
	}
}

func assertStructpbValueIsMapWithDynamicDates(t *testing.T, output interface{}, expected interface{}) {
	// Special assertion for maps that may contain dynamic date values
	// This is a simplified version - you might want to add more sophisticated date validation
	val, ok := output.(*structpb.Value)
	if !ok {
		t.Errorf("Expected *structpb.Value output, got %T", output)
		return
	}

	structValue, ok := val.GetKind().(*structpb.Value_StructValue)
	if !ok {
		t.Errorf("Expected struct value, got %T", val.GetKind())
		return
	}

	result := structValue.StructValue.GetFields()
	expectedMap, ok := expected.(map[string]interface{})
	if !ok {
		t.Errorf("Expected map in expected value, got %T", expected)
		return
	}

	// Check non-date fields
	for k, v := range expectedMap {
		if k != "dates" {
			fieldVal, exists := result[k]
			if !exists {
				t.Errorf("Expected field %s to exist in result", k)
				continue
			}

			switch expectedVal := v.(type) {
			case float64:
				numVal, ok := fieldVal.GetKind().(*structpb.Value_NumberValue)
				if !ok {
					t.Errorf("Expected number value for field %s, got %T", k, fieldVal.GetKind())
					continue
				}
				if numVal.NumberValue != expectedVal {
					t.Errorf("Expected %v for key %s, got %v", expectedVal, k, numVal.NumberValue)
				}
			case bool:
				boolVal, ok := fieldVal.GetKind().(*structpb.Value_BoolValue)
				if !ok {
					t.Errorf("Expected bool value for field %s, got %T", k, fieldVal.GetKind())
					continue
				}
				if boolVal.BoolValue != expectedVal {
					t.Errorf("Expected %v for key %s, got %v", expectedVal, k, boolVal.BoolValue)
				}
			case string:
				strVal, ok := fieldVal.GetKind().(*structpb.Value_StringValue)
				if !ok {
					t.Errorf("Expected string value for field %s, got %T", k, fieldVal.GetKind())
					continue
				}
				if strVal.StringValue != expectedVal {
					t.Errorf("Expected %v for key %s, got %v", expectedVal, k, strVal.StringValue)
				}
			}
		}
	}

	// Check dates array exists and has correct length
	datesField, exists := result["dates"]
	if !exists {
		t.Errorf("Expected 'dates' field to exist in result")
		return
	}

	listVal, ok := datesField.GetKind().(*structpb.Value_ListValue)
	if !ok {
		t.Errorf("Expected dates array, got %T", datesField.GetKind())
		return
	}

	dates := listVal.ListValue.GetValues()
	expectedDates, ok := expectedMap["dates"].([]interface{})
	if !ok {
		t.Errorf("Expected dates array in expected value, got %T", expectedMap["dates"])
		return
	}

	if len(dates) != len(expectedDates) {
		t.Errorf("Expected %d dates, got %d", len(expectedDates), len(dates))
	}
}

// Note: Other assertion functions (assertStructpbValueIsFloat64Slice, assertStructpbValueIsString,
// assertStructpbValueIsBool) are defined in vm_runner_customcode_test.go and used via the wrapper
// functions above.
