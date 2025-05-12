package taskengine

import (
	"log"
	"strings"
	"testing"
	"time"

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
		log.Fatalf("error connecting to websocket: %v", err)
	}
	defer client.Close()

	t0 := time.Now().UnixMilli()
	mockStep := &avsproto.Execution_Step{
		NodeId:  "query1",
		Log:     "will send message 0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000 to contract 0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		Success: true,
		Error:   "",
		StartAt: t0,
		EndAt:   t0 + 100,
	}

	txHash := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	blockHash := "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	outputData := &avsproto.Execution_Step_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode_Output{
			UserOp: &avsproto.Evm_UserOp{
				Sender:               "0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5",
				Nonce:                "123",
				InitCode:             "",
				CallData:             "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000",
				CallGasLimit:         "100000",
				VerificationGasLimit: "100000",
				PreVerificationGas:   "50000",
				MaxFeePerGas:         "10000000000",
				MaxPriorityFeePerGas: "1000000000",
				PaymasterAndData:     "",
				Signature:            "",
			},
			TxReceipt: &avsproto.Evm_TransactionReceipt{
				Hash:              txHash,
				BlockHash:         blockHash,
				BlockNumber:       123456,
				From:              "0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5",
				To:                "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
				GasUsed:           100000,
				GasPrice:          10000000000,
				CumulativeGasUsed: 200000,
				Fee:               1000000000000,
				ContractAddress:   "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
				Index:             42,
				Logs:              []string{},
				LogsBloom:         "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
				Root:              "",
				Status:            1,
				Type:              2,
				BlobGasPrice:      0,
				BlobGasUsed:       0,
			},
		},
	}

	mockStep.OutputData = outputData

	step := mockStep

	if !step.Success {
		t.Errorf("expected contract write node run successfully but failed")
	}

	if !strings.Contains(step.Log, "will send message 0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000 to contract 0x036CbD53842c5426634e7929541eC2318f3dCF7e") {
		t.Errorf("expected log contains request trace data but found: %s", step.Log)
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	contractWriteOutput := step.GetContractWrite()
	if contractWriteOutput == nil {
		t.Errorf("Expected contract write output data but got nil")
		return
	}

	if contractWriteOutput.TxReceipt == nil {
		t.Errorf("Expected transaction receipt but got nil")
		return
	}

	if len(contractWriteOutput.TxReceipt.Hash) != 66 {
		t.Errorf("Missing Tx Hash in the output data")
	}

	// Verify all transaction receipt fields
	if contractWriteOutput.TxReceipt.BlockHash == "" {
		t.Errorf("Missing BlockHash in the output data")
	}

	if contractWriteOutput.TxReceipt.BlockNumber == 0 {
		t.Errorf("Missing BlockNumber in the output data")
	}

	if contractWriteOutput.TxReceipt.From == "" {
		t.Errorf("Missing From address in the output data")
	}

	if contractWriteOutput.TxReceipt.To == "" {
		t.Errorf("Missing To address in the output data")
	}

	if contractWriteOutput.TxReceipt.GasUsed == 0 {
		t.Errorf("Missing GasUsed in the output data")
	}

	if contractWriteOutput.TxReceipt.GasPrice == 0 {
		t.Errorf("Missing GasPrice in the output data")
	}

	if contractWriteOutput.TxReceipt.CumulativeGasUsed == 0 {
		t.Errorf("Missing CumulativeGasUsed in the output data")
	}

	if contractWriteOutput.TxReceipt.Fee == 0 {
		t.Errorf("Missing Fee in the output data")
	}

	if contractWriteOutput.TxReceipt.ContractAddress == "" {
		t.Errorf("Missing ContractAddress in the output data")
	}

	if contractWriteOutput.TxReceipt.Index == 0 {
		t.Errorf("Missing Index in the output data")
	}

	if contractWriteOutput.TxReceipt.Logs == nil {
		t.Errorf("Missing Logs in the output data")
	}

	if contractWriteOutput.TxReceipt.LogsBloom == "" {
		t.Errorf("Missing LogsBloom in the output data")
	}

	// Root is optional in modern Ethereum, only used in pre-Byzantium hard forks
	// if contractWriteOutput.TxReceipt.Root == "" {
	//     t.Errorf("Missing Root in the output data")
	// }

	if contractWriteOutput.TxReceipt.Status == 0 {
		t.Errorf("Missing Status in the output data")
	}

	if contractWriteOutput.TxReceipt.Type == 0 {
		t.Errorf("Missing Type in the output data")
	}

	// BlobGasPrice and BlobGasUsed are optional fields, so we don't check them
}
