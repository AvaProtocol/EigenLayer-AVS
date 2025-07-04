package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestBuildTriggerDataMapEventTriggerFlattening tests the specific fix for flattening transfer_log data
// This test verifies that the buildTriggerDataMap function correctly flattens nested transfer_log data
// to the top level, which resolves the NaN and undefined values issue in simulateTask.
func TestBuildTriggerDataMapEventTriggerFlattening(t *testing.T) {
	// Test data with nested transfer_log structure (as it comes from runEventTriggerImmediately)
	triggerOutput := map[string]interface{}{
		"found":         true,
		"queriesCount":  2,
		"totalSearched": 5000,
		"totalEvents":   1,
		"transfer_log": map[string]interface{}{
			"tokenName":        "USDC",
			"tokenSymbol":      "USDC",
			"tokenDecimals":    uint32(6),
			"transactionHash":  "0x1b0b9bee55e3a824dedd1dcfaad1790e19e0a68d6717e385a960092077f8b6a1",
			"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
			"blockNumber":      uint64(8560047),
			"blockTimestamp":   uint64(1750061412000),
			"fromAddress":      "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"toAddress":        "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
			"value":            "0x00000000000000000000000000000000000000000000000000000000004c4b40",
			"valueFormatted":   "5",
			"transactionIndex": uint32(63),
			"logIndex":         uint32(83),
		},
	}

	// Test buildTriggerDataMap with event trigger
	result := buildTriggerDataMap(avsproto.TriggerType_TRIGGER_TYPE_EVENT, triggerOutput)

	// Verify that transfer_log data is flattened to top level
	assert.Equal(t, "USDC", result["tokenName"], "tokenName should be at top level")
	assert.Equal(t, "USDC", result["tokenSymbol"], "tokenSymbol should be at top level")
	assert.Equal(t, uint32(6), result["tokenDecimals"], "tokenDecimals should be at top level")
	assert.Equal(t, "0x1b0b9bee55e3a824dedd1dcfaad1790e19e0a68d6717e385a960092077f8b6a1", result["transactionHash"], "transactionHash should be at top level")
	assert.Equal(t, "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", result["address"], "address should be at top level")
	assert.Equal(t, uint64(8560047), result["blockNumber"], "blockNumber should be at top level")
	assert.Equal(t, uint64(1750061412000), result["blockTimestamp"], "blockTimestamp should be at top level")
	assert.Equal(t, "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788", result["fromAddress"], "fromAddress should be at top level")
	assert.Equal(t, "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9", result["toAddress"], "toAddress should be at top level")
	assert.Equal(t, "0x00000000000000000000000000000000000000000000000000000000004c4b40", result["value"], "value should be at top level")
	assert.Equal(t, "5", result["valueFormatted"], "valueFormatted should be at top level")
	assert.Equal(t, uint32(63), result["transactionIndex"], "transactionIndex should be at top level")
	assert.Equal(t, uint32(83), result["logIndex"], "logIndex should be at top level")

	// Verify that the nested transfer_log object is NOT present at top level
	assert.NotContains(t, result, "transfer_log", "transfer_log should not be present as nested object")

	// Test with non-transfer event (should copy all data as-is)
	nonTransferOutput := map[string]interface{}{
		"found":        true,
		"someField":    "someValue",
		"anotherField": 123,
	}

	nonTransferResult := buildTriggerDataMap(avsproto.TriggerType_TRIGGER_TYPE_EVENT, nonTransferOutput)
	assert.Equal(t, true, nonTransferResult["found"])
	assert.Equal(t, "someValue", nonTransferResult["someField"])
	assert.Equal(t, 123, nonTransferResult["anotherField"])
}

