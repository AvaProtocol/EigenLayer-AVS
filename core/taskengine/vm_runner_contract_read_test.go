package taskengine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestContractReadSimpleReturn(t *testing.T) {
	node := &avsproto.ContractReadNode{
		ContractAddress: "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
		CallData:        "0x70a08231000000000000000000000000ce289bb9fb0a9591317981223cbe33d5dc42268d",
		ContractAbi:     `[{"inputs":[{"internalType":"address","name":"account","type":"address"}],"name":"balanceOf","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`,
		Method:          "balanceOf",
	}
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123",
			Name: "contractQuery",
			TaskType: &avsproto.TaskNode_ContractRead{
				ContractRead: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id: "trigger123",
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "trigger123",
			Target: "123",
		},
	}

	vm, err := NewVMWithData("123", trigger, nil, nodes, edges)

	n := NewContractReadProcessor(vm, testutil.GetRpcClient())

	step, err := n.Execute("123", node)

	if err != nil {
		t.Errorf("expected contract read node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract read node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Call balanceOf on 0x1c7d4b196cb0c7b01d743fbc6116a902379c7238 at") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}
	if step.OutputData != "[313131]" {
		t.Errorf("read balanceOf doesn't return right data. expect [313131], got %s", step.OutputData)
	}
}

func TestContractReadComplexReturn(t *testing.T) {
	node := &avsproto.ContractReadNode{
		ContractAddress: "0xc59E3633BAAC79493d908e63626716e204A45EdF",
		CallData:        "0x9a6fc8f500000000000000000000000000000000000000000000000100000000000052e7",
		ContractAbi:     `[{"inputs":[{"internalType":"uint80","name":"_roundId","type":"uint80"}],"name":"getRoundData","outputs":[{"internalType":"uint80","name":"roundId","type":"uint80"},{"internalType":"int256","name":"answer","type":"int256"},{"internalType":"uint256","name":"startedAt","type":"uint256"},{"internalType":"uint256","name":"updatedAt","type":"uint256"},{"internalType":"uint80","name":"answeredInRound","type":"uint80"}],"stateMutability":"view","type":"function"}]`,
		Method:          "getRoundData",
	}

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
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
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData("123abc", trigger, nil, nodes, edges)
	n := NewContractReadProcessor(vm, testutil.GetRpcClient())
	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected contract read node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract read node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Call getRoundData on 0xc59E3633BAAC79493d908e63626716e204A45EdF at") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	var data []any
	json.Unmarshal([]byte(step.OutputData), &data)
	if len(data) < 5 {
		t.Errorf("contract read doesn't return right data, wrong length. expect 5, got %d", len(data))
	}

	roundIdExpected := float64(18446744073709572839)
	roundId := data[0].(float64)
	if roundIdExpected != roundId {
		t.Errorf("contract read returns incorrect data expect %f got %f", roundIdExpected, roundId)
	}
	if data[1].(float64) != float64(2189300000) {
		t.Errorf("contract read returns incorrect data expect %d got %f", 2189300000, data[1])
	}
	if data[2].(float64) != 1733878404 {
		t.Errorf("contract read returns incorrect data expect %d got %f", 2189300000, data[1])
	}
	if data[3].(float64) != 1733878404 {
		t.Errorf("contract read returns incorrect data expect %d got %f", 2189300000, data[1])
	}

	if data[4].(float64) != float64(18446744073709572839) {
		t.Errorf("contract read returns incorrect data expect %d got %f", 2189300000, data[1])
	}

}
