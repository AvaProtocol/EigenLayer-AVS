package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type SendUserOpFunc func(
	config *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *preset.VerifyingPaymasterRequest,
) (*userop.UserOperation, *types.Receipt, error)

type ContractWriteProcessor struct {
	*CommonProcessor
	client            *ethclient.Client
	smartWalletConfig *config.SmartWalletConfig
	owner             common.Address
	sendUserOpFunc    SendUserOpFunc
}

func NewContractWriteProcessor(vm *VM, client *ethclient.Client, smartWalletConfig *config.SmartWalletConfig, owner common.Address) *ContractWriteProcessor {
	r := &ContractWriteProcessor{
		client:            client,
		smartWalletConfig: smartWalletConfig,
		owner:             owner,
		sendUserOpFunc:    preset.SendUserOp, // Default to the real implementation
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}

	return r
}

func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().UnixMilli()
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
		s.EndAt = time.Now().UnixMilli()
		s.Success = err == nil
	}()

	// Get configuration from input variables (new architecture)
	r.vm.mu.Lock()
	contractAddressVar, contractAddressExists := r.vm.vars["contract_address"]
	callDataVar, callDataExists := r.vm.vars["call_data"]
	r.vm.mu.Unlock()

	if !contractAddressExists || !callDataExists {
		err = fmt.Errorf("missing required input variables: contract_address and call_data")
		s.Error = err.Error()
		return s, err
	}

	contractAddressStr, ok := contractAddressVar.(string)
	if !ok {
		err = fmt.Errorf("contract_address variable must be a string")
		s.Error = err.Error()
		return s, err
	}

	callDataStr, ok := callDataVar.(string)
	if !ok {
		err = fmt.Errorf("call_data variable must be a string")
		s.Error = err.Error()
		return s, err
	}

	contractAddressHex := strings.Clone(contractAddressStr)
	callDataHex := strings.Clone(callDataStr)

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
	// TODO: move to config
	// Paymaster request logic:
	// - No paymaster request for transactions >= 10 from non-whitelisted addresses
	// - Paymaster requests are created for all other cases (transactions < 10 or whitelisted addresses)
	if total >= 10 && !isWhitelistedAddress(r.owner, r.smartWalletConfig.WhitelistAddresses) {
		// No paymaster request for non-whitelisted addresses after 10 transactions
	} else {
		paymasterRequest = preset.GetVerifyingPaymasterRequestForDuration(r.smartWalletConfig.PaymasterAddress, 15*time.Minute)
	}
	userOp, txReceipt, err := r.sendUserOpFunc(
		r.smartWalletConfig,
		r.owner,
		userOpCalldata,
		paymasterRequest,
	)

	if err != nil {
		s.Error = fmt.Sprintf("error send userops to bundler : %s", err)
		return s, err
	}

	_, err = r.vm.db.IncCounter(ContractWriteCounterKey(r.owner), 0)
	if err != nil {
		if r.vm.logger != nil {
			r.vm.logger.Error("failed to increment counter", "error", err)
		}
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
		},
	}

	// Only add TxReceipt if it exists
	if txReceipt != nil {
		var bloom []byte
		bloom, _ = txReceipt.Bloom.MarshalText()

		blobGasPrice := uint64(0)
		if txReceipt.BlobGasPrice != nil {
			blobGasPrice = uint64(txReceipt.BlobGasPrice.Int64())
		}

		// Get the transaction to access From and To fields
		tx, _, err := r.client.TransactionByHash(context.Background(), txReceipt.TxHash)
		if err != nil {
			return nil, fmt.Errorf("failed to get transaction: %w", err)
		}

		// Get the sender address using the newer method
		signer := types.LatestSignerForChainID(tx.ChainId())
		from, err := types.Sender(signer, tx)
		if err != nil {
			return nil, fmt.Errorf("failed to get sender from transaction: %w", err)
		}

		outputData.ContractWrite.TxReceipt = &avsproto.Evm_TransactionReceipt{
			Hash:              txReceipt.TxHash.Hex(),
			BlockHash:         txReceipt.BlockHash.Hex(),
			BlockNumber:       uint64(txReceipt.BlockNumber.Int64()),
			From:              from.Hex(),
			To:                tx.To().Hex(),
			GasUsed:           txReceipt.GasUsed,
			GasPrice:          uint64(txReceipt.EffectiveGasPrice.Int64()),
			CumulativeGasUsed: txReceipt.CumulativeGasUsed,
			Fee:               uint64(txReceipt.GasUsed * txReceipt.EffectiveGasPrice.Uint64()),
			ContractAddress:   txReceipt.ContractAddress.Hex(),
			Index:             uint64(txReceipt.TransactionIndex),
			Logs:              make([]string, len(txReceipt.Logs)),
			LogsBloom:         common.Bytes2Hex(bloom),
			Root:              common.Bytes2Hex(txReceipt.PostState),
			Status:            uint32(txReceipt.Status),
			Type:              uint32(txReceipt.Type),
			BlobGasPrice:      blobGasPrice,
			BlobGasUsed:       uint64(txReceipt.BlobGasUsed),
		}

		// Convert logs to JSON strings for storage in the protobuf message
		for i, log := range txReceipt.Logs {
			logBytes, err := json.Marshal(log)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal log: %w", err)
			}
			outputData.ContractWrite.TxReceipt.Logs[i] = string(logBytes)
		}
	}

	s.OutputData = outputData
	outputVars := map[string]any{
		"userOp": outputData.ContractWrite.UserOp,
	}
	if txReceipt != nil {
		outputVars["txReceipt"] = outputData.ContractWrite.TxReceipt
	}
	r.SetOutputVarForStep(stepID, outputVars)

	return s, nil
}
