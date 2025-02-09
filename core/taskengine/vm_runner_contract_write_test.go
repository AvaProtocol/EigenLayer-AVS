package taskengine

import (
	"log"
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestContractWriteSimpleReturn(t *testing.T) {
	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	node := &avsproto.ContractWriteNode{
		ContractAddress: "0x0a0c037267a690e9792f4660c29989babec9cffb",
		CallData:        "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8",
	}
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123",
			Name: "contractQuery",
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
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "triggertestid",
			Target: "123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	client, _ := ethclient.Dial(smartWalletConfig.EthRpcUrl)

	if err != nil {
		log.Fatalf("error connectiong to websocket: %v", err)
	}
	defer func() {
		client.Close()
	}()
	n := NewContractWriteProcessor(
		vm,
		client,
		smartWalletConfig,

		common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5"),
	)

	step, err := n.Execute("123", node)

	if err != nil {
		t.Errorf("expected contract write node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract write node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "prepare to send userops target contract 0x0a0c037267a690e9792f4660c29989babec9cffb") {
		t.Errorf("expected log contains request trace data but found: %s", step.Log)
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}
	if len(step.OutputData) != 66 && !strings.HasPrefix(step.OutputData, "0x") {
		t.Errorf("output data isn't a valid userops hash: %s", step.OutputData)
	}
}