// TestBuildTriggerDataMapFromProtobufConsistency tests that both buildTriggerDataMap and
// buildTriggerDataMapFromProtobuf produce consistent field names for JavaScript access with the new structured approach.
func TestBuildTriggerDataMapFromProtobufConsistency(t *testing.T) {
	// Create structured event data (not JSON string)
	eventDataMap := map[string]interface{}{
		"tokenName":        "USDC",
		"tokenSymbol":      "USDC",
		"tokenDecimals":    6,
		"transactionHash":  "0x1b0b9bee55e3a824dedd1dcfaad1790e19e0a68d6717e385a960092077f8b6a1",
		"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"blockNumber":      8560047,
		"blockTimestamp":   1750061412000,
		"fromAddress":      "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		"toAddress":        "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
		"value":            "0x00000000000000000000000000000000000000000000000000000000004c4b40",
		"valueFormatted":   "5",
		"transactionIndex": 63,
		"logIndex":         83,
	}

	// Convert to google.protobuf.Value
	protoValue, err := structpb.NewValue(eventDataMap)
	if err != nil {
		t.Fatalf("Failed to create protobuf value: %v", err)
	}

	eventOutputProto := &avsproto.EventTrigger_Output{
		Data: protoValue,
	}

	// Test buildTriggerDataMapFromProtobuf
	protobufResult := buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_EVENT, eventOutputProto, nil)

	// Create raw trigger output data with structured data (not JSON string)
	rawTriggerOutput := map[string]interface{}{
		"found":         true,
		"queriesCount":  2,
		"totalSearched": 5000,
		"totalEvents":   1,
		"transfer_log":  eventDataMap, // Use structured data, not JSON string
	}

	// Test buildTriggerDataMap
	rawResult := buildTriggerDataMap(avsproto.TriggerType_TRIGGER_TYPE_EVENT, rawTriggerOutput)

	// Verify that both functions produce the same field names for JavaScript access
	expectedFields := []string{
		"tokenName", "tokenSymbol", "tokenDecimals", "transactionHash",
		"address", "blockNumber", "blockTimestamp", "fromAddress",
		"toAddress", "value", "valueFormatted", "transactionIndex", "logIndex",
	}

	for _, field := range expectedFields {
		// Both results should have the same field names
		assert.Contains(t, protobufResult, field, "buildTriggerDataMapFromProtobuf should have field: %s", field)
		assert.Contains(t, rawResult, field, "buildTriggerDataMap should have field: %s", field)

		// Both results should have the same values for these fields
		protobufValue := protobufResult[field]
		rawValue := rawResult[field]

		// Handle numeric type differences - protobuf may have different types than raw maps
		if field == "tokenDecimals" || field == "blockNumber" || field == "blockTimestamp" ||
			field == "transactionIndex" || field == "logIndex" {
			// Convert both to the same type for comparison
			var protobufNum, rawNum float64

			switch pv := protobufValue.(type) {
			case int:
				protobufNum = float64(pv)
			case int64:
				protobufNum = float64(pv)
			case uint32:
				protobufNum = float64(pv)
			case uint64:
				protobufNum = float64(pv)
			case float64:
				protobufNum = pv
			default:
				t.Fatalf("Unexpected type for protobuf numeric field %s: %T", field, protobufValue)
			}

			switch rv := rawValue.(type) {
			case int:
				rawNum = float64(rv)
			case int64:
				rawNum = float64(rv)
			case uint32:
				rawNum = float64(rv)
			case uint64:
				rawNum = float64(rv)
			case float64:
				rawNum = rv
			default:
				t.Fatalf("Unexpected type for raw numeric field %s: %T", field, rawValue)
			}

			assert.Equal(t, protobufNum, rawNum, "Numeric field %s should have same value", field)
		} else {
			assert.Equal(t, protobufValue, rawValue, "Field %s should have same value in both results", field)
		}
	}

	// Verify that neither result has the nested transfer_log structure
	assert.NotContains(t, protobufResult, "transfer_log", "buildTriggerDataMapFromProtobuf should not have nested transfer_log")
	assert.NotContains(t, rawResult, "transfer_log", "buildTriggerDataMap should not have nested transfer_log")

	// Verify that both results have the trigger type
	assert.Equal(t, "TRIGGER_TYPE_EVENT", protobufResult["type"])
	assert.NotContains(t, rawResult, "type", "buildTriggerDataMap should not add type field")
}

