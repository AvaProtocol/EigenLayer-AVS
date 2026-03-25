package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// shouldWaitForContractWriteConfirmation checks if a contract_write execution step requires waiting for on-chain confirmation
// This is true when:
// 1. The node is a contract_write
// 2. The execution is NOT simulated (real UserOps were sent)
// 3. The execution step metadata contains a "pending" status (UserOp sent but not yet mined)
func shouldWaitForContractWriteConfirmation(step *avsproto.Execution_Step, isSimulation bool) bool {
	// Skip waiting if this is a simulation
	if isSimulation {
		return false
	}

	// Check if this is a contract_write step
	if step == nil || step.Metadata == nil {
		return false
	}

	// Extract metadata and check for pending UserOp
	metadataInterface := step.Metadata.AsInterface()

	// metadata should be an array of method results
	if resultsArray, ok := metadataInterface.([]interface{}); ok {
		for _, result := range resultsArray {
			if resultMap, ok := result.(map[string]interface{}); ok {
				// Check if receipt exists and has pending status
				if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
					if status, hasStatus := receipt["status"].(string); hasStatus && status == "pending" {
						return true
					}
				}
			}
		}
	}

	return false
}

// extractUserOpHashFromStep extracts the UserOp hash from a contract_write execution step
func extractUserOpHashFromStep(step *avsproto.Execution_Step) string {
	if step == nil || step.Metadata == nil {
		return ""
	}

	metadataInterface := step.Metadata.AsInterface()

	// metadata should be an array of method results
	if resultsArray, ok := metadataInterface.([]interface{}); ok {
		for _, result := range resultsArray {
			if resultMap, ok := result.(map[string]interface{}); ok {
				if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
					if userOpHash, hasHash := receipt["userOpHash"].(string); hasHash {
						return userOpHash
					}
				}
			}
		}
	}

	return ""
}

// waitForUserOpConfirmation waits for a UserOperation to be confirmed on-chain using exponential backoff polling
// This is similar to the polling logic in pkg/erc4337/preset/builder.go
// waitForUserOpConfirmationResult holds the confirmation result including the on-chain receipt.
type waitForUserOpConfirmationResult struct {
	TxHash  string
	Receipt *types.Receipt // Actual on-chain receipt (may be nil if only bundler receipt was available)
}

func (v *VM) waitForUserOpConfirmation(userOpHash string) (*waitForUserOpConfirmationResult, error) {
	if v.smartWalletConfig == nil {
		return nil, fmt.Errorf("smart wallet config not available for UserOp confirmation")
	}

	// Create eth client
	client, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer client.Close()

	// Create bundler client
	bundlerClient, err := bundler.NewBundlerClient(v.smartWalletConfig.BundlerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create bundler client: %w", err)
	}

	entrypoint := v.smartWalletConfig.EntrypointAddress

	v.logger.Info("⏳ Waiting for UserOp confirmation",
		"userOpHash", userOpHash,
		"entrypoint", entrypoint.Hex())

	// Exponential backoff intervals (in seconds): 1s → 2s → 4s → 8s → 15s → 30s → 60s
	intervals := []time.Duration{1, 2, 4, 8, 15, 30, 60}
	timeout := 5 * time.Minute
	startTime := time.Now()

	intervalIndex := 0

	for {
		// Check if timeout reached
		if time.Since(startTime) > timeout {
			return nil, fmt.Errorf("timeout waiting for UserOp confirmation after %v", timeout)
		}

		// Try to get receipt from bundler
		receipt, err := bundlerClient.GetUserOperationReceipt(context.Background(), userOpHash)
		if err == nil && receipt != nil {
			// Receipt is found, extract block number and tx hash for logging
			var blockNumber interface{}
			var txHash string

			if receiptMap, ok := receipt.(map[string]interface{}); ok {
				blockNumber = receiptMap["blockNumber"]
				if txHashVal, ok := receiptMap["transactionHash"].(string); ok {
					txHash = txHashVal
				}
			}

			v.logger.Info("✅ UserOp confirmed on-chain",
				"userOpHash", userOpHash,
				"blockNumber", blockNumber,
				"txHash", txHash,
				"elapsed", time.Since(startTime))

			// Fetch the actual on-chain receipt to get gas data
			result := &waitForUserOpConfirmationResult{TxHash: txHash}
			if txHash != "" {
				onChainReceipt, receiptErr := client.TransactionReceipt(context.Background(), common.HexToHash(txHash))
				if receiptErr == nil && onChainReceipt != nil {
					result.Receipt = onChainReceipt
				} else if v.logger != nil {
					v.logger.Warn("Failed to fetch on-chain receipt for gas data",
						"txHash", txHash, "error", receiptErr)
				}
			}

			return result, nil
		}

		// Determine current interval (use last interval if we've exceeded the array)
		var currentInterval time.Duration
		if intervalIndex < len(intervals) {
			currentInterval = intervals[intervalIndex] * time.Second
			intervalIndex++
		} else {
			currentInterval = intervals[len(intervals)-1] * time.Second
		}

		v.logger.Debug("UserOp not confirmed yet, retrying",
			"userOpHash", userOpHash,
			"nextRetryIn", currentInterval,
			"elapsed", time.Since(startTime))

		// Wait before next retry
		time.Sleep(currentInterval)
	}
}

