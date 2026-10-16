package taskengine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"
)

// receiptForTx fetches a transaction receipt for the VM's chain via the
// per-chain worker (gateway mode) when a reader is registered, falling back
// to a direct dial otherwise. Returns (nil, nil) when the receipt isn't
// available yet (pending).
func (v *VM) receiptForTx(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	if v.smartWalletConfig == nil {
		return nil, fmt.Errorf("smart wallet config not available")
	}
	if reader := GetChainStateReaderForChain(uint64(v.smartWalletConfig.ChainID)); reader != nil {
		return reader.GetTransactionReceipt(ctx, txHash)
	}
	client, err := ethclient.DialContext(ctx, v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	receipt, err := client.TransactionReceipt(ctx, txHash)
	if errors.Is(err, ethereum.NotFound) {
		return nil, nil
	}
	return receipt, err
}

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
					if receipt["status"] != "pending" {
						continue
					}
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
	TxHash        string
	Receipt       *types.Receipt // Actual on-chain receipt (may be nil if only bundler receipt was available)
	Success       bool
	Fields        map[string]interface{} // Standard nested Ethereum receipt fields from the bundler.
	FailureReason string
	RevertData    string
}

func (v *VM) waitForUserOpConfirmation(userOpHash string) (*waitForUserOpConfirmationResult, error) {
	ctx, cancel := context.WithTimeout(v.context(), 5*time.Minute)
	defer cancel()
	return v.waitForUserOpConfirmationContext(ctx, userOpHash)
}

