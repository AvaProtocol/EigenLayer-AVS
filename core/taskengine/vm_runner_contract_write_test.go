package taskengine

import (
	"log"
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestContractWriteSimpleReturn(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	baseSepoliaUsdcAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")
	node := &avsproto.ContractWriteNode{
		ContractAddress: baseSepoliaUsdcAddress.Hex(),
		CallData:        "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000",
	}
	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "query1",
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
			Id:      "query1",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, smartWalletConfig, nil)
	vm.WithDb(db)

	client, _ := ethclient.Dial(smartWalletConfig.EthRpcUrl)

	if err != nil {
		log.Fatalf("error connectiong to websocket: %v", err)
	}
	defer client.Close()

	n := NewContractWriteProcessor(
		vm,
		client,
		smartWalletConfig,

		common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5"),
	)

	step, err := n.Execute("query1", node)

	if err != nil && strings.Contains(err.Error(), "websocket") {
		t.Logf("Test skipped: Contract write operation could not be completed due to websocket connection issues: %v", err)
		return
	}

	if err != nil {
		t.Errorf("expected contract write node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract write node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "will send message 0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000 to contract 0x036CbD53842c5426634e7929541eC2318f3dCF7e") {
		t.Errorf("expected log contains request trace data but found: %s", step.Log)
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	outputData := step.GetContractWrite()
	if outputData == nil || outputData.TxReceipt == nil {
		t.Logf("Test skipped: Contract write operation could not be completed due to connection issues")
		return
	}
	
	if len(outputData.TxReceipt.Hash) != 66 {
		t.Errorf("Missing Tx Hash in the output data")
	}
}
