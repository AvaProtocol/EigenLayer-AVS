package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestContractReadSimpleReturn(t *testing.T) {
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
			ContractAbi:     `[{"inputs":[{"internalType":"address","name":"account","type":"address"}],"name":"balanceOf","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`,
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:   "0x70a08231000000000000000000000000ce289bb9fb0a9591317981223cbe33d5dc42268d",
					MethodName: "balanceOf",
				},
			},
		},
	}
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "contractQuery",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertestid",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: "triggertestid",
			Target: "123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Errorf("failed to create VM: %v", err)
		return
	}

	n := NewContractReadProcessor(vm, testutil.GetRpcClient())

	step, err := n.Execute("123", node)

	if err != nil {
		t.Errorf("expected contract read node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract read node run successfully but failed")
	}

	if !strings.Contains(step.Log, "Call 1: balanceOf on 0x1c7d4b196cb0c7b01d743fbc6116a902379c7238") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	var results []interface{}
	if step.GetContractRead().GetData() != nil {
		// Extract results from the protobuf Value
		if step.GetContractRead().GetData().GetListValue() != nil {
			// Data is an array
			for _, item := range step.GetContractRead().GetData().GetListValue().GetValues() {
				results = append(results, item.AsInterface())
			}
		} else {
			// Data might be a single object, wrap it in an array for consistency
			results = append(results, step.GetContractRead().GetData().AsInterface())
		}
	}

	if len(results) == 0 {
		t.Errorf("expected contract read to return data but got empty results")
		return
	}

	// Get the first result and extract data
	if resultMap, ok := results[0].(map[string]interface{}); ok {
		if data, ok := resultMap["data"].(map[string]interface{}); ok {
			// Find the balance value - it should be the first/only field in balanceOf
			for _, value := range data {
				if valueStr, ok := value.(string); ok && valueStr == "313131" {
					// Found the expected value
					return
				}
			}
			t.Errorf("read balanceOf doesn't return right data. expected 313131 but didn't find it in data: %v", data)
		} else {
			t.Errorf("expected data field in result but got: %v", resultMap)
		}
	} else {
		t.Errorf("expected result to be a map but got: %v", results[0])
	}
}

func TestContractReadComplexReturn(t *testing.T) {
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0xc59E3633BAAC79493d908e63626716e204A45EdF",
			ContractAbi:     `[{"inputs":[{"internalType":"uint80","name":"_roundId","type":"uint80"}],"name":"getRoundData","outputs":[{"internalType":"uint80","name":"roundId","type":"uint80"},{"internalType":"int256","name":"answer","type":"int256"},{"internalType":"uint256","name":"startedAt","type":"uint256"},{"internalType":"uint256","name":"updatedAt","type":"uint256"},{"internalType":"uint80","name":"answeredInRound","type":"uint80"}],"stateMutability":"view","type":"function"}]`,
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:   "0x9a6fc8f500000000000000000000000000000000000000000000000100000000000052e7",
					MethodName: "getRoundData",
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "contractQuery",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Errorf("failed to create VM: %v", err)
		return
	}

	n := NewContractReadProcessor(vm, testutil.GetRpcClient())
	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected contract read node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract read node run successfully but failed")
	}

	if !strings.Contains(step.Log, "Call 1: getRoundData on 0xc59E3633BAAC79493d908e63626716e204A45EdF") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	var results []interface{}
	if step.GetContractRead().GetData() != nil {
		// Extract results from the protobuf Value
		if step.GetContractRead().GetData().GetListValue() != nil {
			// Data is an array
			for _, item := range step.GetContractRead().GetData().GetListValue().GetValues() {
				results = append(results, item.AsInterface())
			}
		} else {
			// Data might be a single object, wrap it in an array for consistency
			results = append(results, step.GetContractRead().GetData().AsInterface())
		}
	}

	if len(results) == 0 {
		t.Errorf("expected contract read to return data but got empty results")
		return
	}

	// Get the first result and extract data
	if resultMap, ok := results[0].(map[string]interface{}); ok {
		if data, ok := resultMap["data"].(map[string]interface{}); ok {
			if len(data) < 5 {
				t.Errorf("contract read doesn't return right data, wrong length. expect 5 fields, got %d fields", len(data))
				return
			}

			// When reading data out and return over the wire, we have to serialize big int to string.
			// Check specific field values based on the getRoundData function
			expectedValues := map[string]string{
				"roundId":         "18446744073709572839",
				"answer":          "2189300000",
				"startedAt":       "1733878404",
				"updatedAt":       "1733878404",
				"answeredInRound": "18446744073709572839",
			}

			for fieldName, expectedValue := range expectedValues {
				if actualValue, exists := data[fieldName]; exists {
					if actualValueStr, ok := actualValue.(string); ok {
						if actualValueStr != expectedValue {
							t.Errorf("contract read returns incorrect data for field %s: expected %s got %s", fieldName, expectedValue, actualValueStr)
						}
					} else {
						t.Errorf("expected field %s to be string but got %T", fieldName, actualValue)
					}
				} else {
					t.Errorf("expected field %s not found in data: %v", fieldName, data)
				}
			}
		} else {
			t.Errorf("expected data field in result but got: %v", resultMap)
		}
	} else {
		t.Errorf("expected result to be a map but got: %v", results[0])
	}
}

