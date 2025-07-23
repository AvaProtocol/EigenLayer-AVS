package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

// createMockTaskNode is a helper function to create mock TaskNodes for testing
func createMockTaskNode(id, name string, nodeType avsproto.NodeType) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		Id:   id,
		Name: name,
		Type: nodeType,
	}
}

func TestLoopProcessor_Execute_Sequential(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	// Create a mock data generation node for testing
	dataGenNode := createMockTaskNode("test_data_gen", "testArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen",
			IterVal:       "value",
			IterKey:       "index",
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
	dataGenNode := createMockTaskNode("test_data_gen", "testArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen",
			IterVal:       "value",
			IterKey:       "index",
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
	dataGenNode := createMockTaskNode("test_data_gen_empty", "emptyArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen_empty"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen_empty",
			IterVal:       "value",
			IterKey:       "index",
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
	dataGenNode := createMockTaskNode("test_data_gen_invalid", "notArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen_invalid"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen_invalid",
			IterVal:       "value",
			IterKey:       "index",
		},
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_invalid_input", loopNode)

	assert.Error(t, err, "Expected an error for invalid input type")
	assert.False(t, executionLog.Success, "Execution should not be successful")
	assert.Contains(t, executionLog.Error, "expected array", "Error message should indicate input must be an array")
}

func TestLoopProcessor_Execute_MissingInput(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	// Create a mock data generation node for testing (but don't add corresponding VM variable)
	dataGenNode := createMockTaskNode("test_data_gen_missing", "nonExistentArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen_missing"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen_missing",
			IterVal:       "value",
			IterKey:       "index",
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

func TestLoopProcessor_Execute_ParallelMode(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0, 4.0, 5.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	// Create a mock data generation node for testing
	dataGenNode := createMockTaskNode("test_data_gen", "testArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value * 2;", // Simple operation for testing
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen",
			IterVal:       "value",
			IterKey:       "index",
			ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL,
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
	assert.Contains(t, executionLog.Log, "parallel mode", "Log should indicate parallel execution")

	outputVar := processor.GetOutputVar("test_loop_parallel")
	assert.NotNil(t, outputVar, "Output variable should not be nil")

	results, ok := outputVar.([]interface{})
	assert.True(t, ok, "Output variable should be a slice of interfaces")
	assert.Equal(t, len(testArray), len(results), "Length of results should match testArray")
}

func TestLoopProcessor_Execute_SequentialMode(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0, 4.0, 5.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	// Create a mock data generation node for testing
	dataGenNode := createMockTaskNode("test_data_gen", "testArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value * 3;", // Simple operation for testing
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen",
			IterVal:       "value",
			IterKey:       "index",
			ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL,
		},
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_sequential", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success, "Execution should be successful")
	assert.Empty(t, executionLog.Error, "Error message should be empty on success")
	assert.NotNil(t, executionLog.OutputData, "OutputData should not be nil")
	assert.Contains(t, executionLog.Log, "sequential mode", "Log should indicate sequential execution")

	outputVar := processor.GetOutputVar("test_loop_sequential")
	assert.NotNil(t, outputVar, "Output variable should not be nil")

	results, ok := outputVar.([]interface{})
	assert.True(t, ok, "Output variable should be a slice of interfaces")
	assert.Equal(t, len(testArray), len(results), "Length of results should match testArray")
}

func TestLoopProcessor_Execute_ContractWriteAlwaysSequential(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	// Create a mock data generation node for testing
	dataGenNode := createMockTaskNode("test_data_gen", "testArray", avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE)
	vm.TaskNodes["test_data_gen"] = dataGenNode

	contractWrite := &avsproto.ContractWriteNode{
		Config: &avsproto.ContractWriteNode_Config{
			ContractAddress: "0x1234567890123456789012345678901234567890",
			CallData:        "0x",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen",
			IterVal:       "value",
			IterKey:       "index",
			ExecutionMode: avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL, // Set to parallel
		},
		Runner: &avsproto.LoopNode_ContractWrite{
			ContractWrite: contractWrite,
		},
	}

	executionLog, _ := processor.Execute("test_loop_contract_write", loopNode)

	// ContractWrite operations may fail due to missing smart wallet config, but the important part
	// is that it should attempt sequential execution despite parallel being requested
	assert.NotNil(t, executionLog, "Execution log should not be nil")
	assert.Contains(t, executionLog.Log, "sequentially due to contract write operation (security requirement)",
		"Log should indicate forced sequential execution for contract write")
}

func TestLoopProcessor_Execute_DefaultExecutionMode(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())
	processor := NewLoopProcessor(vm)

	testArray := []interface{}{1.0, 2.0, 3.0}
	vm.AddVar("testArray", map[string]interface{}{
		"data": testArray,
	})

	// Create a mock data generation node for testing
	dataGenNode := createMockTaskNode("test_data_gen", "testArray", avsproto.NodeType_NODE_TYPE_CUSTOM_CODE)
	vm.TaskNodes["test_data_gen"] = dataGenNode

	customCode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return value;",
		},
	}

	loopNode := &avsproto.LoopNode{
		Config: &avsproto.LoopNode_Config{
			InputNodeName: "test_data_gen",
			IterVal:       "value",
			IterKey:       "index",
			// ExecutionMode not set - should default to sequential (safer default)
		},
		Runner: &avsproto.LoopNode_CustomCode{
			CustomCode: customCode,
		},
	}

	executionLog, err := processor.Execute("test_loop_default", loopNode)

	assert.NoError(t, err)
	assert.True(t, executionLog.Success, "Execution should be successful")
	assert.Contains(t, executionLog.Log, "sequential mode", "Log should indicate sequential execution (default)")
}
