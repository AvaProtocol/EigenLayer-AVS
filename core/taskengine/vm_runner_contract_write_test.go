package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestContractWriteSimpleReturn(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	baseSepoliaUsdcAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")

	node := &avsproto.ContractWriteNode{}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "query1",
			Name: "contractQuery",
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: node,
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
			Target: "query1",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "TestContractWrite",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, smartWalletConfig, nil)

	if err != nil {
		t.Fatalf("error creating VM: %v", err)
	}

	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())

	vm.AddVar("contract_address", baseSepoliaUsdcAddress.Hex())
	vm.AddVar("call_data", "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000")

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("error connecting to ethereum client: %v", err)
	}
	defer client.Close()

	processor := NewContractWriteProcessor(vm, client, smartWalletConfig, common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5"))
	step, err := processor.Execute("query1", node)

	if err != nil {
		if strings.Contains(err.Error(), "paymaster deposit") ||
			strings.Contains(err.Error(), "insufficient funds") ||
			strings.Contains(err.Error(), "network") ||
			strings.Contains(err.Error(), "connection") {
			t.Skipf("Skipping test due to expected infrastructure issue: %v", err)
		}
		t.Fatalf("unexpected error executing contract write: %v", err)
	}

	if step.Id != "query1" {
		t.Errorf("expected step ID to be 'query1' but got: %s", step.Id)
	}

	if !step.Success {
		t.Errorf("expected contract write node to run successfully but failed with error: %s", step.Error)
	}

	if step.Error != "" {
		t.Errorf("expected no error but got: %s", step.Error)
	}

	contractWriteOutput := step.GetContractWrite()
	if contractWriteOutput == nil {
		t.Errorf("Expected contract write output data but got nil")
		return
	}

	var results []interface{}
	if contractWriteOutput.GetData() != nil {
		// Extract results from the protobuf Value
		if contractWriteOutput.GetData().GetListValue() != nil {
			// Data is an array
			for _, item := range contractWriteOutput.GetData().GetListValue().GetValues() {
				results = append(results, item.AsInterface())
			}
		} else {
			// Data might be a single object, wrap it in an array for consistency
			results = append(results, contractWriteOutput.GetData().AsInterface())
		}
	}

	if len(results) == 0 {
		t.Errorf("Expected at least one result but got none")
		return
	}

	// Get the first result and verify its structure
	if resultMap, ok := results[0].(map[string]interface{}); ok {
		if methodName, ok := resultMap["methodName"].(string); ok && methodName == "" {
			t.Errorf("Expected method name but got empty string")
		}

		if success, ok := resultMap["success"].(bool); ok && !success {
			t.Errorf("Expected successful result but got failure")
		}

		// Check transaction data
		if transaction, ok := resultMap["transaction"].(map[string]interface{}); ok {
			if from, ok := transaction["from"].(string); ok && from == "" {
				t.Errorf("Missing From address in the transaction data")
			}

			if to, ok := transaction["to"].(string); ok && to == "" {
				t.Errorf("Missing To address in the transaction data")
			}

			if hash, ok := transaction["hash"].(string); ok && hash != "" && len(hash) != 66 {
				t.Errorf("Invalid Tx Hash length in the output data, expected 66 chars but got %d", len(hash))
			}
		} else {
			t.Errorf("Expected transaction data but got nil")
			return
		}

		if inputData, ok := resultMap["inputData"].(string); ok && inputData == "" {
			t.Errorf("Missing input data in the result")
		}
	} else {
		t.Errorf("Expected result to be a map but got: %v", results[0])
	}

	expectedLogSubstring := strings.ToLower(baseSepoliaUsdcAddress.Hex())
	if !strings.Contains(strings.ToLower(step.Log), expectedLogSubstring) {
		t.Errorf("expected log to contain contract address '%s' but found: %s", expectedLogSubstring, step.Log)
	}
}
