package taskengine

import (
	"context"
	"fmt"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
func (v *VM) waitForUserOpConfirmation(userOpHash string) error {
	if v.smartWalletConfig == nil {
		return fmt.Errorf("smart wallet config not available for UserOp confirmation")
	}

	// Create eth client
	client, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		return fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer client.Close()

	// Create bundler client
	bundlerClient, err := bundler.NewBundlerClient(v.smartWalletConfig.BundlerURL)
	if err != nil {
		return fmt.Errorf("failed to create bundler client: %w", err)
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
			return fmt.Errorf("timeout waiting for UserOp confirmation after %v", timeout)
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
			return nil
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