// getReceiptByUserOpHash queries the EntryPoint contract for UserOperation events to find the transaction receipt
// This is a fallback method when the bundler doesn't provide the receipt directly
func (v *VM) getReceiptByUserOpHash(userOpHash string) (*types.Receipt, error) {
	if v.smartWalletConfig == nil {
		return nil, fmt.Errorf("smart wallet config not available")
	}

	client, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer client.Close()

	// This method is currently unused - we rely on bundler.GetUserOperationReceipt instead
	// Keeping this as a placeholder for future implementation if needed

	// In a real implementation, you would query eth_getLogs with UserOperationEvent signature
	// and search for the matching userOpHash in recent blocks

	// In a real implementation, you would:
	// 1. Query eth_getLogs with UserOperationEvent signature
	// 2. Parse the logs to find the matching userOpHash
	// 3. Extract the transaction hash from the log
	// 4. Get the receipt using eth_getTransactionReceipt

	// For now, we rely on the bundler's GetUserOperationReceipt method
	// which is called in waitForUserOpConfirmation

	return nil, fmt.Errorf("receipt not found (use bundler.GetUserOperationReceipt instead)")
}

// waitForOnChainConfirmationIfNeeded is the single entry point for receipt-waiting logic.
// It handles both contractWrite (array metadata with pending status) and ethTransfer
// (flat metadata) steps. Called from both the main execution path and loop iterations.
//
// Gas cost tracking: When SendUserOp times out (returns nil receipt, nil error),
// the node processor cannot extract gas data. This function polls for the on-chain
// receipt and backfills GasUsed/GasPrice/TotalGasCost on the step.
//
// Known limitation (double-timeout edge case): If SendUserOp times out (~1 min)
// AND this confirmation waiter also times out (~5 min), gas costs are permanently
// lost for that step even though the transaction may eventually confirm on-chain.
// This is a ~6 min total window; on a healthy network it should not occur.
//
// Returns true if confirmation was attempted (regardless of success/failure).
func (v *VM) waitForOnChainConfirmationIfNeeded(step *avsproto.Execution_Step) bool {
	if v.IsSimulation || step == nil {
		return false
	}

	// Contract write: check for pending status in metadata array
	if shouldWaitForContractWriteConfirmation(step, false) {
		userOpHash := extractUserOpHashFromStep(step)
		if userOpHash == "" {
			return false
		}

		v.logger.Info("⏳ On-chain confirmation needed (contractWrite pending)",
			"stepID", step.Id,
			"userOpHash", userOpHash)

		result, waitErr := v.waitForUserOpConfirmation(userOpHash)
		if waitErr != nil {
			v.logger.Error("❌ Failed to wait for on-chain confirmation",
				"stepID", step.Id,
				"userOpHash", userOpHash,
				"error", waitErr)
		} else {
			v.logger.Info("✅ On-chain confirmation received",
				"stepID", step.Id,
				"userOpHash", userOpHash)

			v.mu.Lock()
			step.Success = true
			step.Error = ""
			v.mu.Unlock()

			// Update gas cost fields from the on-chain receipt if the step doesn't already have them
			if result != nil && result.Receipt != nil && (step.TotalGasCost == "" || step.TotalGasCost == "0") {
				updateStepGasCostFromReceipt(step, result.Receipt, v)
			}

			v.addRPCPropagationDelay()
		}
		return true
	}

	// ETH transfer: SendUserOp may have timed out with a nil receipt.
	// The ethTransfer processor stores receiptStatus="pending" and userOpHash in metadata
	// when the receipt was not available. Poll for confirmation the same way as contractWrite.
	if step.GetEthTransfer() != nil && step.Success && step.Metadata != nil {
		meta := step.Metadata.AsInterface()
		if metaMap, ok := meta.(map[string]interface{}); ok {
			status, _ := metaMap["receiptStatus"].(string)
			userOpHash, _ := metaMap["userOpHash"].(string)
			if status == "pending" && userOpHash != "" {
				v.logger.Info("⏳ On-chain confirmation needed (ethTransfer pending)",
					"stepID", step.Id,
					"userOpHash", userOpHash)

				result, waitErr := v.waitForUserOpConfirmation(userOpHash)
				if waitErr != nil {
					v.logger.Error("❌ Failed to wait for ethTransfer confirmation",
						"stepID", step.Id,
						"userOpHash", userOpHash,
						"error", waitErr)
				} else {
					v.logger.Info("✅ ethTransfer confirmed on-chain",
						"stepID", step.Id,
						"userOpHash", userOpHash)

					// Update gas cost fields from the on-chain receipt if the step doesn't already have them
					if result != nil && result.Receipt != nil && (step.TotalGasCost == "" || step.TotalGasCost == "0") {
						updateStepGasCostFromReceipt(step, result.Receipt, v)
					}

					v.addRPCPropagationDelay()
				}
				return true
			}
		}
	}

	return false
}

