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

	// Create a mock data generation node for testing
	dataGenNode := &avsproto.TaskNode{
		Id:   "test_data_gen",
		Name: "testArray",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
	}
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return loopItemValueForTest;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			SourceId: "test_data_gen",
			IterVal:  "loopItemValueForTest",
			IterKey:  "index",
		},
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

	// Create a mock data generation node for testing
	dataGenNode := &avsproto.TaskNode{
		Id:   "test_data_gen",
		Name: "testArray",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
	}
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return loopItemValueForTest;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			SourceId: "test_data_gen",
			IterVal:  "loopItemValueForTest",
			IterKey:  "index",
		},
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

	// Create a mock data generation node for testing
	dataGenNode := &avsproto.TaskNode{
		Id:   "test_data_gen_empty",
		Name: "emptyArray",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
	}
	vm.TaskNodes["test_data_gen_empty"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return loopItemValueForTest;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			SourceId: "test_data_gen_empty",
			IterVal:  "loopItemValueForTest",
			IterKey:  "index",
		},
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

	// Create a mock data generation node for testing
	dataGenNode := &avsproto.TaskNode{
		Id:   "test_data_gen_invalid",
		Name: "notArray",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
	}
	vm.TaskNodes["test_data_gen_invalid"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return loopItemValueForTest;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			SourceId: "test_data_gen_invalid",
			IterVal:  "loopItemValueForTest",
			IterKey:  "index",
		},
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

	// Create a mock data generation node for testing (but don't add corresponding VM variable)
	dataGenNode := &avsproto.TaskNode{
		Id:   "test_data_gen_missing",
		Name: "nonExistentArray",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
	}
	vm.TaskNodes["test_data_gen_missing"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return loopItemValueForTest;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			SourceId: "test_data_gen_missing",
			IterVal:  "loopItemValueForTest",
			IterKey:  "index",
		},
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_missing_input", loopNode)

	assert.Error(t, err, "Expected an error for missing input variable")
	assert.False(t, executionLog.Success, "Execution should not be successful")
	assert.Contains(t, executionLog.Error, "not found", "Error message should indicate input variable not found")
}
