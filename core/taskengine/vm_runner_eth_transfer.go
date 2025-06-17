package taskengine

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type ETHTransferProcessor struct {
	*CommonProcessor
	ethClient         *ethclient.Client
	smartWalletConfig interface{} // Will be properly typed when we have the actual config structure
	taskOwner         common.Address
}

func NewETHTransferProcessor(vm *VM, ethClient *ethclient.Client, smartWalletConfig interface{}, taskOwner common.Address) *ETHTransferProcessor {
	return &ETHTransferProcessor{
		CommonProcessor:   &CommonProcessor{vm: vm},
		ethClient:         ethClient,
		smartWalletConfig: smartWalletConfig,
		taskOwner:         taskOwner,
	}
}

func (p *ETHTransferProcessor) Execute(stepID string, node *avsproto.ETHTransferNode) (*avsproto.Execution_Step, error) {
	startTime := time.Now()

	// Get node data using helper function to reduce duplication
	nodeName, nodeInput := p.vm.GetNodeDataForExecution(stepID)

	// Create execution log
	executionLog := &avsproto.Execution_Step{
		Id:      stepID,
		StartAt: startTime.UnixMilli(),
		Success: false,
		Type:    avsproto.NodeType_NODE_TYPE_ETH_TRANSFER.String(),
		Name:    nodeName,
		Input:   nodeInput, // Include node input data for debugging
	}

	// Get configuration
	config := node.GetConfig()
	if config == nil {
		err := fmt.Errorf("ETHTransferNode config is nil")
		executionLog.Error = err.Error()
		executionLog.EndAt = time.Now().UnixMilli()
		return executionLog, err
	}

	// Preprocess template variables in configuration
	destination := p.vm.preprocessTextWithVariableMapping(config.GetDestination())
	amountStr := p.vm.preprocessTextWithVariableMapping(config.GetAmount())

	if destination == "" {
		err := fmt.Errorf("destination address is required for ETH transfer")
		executionLog.Error = err.Error()
		executionLog.EndAt = time.Now().UnixMilli()
		return executionLog, err
	}

	if amountStr == "" {
		err := fmt.Errorf("amount is required for ETH transfer")
		executionLog.Error = err.Error()
		executionLog.EndAt = time.Now().UnixMilli()
		return executionLog, err
	}

	// Validate destination address
	if !common.IsHexAddress(destination) {
		err := fmt.Errorf("invalid destination address: %s", destination)
		executionLog.Error = err.Error()
		executionLog.EndAt = time.Now().UnixMilli()
		return executionLog, err
	}

	// Validate amount (assuming it's in wei)
	_, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		err := fmt.Errorf("invalid amount: %s", amountStr)
		executionLog.Error = err.Error()
		executionLog.EndAt = time.Now().UnixMilli()
		return executionLog, err
	}

	// For now, we'll simulate the ETH transfer since we don't have the actual smart wallet implementation
	// In a real implementation, this would interact with the smart wallet to send the transaction

	// Simulate transaction hash
	txHash := fmt.Sprintf("0x%064d", time.Now().UnixNano())

	// Create output data
	outputData := &avsproto.ETHTransferNode_Output{
		TransactionHash: txHash,
	}

	// Set execution log output
	executionLog.OutputData = &avsproto.Execution_Step_EthTransfer{
		EthTransfer: outputData,
	}

	// Set output variable for this step
	p.SetOutputVarForStep(stepID, map[string]interface{}{
		"transaction_hash": txHash,
		"destination":      destination,
		"amount":           amountStr,
		"success":          true,
	})

	// Mark as successful
	executionLog.Success = true
	executionLog.EndAt = time.Now().UnixMilli()

	// Log the simulated transfer
	executionLog.Log = fmt.Sprintf("Simulated ETH transfer of %s wei to %s (tx: %s)", amountStr, destination, txHash)

	return executionLog, nil
}

// TODO: Implement actual ETH transfer logic when smart wallet integration is ready
func (p *ETHTransferProcessor) executeActualTransfer(destination common.Address, amount *big.Int) (string, error) {
	// This would contain the actual smart wallet transaction logic
	// For now, return a simulated transaction hash
	return fmt.Sprintf("0x%064d", time.Now().UnixNano()), nil
}