func TestContractReadWithDecimalFormatting(t *testing.T) {
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0xc59E3633BAAC79493d908e63626716e204A45EdF",
			ContractAbi:     `[{"inputs":[],"name":"decimals","outputs":[{"internalType":"uint8","name":"","type":"uint8"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"uint80","name":"_roundId","type":"uint80"}],"name":"getRoundData","outputs":[{"internalType":"uint80","name":"roundId","type":"uint80"},{"internalType":"int256","name":"answer","type":"int256"},{"internalType":"uint256","name":"startedAt","type":"uint256"},{"internalType":"uint256","name":"updatedAt","type":"uint256"},{"internalType":"uint80","name":"answeredInRound","type":"uint80"}],"stateMutability":"view","type":"function"}]`,
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:      "0x313ce567", // decimals()
					MethodName:    "decimals",
					ApplyToFields: []string{"answer"}, // Apply decimal formatting to answer field
				},
				{
					CallData:   "0x9a6fc8f500000000000000000000000000000000000000000000000100000000000052e7",
					MethodName: "getRoundData",
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123decimal",
			Name: "contractQueryWithDecimals",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123decimal",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123decimal",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Errorf("failed to create VM: %v", err)
		return
	}

	n := NewContractReadProcessor(vm, testutil.GetRpcClient())
	step, err := n.Execute("123decimal", node)

	if err != nil {
		t.Errorf("expected contract read node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract read node run successfully but failed")
	}

	var results []interface{}
	if step.GetContractRead().GetData() != nil {
		// Extract results from the protobuf Value
		if step.GetContractRead().GetData().GetListValue() != nil {
			// Data is an array
			for _, item := range step.GetContractRead().GetData().GetListValue().GetValues() {
				results = append(results, item.AsInterface())
			}
		} else {
			// Data might be a single object, wrap it in an array for consistency
			results = append(results, step.GetContractRead().GetData().AsInterface())
		}
	}

	if len(results) == 0 {
		t.Errorf("expected contract read to return data but got empty results")
		return
	}

	// Check that rawStructuredFields is NOT present in the response data
	// This ensures the backend fix is working correctly for simulation
	for i, result := range results {
		if resultMap, ok := result.(map[string]interface{}); ok {
			// Check that rawStructuredFields is not present at the top level of the result
			if _, exists := resultMap["rawStructuredFields"]; exists {
				t.Errorf("rawStructuredFields should not be present in result %d, but found: %v", i, resultMap["rawStructuredFields"])
			}

			// Check that rawStructuredFields is not present in the data field
			if data, ok := resultMap["data"].(map[string]interface{}); ok {
				if _, exists := data["rawStructuredFields"]; exists {
					t.Errorf("rawStructuredFields should not be present in data field of result %d, but found: %v", i, data["rawStructuredFields"])
				}
			}
		}
	}

	// Verify that we have results for both method calls (decimals and getRoundData)
	if len(results) != 2 {
		t.Errorf("expected 2 method results (decimals and getRoundData), got %d", len(results))
		return
	}

	// Find the getRoundData result
	var getRoundDataResult map[string]interface{}
	for _, result := range results {
		if resultMap, ok := result.(map[string]interface{}); ok {
			if methodName, ok := resultMap["methodName"].(string); ok && methodName == "getRoundData" {
				getRoundDataResult = resultMap
				break
			}
		}
	}

	if getRoundDataResult == nil {
		t.Errorf("getRoundData result not found in results")
		return
	}

	// Verify that the actual data fields are present in the getRoundData result
	if data, ok := getRoundDataResult["data"].(map[string]interface{}); ok {
		// Check that expected fields are present
		expectedFields := []string{"roundId", "answer", "startedAt", "updatedAt", "answeredInRound"}
		for _, field := range expectedFields {
			if _, exists := data[field]; !exists {
				t.Errorf("expected field %s not found in data: %v", field, data)
			}
		}
	} else {
		t.Errorf("expected data field in getRoundData result but got: %v", getRoundDataResult)
	}
}
