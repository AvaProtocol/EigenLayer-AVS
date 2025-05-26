package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestLoopProcessor_Execute_Sequential(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return loopItemValueForTest;",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "testArray",
		IterKey: "index",
		IterVal: "loopItemValueForTest",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_seq", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success, "Execution should be successful")
	assert.Empty(t, executionLog.Error, "Error message should be empty on success")
	assert.NotNil(t, executionLog.OutputData, "OutputData should not be nil")

	outputVar := processor.GetOutputVar("test_loop_seq")
	assert.NotNil(t, outputVar, "Output variable from GetOutputVar should not be nil")

	results, ok := outputVar.([]interface{})
	assert.True(t, ok, "Output variable should be a slice of interfaces")
	assert.Equal(t, len(testArray), len(results), "Length of results should match testArray")

	for i, expected := range testArray {
		assert.Equal(t, expected, results[i], fmt.Sprintf("Sequential test: Expected %v at index %d, got %v", expected, i, results[i]))
	}
}

func TestLoopProcessor_Execute_Parallel(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return loopItemValueForTest;",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "testArray",
		IterKey: "index",
		IterVal: "loopItemValueForTest",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_parallel", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success, "Execution should be successful")
	assert.Empty(t, executionLog.Error, "Error message should be empty on success")
	assert.NotNil(t, executionLog.OutputData, "OutputData should not be nil")

	outputVar := processor.GetOutputVar("test_loop_parallel")
	assert.NotNil(t, outputVar, "Output variable from GetOutputVar should not be nil")

	results, ok := outputVar.([]interface{})
	assert.True(t, ok, "Output variable should be a slice of interfaces")
	assert.Equal(t, len(testArray), len(results), "Length of results should match testArray")

	resultSet := make(map[interface{}]bool)
	for _, result := range results {
		resultSet[result] = true
	}

	for _, input := range testArray {
		assert.True(t, resultSet[input], fmt.Sprintf("Parallel test: Expected result %v not found in results %v", input, results))
	}
}

func TestLoopProcessor_Execute_EmptyArray(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	vm.AddVar("emptyArray", map[string]interface{}{
		"data": []interface{}{},
	})

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return loopItemValueForTest;",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "emptyArray",
		IterKey: "index",
		IterVal: "loopItemValueForTest",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_empty", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, executionLog.OutputData)

	outputVar := processor.GetOutputVar("test_loop_empty")
	assert.NotNil(t, outputVar)

	results, ok := outputVar.([]interface{})
	assert.True(t, ok)
	assert.Equal(t, 0, len(results))
}

func TestLoopProcessor_Execute_InvalidInput(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	vm.AddVar("notArray", map[string]interface{}{
		"data": "string value",
	})

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return loopItemValueForTest;",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "notArray",
		IterKey: "index",
		IterVal: "loopItemValueForTest",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_invalid_input", loopNode)

	assert.Error(t, err, "Expected an error for invalid input type")
	assert.False(t, executionLog.Success, "Execution should not be successful")
	assert.Contains(t, executionLog.Error, "is not an array", "Error message should indicate input is not an array")
}

func TestLoopProcessor_Execute_MissingInput(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return loopItemValueForTest;",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "nonExistentArray",
		IterKey: "index",
		IterVal: "loopItemValueForTest",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_missing_input", loopNode)

	assert.Error(t, err, "Expected an error for missing input variable")
	assert.False(t, executionLog.Success, "Execution should not be successful")
	assert.Contains(t, executionLog.Error, "not found", "Error message should indicate input variable not found")
}