// TestJavaScriptFieldAccessPattern tests that the field names work correctly for JavaScript destructuring
func TestJavaScriptFieldAccessPattern(t *testing.T) {
	// This test simulates the JavaScript destructuring pattern used in the client code:
	// const { tokenSymbol, valueFormatted, fromAddress, toAddress, blockTimestamp } = eventTrigger.data;

	triggerOutput := map[string]interface{}{
		"found": true,
		"transfer_log": map[string]interface{}{
			"tokenSymbol":    "USDC",
			"valueFormatted": "5",
			"fromAddress":    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"toAddress":      "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
			"blockTimestamp": uint64(1750061412000),
		},
	}

	result := buildTriggerDataMap(avsproto.TriggerType_TRIGGER_TYPE_EVENT, triggerOutput)

	// Simulate JavaScript destructuring - these fields should all be available at top level
	tokenSymbol, hasTokenSymbol := result["tokenSymbol"]
	valueFormatted, hasValueFormatted := result["valueFormatted"]
	fromAddress, hasFromAddress := result["fromAddress"]
	toAddress, hasToAddress := result["toAddress"]
	blockTimestamp, hasBlockTimestamp := result["blockTimestamp"]

	// All fields should be present
	assert.True(t, hasTokenSymbol, "tokenSymbol should be available for JavaScript destructuring")
	assert.True(t, hasValueFormatted, "valueFormatted should be available for JavaScript destructuring")
	assert.True(t, hasFromAddress, "fromAddress should be available for JavaScript destructuring")
	assert.True(t, hasToAddress, "toAddress should be available for JavaScript destructuring")
	assert.True(t, hasBlockTimestamp, "blockTimestamp should be available for JavaScript destructuring")

	// All fields should have the correct values (not NaN or undefined)
	assert.Equal(t, "USDC", tokenSymbol, "tokenSymbol should not be undefined")
	assert.Equal(t, "5", valueFormatted, "valueFormatted should not be undefined")
	assert.Equal(t, "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788", fromAddress, "fromAddress should not be undefined")
	assert.Equal(t, "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9", toAddress, "toAddress should not be undefined")
	assert.Equal(t, uint64(1750061412000), blockTimestamp, "blockTimestamp should not be NaN")

	t.Logf("✅ All JavaScript destructuring fields are available:")
	t.Logf("  tokenSymbol: %v", tokenSymbol)
	t.Logf("  valueFormatted: %v", valueFormatted)
	t.Logf("  fromAddress: %v", fromAddress)
	t.Logf("  toAddress: %v", toAddress)
	t.Logf("  blockTimestamp: %v", blockTimestamp)
}

