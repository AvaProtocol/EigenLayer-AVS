package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestLoopProcessor_Execute_Sequential(t *testing.T) {
	vm := NewVM()
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1, 2, 3}
	vm.vars["testArray"] = map[string]interface{}{
		"data": testArray,
	}

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return value",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "testArray",
		IterVal: "value",
		IterKey: "index",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, executionLog.OutputData)

	outputVar := vm.GetOutputVar("test_loop")
	assert.NotNil(t, outputVar)

	results, ok := outputVar.([]interface{})
	assert.True(t, ok)
	assert.Equal(t, len(testArray), len(results))

	for i, result := range results {
		assert.Equal(t, testArray[i], result)
	}
}

func TestLoopProcessor_Execute_Parallel(t *testing.T) {
	vm := NewVM()
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	vm.vars["testArray"] = map[string]interface{}{
		"data": testArray,
	}

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return value",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "testArray",
		IterVal: "value",
		IterKey: "index",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, executionLog.OutputData)

	outputVar := vm.GetOutputVar("test_loop")
	assert.NotNil(t, outputVar)

	results, ok := outputVar.([]interface{})
	assert.True(t, ok)
	assert.Equal(t, len(testArray), len(results))

	resultSet := make(map[interface{}]bool)
	for _, result := range results {
		resultSet[result] = true
	}

	for _, input := range testArray {
		assert.True(t, resultSet[input])
	}
}

func TestLoopProcessor_Execute_EmptyArray(t *testing.T) {
	vm := NewVM()
	processor := NewLoopProcessor(vm)

	vm.vars["emptyArray"] = map[string]interface{}{
		"data": []interface{}{},
	}

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return value",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "emptyArray",
		IterVal: "value",
		IterKey: "index",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success)
	assert.NotNil(t, executionLog.OutputData)

	outputVar := vm.GetOutputVar("test_loop")
	assert.NotNil(t, outputVar)

	results, ok := outputVar.([]interface{})
	assert.True(t, ok)
	assert.Equal(t, 0, len(results))
}

func TestLoopProcessor_Execute_InvalidInput(t *testing.T) {
	vm := NewVM()
	processor := NewLoopProcessor(vm)

	vm.vars["notArray"] = map[string]interface{}{
		"data": "string value",
	}

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return value",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "notArray",
		IterVal: "value",
		IterKey: "index",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop", loopNode)

	assert.Error(t, err)
	assert.False(t, executionLog.Success)
	assert.Contains(t, executionLog.Error, "is not an array")
}

func TestLoopProcessor_Execute_MissingInput(t *testing.T) {
	vm := NewVM()
	processor := NewLoopProcessor(vm)

	customCode := &avsproto.CustomCodeNode{
		Lang:   avsproto.CustomCodeLang_JavaScript,
		Source: "return value",
	}

	loopNode := &avsproto.LoopNode{
		Input:   "nonExistentArray",
		IterVal: "value",
		IterKey: "index",
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop", loopNode)

	assert.Error(t, err)
	assert.False(t, executionLog.Success)
	assert.Contains(t, executionLog.Error, "not found")
}
