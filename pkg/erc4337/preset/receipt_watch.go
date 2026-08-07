package preset

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// Watching an operation to its receipt.
//
// A bundler answers with a userOpHash, not a transaction, so confirmation
// means finding the EntryPoint's UserOperationEvent for that hash. This is
// shared by every send path and is deliberately EntryPoint-version agnostic:
// the caller supplies which EntryPoint to watch, and the event layout it
// decodes is identical across v0.6 and v0.7.

// userOpEventTopic0 is EntryPoint's UserOperationEvent signature hash, the
// same on v0.6 and v0.7. Example log:
// https://sepolia.basescan.org/tx/0x7580ac508a2ac34cf6a4f4346fb6b4f09edaaa4f946f42ecdb2bfd2a633d43af#eventlog
var userOpEventTopic0 = common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")

func waitForUserOpConfirmation(
	client *ethclient.Client,
	wsClient *ethclient.Client,
	entrypoint common.Address,
	userOpHash string,
	lgr logger.Logger,
) (*types.Receipt, error) {
	// Ensure logger is never nil to avoid panic
	logger := logger.EnsureLogger(lgr)

	// Configuration for exponential backoff polling
	// Increased timeout to 1 minute to account for slow chains (e.g., Sepolia) where bundle
	// transactions may take longer to be mined. Bundlers typically process within 2-5s, but
	// the actual on-chain confirmation depends on network block times.
	const (
		maxWaitTime     = 2 * time.Minute // Maximum total wait time. Sepolia bundler+mining often exceeds 60s, so 120s matches the SDK's TimeoutPresets.SLOW.
		initialInterval = 1 * time.Second // Start polling every 1 second
		maxInterval     = 5 * time.Second // Max polling interval (cap exponential growth)
		backoffFactor   = 1.5             // Multiply interval by 1.5 each retry
	)

	// Try WebSocket subscription first (most efficient for real-time events)
	if wsClient != nil {
		logger.Debug("Transaction waiting: attempting WebSocket subscription")

		query := ethereum.FilterQuery{
			Addresses: []common.Address{entrypoint},
			Topics:    [][]common.Hash{{userOpEventTopic0}, {common.HexToHash(userOpHash)}},
		}

		logs := make(chan types.Log)
		sub, err := wsClient.SubscribeFilterLogs(context.Background(), query, logs)

		if err == nil {
			// WebSocket subscription successful - use it with a polling fallback
			logger.Debug("Transaction waiting: websocket subscription active, polling as fallback")
			defer sub.Unsubscribe()

			startTime := time.Now()
			pollInterval := initialInterval
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			for {
				select {
				case err := <-sub.Err():
					if err != nil {
						logger.Warn("Transaction waiting: websocket error, falling back to polling", "error", err)
						// Continue with polling below
						goto PollingOnly
					}

				case vLog := <-logs:
					// Got the event via WebSocket - fastest path!
					logger.Debug("UserOp confirmed via websocket", "tx", vLog.TxHash.Hex())
					receipt, err := client.TransactionReceipt(context.Background(), vLog.TxHash)
					if err != nil {
						logger.Warn("Failed to get receipt", "tx", vLog.TxHash.Hex(), "error", err)
						continue
					}
					// Check UserOp execution success from the event log
					userOpSuccess := checkUserOpExecutionSuccess(vLog)
					if !userOpSuccess {
						return nil, fmt.Errorf("UserOp execution failed (success=false in UserOperationEvent) - tx: %s", vLog.TxHash.Hex())
					}
					return receipt, nil

				case <-ticker.C:
					// Periodic polling as fallback (in case WebSocket misses events)
					elapsed := time.Since(startTime)
					if elapsed > maxWaitTime {
						logger.Debug("Transaction waiting timeout, UserOp may still be pending", "elapsed", elapsed.String())
						return nil, nil
					}

					logger.Debug("Transaction waiting: polling",
						"elapsed", elapsed.Round(time.Second).String(),
						"interval", pollInterval.String())

					result, found, err := pollUserOpReceipt(client, entrypoint, userOpHash)
					if err != nil {
						logger.Warn("Transaction waiting: polling error", "error", err)
					}
					if found {
						if !result.Success {
							return nil, fmt.Errorf("UserOp execution failed (success=false in UserOperationEvent) - tx: %s", result.Receipt.TxHash.Hex())
						}
						logger.Debug("UserOp confirmed via polling")
						return result.Receipt, nil
					}

					// Increase polling interval with exponential backoff (up to max)
					pollInterval = time.Duration(float64(pollInterval) * backoffFactor)
					if pollInterval > maxInterval {
						pollInterval = maxInterval
					}
					ticker.Reset(pollInterval)
				}
			}
		} else {
			logger.Debug("Transaction waiting: websocket subscription failed, using polling only", "error", err)
		}
	} else {
		logger.Debug("Transaction waiting: no WebSocket client, using polling only")
	}

PollingOnly:
	// Polling-only mode (WebSocket unavailable or failed)
	logger.Debug("Transaction waiting: polling-only mode with exponential backoff")

	startTime := time.Now()
	pollInterval := initialInterval
	attempt := 0

	for {
		attempt++
		elapsed := time.Since(startTime)

		if elapsed > maxWaitTime {
			logger.Debug("Transaction waiting timeout, UserOp may still be pending", "elapsed", elapsed.String(), "attempts", attempt)
			return nil, nil
		}

		logger.Debug("Transaction waiting: poll attempt", "attempt", attempt, "elapsed", elapsed.Round(time.Second).String(), "interval", pollInterval.String())

		result, found, err := pollUserOpReceipt(client, entrypoint, userOpHash)
		if err != nil {
			logger.Warn("Transaction waiting: polling error", "error", err)
			// Continue polling despite errors (transient network issues)
		}
		if found {
			if !result.Success {
				return nil, fmt.Errorf("UserOp execution failed (success=false in UserOperationEvent) - tx: %s", result.Receipt.TxHash.Hex())
			}
			logger.Debug("UserOp confirmed via polling", "elapsed", elapsed.String(), "attempts", attempt)
			return result.Receipt, nil
		}

		// Wait before next poll with exponential backoff
		time.Sleep(pollInterval)
		pollInterval = time.Duration(float64(pollInterval) * backoffFactor)
		if pollInterval > maxInterval {
			pollInterval = maxInterval
		}
	}
}

