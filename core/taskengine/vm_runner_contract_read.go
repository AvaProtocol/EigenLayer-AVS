package taskengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/byte4"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type ContractReadProcessor struct {
	*CommonProcessor
	client *ethclient.Client
}

func NewContractReadProcessor(vm *VM, client *ethclient.Client) *ContractReadProcessor {
	return &ContractReadProcessor{
		client: client,
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}
}

func (r *ContractReadProcessor) Execute(stepID string, node *avsproto.ContractReadNode) (*avsproto.Execution_Step, error) {
	ctx := context.Background()
	t0 := time.Now().UnixMilli()

	// Look up the task node to get the name
	var nodeName string = "unknown"
	r.vm.mu.Lock()
	if taskNode, exists := r.vm.TaskNodes[stepID]; exists {
		nodeName = taskNode.Name
	}
	r.vm.mu.Unlock()

	s := &avsproto.Execution_Step{
		Id:         stepID,
		Log:        "",
		OutputData: nil,
		Success:    true,
		Error:      "",
		StartAt:    t0,
		Type:       avsproto.NodeType_NODE_TYPE_CONTRACT_READ.String(),
		Name:       nodeName,
	}

	var err error
	defer func() {
		s.EndAt = time.Now().UnixMilli()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder

	// Get configuration from input variables (new architecture)
	r.vm.mu.Lock()
	contractAbiVar, abiExists := r.vm.vars["contractAbi"]
	contractAddressVar, addrExists := r.vm.vars["contractAddress"]
	callDataVar, dataExists := r.vm.vars["callData"]
	r.vm.mu.Unlock()

	if !abiExists || !addrExists || !dataExists {
		err = fmt.Errorf("missing required input variables: contractAbi, contractAddress, callData")
		return s, err
	}

	contractAbi, ok := contractAbiVar.(string)
	if !ok {
		err = fmt.Errorf("contractAbi must be a string")
		return s, err
	}

	contractAddress, ok := contractAddressVar.(string)
	if !ok {
		err = fmt.Errorf("contractAddress must be a string")
		return s, err
	}

	callData, ok := callDataVar.(string)
	if !ok {
		err = fmt.Errorf("callData must be a string")
		return s, err
	}

	// TODO: support load pre-define ABI
	parsedABI, err := abi.JSON(strings.NewReader(contractAbi))
	if err != nil {
		return nil, fmt.Errorf("error parse abi: %w", err)
	}

	contractAddr := common.HexToAddress(contractAddress)
	calldata := common.FromHex(callData)
	msg := ethereum.CallMsg{
		To:   &contractAddr,
		Data: calldata,
	}

	output, err := r.client.CallContract(ctx, msg, nil)

	if err != nil {
		s.Success = false
		s.Error = fmt.Errorf("error invoke contract method: %w", err).Error()
		return s, err
	}

	// Unpack the output by parsing the 4byte from calldata, compare with the right method in ABI
	method, err := byte4.GetMethodFromCalldata(parsedABI, common.FromHex(callData))
	if err != nil {
		s.Success = false
		s.Error = fmt.Errorf("error detect method from ABI: %w", err).Error()
		return s, err
	}
	result, err := parsedABI.Unpack(method.Name, output)

	if err != nil {
		s.Success = false
		s.Error = fmt.Errorf("error decode result: %w", err).Error()
		return s, err
	}

	log.WriteString(fmt.Sprintf("Call %s on %s at %s", method.Name, contractAddress, time.Now()))
	s.Log = log.String()

	s.OutputData = &avsproto.Execution_Step_ContractRead{
		ContractRead: &avsproto.ContractReadNode_Output{
			Data: gow.SliceToStructPbSlice(result),
		},
	}

	r.SetOutputVarForStep(stepID, result)
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}

	return s, nil
}
