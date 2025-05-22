package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestJavaScriptWithLodash(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: `
			const _ = require('lodash');
			return _.map([1, 2, 3], n => n * 2);
		`,
	}
	
	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test_lodash",
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	
	if err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	n := NewJSProcessor(vm)
	
	step, err := n.Execute("test_lodash", node)
	
	if err != nil {
		t.Errorf("Expected JavaScript with lodash to run successfully but got error: %v", err)
	}
	
	if !step.Success {
		t.Errorf("Expected JavaScript with lodash to run successfully but failed")
	}
	
	customCodeOutput := step.GetCustomCode().Data
	assertStructpbValueIsFloat64Slice(t, customCodeOutput, []float64{2, 4, 6}, "TestJavaScriptWithLodash: result check failed")
}

func TestJavaScriptWithMoment(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: `
			const moment = require('moment');
			const date = moment('2023-01-01');
			return date.format('YYYY-MM-DD');
		`,
	}
	
	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test_moment",
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	
	if err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	n := NewJSProcessor(vm)
	
	step, err := n.Execute("test_moment", node)
	
	if err != nil {
		t.Errorf("Expected JavaScript with moment to run successfully but got error: %v", err)
	}
	
	if !step.Success {
		t.Errorf("Expected JavaScript with moment to run successfully but failed")
	}
	
	customCodeOutput := step.GetCustomCode().Data
	assertStructpbValueIsString(t, customCodeOutput, "2023-01-01", "TestJavaScriptWithMoment: result check failed")
}

func TestJavaScriptWithUUID(t *testing.T) {
	node := &avsproto.CustomCodeNode{
		Source: `
			const { v4: uuidv4 } = require('uuid');
			const id = uuidv4();
			return typeof id === 'string' && id.length > 0;
		`,
	}
	
	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test_uuid",
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	
	if err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	n := NewJSProcessor(vm)
	
	step, err := n.Execute("test_uuid", node)
	
	if err != nil {
		t.Errorf("Expected JavaScript with uuid to run successfully but got error: %v", err)
	}
	
	if !step.Success {
		t.Errorf("Expected JavaScript with uuid to run successfully but failed")
	}
	
	customCodeOutput := step.GetCustomCode().Data
	assertStructpbValueIsBool(t, customCodeOutput, true, "TestJavaScriptWithUUID: result check failed")
}

func TestModuleImportDetection(t *testing.T) {
	testCases := []struct {
		name     string
		code     string
		expected bool
	}{
		{
			name:     "No module syntax",
			code:     "const a = 1; return a + 2;",
			expected: false,
		},
		{
			name:     "Import statement",
			code:     "import _ from 'lodash'; return _.map([1,2,3], n => n * 2);",
			expected: true,
		},
		{
			name:     "Export statement",
			code:     "const a = 1; export default a;",
			expected: true,
		},
		{
			name:     "Import with whitespace",
			code:     "  import   _   from   'lodash';  ",
			expected: true,
		},
		{
			name:     "Import in comment",
			code:     "// import _ from 'lodash';\nconst a = 1; return a;",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := containsModuleSyntax(tc.code)
			if result != tc.expected {
				t.Errorf("Expected containsModuleSyntax(%q) to be %v, got %v", tc.code, tc.expected, result)
			}
		})
	}
}