// UserOpReceiptResult contains the receipt and execution success status for a UserOp
type UserOpReceiptResult struct {
	Receipt *types.Receipt
	Success bool // UserOp execution success (from UserOperationEvent.success field)
}

// checkUserOpExecutionSuccess decodes the UserOperationEvent log to check if execution succeeded.
// Returns true if the UserOp execution was successful, false otherwise.
func checkUserOpExecutionSuccess(vLog types.Log) bool {
	// UserOperationEvent(bytes32 indexed userOpHash, address indexed sender, address indexed paymaster, uint256 nonce, bool success, uint256 actualGasCost, uint256 actualGasUsed)
	// Event data structure: nonce (32 bytes), success (32 bytes), actualGasCost (32 bytes), actualGasUsed (32 bytes)
	if len(vLog.Data) >= 128 {
		// success is at bytes 32-64 (after nonce)
		successBytes := vLog.Data[32:64]
		// Check if the last byte is 1 (bool true in ABI encoding)
		return len(successBytes) > 0 && successBytes[len(successBytes)-1] == 1
	}
	// Data too short — assume failure (shouldn't happen with valid events)
	return false
}

// pollUserOpReceipt queries the chain for a UserOp receipt by searching recent blocks for the UserOperationEvent.
// Returns (result, found, error) where found=true if the event was found.
// The result includes both the transaction receipt and the UserOp execution success status.
func pollUserOpReceipt(
	client *ethclient.Client,
	entrypoint common.Address,
	userOpHash string,
) (*UserOpReceiptResult, bool, error) {
	// Query recent blocks for the UserOperationEvent
	// We look back ~50 blocks to handle reorgs and slow chains (e.g., Sepolia with ~12s block time = ~10 minutes)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	currentBlock, err := client.BlockNumber(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get current block: %w", err)
	}

	// Look back 50 blocks to handle slow chains and ensure we catch recently mined bundle transactions
	// For Sepolia (12s blocks), this covers ~10 minutes of history
	fromBlock := currentBlock
	if currentBlock > 50 {
		fromBlock = currentBlock - 50
	}

	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(fromBlock)),
		ToBlock:   big.NewInt(int64(currentBlock)),
		Addresses: []common.Address{entrypoint},
		Topics:    [][]common.Hash{{userOpEventTopic0}, {common.HexToHash(userOpHash)}},
	}

	logs, err := client.FilterLogs(ctx, query)
	if err != nil {
		return nil, false, fmt.Errorf("failed to filter logs: %w", err)
	}

	if len(logs) == 0 {
		return nil, false, nil // Not found yet
	}

	// Found the event! Get the transaction receipt
	vLog := logs[0] // Use first match (should only be one)
	receipt, err := client.TransactionReceipt(ctx, vLog.TxHash)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get receipt for tx %s: %w", vLog.TxHash.Hex(), err)
	}

	// Decode UserOperationEvent to check execution success
	// UserOperationEvent(bytes32 indexed userOpHash, address indexed sender, address indexed paymaster, uint256 nonce, bool success, uint256 actualGasCost, uint256 actualGasUsed)
	// Event data structure: nonce (32 bytes), success (32 bytes), actualGasCost (32 bytes), actualGasUsed (32 bytes)
	userOpSuccess := false
	if len(vLog.Data) >= 128 {
		// success is at bytes 32-64 (after nonce)
		successBytes := vLog.Data[32:64]
		// Check if the last byte is 1 (bool true in ABI encoding)
		userOpSuccess = len(successBytes) > 0 && successBytes[len(successBytes)-1] == 1
	}
	// Data too short — assume failure (shouldn't happen with valid events)

	return &UserOpReceiptResult{
		Receipt: receipt,
		Success: userOpSuccess,
	}, true, nil
}
