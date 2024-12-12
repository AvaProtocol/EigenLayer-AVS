package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type ContractReadProcessor struct {
	client *ethclient.Client
}

func NewContractReadProcessor(client *ethclient.Client) *ContractReadProcessor {
	return &ContractReadProcessor{
		client: client,
	}
}

func (r *ContractReadProcessor) Execute(stepID string, node *avsproto.ContractReadNode) (*avsproto.Execution_Step, error) {
	ctx := context.Background()
	t0 := time.Now().Unix()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: "",
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var err error
	defer func() {
		s.EndAt = time.Now().Unix()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder

	// TODO: support load pre-define ABI
	parsedABI, err := abi.JSON(strings.NewReader(node.ContractAbi))
	if err != nil {
		return nil, fmt.Errorf("error parse abi: %w", err)
	}

	contractAddress := common.HexToAddress(node.ContractAddress)
	calldata := common.FromHex(node.CallData)
	msg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: calldata,
	}

	output, err := r.client.CallContract(ctx, msg, nil)

	if err != nil {
		s.Success = false
		s.Error = fmt.Errorf("error invoke contract method: %w", err).Error()
		return s, err
	}

	// Unpack the output
	result, err := parsedABI.Unpack(node.Method, output)
	if err != nil {
		s.Success = false
		s.Error = fmt.Errorf("error decode result: %w", err).Error()
		return s, err
	}

	log.WriteString(fmt.Sprintf("Call %s on %s at %s", node.Method, node.ContractAddress, time.Now()))
	s.Log = log.String()
	outputData, err := json.Marshal(result)
	s.OutputData = string(outputData)
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}

	return s, nil
}
