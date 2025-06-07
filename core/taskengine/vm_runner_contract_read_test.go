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

	results := step.GetContractRead().Results
	if len(results) == 0 || len(results[0].Data) == 0 {
		t.Errorf("expected contract read to return data but got empty results")
		return
	}

	// Get the first field value from the first result
	firstValue := results[0].Data[0].Value
	if firstValue != "313131" {
		t.Errorf("read balanceOf doesn't return right data. expect 313131 got %s", firstValue)
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

	results := step.GetContractRead().Results
	if len(results) == 0 || len(results[0].Data) < 5 {
		t.Errorf("contract read doesn't return right data, wrong length. expect 5 fields, got %d results with %d fields", len(results), len(results[0].Data))
		return
	}

	// When reading data out and return over the wire, we have to serialize big int to string.
	roundIdExpected := "18446744073709572839"
	roundId := results[0].Data[0].Value
	if roundIdExpected != roundId {
		t.Errorf("contract read returns incorrect data expect %s got %s", roundIdExpected, roundId)
	}
	if results[0].Data[1].Value != "2189300000" {
		t.Errorf("contract read returns incorrect data expect %s got %s", "2189300000", results[0].Data[1].Value)
	}
	if results[0].Data[2].Value != "1733878404" {
		t.Errorf("contract read returns incorrect data expect %s got %s", "1733878404", results[0].Data[2].Value)
	}
	if results[0].Data[3].Value != "1733878404" {
		t.Errorf("contract read returns incorrect data expect %s got %s", "1733878404", results[0].Data[3].Value)
	}

	if results[0].Data[4].Value != "18446744073709572839" {
		t.Errorf("contract read returns incorrect data expect %s got %s", "18446744073709572839", results[0].Data[4].Value)
	}

}