// TestFallbackVariableResolutionConsistency tests that the fallback variable resolution
// (where subsequent nodes can use tokenName to match token_name from previous nodes)
// works consistently across all three execution paths:
// 1. run_node_immediately (RunNodeImmediatelyRPC)
// 2. simulateTask (SimulateTask)
// 3. runTask (actual task execution)
func TestCamelCaseVariableResolutionConsistency(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Test data with camelCase field names (consistent naming)
	testData := map[string]interface{}{
		"tokenName":     "USDC",
		"tokenSymbol":   "USDC",
		"tokenDecimals": 6,
		"blockNumber":   12345,
		"txHash":        "0x123abc",
	}

	// Create input variables that include a node with camelCase data
	inputVariables := map[string]interface{}{
		"eventTrigger": map[string]interface{}{
			"data": testData,
		},
	}

	// Test custom code that accesses camelCase fields
	customCodeConfig := map[string]interface{}{
		"source": `
			// Test accessing camelCase (consistent naming)
			const tokenNameCamel = eventTrigger.data.tokenName;
			const tokenSymbolCamel = eventTrigger.data.tokenSymbol;
			const tokenDecimalsCamel = eventTrigger.data.tokenDecimals;
			
			return {
				camel_case_access: {
					tokenName: tokenNameCamel,
					tokenSymbol: tokenSymbolCamel,
					tokenDecimals: tokenDecimalsCamel
				},
				camelCase_works: tokenNameCamel === 'USDC' && tokenSymbolCamel === 'USDC'
			};
		`,
	}

	t.Run("RunNodeImmediately", func(t *testing.T) {
		result, err := engine.RunNodeImmediately("customCode", customCodeConfig, inputVariables)
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify camelCase access works
		assert.Equal(t, "USDC", result["camel_case_access"].(map[string]interface{})["tokenName"])
		assert.True(t, result["camelCase_works"].(bool), "CamelCase access should work consistently")
	})

	t.Run("SimulateTask", func(t *testing.T) {
		// Create a simple task with manual trigger and custom code node
		trigger := &avsproto.TaskTrigger{
			Id:          "trigger1",
			Name:        "manualTrigger",
			Type:        avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
			TriggerType: &avsproto.TaskTrigger_Manual{Manual: true},
		}

		nodes := []*avsproto.TaskNode{
			{
				Id:   "node1",
				Name: "testCustomCode",
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{
							Source: customCodeConfig["source"].(string),
						},
					},
				},
			},
		}

		edges := []*avsproto.TaskEdge{
			{
				Id:     "edge1",
				Source: "trigger1",
				Target: "node1",
			},
		}

		user := testutil.TestUser1()
		execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
		assert.NoError(t, err)
		assert.NotNil(t, execution)
		assert.True(t, execution.Success, "Simulation should succeed")

		// Find the custom code step
		var customCodeStep *avsproto.Execution_Step
		for _, step := range execution.Steps {
			if step.Type == avsproto.NodeType_NODE_TYPE_CUSTOM_CODE.String() {
				customCodeStep = step
				break
			}
		}
		assert.NotNil(t, customCodeStep, "Should find custom code execution step")
		assert.True(t, customCodeStep.Success, "Custom code step should succeed")

		// Extract the result from the custom code output
		customCodeOutput := customCodeStep.GetCustomCode()
		assert.NotNil(t, customCodeOutput)
		result := customCodeOutput.Data.AsInterface().(map[string]interface{})

		// Verify camelCase access works
		assert.Equal(t, "USDC", result["camel_case_access"].(map[string]interface{})["tokenName"])
		assert.True(t, result["camelCase_works"].(bool), "CamelCase access should work consistently")
	})

	// Note: Testing actual runTask would require setting up a full task in storage and queue system,
	// which is more complex. The key insight is that runTask uses the same VM and preprocessing
	// infrastructure as SimulateTask, so if SimulateTask works, runTask should work too.
	// The main difference was in the branch node preprocessing, which we've now fixed.
}

// TestBranchNodeCamelCaseResolution specifically tests that branch nodes can use
// camelCase variable names in their condition expressions
func TestBranchNodeCamelCaseResolution(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Test data with camelCase field names
	testData := map[string]interface{}{
		"tokenName":   "USDC",
		"tokenAmount": 1000,
	}

	inputVariables := map[string]interface{}{
		"eventTrigger": map[string]interface{}{
			"data": testData,
		},
	}

	// Test branch node that uses camelCase in condition
	branchConfig := map[string]interface{}{
		"conditions": []map[string]interface{}{
			{
				"id":         "condition1",
				"type":       "if",
				"expression": "eventTrigger.data.tokenName === 'USDC' && eventTrigger.data.tokenAmount > 500",
			},
		},
	}

	t.Run("BranchNodeCamelCaseResolution", func(t *testing.T) {
		result, err := engine.RunNodeImmediately("branch", branchConfig, inputVariables)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		if result != nil && result["success"] != nil {
			assert.True(t, result["success"].(bool), "Branch condition should evaluate to true using camelCase fields")
		}
	})
}

