package taskengine

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type ContractWriteProcessor struct {
	*CommonProcessor
	client            *ethclient.Client
	smartWalletConfig *config.SmartWalletConfig
	owner             common.Address
}

func NewContractWriteProcessor(vm *VM, client *ethclient.Client, smartWalletConfig *config.SmartWalletConfig, owner common.Address) *ContractWriteProcessor {
	return &ContractWriteProcessor{
		client:            client,
		smartWalletConfig: smartWalletConfig,
		owner:             owner,
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}
}

func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().Unix()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: nil,
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var log strings.Builder
	var err error

	defer func() {
		s.Log = log.String()
		s.EndAt = time.Now().Unix()
		s.Success = err == nil
	}()

	contractAddressHex := strings.Clone(node.ContractAddress)
	callDataHex := strings.Clone(node.CallData)

	if strings.Contains(contractAddressHex, "{{") {
		contractAddressHex = r.vm.preprocessText(contractAddressHex)
	}
	if strings.Contains(callDataHex, "{{") {
		callDataHex = r.vm.preprocessText(callDataHex)
	}

	contractAddress := common.HexToAddress(contractAddressHex)
	calldata := common.FromHex(callDataHex)

	userOpCalldata, err := aa.PackExecute(
		contractAddress,
		big.NewInt(0), // TODO: load correct salt from the task
		calldata,
	)
	log.WriteString(fmt.Sprintf("\nwill send message %s to contract %s\n", callDataHex, contractAddressHex))
	log.WriteString(fmt.Sprintf("\nprepare to send userops to target contract %s\ninitialize bundler client\n", contractAddress.Hex()))

	log.WriteString("\nsend userops to bundler rpc\n")

	total, _ := r.vm.db.GetCounter(ContractWriteCounterKey(r.owner), 0)
	
	var paymasterRequest *preset.VerifyingPaymasterRequest
	if total > 10 {
		paymasterRequest = &preset.VerifyingPaymasterRequest{
			PaymasterAddress: r.smartWalletConfig.PaymasterAddress,
			ValidUntil:       big.NewInt(time.Now().Add(10 * time.Minute).Unix()),
			ValidAfter:       big.NewInt(time.Now().Unix()),
		}
	}
	userOp, txReceipt, err := preset.SendUserOp(
		r.smartWalletConfig,
		r.owner,
		userOpCalldata,
		paymasterRequest,
	)

	if err != nil {
		s.Error = fmt.Sprintf("error send userops to bundler : %s", err)
		return s, err
	}
	r.vm.db.IncCounter(ContractWriteCounterKey(r.owner), 0)

	bloom, _ := txReceipt.Bloom.MarshalText()

	blobGasPrice := uint64(0)

	if txReceipt.BlobGasPrice != nil {
		blobGasPrice = uint64(txReceipt.BlobGasPrice.Int64())
	}

	outputData := &avsproto.Execution_Step_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode_Output{
			UserOp: &avsproto.Evm_UserOp{
				Sender:               userOp.Sender.Hex(),
				Nonce:                userOp.Nonce.String(),
				InitCode:             common.Bytes2Hex(userOp.InitCode),
				CallData:             common.Bytes2Hex(userOp.CallData),
				CallGasLimit:         userOp.CallGasLimit.String(),
				VerificationGasLimit: userOp.VerificationGasLimit.String(),
				PreVerificationGas:   userOp.PreVerificationGas.String(),
				MaxFeePerGas:         userOp.MaxFeePerGas.String(),
				MaxPriorityFeePerGas: userOp.MaxPriorityFeePerGas.String(),
				PaymasterAndData:     common.Bytes2Hex(userOp.PaymasterAndData),
				Signature:            common.Bytes2Hex(userOp.Signature),
			},

			TxReceipt: &avsproto.Evm_TransactionReceipt{
				Hash:        txReceipt.TxHash.Hex(),
				BlockHash:   txReceipt.BlockHash.Hex(),
				BlockNumber: uint64(txReceipt.BlockNumber.Int64()),
				// TODO: Need to fetch this, it isn't available
				//From:            txReceipt.From.Hex(),
				//To:              txReceipt.To.Hex(),
				GasUsed:           txReceipt.GasUsed,
				GasPrice:          uint64(txReceipt.EffectiveGasPrice.Int64()),
				CumulativeGasUsed: txReceipt.CumulativeGasUsed,
				// Fee:                 txReceipt.Fee,
				ContractAddress: txReceipt.ContractAddress.Hex(),
				Index:           uint64(txReceipt.TransactionIndex),
				// TODO: convert raw log
				//Logs:                txReceipt.Logs,
				LogsBloom:    common.Bytes2Hex(bloom),
				Root:         common.Bytes2Hex(txReceipt.PostState),
				Status:       uint32(txReceipt.Status),
				Type:         uint32(txReceipt.Type),
				BlobGasPrice: blobGasPrice,
				BlobGasUsed:  uint64(txReceipt.BlobGasUsed),
			},
		},
	}
	s.OutputData = outputData
	r.SetOutputVarForStep(stepID, map[string]any{
		"userOp":    outputData.ContractWrite.UserOp,
		"txReceipt": outputData.ContractWrite.TxReceipt,
	})

	return s, nil
}