// updateStepGasCostFromReceipt extracts gas cost information from an on-chain receipt
// and sets it on the execution step. Called when the initial SendUserOp timed out
// (nil receipt) but the subsequent confirmation poll retrieved the actual receipt.
func updateStepGasCostFromReceipt(step *avsproto.Execution_Step, receipt *types.Receipt, vm *VM) {
	if step == nil || receipt == nil {
		return
	}

	gasUsed := receipt.GasUsed
	gasPrice := receipt.EffectiveGasPrice

	if gasUsed == 0 || gasPrice == nil || gasPrice.Sign() <= 0 {
		return
	}

	gasUsedBig := new(big.Int).SetUint64(gasUsed)
	totalGasCost := new(big.Int).Mul(gasUsedBig, gasPrice)

	step.GasUsed = gasUsedBig.String()
	step.GasPrice = gasPrice.String()
	step.TotalGasCost = totalGasCost.String()

	if vm != nil && vm.logger != nil {
		vm.logger.Info("Updated gas cost from on-chain receipt",
			"step_id", step.Id,
			"gas_used", step.GasUsed,
			"gas_price", step.GasPrice,
			"total_gas_cost", step.TotalGasCost,
			"tx_hash", receipt.TxHash.Hex())
	}
}

// addRPCPropagationDelay adds a delay to ensure RPC nodes have propagated the latest state
// This prevents issues where dependent transactions fail because the RPC node hasn't seen
// the previous transaction's state changes yet
func (v *VM) addRPCPropagationDelay() {
	const propagationDelay = 5 * time.Second

	v.logger.Info("⏳ Adding RPC propagation delay",
		"delay", propagationDelay,
		"reason", "ensure state changes are visible to all RPC nodes")

	time.Sleep(propagationDelay)

	v.logger.Debug("✅ RPC propagation delay complete")
}