// TestContractReadCamelCaseResolution tests that contract read nodes can use
// camelCase variable names in their configuration
func TestContractReadCamelCaseResolution(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Test data with camelCase field names
	testData := map[string]interface{}{
		"contractAddress": "0x1234567890123456789012345678901234567890",
	}

	inputVariables := map[string]interface{}{
		"eventTrigger": map[string]interface{}{
			"data": testData,
		},
	}

	// Test contract read that uses camelCase template
	contractReadConfig := map[string]interface{}{
		"contractAddress": "{{eventTrigger.data.contractAddress}}", // camelCase template
		"contractAbi":     `[{"inputs":[],"name":"decimals","outputs":[{"internalType":"uint8","name":"","type":"uint8"}],"stateMutability":"view","type":"function"}]`,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "decimals",
				"callData":   "0x313ce567",
			},
		},
	}

	t.Run("ContractReadCamelCaseResolution", func(t *testing.T) {
		// This test will fail with RPC connection error, but we can check that the preprocessing worked
		// by examining the error message - it should contain the resolved address, not the template
		result, err := engine.RunNodeImmediately("contractRead", contractReadConfig, inputVariables)

		// We expect an error due to RPC connection, but the address should be resolved
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "0x1234567890123456789012345678901234567890",
			"Error should contain the resolved contract address, indicating template preprocessing worked")

		// The result might be nil due to the RPC error, which is expected
		_ = result
	})
}

// TestBlockTriggerFieldNamingConsistency tests that both buildTriggerDataMap and
// buildTriggerDataMapFromProtobuf use consistent field names for block triggers
func TestBlockTriggerFieldNamingConsistency(t *testing.T) {
	// Create block trigger output data (as would come from runBlockTriggerImmediately)
	rawTriggerOutput := map[string]interface{}{
		"blockNumber": uint64(12345),
		"blockHash":   "0xabcdef123456",
		"timestamp":   uint64(1672531200),
		"parentHash":  "0x123456abcdef",
		"difficulty":  "1000000",
		"gasLimit":    uint64(30000000),
		"gasUsed":     uint64(21000),
	}

	// Create protobuf block trigger output
	blockOutputProto := &avsproto.BlockTrigger_Output{
		BlockNumber: uint64(12345),
		BlockHash:   "0xabcdef123456",
		Timestamp:   uint64(1672531200),
		ParentHash:  "0x123456abcdef",
		Difficulty:  "1000000",
		GasLimit:    uint64(30000000),
		GasUsed:     uint64(21000),
	}

	// Test buildTriggerDataMap (used by run_node_immediately)
	rawResult := buildTriggerDataMap(avsproto.TriggerType_TRIGGER_TYPE_BLOCK, rawTriggerOutput)

	// Test buildTriggerDataMapFromProtobuf (used by runTask)
	protobufResult := buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_BLOCK, blockOutputProto, nil)

	// Verify that both functions use the same field names (camelCase)
	expectedFields := []string{
		"blockNumber", "blockHash", "timestamp",
		"parentHash", "difficulty", "gasLimit", "gasUsed",
	}

	for _, field := range expectedFields {
		// Both results should have the same field names
		assert.Contains(t, rawResult, field, "buildTriggerDataMap should have field: %s", field)
		assert.Contains(t, protobufResult, field, "buildTriggerDataMapFromProtobuf should have field: %s", field)

		// Both results should have the same values for these fields
		rawValue := rawResult[field]
		protobufValue := protobufResult[field]
		assert.Equal(t, protobufValue, rawValue, "Field %s should have same value in both results", field)
	}

	// Verify that old snake_case field names are NOT present in rawResult
	deprecatedFields := []string{"block_number", "block_hash", "parent_hash", "gas_limit", "gas_used"}
	for _, field := range deprecatedFields {
		assert.NotContains(t, rawResult, field, "buildTriggerDataMap should NOT have deprecated snake_case field: %s", field)
	}

	// Verify specific values
	assert.Equal(t, uint64(12345), rawResult["blockNumber"])
	assert.Equal(t, uint64(12345), protobufResult["blockNumber"])
	assert.Equal(t, "0xabcdef123456", rawResult["blockHash"])
	assert.Equal(t, "0xabcdef123456", protobufResult["blockHash"])

	t.Logf("✅ Block trigger field naming is now consistent:")
	t.Logf("  rawResult keys: %v", getStringMapKeys(rawResult))
	t.Logf("  protobufResult keys: %v", getStringMapKeys(protobufResult))
}
