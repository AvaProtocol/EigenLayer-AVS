package taskengine

import (
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type ETHTransferProcessor struct {
	*CommonProcessor
	ethClient         *ethclient.Client
	smartWalletConfig *config.SmartWalletConfig
	taskOwner         *common.Address
}

func NewETHTransferProcessor(vm *VM, ethClient *ethclient.Client, smartWalletConfig *config.SmartWalletConfig, taskOwner *common.Address) *ETHTransferProcessor {
	return &ETHTransferProcessor{
		CommonProcessor:   &CommonProcessor{vm: vm},
		ethClient:         ethClient,
		smartWalletConfig: smartWalletConfig,
		taskOwner:         taskOwner,
	}
}

func (p *ETHTransferProcessor) Execute(stepID string, node *avsproto.ETHTransferNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	executionLog := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_ETH_TRANSFER, p.vm)

	// Initially assume failure until we succeed
	executionLog.Success = false

	// Get configuration
	config := node.GetConfig()
	if config == nil {
		err := fmt.Errorf("ETHTransferNode config is nil")
		finalizeExecutionStep(executionLog, false, err.Error(), "")
		return executionLog, err
	}

	// Preprocess template variables in configuration
	destination := p.vm.preprocessTextWithVariableMapping(config.GetDestination())
	amountStr := p.vm.preprocessTextWithVariableMapping(config.GetAmount())

	if destination == "" {
		err := fmt.Errorf("destination address is required for ETH transfer")
		finalizeExecutionStep(executionLog, false, err.Error(), "")
		return executionLog, err
	}

	if amountStr == "" {
		err := fmt.Errorf("amount is required for ETH transfer")
		finalizeExecutionStep(executionLog, false, err.Error(), "")
		return executionLog, err
	}

	// Validate destination address
	if !common.IsHexAddress(destination) {
		err := fmt.Errorf("invalid destination address: %s", destination)
		finalizeExecutionStep(executionLog, false, err.Error(), "")
		return executionLog, err
	}

	// Validate amount (assuming it's in wei)
	_, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		err := fmt.Errorf("invalid amount: %s", amountStr)
		finalizeExecutionStep(executionLog, false, err.Error(), "")
		return executionLog, err
	}

	// Real transactions only when not in simulation context
	if p.smartWalletConfig != nil && !p.vm.IsSimulation {
		p.vm.logger.Info("🚀 ETH TRANSFER DEBUG - Using real UserOp transaction path",
			"destination", destination,
			"amount", amountStr)

		return p.executeRealETHTransfer(stepID, destination, amountStr, executionLog)
	}

	// Simulation path for ETH transfers (SimulateTask / RunNodeImmediately)
	p.vm.logger.Info("🔮 ETH TRANSFER DEBUG - Using simulation path",
		"destination", destination,
		"amount", amountStr,
		"reason", func() string {
			if p.vm.IsSimulation {
				return "vm_is_simulation"
			}
			if p.smartWalletConfig == nil {
				return "smart_wallet_config_is_nil"
			}
			return "unknown"
		}())

	// Simulate transaction hash
	txHash := fmt.Sprintf("0x%064d", time.Now().UnixNano())

	// Create output data using standardized data field
	ethData := map[string]interface{}{
		"transactionHash": txHash,
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(ethData)
	if err != nil {
		// Fallback to empty data on error
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	outputData := &avsproto.ETHTransferNode_Output{
		Data: dataValue,
	}

	// Set execution log output
	executionLog.OutputData = &avsproto.Execution_Step_EthTransfer{
		EthTransfer: outputData,
	}

	// Use shared function to set output variable for this step
	setNodeOutputData(p.CommonProcessor, stepID, map[string]interface{}{
		"transaction_hash": txHash,
		"destination":      destination,
		"amount":           amountStr,
		"success":          true,
	})

	// Create log message
	logMessage := fmt.Sprintf("Simulated ETH transfer of %s wei to %s (tx: %s)", amountStr, destination, txHash)

	// Use shared function to finalize execution step
	finalizeExecutionStep(executionLog, true, "", logMessage)

	return executionLog, nil
}

// executeRealETHTransfer executes a real UserOp transaction for ETH transfers
func (p *ETHTransferProcessor) executeRealETHTransfer(stepID, destination, amountStr string, executionLog *avsproto.Execution_Step) (*avsproto.Execution_Step, error) {
	p.vm.logger.Info("🔍 REAL ETH TRANSFER DEBUG - Starting real UserOp ETH transfer execution",
		"destination", destination,
		"amount", amountStr)

	// Parse amount to big.Int
	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		err := fmt.Errorf("failed to parse amount: %s", amountStr)
		finalizeExecutionStep(executionLog, false, err.Error(), "")
		return executionLog, err
	}

	// Parse destination address
	destinationAddr := common.HexToAddress(destination)

	// Use the aa and preset packages that should already be imported

	// Set up factory address for AA operations
	aa.SetFactoryAddress(p.smartWalletConfig.FactoryAddress)
	aa.SetEntrypointAddress(p.smartWalletConfig.EntrypointAddress)

	// For ETH transfers, we need to create a call to the smart wallet's execute function
	// execute(target, value, data) where data is empty for pure ETH transfers
	smartWalletCallData, err := aa.PackExecute(
		destinationAddr, // target address
		amount,          // ETH value to send
		[]byte{},        // empty data for pure ETH transfer
	)
	if err != nil {
		p.vm.logger.Error("Failed to pack smart wallet execute calldata for ETH transfer", "error", err)
		finalizeExecutionStep(executionLog, false, fmt.Sprintf("Failed to pack execute calldata: %v", err), "")
		return executionLog, err
	}

	// Determine if paymaster should be used (similar to contract write logic)
	var paymasterReq *preset.VerifyingPaymasterRequest
	if p.shouldUsePaymaster() {
		paymasterReq = preset.GetVerifyingPaymasterRequestForDuration(
			p.smartWalletConfig.PaymasterAddress,
			15*time.Minute, // 15 minute validity window
		)
		p.vm.logger.Info("🎫 Using paymaster for sponsored ETH transfer",
			"paymaster", p.smartWalletConfig.PaymasterAddress.Hex(),
			"owner", p.taskOwner.Hex())
	} else {
		p.vm.logger.Info("💰 Using regular ETH transfer (no paymaster)",
			"owner", p.taskOwner.Hex())
	}

	// AA overrides from VM
	var senderOverride *common.Address
	p.vm.mu.Lock()
	if v, ok := p.vm.vars["aa_sender"]; ok {
		if s, ok2 := v.(string); ok2 && common.IsHexAddress(s) {
			addr := common.HexToAddress(s)
			senderOverride = &addr
		}
	}
	p.vm.mu.Unlock()

	// Send UserOp transaction with overrides
	userOp, receipt, err := preset.SendUserOp(
		p.smartWalletConfig,
		*p.taskOwner,
		smartWalletCallData,
		paymasterReq,
		senderOverride,
		p.vm.logger,
	)

	if err != nil {
		p.vm.logger.Error("🚫 BUNDLER FAILED - ETH transfer UserOp transaction failed",
			"bundler_error", err.Error(),
			"bundler_url", p.smartWalletConfig.BundlerURL,
			"destination", destination,
			"amount", amountStr)

		// Return error result - deployed workflows must fail if bundler is unavailable
		finalizeExecutionStep(executionLog, false, fmt.Sprintf("Bundler failed - ETH transfer UserOp transaction could not be sent: %v", err), "")
		return executionLog, err
	}

	// Success! Extract transaction hash
	var txHash string
	if receipt != nil && receipt.TxHash != (common.Hash{}) {
		txHash = receipt.TxHash.Hex()
	} else if userOp != nil {
		// Fallback: use a deterministic hash based on UserOp
		txHash = fmt.Sprintf("0x%064x", userOp.GetUserOpHash(aa.EntrypointAddress, big.NewInt(p.smartWalletConfig.ChainID)))
	} else {
		txHash = fmt.Sprintf("0x%064d", time.Now().UnixNano())
	}

	p.vm.logger.Info("✅ REAL ETH TRANSFER SUCCESS - UserOp transaction completed",
		"tx_hash", txHash,
		"destination", destination,
		"amount", amountStr)

	// Create output data
	ethData := map[string]interface{}{
		"transactionHash": txHash,
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(ethData)
	if err != nil {
		// Fallback to empty data on error
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	outputData := &avsproto.ETHTransferNode_Output{
		Data: dataValue,
	}

	// Set execution log output
	executionLog.OutputData = &avsproto.Execution_Step_EthTransfer{
		EthTransfer: outputData,
	}

	// Use shared function to set output variable for this step
	setNodeOutputData(p.CommonProcessor, stepID, map[string]interface{}{
		"transaction_hash": txHash,
		"destination":      destination,
		"amount":           amountStr,
		"success":          true,
	})

	// Create log message
	logMessage := fmt.Sprintf("Real ETH transfer of %s wei to %s (tx: %s)", amountStr, destination, txHash)

	// Extract gas information from receipt if available
	if receipt != nil {
		// Convert types.Receipt to gas cost information
		gasUsed := receipt.GasUsed
		gasPrice := receipt.EffectiveGasPrice

		if gasUsed > 0 && gasPrice != nil && gasPrice.Cmp(big.NewInt(0)) > 0 {
			// Calculate total gas cost: gasUsed * gasPrice
			totalGasCost := new(big.Int).Mul(big.NewInt(int64(gasUsed)), gasPrice)

			// Set gas cost fields in execution step
			executionLog.GasUsed = big.NewInt(int64(gasUsed)).String()
			executionLog.GasPrice = gasPrice.String()
			executionLog.TotalGasCost = totalGasCost.String()

			p.vm.logger.Info("✅ Set gas cost information for ETH transfer",
				"step_id", stepID,
				"gas_used", executionLog.GasUsed,
				"gas_price", executionLog.GasPrice,
				"total_gas_cost", executionLog.TotalGasCost)
		} else {
			p.vm.logger.Debug("⚠️ No gas cost information available from ETH transfer receipt",
				"step_id", stepID,
				"gas_used", gasUsed,
				"gas_price", gasPrice)
		}
	}

	// Use shared function to finalize execution step
	finalizeExecutionStep(executionLog, true, "", logMessage)

	return executionLog, nil
}

// shouldUsePaymaster determines if paymaster should be used for this ETH transfer
// - ALWAYS use paymaster if configured (no more balance/deposit checking or override flags)
// - Paymaster sponsors gas upfront, wallet reimburses via executeBatchWithValues
// - If reimbursement fails (insufficient wallet balance), UserOp still completes without reimbursement
func (p *ETHTransferProcessor) shouldUsePaymaster() bool {
	// If no paymaster configured, must self-fund
	if p.smartWalletConfig.PaymasterAddress == (common.Address{}) {
		log.Printf("[ETHTransfer] No paymaster configured, must self-fund")
		return false
	}

	// ALWAYS use paymaster if configured
	// Paymaster sponsors gas, wallet reimburses via executeBatchWithValues
	// If wallet can't reimburse, UserOp still completes (paymaster absorbs cost)
	log.Printf("[ETHTransfer] Using paymaster for gas sponsorship (with automatic reimbursement)")
	return true
}

// TODO: Remove this old function - replaced by executeRealETHTransfer
func (p *ETHTransferProcessor) executeActualTransfer(destination common.Address, amount *big.Int) (string, error) {
	// This would contain the actual smart wallet transaction logic
	// For now, return a simulated transaction hash
	return fmt.Sprintf("0x%064d", time.Now().UnixNano()), nil
}
