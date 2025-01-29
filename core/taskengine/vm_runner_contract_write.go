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
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/bundler"
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
		OutputData: "",
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

	contractAddress := common.HexToAddress(node.ContractAddress)
	calldata := common.FromHex(node.CallData)
	userOpCalldata, err := aa.PackExecute(
		contractAddress,
		big.NewInt(0), // TODO: load correct salt from the task
		calldata,
	)
	log.WriteString("\ncreate bundle client to send userops to bundler rpc\n")
	bundlerClient, err := bundler.NewBundlerClient(r.smartWalletConfig.BundlerURL)

	if err != nil {
		log.WriteString(fmt.Sprintf("error creating bundle client: %s", err))
		s.Error = fmt.Sprintf("error creating bundler client : %s", err)
		return s, err
	}

	log.WriteString("\nsend userops to bundler rpc\n")
	fmt.Println("send userops to bundler rpc")

	txResult, err := preset.SendUserOp(
		r.client,
		bundlerClient,
		r.smartWalletConfig.ControllerPrivateKey,
		r.owner,
		userOpCalldata,
	)

	if err != nil {
		s.Error = fmt.Sprintf("error send userops to bundler : %s", err)
		return s, err
	}

	s.OutputData = txResult
	r.SetOutputVarForStep(stepID, txResult)

	return s, nil
}
