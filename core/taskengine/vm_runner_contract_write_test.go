package taskengine

import (
	"log"
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
	node := &avsproto.ContractWriteNode{
		ContractAddress: baseSepoliaUsdcAddress.Hex(),
		CallData:        "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000",
	}
	nodes := []*avsproto.TaskNode{
		{
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
		{
			Id:     "e1",
			Source: "triggertestid",
			Target: "123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
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
		t.Errorf("Test skipped: Contract write operation could not be completed due to connection issues")
		return
	}

	if len(outputData.TxReceipt.Hash) != 66 {
		t.Errorf("Missing Tx Hash in the output data")
	}

	// Verify all transaction receipt fields
	if outputData.TxReceipt.BlockHash == "" {
		t.Errorf("Missing BlockHash in the output data")
	}

	if outputData.TxReceipt.BlockNumber == 0 {
		t.Errorf("Missing BlockNumber in the output data")
	}

	if outputData.TxReceipt.From == "" {
		t.Errorf("Missing From address in the output data")
	}

	if outputData.TxReceipt.To == "" {
		t.Errorf("Missing To address in the output data")
	}

	if outputData.TxReceipt.GasUsed == 0 {
		t.Errorf("Missing GasUsed in the output data")
	}

	if outputData.TxReceipt.GasPrice == 0 {
		t.Errorf("Missing GasPrice in the output data")
	}

	if outputData.TxReceipt.CumulativeGasUsed == 0 {
		t.Errorf("Missing CumulativeGasUsed in the output data")
	}

	if outputData.TxReceipt.Fee == 0 {
		t.Errorf("Missing Fee in the output data")
	}

	if outputData.TxReceipt.ContractAddress == "" {
		t.Errorf("Missing ContractAddress in the output data")
	}

	if outputData.TxReceipt.Index == 0 {
		t.Errorf("Missing Index in the output data")
	}

	if outputData.TxReceipt.Logs == nil {
		t.Errorf("Missing Logs in the output data")
	}

	if outputData.TxReceipt.LogsBloom == "" {
		t.Errorf("Missing LogsBloom in the output data")
	}

	// Root is optional in modern Ethereum, only used in pre-Byzantium hard forks
	// if outputData.TxReceipt.Root == "" {
	//     t.Errorf("Missing Root in the output data")
	// }

	if outputData.TxReceipt.Status == 0 {
		t.Errorf("Missing Status in the output data")
	}

	if outputData.TxReceipt.Type == 0 {
		t.Errorf("Missing Type in the output data")
	}

	// BlobGasPrice and BlobGasUsed are optional fields, so we don't check them
}