func (v *VM) waitForUserOpConfirmationContext(ctx context.Context, userOpHash string) (*waitForUserOpConfirmationResult, error) {
	if v.smartWalletConfig == nil {
		return nil, fmt.Errorf("smart wallet config not available for UserOp confirmation")
	}

	// Create bundler client
	activeBundlerURL, err := v.smartWalletConfig.ActiveBundlerURL()
	if err != nil {
		return nil, fmt.Errorf("resolve bundler endpoint: %w", err)
	}
	bundlerClient, err := bundler.NewBundlerClient(activeBundlerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create bundler client: %w", err)
	}

	// The EntryPoint the operation actually ran against, not the configured
	// v0.6 address — polling the wrong one just waits out the full timeout.
	entrypoint := v.smartWalletConfig.EntryPointAddress()

	v.logger.Info("⏳ Waiting for UserOp confirmation",
		"userOpHash", userOpHash,
		"entrypoint", entrypoint.Hex())

	// Exponential backoff intervals (in seconds): 1s → 2s → 4s → 8s → 15s → 30s → 60s
	intervals := []time.Duration{1, 2, 4, 8, 15, 30, 60}
	startTime := time.Now()

	intervalIndex := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("timeout or cancellation waiting for UserOp confirmation: %w", err)
		}

		// Try to get receipt from bundler
		receipt, err := bundlerClient.GetUserOperationReceipt(ctx, userOpHash)
		if err == nil && receipt != nil {
			// Inclusion of the EntryPoint transaction does not imply that this
			// UserOperation succeeded. Reuse the parser from the status API,
			// including standard bundlers' nested transaction receipt.
			success, txHash, blockNumber := parseBundlerReceipt(receipt)
			if success == nil || txHash == "" {
				return nil, fmt.Errorf("bundler confirmation receipt is missing success or transactionHash")
			}

			v.logger.Info("✅ UserOp confirmed on-chain",
				"userOpHash", userOpHash,
				"blockNumber", blockNumber,
				"txHash", txHash,
				"elapsed", time.Since(startTime))

			// Fetch the actual on-chain receipt to get gas data.
			// Use a bounded context so a slow/unresponsive RPC node cannot block indefinitely.
			result := &waitForUserOpConfirmationResult{TxHash: txHash, Success: *success}
			if raw := asStringMap(receipt); raw != nil {
				result.Fields = asStringMap(raw["receipt"])
			}
			if result.Fields["status"] == "0x0" || result.Fields["status"] == "0" {
				result.Success = false
			}
			if txHash != "" {
				receiptCtx, receiptCancel := context.WithTimeout(ctx, 30*time.Second)
				onChainReceipt, receiptErr := v.receiptForTx(receiptCtx, common.HexToHash(txHash))
				receiptCancel()
				if receiptErr == nil && onChainReceipt != nil {
					result.Receipt = onChainReceipt
					result.Success = result.Success && onChainReceipt.Status == types.ReceiptStatusSuccessful
				} else if v.logger != nil {
					v.logger.Warn("Failed to fetch on-chain receipt for gas data",
						"txHash", txHash, "error", receiptErr)
				}
			}

			if !result.Success {
				result.FailureReason, result.RevertData = confirmedUserOpFailure(userOpHash, entrypoint, result)
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
		timer := time.NewTimer(currentInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("timeout or cancellation waiting for UserOp confirmation: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// Only the EntryPoint event for this UserOperation can explain its revert.
// A bundle can contain another wallet's failed operation; do not borrow its
// reason. Standard Error(string) and Panic(uint256) decode through abi;
// unknown custom errors keep their raw selector/data for diagnosis.
func confirmedUserOpFailure(userOpHash string, entrypoint common.Address, result *waitForUserOpConfirmationResult) (string, string) {
	if (result.Receipt != nil && result.Receipt.Status == types.ReceiptStatusFailed) || result.Fields["status"] == "0x0" || result.Fields["status"] == "0" {
		return "UserOperation outer transaction reverted after confirmation", ""
	}
	var logs []*types.Log
	if result.Receipt != nil {
		logs = result.Receipt.Logs
	} else if raw, err := json.Marshal(result.Fields["logs"]); err == nil {
		_ = json.Unmarshal(raw, &logs)
	}
	parsed, err := aa.EntryPointMetaData.GetAbi()
	if err == nil {
		event := parsed.Events["UserOperationRevertReason"]
		for _, log := range logs {
			if log == nil || log.Address != entrypoint || len(log.Topics) < 2 || log.Topics[0] != event.ID || log.Topics[1] != common.HexToHash(userOpHash) {
				continue
			}
			values, err := event.Inputs.NonIndexed().Unpack(log.Data)
			if err != nil || len(values) != 2 {
				continue
			}
			data, ok := values[1].([]byte)
			if !ok || len(data) == 0 {
				continue
			}
			raw := "0x" + hex.EncodeToString(data)
			if reason, err := abi.UnpackRevert(data); err == nil {
				return "UserOperation execution reverted: " + reason, raw
			}
			return "UserOperation execution reverted with data " + raw, raw
		}
	}
	return "UserOperation execution reverted after confirmation", ""
}

func confirmedFailureReason(result *waitForUserOpConfirmationResult) string {
	if result.FailureReason != "" {
		return result.FailureReason
	}
	return "UserOperation execution reverted after confirmation"
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
		for shouldWaitForContractWriteConfirmation(step, false) {
			userOpHash := extractUserOpHashFromStep(step)
			if userOpHash == "" {
				v.markUserOpConfirmationUnresolved(step)
				return true
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
				// Keep receipt metadata pending, but fail this workflow's attempt
				// to confirm it. An empty step error would let AnalyzeExecutionResult
				// declare success even though the on-chain outcome is unresolved.
				v.markUserOpConfirmationUnresolved(step)
				return true
			} else {
				v.logger.Info("✅ On-chain confirmation received",
					"stepID", step.Id,
					"userOpHash", userOpHash)

				v.mu.Lock()
				updateConfirmedWriteMetadata(step, userOpHash, result)
				// Update gas cost fields from the on-chain receipt if the step doesn't already have them.
				// Done under v.mu to avoid data races with readers of ExecutionLogs.
				if result != nil && result.Receipt != nil && (step.TotalGasCost == "" || step.TotalGasCost == "0") {
					updateStepGasCostFromReceipt(step, result.Receipt, v)
				}
				v.mu.Unlock()

				if result.Success {
					v.addRPCPropagationDelay()
				}
			}
		}
		return true
	}

	// ETH transfer: SendUserOp may have timed out with a nil receipt.
	// The ethTransfer processor stores receiptStatus="pending" and userOpHash in metadata
	// when the receipt was not available. Poll for confirmation the same way as contractWrite.
	if step.GetEthTransfer() != nil && step.Metadata != nil {
		meta := step.Metadata.AsInterface()
		if metaMap, ok := meta.(map[string]interface{}); ok {
			status, _ := metaMap["receiptStatus"].(string)
			userOpHash, _ := metaMap["userOpHash"].(string)
			if status == "pending" && userOpHash != "" {
				v.mu.Lock()
				step.Success = false // Submission is not confirmed execution.
				v.mu.Unlock()
				v.logger.Info("⏳ On-chain confirmation needed (ethTransfer pending)",
					"stepID", step.Id,
					"userOpHash", userOpHash)

				result, waitErr := v.waitForUserOpConfirmation(userOpHash)
				if waitErr != nil {
					v.logger.Error("❌ Failed to wait for ethTransfer confirmation",
						"stepID", step.Id,
						"userOpHash", userOpHash,
						"error", waitErr)
					v.markUserOpConfirmationUnresolved(step)
				} else {
					v.logger.Info("✅ ethTransfer confirmed on-chain",
						"stepID", step.Id,
						"userOpHash", userOpHash)

					// Update gas cost fields under v.mu to avoid data races with readers of ExecutionLogs
					v.mu.Lock()
					step.Success = result.Success
					step.Error = ""
					step.ErrorCode = avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED
					if !result.Success {
						step.Error = confirmedFailureReason(result)
						step.ErrorCode = avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED
						if result.RevertData != "" {
							metaMap["revertData"] = result.RevertData
						}
					}
					metaMap["receiptStatus"] = "confirmed"
					metaMap["transactionHash"] = result.TxHash
					if !result.Success {
						metaMap["receiptStatus"] = "failed"
					}
					step.Metadata, _ = structpb.NewValue(metaMap)
					step.EndAt = time.Now().UnixMilli()
					if result != nil && result.Receipt != nil && (step.TotalGasCost == "" || step.TotalGasCost == "0") {
						updateStepGasCostFromReceipt(step, result.Receipt, v)
					}
					v.mu.Unlock()

					if result.Success {
						v.addRPCPropagationDelay()
					}
				}
				return true
			}
		}
	}

	return false
}

func (v *VM) markUserOpConfirmationUnresolved(step *avsproto.Execution_Step) {
	v.mu.Lock()
	defer v.mu.Unlock()
	step.Success = false
	step.Error = "UserOperation confirmation unresolved; check its status before retrying"
	step.ErrorCode = avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED
	step.EndAt = time.Now().UnixMilli()
}

// updateConfirmedWriteMetadata resolves only the matching operation. A node
// can contain multiple sequential calls, each with its own UserOperation.
// Preserve method/target/inner-call metadata for the report and recompute the
// step outcome from all calls rather than declaring the whole step successful
// when its first receipt arrives. Caller holds v.mu.
func updateConfirmedWriteMetadata(step *avsproto.Execution_Step, userOpHash string, result *waitForUserOpConfirmationResult) {
	methods := step.Metadata.AsInterface().([]interface{})
	step.Success = true
	step.Error = ""
	step.ErrorCode = avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED
	for _, raw := range methods {
		method, ok := raw.(map[string]interface{})
		if !ok {
			step.Success = false
			continue
		}
		receipt, _ := method["receipt"].(map[string]interface{})
		if receipt != nil && receipt["userOpHash"] == userOpHash && receipt["status"] == "pending" {
			for key, value := range result.Fields {
				receipt[key] = value
			}
			receipt["transactionHash"] = result.TxHash
			if _, ok := receipt["status"].(string); !ok || receipt["status"] == "pending" {
				receipt["status"] = "0x1"
			}
			receipt["executionStatus"] = "confirmed"
			method["success"] = result.Success
			method["error"] = ""
			if !result.Success {
				receipt["executionStatus"] = "failed"
				method["error"] = confirmedFailureReason(result)
				if result.RevertData != "" {
					receipt["revertData"] = result.RevertData
				}
			}
		}
		if method["success"] != true {
			step.Success = false
			if message, _ := method["error"].(string); step.Error == "" && message != "" {
				step.Error = message
			}
			if receipt["executionStatus"] == "failed" {
				step.ErrorCode = avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED
			}
		}
	}
	step.Metadata, _ = structpb.NewValue(methods)
	step.EndAt = time.Now().UnixMilli()
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
