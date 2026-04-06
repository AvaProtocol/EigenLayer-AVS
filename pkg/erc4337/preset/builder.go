package preset

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/paymaster"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

var (
	// Realistic gas limits for UserOp construction (bundler estimation often fails)
	// These values are based on actual ETH transfer and smart wallet operations
	// Last validated: Oct 2025. To update: run representative UserOperations on target network,
	// observe actual gas usage, and adjust these values accordingly
	DEFAULT_CALL_GAS_LIMIT         = big.NewInt(200000)  // 200K for smart wallet execute + ETH transfer (more headroom)
	ETH_TRANSFER_GAS_COST          = big.NewInt(21000)   // Standard ETH transfer gas cost
	ETH_TRANSFER_GAS_MULTIPLIER    = int64(5)            // Multiplier for wrapped operations with ETH transfers
	BATCH_OVERHEAD_BUFFER_PERCENT  = 0                   // No buffer - bundler estimation is already accurate
	DEFAULT_VERIFICATION_GAS_LIMIT = big.NewInt(1000000) // 1M for signature verification + paymaster validation (very conservative)
	DEFAULT_PREVERIFICATION_GAS    = big.NewInt(50000)   // 50K for bundler overhead

	// UUPS proxy wallet deployment gas limit
	// Based on real-world data from Base/Sepolia mainnet: UUPS proxy + initialize(owner) requires 1.3M-1.6M gas
	// This covers:
	// - Factory contract execution (~100K)
	// - Proxy deployment (~200K)
	// - Initialization with owner (~100K-300K)
	// - validateUserOp() with AAConfig.controller() call (~200K-500K)
	// - Paymaster validation if present
	// Observed AA95 on Sepolia with 2.4M; use 3.0M to avoid bundler struct caps
	DEPLOYMENT_VERIFICATION_GAS_LIMIT = big.NewInt(3000000) // 3.0M gas for wallet deployment + validation

	callGasLimit         = DEFAULT_CALL_GAS_LIMIT
	verificationGasLimit = DEFAULT_VERIFICATION_GAS_LIMIT
	preVerificationGas   = DEFAULT_PREVERIFICATION_GAS

	// the signature isnt important, only length check
	dummySigForGasEstimation = crypto.Keccak256Hash(common.FromHex("0xdead123"))
	accountSalt              = big.NewInt(0)

	// example tx send to entrypoint: https://sepolia.basescan.org/tx/0x7580ac508a2ac34cf6a4f4346fb6b4f09edaaa4f946f42ecdb2bfd2a633d43af#eventlog
	userOpEventTopic0 = common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")

	// EnablePaymasterReimbursement controls whether to add ETH reimbursement to paymaster
	// When true: Wraps execute() with executeBatchWithValues() to atomically reimburse paymaster
	// Default: true (reimburse paymaster for gas costs)
	EnablePaymasterReimbursement = true

	// GasReimbursementBufferPercent is the extra buffer percentage added to the reimbursement amount
	// Formula: reimbursement = (total UserOp gas × maxFeePerGas) × (100% + buffer%)
	// Default: 0% - bundler gas estimation is already conservative (~22% higher than actual)
	// Adding buffers would INCREASE the remaining balance, not reduce it.
	// The remaining balance (~0.000065 ETH) is due to estimation variance and is acceptable.
	GasReimbursementBufferPercent = 0

	// globalNonceManager tracks pending nonces across all UserOp submissions
	// to prevent conflicts with transactions pending in the bundler's mempool
	globalNonceManager = bundler.NewNonceManager(nil)

	// senderLockStripesArr provides a bounded set of mutexes used to serialize
	// UserOp processing per sender address. The sender address is deterministically
	// mapped to one of these locks to avoid unbounded growth of a per-sender map.
	senderLockStripesArr [256]sync.Mutex
)

// getSenderLock returns a mutex for the given sender address using a striped
// locking strategy. This serializes UserOp processing for a given sender
// while keeping the total number of locks bounded.
func getSenderLock(sender common.Address) *sync.Mutex {
	idx := int(sender[common.AddressLength-1]) % len(senderLockStripesArr)
	return &senderLockStripesArr[idx]
}

// ErrPaymasterNonceConflict is returned by sendUserOpCore when a paymaster UserOp
// hits an AA25 nonce conflict. The paymaster signature is bound to the original nonce,
// so the entire UserOp must be rebuilt at the sendUserOpShared level with the correct nonce.
type ErrPaymasterNonceConflict struct {
	Nonce *big.Int // The correct nonce to use for the rebuild
}

func (e *ErrPaymasterNonceConflict) Error() string {
	return fmt.Sprintf("paymaster nonce conflict: rebuild required with nonce %s", e.Nonce.String())
}

// VerifyingPaymasterRequest contains the parameters needed for paymaster functionality. This use the reference from https://github.com/eth-optimism/paymaster-reference
type VerifyingPaymasterRequest struct {
	PaymasterAddress  common.Address
	ValidUntil        *big.Int
	ValidAfter        *big.Int
	SkipReimbursement bool // When true, skip gas reimbursement wrapping (e.g., for withdrawals)
}

func GetVerifyingPaymasterRequestForDuration(address common.Address, duration time.Duration) *VerifyingPaymasterRequest {
	now := time.Now().Unix()
	validUntil := now + int64(duration.Seconds())

	return &VerifyingPaymasterRequest{
		PaymasterAddress: address,
		ValidUntil:       big.NewInt(validUntil),
		// validAfter=0 means "valid from genesis, therefore valid immediately". This avoids
		// clock drift between the aggregator's wall clock and the bundler's block.timestamp,
		// which can differ by tens of minutes (especially with archive RPC nodes).
		//
		// Security: The paymaster signature is bound to this specific UserOp (sender, nonce,
		// calldata, paymasterAndData), so once included on-chain, nonce prevents replay.
		// If a signed UserOp is leaked before submission, it could be submitted by anyone
		// until validUntil expires. The replay window is controlled by `duration`, so callers
		// should use a short duration (minutes, not hours) to limit pre-submission exposure.
		ValidAfter: big.NewInt(0),
	}
}

// waitForUserOpConfirmation waits for a UserOperation to be confirmed on-chain using
// a hybrid approach: WebSocket subscription for real-time events + exponential backoff polling as fallback.
// This handles bundler delays gracefully without blocking for a fixed timeout.
//
// Returns:
// - (*types.Receipt, nil) if UserOp was confirmed and executed successfully
// - (nil, nil) if timeout reached without confirmation (UserOp may still be pending)
// - (nil, error) if an unrecoverable error occurred or UserOp execution failed
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
		maxWaitTime     = 1 * time.Minute // Maximum total wait time (increased from 30s to handle slow chains)
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

// EstimateGasReimbursementAmount computes the ETH amount to reimburse the paymaster.
// Formula: (bundler's estimated gas OR fallback gas + ETH transfer) × 20% buffer × maxFeePerGas
// This uses the bundler's accurate gas estimation when available, otherwise uses conservative fallbacks.
// This function is exported so it can be used by the aggregator for pre-flight balance validation.
func EstimateGasReimbursementAmount(client *ethclient.Client, gasEstimate *bundler.GasEstimation, lgr logger.Logger) (*big.Int, error) {
	l := logger.EnsureLogger(lgr)

	maxFeePerGas, _, err := eip1559.SuggestFee(client)
	if err != nil {
		l.Warn("Failed to get gas price, using 20 gwei fallback", "error", err)
		maxFeePerGas = big.NewInt(20_000_000_000) // 20 gwei
	}

	// Dynamic gas calculation: Use bundler's gas estimation for reimbursement
	// Formula: (bundler's estimated gas + ETH transfer) × buffer
	var totalUserOpGas *big.Int

	if gasEstimate != nil {
		preVerificationGas := new(big.Int).Set(gasEstimate.PreVerificationGas)
		verificationGas := new(big.Int).Set(gasEstimate.VerificationGasLimit)
		callGas := new(big.Int).Set(gasEstimate.CallGasLimit)
		ethTransferGas := new(big.Int).Set(ETH_TRANSFER_GAS_COST)

		totalUserOpGas = new(big.Int).Add(preVerificationGas, verificationGas)
		totalUserOpGas.Add(totalUserOpGas, callGas)
		totalUserOpGas.Add(totalUserOpGas, ethTransferGas)

		l.Debug("Using bundler gas estimation", "preVerification", preVerificationGas, "verification", verificationGas, "call", callGas, "ethTransfer", ethTransferGas)
	} else {
		preVerificationGas := new(big.Int).Set(DEFAULT_PREVERIFICATION_GAS)
		verificationGas := new(big.Int).Set(DEFAULT_VERIFICATION_GAS_LIMIT)
		callGas := new(big.Int).Set(DEFAULT_CALL_GAS_LIMIT)
		ethTransferGas := new(big.Int).Set(ETH_TRANSFER_GAS_COST)

		totalUserOpGas = new(big.Int).Add(preVerificationGas, verificationGas)
		totalUserOpGas.Add(totalUserOpGas, callGas)
		totalUserOpGas.Add(totalUserOpGas, ethTransferGas)

		l.Debug("Using fallback gas estimation", "preVerification", preVerificationGas, "verification", verificationGas, "call", callGas)
	}

	gasBufferMultiplier := big.NewInt(100 + int64(BATCH_OVERHEAD_BUFFER_PERCENT))
	effectiveGas := new(big.Int).Mul(totalUserOpGas, gasBufferMultiplier)
	effectiveGas.Div(effectiveGas, big.NewInt(100))

	baseCost := new(big.Int).Mul(effectiveGas, maxFeePerGas)

	bufferMultiplier := big.NewInt(100 + int64(GasReimbursementBufferPercent))
	reimbursement := new(big.Int).Mul(baseCost, bufferMultiplier)
	reimbursement = new(big.Int).Div(reimbursement, big.NewInt(100))

	l.Debug("Gas reimbursement estimated", "totalGas", totalUserOpGas, "maxFee", maxFeePerGas, "reimbursement", reimbursement)

	return reimbursement, nil
}

// wrapWithReimbursement wraps original SimpleAccount.execute() calldata with executeBatchWithValues
// to add a second step that transfers reimbursement ETH to the reimbursement recipient (paymaster owner or paymaster), atomically.
// wrapWithReimbursement wraps original SimpleAccount.execute() calldata with executeBatchWithValues
// to add reimbursement + optional execution fee transfers, atomically.
// If executionFeeWei is non-nil and > 0, a third batch operation is added to transfer the fee
// to the feeRecipient (same as reimbursement recipient).
func wrapWithReimbursement(
	client *ethclient.Client,
	originalCallData []byte,
	reimbursementRecipient common.Address,
	gasEstimate *bundler.GasEstimation,
	executionFeeWei *big.Int,
	lgr logger.Logger,
) (wrappedCalldata []byte, reimbursementAmount *big.Int, outgoingValue *big.Int, err error) {
	l := logger.EnsureLogger(lgr)
	// Estimate reimbursement
	reimbursementAmount, err = EstimateGasReimbursementAmount(client, gasEstimate, l)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to estimate reimbursement: %w", err)
	}

	// Decode execute(dest, value, data) - we need to manually decode since we don't have a public GetAccountABI
	// The execute() function signature is: execute(address dest, uint256 value, bytes calldata func)
	// Calldata format: [4-byte selector][32-byte dest][32-byte value][offset to bytes][length][data...]

	if len(originalCallData) < 4 {
		return nil, nil, nil, fmt.Errorf("calldata too short: %d bytes", len(originalCallData))
	}

	// Skip function selector (4 bytes) and decode the ABI-encoded parameters
	params := originalCallData[4:]
	if len(params) < 96 { // minimum: 32 (dest) + 32 (value) + 32 (offset)
		return nil, nil, nil, fmt.Errorf("calldata params too short: %d bytes", len(params))
	}

	// Decode: address dest (32 bytes, right-aligned)
	dest := common.BytesToAddress(params[12:32]) // Skip 12 padding bytes, take last 20

	// Decode: uint256 value (32 bytes)
	value := new(big.Int).SetBytes(params[32:64])

	// Decode: bytes data (dynamic, offset at params[64:96])
	dataOffset := new(big.Int).SetBytes(params[64:96]).Uint64()
	var data []byte
	if dataOffset < uint64(len(params)) {
		// Data exists, read length and content
		dataLength := new(big.Int).SetBytes(params[dataOffset : dataOffset+32]).Uint64()
		if dataOffset+32+dataLength <= uint64(len(params)) {
			data = params[dataOffset+32 : dataOffset+32+dataLength]
		}
	}

	// If data is nil or empty, use make([]byte, 0) to avoid ABI encoding issues
	if len(data) == 0 {
		data = make([]byte, 0)
	}

	l.Debug("Decoded execute() params", "dest", dest.Hex(), "value", value.String(), "dataLen", len(data))

	// Create batch arrays for executeBatchWithValues
	// [0] = original operation (e.g., withdrawal)
	// [1] = reimbursement to paymaster owner (to compensate for gas paid by paymaster)
	// [2] = execution fee to fee recipient (optional, only if executionFeeWei > 0)
	targets := []common.Address{dest, reimbursementRecipient}
	values := []*big.Int{value, reimbursementAmount}
	calldatas := [][]byte{data, make([]byte, 0)}

	// Add execution fee as 3rd batch operation if specified
	if executionFeeWei != nil && executionFeeWei.Sign() > 0 {
		targets = append(targets, reimbursementRecipient)
		values = append(values, executionFeeWei)
		calldatas = append(calldatas, make([]byte, 0))
		l.Debug("Execution fee added to batch", "fee_wei", executionFeeWei.String(), "recipient", reimbursementRecipient.Hex())
	}

	l.Debug("Reimbursement wrapping", "dest", dest.Hex(), "value", value.String(), "reimburse", reimbursementAmount.String(), "recipient", reimbursementRecipient.Hex(), "batch_ops", len(targets))

	// Use manual ABI encoding to bypass Go's ABI encoder bug with empty []byte slices
	wrappedCalldata, err = aa.PackExecuteBatchWithValues(targets, values, calldatas)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to pack executeBatchWithValues: %w", err)
	}

	l.Debug("Calldata manually encoded", "bytes", len(wrappedCalldata))

	// outgoingValue = original value + execution fee (reimbursement is added by caller)
	totalValue := new(big.Int).Set(value)
	if executionFeeWei != nil && executionFeeWei.Sign() > 0 {
		totalValue.Add(totalValue, executionFeeWei)
	}

	return wrappedCalldata, reimbursementAmount, totalValue, nil
}

// SendUserOp builds, signs, and sends a UserOperation to be executed.
// It then listens on-chain for 60 seconds to wait until the userops is executed.
// If the userops is executed, the transaction Receipt is also returned.
// If paymasterReq is nil, a standard UserOp without paymaster is sent.
// sendUserOpShared contains the core UserOp processing logic shared between SendUserOp and SendUserOpWithWsClient.
// It handles UserOp building, signing, bundler communication, and transaction confirmation monitoring.
// The WebSocket client is only used for efficient transaction confirmation monitoring, not for sending UserOps.
// This eliminates code duplication and makes maintenance easier.
func sendUserOpShared(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	saltOverride *big.Int,
	wsClient *ethclient.Client,
	executionFeeWei *big.Int,
	lgr logger.Logger,
) (*userop.UserOperation, *types.Receipt, error) {
	l := logger.EnsureLogger(lgr)

	// Serialize UserOp processing per sender to prevent concurrent nonce races.
	// The effective sender is the senderOverride (if provided) or the owner.
	lockAddr := owner
	if senderOverride != nil {
		lockAddr = *senderOverride
	}
	senderLock := getSenderLock(lockAddr)
	senderLock.Lock()
	defer senderLock.Unlock()

	var userOp *userop.UserOperation
	var err error
	entrypoint := smartWalletConfig.EntrypointAddress

	// Preserve original calldata for possible no-paymaster fallback
	originalCallData := make([]byte, len(callData))
	copy(originalCallData, callData)

	// Initialize clients once and reuse them
	bundlerClient, err := bundler.NewBundlerClient(smartWalletConfig.BundlerURL)
	if err != nil {
		return nil, nil, err
	}

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, nil, err
	}
	defer client.Close()

	// Step 0: Estimate gas for UNWRAPPED calldata first (only needed for reimbursement wrapping)
	// Skip entirely when SkipReimbursement is set — the result is only used in Step 0.5
	//
	// NOTE on nonce safety: BuildUserOpWithPaymaster calls GetNextNonce internally, which is
	// a read-only operation on the NonceManager cache (it does NOT call IncrementNonce or
	// SetNonce). These temporary UserOps are never sent, so the cache is not polluted by
	// gas estimation calls. The cached nonce is only advanced when IncrementNonce is invoked
	// after a successful send.
	var unwrappedGasEstimate *bundler.GasEstimation
	if paymasterReq != nil && !paymasterReq.SkipReimbursement {
		// Build a temporary UserOp with unwrapped calldata to get accurate gas estimate
		tempUserOp, tempErr := BuildUserOpWithPaymaster(
			smartWalletConfig,
			client,
			bundlerClient,
			owner,
			callData, // Unwrapped calldata
			paymasterReq.PaymasterAddress,
			paymasterReq.ValidUntil,
			paymasterReq.ValidAfter,
			senderOverride,
			saltOverride,
			nil,                         // nonceOverride - fetch from chain
			big.NewInt(0),               // callGasLimit: 0 to force bundler estimation
			big.NewInt(0),               // verificationGasLimit: 0 to force bundler estimation
			DEFAULT_PREVERIFICATION_GAS, // preVerificationGas: non-zero for simulation
			l,
		)
		if tempErr == nil {
			// Set dummy signature for estimation
			tempUserOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

			// Estimate gas for unwrapped calldata
			gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *tempUserOp, aa.EntrypointAddress, map[string]any{})
			if gasErr == nil && gas != nil {
				unwrappedGasEstimate = gas
				l.Debug("Gas estimation for unwrapped calldata", "callGas", gas.CallGasLimit, "verificationGas", gas.VerificationGasLimit, "preVerificationGas", gas.PreVerificationGas)
			} else {
				l.Warn("Gas estimation for unwrapped calldata failed, will use fallback", "error", gasErr)
			}
		}
	}

	// Step 0.5: If paymaster reimbursement is enabled and not explicitly skipped,
	// wrap with executeBatchWithValues to atomically reimburse the paymaster.
	// SkipReimbursement is set for withdrawals where the paymaster absorbs gas costs.
	if paymasterReq != nil && EnablePaymasterReimbursement && !paymasterReq.SkipReimbursement {
		// Get reimbursement recipient from config (paymaster owner address)
		reimbursementRecipient := smartWalletConfig.PaymasterOwnerAddress
		if reimbursementRecipient == (common.Address{}) {
			reimbursementRecipient = paymasterReq.PaymasterAddress
		}

		// Prepare wrapped candidate using the gas estimate from unwrapped calldata
		// This ensures consistent reimbursement calculation
		wrappedCandidate, initialReimb, opValue, wrapErr := wrapWithReimbursement(client, callData, reimbursementRecipient, unwrappedGasEstimate, executionFeeWei, l)
		if wrapErr != nil {
			l.Warn("Failed to prepare reimbursement wrapping, paymaster absorbs gas costs", "error", wrapErr)
		} else {
			// Resolve smart wallet sender
			var sender *common.Address
			if senderOverride != nil {
				sender = senderOverride
			} else {
				// Derive sender from owner (salt:0)
				derived, derr := aa.GetSenderAddress(client, owner, accountSalt)
				if derr == nil {
					sender = derived
				}
			}

			// Fetch current smart wallet ETH balance (0 if unknown)
			balance := big.NewInt(0)
			if sender != nil {
				if bal, balErr := client.BalanceAt(context.Background(), *sender, nil); balErr == nil {
					balance = bal
				} else {
					l.Warn("Failed to fetch smart wallet balance", "error", balErr)
				}
			}

			// Use initial (fallback) reimbursement estimate; avoid an extra bundler estimation here
			// The final bundler estimation will still occur later for the chosen path
			var reimbToUse = initialReimb

			// The wrapped operation sends BOTH the original value AND the reimbursement from the wallet.
			// We must check that the wallet can cover both, not just the reimbursement alone.
			totalOutflow := new(big.Int).Add(opValue, reimbToUse)
			if balance.Cmp(totalOutflow) < 0 {
				l.Debug("Skipping reimbursement wrap: insufficient ETH for value + reimbursement",
					"balance", balance, "opValue", opValue, "reimburse", reimbToUse, "totalNeeded", totalOutflow)
				// Keep original unwrapped callData
			} else {
				callData = wrappedCandidate
				l.Debug("Paymaster reimbursement enabled", "amount", reimbToUse, "recipient", reimbursementRecipient.Hex())
			}
		}
	}

	// Step 1: Estimate gas for the FINAL calldata (wrapped or unwrapped)
	// Build a temporary UserOp to estimate gas
	var estimatedCallGas, estimatedVerificationGas, estimatedPreVerificationGas *big.Int
	if paymasterReq != nil {
		// Check if calldata is wrapped (executeBatchWithValues) - bundler can't simulate wrapped operations
		// Wrapped operations include reimbursement and cause UserOperationReverted (-32521) during simulation
		// Bundler logs confirm: unwrapped with 0x0 succeeds, wrapped with 0x0 fails with -32521
		isWrappedOperation := len(callData) >= 4 && hexutil.Encode(callData[:4]) == "0xc3ff72fc"

		if isWrappedOperation {
			// SKIP gas estimation for wrapped operations - bundler simulation always fails with -32521
			// Use fallback defaults directly since estimation will fail anyway
			l.Debug("Skipping gas estimation for wrapped operation (executeBatchWithValues)")
			estimatedCallGas = new(big.Int).Mul(DEFAULT_CALL_GAS_LIMIT, big.NewInt(ETH_TRANSFER_GAS_MULTIPLIER))
			estimatedVerificationGas = DEFAULT_VERIFICATION_GAS_LIMIT
			estimatedPreVerificationGas = DEFAULT_PREVERIFICATION_GAS
		} else {
			// Attempt bundler gas estimation on unwrapped calldata
			// IMPORTANT: Pass 0 for callGasLimit and verificationGasLimit to force bundler to actually estimate
			// (Bundler only estimates when input values are 0, otherwise it just echoes them back)
			// For unwrapped operations, 0 works fine and bundler can successfully estimate
			tempUserOp, tempErr := BuildUserOpWithPaymaster(
				smartWalletConfig,
				client,
				bundlerClient,
				owner,
				callData, // Unwrapped calldata
				paymasterReq.PaymasterAddress,
				paymasterReq.ValidUntil,
				paymasterReq.ValidAfter,
				senderOverride,
				saltOverride,
				nil,                         // nonceOverride - fetch from chain
				big.NewInt(0),               // callGasLimit: 0 to force bundler estimation (works for unwrapped)
				big.NewInt(0),               // verificationGasLimit: 0 to force bundler estimation (works for unwrapped)
				DEFAULT_PREVERIFICATION_GAS, // preVerificationGas: non-zero for simulation (bundler will recalculate)
				l,
			)
			if tempErr != nil {
				l.Warn("Failed to build UserOp for gas estimation", "error", tempErr)
				return nil, nil, tempErr
			}

			// Set dummy signature for estimation (paymaster signature already set in BuildUserOpWithPaymaster)
			tempUserOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

			// Estimate gas using bundler with the EXACT UserOp structure (including paymaster)
			// Retry up to 3 times for better reliability
			var gas *bundler.GasEstimation
			var gasErr error
			maxRetries := 3
			for attempt := 0; attempt < maxRetries; attempt++ {
				if attempt > 0 {
					l.Debug("Retrying gas estimation", "attempt", attempt+1, "maxRetries", maxRetries)
					// Small delay between retries
					time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
				}
				gas, gasErr = bundlerClient.EstimateUserOperationGas(context.Background(), *tempUserOp, aa.EntrypointAddress, map[string]any{})
				if gasErr == nil && gas != nil {
					break // Success
				}
				if attempt < maxRetries-1 {
					l.Warn("Gas estimation attempt failed", "attempt", attempt+1, "maxRetries", maxRetries, "error", gasErr)
				}
			}
			if gasErr != nil || gas == nil {
				l.Warn("Gas estimation failed, using defaults", "attempts", maxRetries, "error", gasErr)
				// Fallback to conservative defaults when estimation fails
				estimatedCallGas = new(big.Int).Mul(DEFAULT_CALL_GAS_LIMIT, big.NewInt(ETH_TRANSFER_GAS_MULTIPLIER))
				estimatedVerificationGas = DEFAULT_VERIFICATION_GAS_LIMIT
				estimatedPreVerificationGas = DEFAULT_PREVERIFICATION_GAS
			} else {
				estimatedCallGas = gas.CallGasLimit
				estimatedVerificationGas = gas.VerificationGasLimit
				estimatedPreVerificationGas = gas.PreVerificationGas
				l.Debug("Gas estimation successful", "callGas", estimatedCallGas, "verificationGas", estimatedVerificationGas, "preVerificationGas", estimatedPreVerificationGas)
			}
		}
	}

	// Build the userOp based on whether paymaster is requested or not
	if paymasterReq == nil {
		// Standard UserOp without paymaster
		userOp, err = BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride, saltOverride, l)
	} else {
		// UserOp with paymaster support - use estimated gas limits
		userOp, err = BuildUserOpWithPaymaster(
			smartWalletConfig,
			client,
			bundlerClient,
			owner,
			callData,
			paymasterReq.PaymasterAddress,
			paymasterReq.ValidUntil,
			paymasterReq.ValidAfter,
			senderOverride,
			saltOverride,
			nil,                         // nonceOverride - let it fetch from chain
			estimatedCallGas,            // Use estimated gas
			estimatedVerificationGas,    // Use estimated gas
			estimatedPreVerificationGas, // Use estimated gas
			l,
		)
	}

	if err != nil {
		return nil, nil, err
	}

	// Run full validation on the final, fully-built userOp to catch signature/paymaster issues early
	if paymasterReq != nil {
		if simErr := bundlerClient.SimulateUserOperation(context.Background(), *userOp, entrypoint); simErr != nil {
			l.Warn("SimulateUserOperation failed", "error", simErr)
			return userOp, nil, fmt.Errorf("simulate validation failed: %w", simErr)
		}
	}

	// Local signature self-check (detects struct/signature drift before sending)
	if paymasterReq != nil {
		chainID, _ := client.ChainID(context.Background())
		hash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
		// Recover signer
		if len(userOp.Signature) == 65 {
			if pub, err := crypto.SigToPub(hash.Bytes(), userOp.Signature); err == nil {
				recovered := crypto.PubkeyToAddress(*pub)
				ctrl := smartWalletConfig.ControllerAddress
				if (ctrl != common.Address{}) && !strings.EqualFold(recovered.Hex(), ctrl.Hex()) {
					l.Warn("Signature check failed", "recovered", recovered.Hex(), "controller", ctrl.Hex())
					return userOp, nil, fmt.Errorf("local signature check failed: recovered %s != controller %s", recovered.Hex(), ctrl.Hex())
				}
			}
		}
	}

	// Send the UserOp to the bundler using the existing sendUserOpCore function.
	// If a paymaster nonce conflict occurs, rebuild the entire UserOp with the correct nonce
	// (new nonce → new paymaster hash → new paymaster signature → new UserOp signature).
	maxPaymasterRetries := 3
	var txResult string
	for paymasterRetry := 0; paymasterRetry < maxPaymasterRetries; paymasterRetry++ {
		txResult, err = sendUserOpCore(smartWalletConfig, userOp, client, bundlerClient, l)
		if err == nil {
			break
		}

		// Check for paymaster nonce conflict — requires full UserOp rebuild
		var nonceConflict *ErrPaymasterNonceConflict
		if errors.As(err, &nonceConflict) && paymasterReq != nil {
			if paymasterRetry >= maxPaymasterRetries-1 {
				l.Warn("Paymaster nonce conflict retries exhausted",
					"retries", maxPaymasterRetries,
					"last_nonce", nonceConflict.Nonce.String())
				return userOp, nil, fmt.Errorf("paymaster nonce conflict after %d rebuilds: %w", maxPaymasterRetries, err)
			}

			// After sendUserOpCore flushed stuck UserOps, the flushed UserOps may still
			// be pending on-chain (not yet mined). Poll until the on-chain nonce advances
			// to at least the nonce we need, so the rebuilt UserOp won't hit AA25 again.
			targetNonce := nonceConflict.Nonce
			l.Info("Waiting for on-chain nonce to advance after flush",
				"target_nonce", targetNonce.String())

			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				freshOnChain, nonceErr := aa.GetNonce(client, userOp.Sender, accountSalt)
				if nonceErr == nil && freshOnChain.Cmp(targetNonce) >= 0 {
					l.Info("On-chain nonce advanced, proceeding with rebuild",
						"on_chain_nonce", freshOnChain.String(),
						"target_nonce", targetNonce.String())
					targetNonce = freshOnChain
					break
				}
				l.Debug("On-chain nonce not yet advanced, polling...",
					"current", freshOnChain,
					"target", targetNonce.String())
				time.Sleep(3 * time.Second)
			}

			// Update the cache with the latest nonce
			globalNonceManager.SetNonce(userOp.Sender, targetNonce)

			l.Info("Rebuilding paymaster UserOp with correct nonce",
				"retry", paymasterRetry+1,
				"nonce", targetNonce.String())

			userOp, err = BuildUserOpWithPaymaster(
				smartWalletConfig,
				client,
				bundlerClient,
				owner,
				callData,
				paymasterReq.PaymasterAddress,
				paymasterReq.ValidUntil,
				paymasterReq.ValidAfter,
				senderOverride,
				saltOverride,
				targetNonce,
				estimatedCallGas,
				estimatedVerificationGas,
				estimatedPreVerificationGas,
				l,
			)
			if err != nil {
				l.Warn("Failed to rebuild paymaster UserOp", "error", err)
				return nil, nil, fmt.Errorf("failed to rebuild paymaster UserOp after nonce conflict: %w", err)
			}
			continue
		}

		// Fallback: if paymaster path failed with invalid params, retry without paymaster (self-funded)
		if paymasterReq != nil && (strings.Contains(strings.ToLower(err.Error()), "invalid useroperation") || strings.Contains(err.Error(), "-32602")) {
			l.Warn("Paymaster send failed with invalid params, retrying without paymaster")
			// Rebuild WITHOUT paymaster and WITHOUT reimbursement wrapping
			userOpNoPM, buildErr := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, originalCallData, senderOverride, saltOverride, l)
			if buildErr != nil {
				l.Warn("Fallback UserOp build without paymaster failed", "error", buildErr)
				return userOp, nil, fmt.Errorf("failed to build fallback UserOp without paymaster: %w", buildErr)
			}
			// Try to send self-funded userOp
			txResult, err = sendUserOpCore(smartWalletConfig, userOpNoPM, client, bundlerClient, l)
			if err != nil {
				return userOpNoPM, nil, err
			}
			userOp = userOpNoPM
		} else {
			return userOp, nil, err
		}
		break // non-nonce-conflict errors don't loop
	}

	// Wait for UserOp confirmation using exponential backoff polling
	// This is more efficient than a fixed 3-minute timeout and handles bundler delays gracefully
	receipt, err := waitForUserOpConfirmation(client, wsClient, entrypoint, txResult, lgr)
	if err != nil {
		// Check if this is a UserOp execution failure (not just a timeout)
		if strings.Contains(err.Error(), "UserOp execution failed") {
			l.Warn("UserOp execution failed", "hash", txResult, "error", err)
			return userOp, nil, fmt.Errorf("UserOp execution failed: %w", err)
		}
		// For other errors (timeout, network issues), return nil receipt but no error
		// This allows the caller to distinguish between execution failure and pending status
		l.Warn("Failed to get UserOp confirmation", "hash", txResult, "error", err)
		return userOp, nil, nil
	}
	if receipt == nil {
		l.Debug("No receipt received for UserOp, may still be pending", "hash", txResult)
		return userOp, nil, nil
	}

	l.Debug("UserOp confirmed", "block", receipt.BlockNumber.Uint64(), "txHash", receipt.TxHash.Hex(), "gasUsed", receipt.GasUsed)

	return userOp, receipt, nil
}

// SendUserOp creates and manages its own WebSocket client for transaction monitoring.
// Use this for single operations where you don't need to optimize WebSocket connection reuse.
// The WebSocket is only used for monitoring transaction confirmation, not for sending UserOps.
// If paymasterReq is provided, it will use the paymaster parameters.
// senderOverride: If provided, use this as the smart account sender.
// saltOverride: If provided (and the account is not yet deployed), use this salt to produce initCode.
func SendUserOp(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	saltOverride *big.Int,
	executionFeeWei *big.Int,
	lgr logger.Logger,
) (*userop.UserOperation, *types.Receipt, error) {
	l := logger.EnsureLogger(lgr)
	l.Debug("SendUserOp started", "owner", owner.Hex(), "bundler", smartWalletConfig.BundlerURL)

	// Only create WebSocket client if URL is provided
	var wsClient *ethclient.Client
	var err error
	if smartWalletConfig.EthWsUrl != "" {
		wsClient, err = ethclient.Dial(smartWalletConfig.EthWsUrl)
		if err != nil {
			l.Warn("WebSocket client creation failed, will use polling", "error", err)
			wsClient = nil
		} else {
			defer wsClient.Close()
		}
	} else {
		l.Debug("No WebSocket URL configured, will use polling for receipt")
		wsClient = nil
	}

	// Use the shared logic for the main UserOp processing
	return sendUserOpShared(smartWalletConfig, owner, callData, paymasterReq, senderOverride, saltOverride, wsClient, executionFeeWei, lgr)
}

// sendUserOpCore contains the shared retry loop logic for sending UserOps to the bundler.
// This is the core implementation used by both SendUserOp and SendUserOpWithWsClient.
// Returns (txResult, error)
func sendUserOpCore(
	smartWalletConfig *config.SmartWalletConfig,
	userOp *userop.UserOperation,
	client *ethclient.Client,
	bundlerClient *bundler.BundlerClient,
	lgr logger.Logger,
) (string, error) {
	l := logger.EnsureLogger(lgr)
	var txResult string
	var err error
	maxRetries := 3

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %w", err)
	}

	entrypoint := smartWalletConfig.EntrypointAddress

	// Log bundler mempool and nonce state for diagnostics
	if pendingOps, mempoolErr := bundlerClient.GetPendingUserOpsForSender(context.Background(), entrypoint, userOp.Sender); mempoolErr == nil && len(pendingOps) > 0 {
		l.Debug("Bundler mempool has pending UserOps for sender",
			"sender", userOp.Sender.Hex(),
			"pending_count", len(pendingOps))
		for i, op := range pendingOps {
			l.Debug("Pending UserOp in mempool",
				"index", i,
				"nonce", op.Nonce,
				"sender", op.Sender.Hex())
		}
	}
	if onChainNonce, onChainErr := aa.GetNonce(client, userOp.Sender, accountSalt); onChainErr == nil {
		cachedNonce, hasCached := globalNonceManager.GetCachedNonce(userOp.Sender)
		l.Debug("Nonce state at sendUserOpCore entry",
			"sender", userOp.Sender.Hex(),
			"on_chain_nonce", onChainNonce.String(),
			"cached_nonce_exists", hasCached,
			"cached_nonce", fmt.Sprintf("%v", cachedNonce),
			"userOp_nonce", fmt.Sprintf("%v", userOp.Nonce))
	}

	// Fetch nonce before entering the retry loop
	// IMPORTANT: Only refresh nonce if it's not already set (e.g., from BuildUserOpWithPaymaster)
	// Changing the nonce invalidates both the UserOp signature and paymaster signature
	// NOTE: A nonce of 0 is valid for new accounts, so we only check for nil (not 0)
	var freshNonce *big.Int
	if userOp.Nonce == nil {
		// Use NonceManager for non-paymaster UserOps
		var err error
		freshNonce, err = globalNonceManager.GetNextNonce(client, userOp.Sender, func() (*big.Int, error) {
			return aa.GetNonce(client, userOp.Sender, accountSalt)
		})
		if err != nil {
			return "", fmt.Errorf("failed to get nonce: %w", err)
		}
		userOp.Nonce = freshNonce
	}

	for retry := 0; retry < maxRetries; retry++ {
		// Re-estimate gas with current nonce (only on first attempt or if previous failed due to gas)
		// IMPORTANT:
		// - Skip gas re-estimation if paymaster is present (would invalidate paymaster signature)
		hasPaymaster := len(userOp.PaymasterAndData) > 0
		if !hasPaymaster && (retry == 0 || (err != nil && strings.Contains(err.Error(), "gas"))) {
			userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

			// IMPORTANT: Set callGasLimit and verificationGasLimit to very small values to force bundler to actually estimate
			// (Bundler only estimates when input values are 0 or very small, otherwise it just echoes them back)
			// However, we can't use 0 because the bundler's simulation fails with UserOperationReverted error
			// Using MIN_CALL_GAS_LIMIT (21,000) as a lower bound that allows simulation to succeed
			userOp.CallGasLimit = big.NewInt(21000)                 // MIN_CALL_GAS_LIMIT to allow simulation (bundler will estimate)
			userOp.VerificationGasLimit = big.NewInt(100000)        // small value to allow simulation (bundler will estimate)
			userOp.PreVerificationGas = DEFAULT_PREVERIFICATION_GAS // non-zero for simulation (bundler will recalculate)

			gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *userOp, aa.EntrypointAddress, map[string]any{})
			if gasErr == nil && gas != nil {
				userOp.PreVerificationGas = gas.PreVerificationGas
				userOp.VerificationGasLimit = gas.VerificationGasLimit
				userOp.CallGasLimit = gas.CallGasLimit
				l.Debug("Gas estimated", "callGas", gas.CallGasLimit, "verificationGas", gas.VerificationGasLimit, "preVerificationGas", gas.PreVerificationGas)
			} else if retry == 0 {
				// Use hardcoded gas limits as fallback (same as paymaster version)
				userOp.PreVerificationGas = big.NewInt(50000)     // 50k gas
				userOp.VerificationGasLimit = big.NewInt(1000000) // 1M gas
				userOp.CallGasLimit = big.NewInt(100000)          // 100k gas
				l.Warn("Gas estimation failed, using defaults", "error", gasErr)
			} else {
				l.Warn("Gas estimation failed on retry", "retry", retry+1, "maxRetries", maxRetries, "error", gasErr)
			}
		}

		// Sign with current nonce
		userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
		userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
		if err != nil {
			return "", fmt.Errorf("failed to sign UserOp: %w", err)
		}

		// Final preflight estimation with the fully signed, final UserOp.
		// Important: DO NOT mutate any field from the result, to keep the signature stable.
		// This ensures the bundler's cached UserOp (from estimation) matches exactly what we send.
		// SKIP for wrapped operations (executeBatchWithValues) - bundler can't simulate them
		// Check if calldata starts with executeBatchWithValues selector (0xc3ff72fc)
		// First 4 bytes of calldata = function selector (method ID)
		isWrappedOperation := len(userOp.CallData) >= 4 && hexutil.Encode(userOp.CallData[:4]) == "0xc3ff72fc"
		if !isWrappedOperation {
			if _, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *userOp, aa.EntrypointAddress, map[string]any{}); gasErr != nil {
				l.Debug("Preflight estimation failed", "error", gasErr)
			}
		}

		// Attempt to send
		txResult, err = bundlerClient.SendUserOperation(context.Background(), *userOp, aa.EntrypointAddress)

		// Bundler send result logging
		if err == nil && txResult != "" {
			l.Debug("UserOp sent", "attempt", retry+1, "maxRetries", maxRetries, "hash", txResult, "nonce", userOp.Nonce.String(), "sender", userOp.Sender.Hex())

			// Brief delay to allow bundler to index the UserOp before checking mempool
			time.Sleep(500 * time.Millisecond)

			// Check for and flush stuck UserOps before triggering bundle for our new one.
			// The bundler bundles in FIFO order and only bundles 1 UserOp at a time,
			// so older stuck UserOps must be flushed first to allow our new one to be bundled.
			l.Debug("Checking for stuck UserOps before bundling")
			flushedCount, flushErr := bundlerClient.FlushStuckUserOps(
				context.Background(),
				aa.EntrypointAddress,
				userOp.Sender,
				userOp.Nonce,
			)
			if flushErr != nil {
				l.Warn("Failed to flush stuck UserOps", "error", flushErr)
			} else if flushedCount > 0 {
				l.Debug("Flushed stuck UserOps", "count", flushedCount)
			}

			// Manually trigger bundling for our new UserOp
			// This is only needed for local bundlers that don't auto-bundle frequently
			triggerErr := bundlerClient.SendBundleNow(context.Background())
			if triggerErr != nil {
				l.Warn("Manual bundle trigger failed", "error", triggerErr)
			} else {
				l.Debug("Bundle triggered", "hash", txResult)
			}

			// Update NonceManager to track this pending UserOp
			// This allows the next UserOp to use nonce+1 even if this UserOp hasn't been mined yet
			globalNonceManager.IncrementNonce(userOp.Sender, userOp.Nonce)

			break
		}

		// Bundler send failure logging
		l.Warn("UserOp send failed", "attempt", retry+1, "maxRetries", maxRetries, "error", err)

		// Detect nonce conflicts from various error messages:
		// - "AA25 invalid account nonce" = EntryPoint validation error
		// - "invalid UserOperation struct/fields" = Voltaire's mempool replacement error (nonce already pending)
		isNonceConflict := err != nil && (strings.Contains(err.Error(), "AA25 invalid account nonce") ||
			strings.Contains(err.Error(), "invalid UserOperation struct/fields"))

		if isNonceConflict {
			if retry < maxRetries-1 {
				l.Warn("Nonce conflict detected", "nonce", userOp.Nonce.String(), "error", err)

				// Fetch the raw on-chain nonce to distinguish between:
				// 1. Prior UserOps not yet mined (on-chain < userOp.Nonce) — retry same nonce
				// 2. Our nonce slot taken in mempool (on-chain >= userOp.Nonce) — increment
				onChainNonce, onChainErr := aa.GetNonce(client, userOp.Sender, accountSalt)
				if onChainErr != nil {
					l.Warn("Failed to fetch on-chain nonce for retry", "error", onChainErr)
					continue
				}

				freshNonce, nonceErr := globalNonceManager.GetNextNonce(client, userOp.Sender, func() (*big.Int, error) {
					return new(big.Int).Set(onChainNonce), nil // reuse already-fetched value
				})
				if nonceErr != nil {
					l.Warn("Failed to fetch nonce for retry", "error", nonceErr)
					continue
				}

				if freshNonce.Cmp(userOp.Nonce) > 0 {
					// On-chain or cache has advanced past our nonce — use the new value
					l.Debug("Nonce advanced", "old_nonce", userOp.Nonce.String(), "new_nonce", freshNonce.String())
				} else if onChainNonce.Cmp(userOp.Nonce) < 0 {
					// On-chain nonce is behind our attempted nonce. Prior UserOps (at lower
					// nonces) haven't mined yet, causing the bundler to reject ours with AA25.
					// The nonce itself is correct — we just need to wait for prior UserOps to
					// mine. Do NOT increment, as that creates an unfillable nonce gap.
					freshNonce = new(big.Int).Set(userOp.Nonce)
					l.Debug("On-chain nonce behind, waiting for prior UserOps to mine before retry",
						"on_chain_nonce", onChainNonce.String(),
						"userOp_nonce", userOp.Nonce.String())
					// Wait briefly for prior UserOps to mine; without this delay
					// the retry loop burns through attempts in milliseconds.
					time.Sleep(2 * time.Second)
				} else {
					// GetNextNonce returned the same nonce we already tried and on-chain has
					// reached this nonce. A UserOp is pending at this nonce in the mempool.
					// Increment by 1 so we use the next slot.
					freshNonce = new(big.Int).Add(userOp.Nonce, big.NewInt(1))
					l.Debug("Nonce unchanged, incrementing past pending UserOp",
						"old_nonce", userOp.Nonce.String(), "new_nonce", freshNonce.String())
				}

				// For paymaster UserOps, we cannot simply update the nonce because the
				// paymaster signature is bound to the original nonce. Changing the nonce
				// would require rebuilding the entire UserOp (new paymaster hash, new
				// paymaster signature, new UserOp signature), which is not possible in
				// this retry loop. Log a clear message and skip the retry.
				// Check this BEFORE updating the cache to avoid advancing past an unsent nonce.
				hasPaymaster = len(userOp.PaymasterAndData) > 0
				if hasPaymaster {
					// Flush any stuck UserOps from the bundler mempool that may be blocking us.
					// The bundler processes in FIFO order, so stale UserOps at lower nonces
					// prevent new ones from being accepted.
					flushed, flushErr := bundlerClient.FlushStuckUserOps(context.Background(), entrypoint, userOp.Sender, freshNonce)
					if flushErr != nil {
						l.Warn("Failed to flush stuck UserOps", "error", flushErr)
					} else if flushed > 0 {
						l.Info("Flushed stuck UserOps from bundler mempool",
							"sender", userOp.Sender.Hex(),
							"flushed_count", flushed)
					}

					// Update the nonce cache so the rebuild uses the correct nonce.
					globalNonceManager.SetNonce(userOp.Sender, freshNonce)

					l.Info("Paymaster nonce conflict: returning to caller for full UserOp rebuild",
						"sender", userOp.Sender.Hex(),
						"original_nonce", userOp.Nonce.String(),
						"rebuild_nonce", freshNonce.String())

					// Return a sentinel error so sendUserOpShared can rebuild the entire
					// UserOp (new nonce → new paymaster hash → new paymaster signature →
					// new UserOp signature) and retry.
					return "", &ErrPaymasterNonceConflict{Nonce: freshNonce}
				}

				// Update the cache to the nonce we are about to use; further advancement
				// should only happen after a successful send (via IncrementNonce).
				globalNonceManager.SetNonce(userOp.Sender, freshNonce)
				userOp.Nonce = freshNonce
				continue
			}
		}

		// For other errors, don't retry unless it's a transient network error or nonce conflict
		if err != nil && !isNonceConflict &&
			!strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "connection") {
			l.Warn("Non-retryable bundler error", "error", err)
			break
		}
	}

	if err != nil || txResult == "" {
		if txResult == "" && err == nil {
			err = errors.New("bundler returned empty transaction result")
		}
		l.Warn("Failed to send UserOp to bundler", "retries", maxRetries, "error", err)
		return "", fmt.Errorf("error sending transaction to bundler: %w", err)
	}

	return txResult, nil
}

// BuildUserOp builds a UserOperation with the given parameters.
// The client and bundlerClient are used for blockchain interaction.
// saltOverride: if provided, use this salt for address derivation and initCode generation
// instead of the default salt (0). This enables auto-deployment of wallets created with non-zero salts.
func BuildUserOp(
	smartWalletConfig *config.SmartWalletConfig,
	client *ethclient.Client,
	bundlerClient *bundler.BundlerClient,
	owner common.Address,
	callData []byte,
	senderOverride *common.Address,
	saltOverride *big.Int,
	lgr logger.Logger,
) (*userop.UserOperation, error) {
	l := logger.EnsureLogger(lgr)

	// Use saltOverride if provided, otherwise default to salt=0
	effectiveSalt := accountSalt
	if saltOverride != nil {
		effectiveSalt = saltOverride
	}

	// Resolve sender by deriving from owner with the effective salt.
	// If an override is provided and matches the derived address, use it directly.
	// If not deployed, we will include initCode to auto-deploy.
	derivedSender, err := aa.GetSenderAddress(client, owner, effectiveSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to derive sender address: %w", err)
	}
	var sender *common.Address = derivedSender
	if senderOverride != nil {
		so := *senderOverride
		if !strings.EqualFold(so.Hex(), derivedSender.Hex()) {
			// Override doesn't match derived address — allow if already deployed on-chain
			codeAtOverride, err := client.CodeAt(context.Background(), so, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to check override sender code: %w", err)
			}
			if len(codeAtOverride) == 0 {
				return nil, fmt.Errorf("sender override %s does not match derived sender %s (salt=%s) and override is not deployed",
					so.Hex(), derivedSender.Hex(), effectiveSalt.String())
			}
			sender = &so
		}
		// If equal, keep derivedSender in sender
	}

	initCode := "0x"
	code, err := client.CodeAt(context.Background(), *sender, nil)
	if err != nil {
		return nil, err
	}

	// account not initialized, feed in init code with the correct salt
	if len(code) == 0 {
		initCode, _ = aa.GetInitCode(owner.Hex(), effectiveSalt)
		l.Debug("Wallet not deployed, generating initCode", "sender", sender.Hex(), "salt", effectiveSalt.String())
	}

	maxFeePerGas, maxPriorityFeePerGas, err := eip1559.SuggestFee(client)
	if err != nil {
		return nil, fmt.Errorf("failed to suggest gas fees: %w", err)
	}

	// Ensure maxFeePerGas has sufficient headroom over maxPriorityFeePerGas (>= 1 gwei)
	minHeadroom := new(big.Int).Add(maxPriorityFeePerGas, big.NewInt(1_000_000_000))
	if maxFeePerGas.Cmp(minHeadroom) < 0 {
		maxFeePerGas = minHeadroom
	}

	// Increase verificationGasLimit if initCode is present (wallet deployment)
	// UUPS proxy + initialize(owner) account deployment requires significantly more gas than normal operations
	actualVerificationGasLimit := verificationGasLimit
	if len(initCode) > 0 && initCode != "0x" {
		actualVerificationGasLimit = DEPLOYMENT_VERIFICATION_GAS_LIMIT
	}

	// Initialize UserOp without nonce (will be fetched from chain in sendUserOpCore)
	userOp := userop.UserOperation{
		Sender:   *sender,
		Nonce:    nil, // Will be fetched from chain before sending
		InitCode: common.FromHex(initCode),
		CallData: callData,

		// dummy value, we will estimate gas with bundler rpc
		CallGasLimit:         callGasLimit,
		VerificationGasLimit: actualVerificationGasLimit,
		PreVerificationGas:   preVerificationGas,

		MaxFeePerGas:         maxFeePerGas,
		MaxPriorityFeePerGas: maxPriorityFeePerGas,
		PaymasterAndData:     common.FromHex("0x"),
	}

	// Gas estimation and signing will be done dynamically in the send loop

	return &userOp, nil
}

// SendUserOpWithWsClient reuses a provided WebSocket client for efficient transaction monitoring.
// Use this for batch operations where you want to reuse the same WebSocket connection.
// The WebSocket is only used for monitoring transaction confirmation, not for sending UserOps.
func SendUserOpWithWsClient(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	saltOverride *big.Int,
	wsClient *ethclient.Client,
	executionFeeWei *big.Int,
	lgr logger.Logger,
) (*userop.UserOperation, *types.Receipt, error) {
	l := logger.EnsureLogger(lgr)
	l.Debug("SendUserOpWithWsClient started", "owner", owner.Hex(), "bundler", smartWalletConfig.BundlerURL)

	// Use provided WebSocket client (no defer close - managed globally)
	if wsClient == nil {
		l.Warn("No WebSocket client provided, falling back to SendUserOp")
		return SendUserOp(smartWalletConfig, owner, callData, paymasterReq, senderOverride, saltOverride, executionFeeWei, lgr)
	}

	// Use the shared logic for the main UserOp processing
	return sendUserOpShared(smartWalletConfig, owner, callData, paymasterReq, senderOverride, saltOverride, wsClient, executionFeeWei, lgr)
}

// BuildUserOpWithPaymaster creates a UserOperation with paymaster support.
// It handles the process of building the UserOp, signing it, and setting the appropriate PaymasterAndData field.
// It works same way as BuildUserOp but with the extra field PaymasterAndData set. The protocol is defined in https://eips.ethereum.org/EIPS/eip-4337#paymasters
// Currently, we use the VerifyingPaymaster contract as the paymaster. We set a signer when initialize the paymaster contract.
// The signer is also the controller private key. It's the only way to generate the signature for paymaster.
// nonceOverride: if provided (not nil), uses this nonce instead of fetching from chain. Use this for sequential UserOps.
// gasOverrides: if provided (not nil), uses these estimated gas limits instead of hardcoded defaults
func BuildUserOpWithPaymaster(
	smartWalletConfig *config.SmartWalletConfig,
	client *ethclient.Client,
	bundlerClient *bundler.BundlerClient,
	owner common.Address,
	callData []byte,
	paymasterAddress common.Address,
	validUntil *big.Int,
	validAfter *big.Int,
	senderOverride *common.Address,
	saltOverride *big.Int,
	nonceOverride *big.Int,
	callGasOverride *big.Int,
	verificationGasOverride *big.Int,
	preVerificationGasOverride *big.Int,
	lgr logger.Logger,
) (*userop.UserOperation, error) {
	l := logger.EnsureLogger(lgr)
	// First build the basic user operation (auto-deploy if needed). If override is provided,
	// it must match the derived sender from owner.
	userOp, err := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride, saltOverride, l)
	if err != nil {
		return nil, fmt.Errorf("failed to build base UserOp: %w", err)
	}

	// Override gas limits with estimated values if provided
	// These must be set BEFORE signing with paymaster, as they're part of the hash
	if callGasOverride != nil {
		userOp.CallGasLimit = callGasOverride
		l.Debug("Gas override: CallGasLimit", "value", callGasOverride)
	}
	if verificationGasOverride != nil {
		// CRITICAL: Do NOT override verificationGasLimit when we have initCode (deployment scenario)
		// Bundler estimates ~1M but wallet deployment needs 3M (DEPLOYMENT_VERIFICATION_GAS_LIMIT)
		// Overriding with bundler's estimate causes "Invalid UserOp signature" errors
		hasInitCode := len(userOp.InitCode) > 0
		if hasInitCode {
			l.Debug("Gas override skipped for VerificationGasLimit: initCode present", "keeping", userOp.VerificationGasLimit, "bundlerSuggested", verificationGasOverride)
		} else {
			userOp.VerificationGasLimit = verificationGasOverride
			l.Debug("Gas override: VerificationGasLimit", "value", verificationGasOverride)
		}
	}
	if preVerificationGasOverride != nil {
		userOp.PreVerificationGas = preVerificationGasOverride
		l.Debug("Gas override: PreVerificationGas", "value", preVerificationGasOverride)
	}

	// Set the correct nonce BEFORE signing (BuildUserOp sets it to 0 as a placeholder)
	var freshNonce *big.Int
	if nonceOverride != nil {
		// Use provided nonce for sequential UserOps (prevents race conditions)
		freshNonce = nonceOverride
		l.Debug("Using provided nonce", "nonce", freshNonce.String())
	} else {
		// Use NonceManager to get next nonce (considers both on-chain state and pending UserOps)
		var err error
		freshNonce, err = globalNonceManager.GetNextNonce(client, userOp.Sender, func() (*big.Int, error) {
			return aa.GetNonce(client, userOp.Sender, accountSalt)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get nonce: %w", err)
		}
		l.Debug("Got nonce from NonceManager", "nonce", freshNonce.String())
	}
	userOp.Nonce = freshNonce

	// Initialize the PayMaster contract
	paymasterContract, err := paymaster.NewPayMaster(paymasterAddress, client)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PayMaster contract: %w", err)
	}

	// Get the chain ID earlier , it's a part of the userOp hash calculation. If cannot get it, we fail fast
	// TODO: we may load chain id from config to improve the speed
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	// Convert our userOp to paymaster.UserOperation for the contract call
	paymasterUserOp := paymaster.UserOperation{
		Sender:               userOp.Sender,
		Nonce:                userOp.Nonce,
		InitCode:             userOp.InitCode,
		CallData:             userOp.CallData,
		CallGasLimit:         userOp.CallGasLimit,
		VerificationGasLimit: userOp.VerificationGasLimit,
		PreVerificationGas:   userOp.PreVerificationGas,
		MaxFeePerGas:         userOp.MaxFeePerGas,
		MaxPriorityFeePerGas: userOp.MaxPriorityFeePerGas,

		// The value of PaymasterAndData and Signature are not used in the hash calculation but we need to keep them the same length
		// Given the below assembly code in the contract
		// assembly {
		//     let ofs := userOp
		//     let len := sub(sub(pnd.offset, ofs), 32)
		//     ret := mload(0x40)
		//     mstore(0x40, add(ret, add(len, 32)))
		//     mstore(ret, len)
		//     calldatacopy(add(ret, 32), ofs, len)
		// }
		PaymasterAndData: common.FromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		Signature:        common.FromHex("0x1234567890abcdef"),
	}

	// Get the hash to sign from the PayMaster contract
	// IMPORTANT: The paymaster maintains its own nonce per sender which is internally included
	// in the GetHash() call. The paymaster contract reads senderNonce[sender] from storage during
	// GetHash() and includes it in the hash computation.
	// IMPORTANT: The GetHash function signature is (userOp, validUntil, validAfter) per the contract ABI

	// Verify the paymaster's verifyingSigner matches our controller address
	verifyingSigner, err := paymasterContract.VerifyingSigner(nil)
	if err != nil {
		l.Warn("Failed to query paymaster verifyingSigner", "error", err)
	} else if !strings.EqualFold(verifyingSigner.Hex(), smartWalletConfig.ControllerAddress.Hex()) {
		return nil, fmt.Errorf("paymaster verifyingSigner (%s) does not match controller address (%s)",
			verifyingSigner.Hex(), smartWalletConfig.ControllerAddress.Hex())
	}

	paymasterHash, err := paymasterContract.GetHash(nil, paymasterUserOp, validUntil, validAfter)

	if err != nil {
		return nil, fmt.Errorf("failed to get paymaster hash: %w", err)
	}

	// Sign the paymaster hash with the controller's private key
	// CRITICAL: The VerifyingPaymaster contract calls ECDSA.toEthSignedMessageHash(getHash(...))
	// which adds the "\x19Ethereum Signed Message:\n32" prefix during validation.
	// Therefore, we must compute the SAME prefixed hash here and sign it with crypto.Sign()
	// WITHOUT using signer.SignMessage() which would double-prefix it.
	//
	// From VerifyingPaymaster.sol line 94:
	//   bytes32 hash = ECDSA.toEthSignedMessageHash(getHash(userOp, validUntil, validAfter));
	//   if (verifyingSigner != ECDSA.recover(hash, signature)) { ... }
	//
	// EIP-191 format: "\x19Ethereum Signed Message:\n" + len(message) + message
	prefix := []byte("\x19Ethereum Signed Message:\n32")
	prefixedData := append(prefix, paymasterHash[:]...)
	prefixedHash := crypto.Keccak256Hash(prefixedData)

	paymasterSignature, err := crypto.Sign(prefixedHash.Bytes(), smartWalletConfig.ControllerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign paymaster hash: %w", err)
	}
	// Adjust v value from 0/1 to 27/28 for Ethereum compatibility
	paymasterSignature[64] += 27

	// FIXED: Pack timestamps as ABI-encoded (uint48, uint48) - 64 bytes total
	// VerifyingPaymasterFixed contract expects ABI-encoded timestamps, NOT raw bytes
	// Total: address(20) + abi.encode(uint48,uint48)(64) + signature(65) = 149 bytes

	// ABI encode (uint48, uint48) as a tuple using the standard ABI encoding
	// This produces 64 bytes: 32 bytes for each uint48 (padded to uint256)
	// Use abi.Arguments to match the contract's abi.decode(paymasterAndData[20:84], (uint48, uint48))
	arguments := abi.Arguments{
		{Type: abi.Type{T: abi.UintTy, Size: 48}}, // uint48
		{Type: abi.Type{T: abi.UintTy, Size: 48}}, // uint48
	}

	encodedData, err := arguments.Pack(&validUntil, &validAfter)
	if err != nil {
		return nil, fmt.Errorf("failed to ABI encode timestamps: %w", err)
	}

	// Create PaymasterAndData: address (20) + abi.encode(uint48,uint48)(64) + signature (65) = 149 bytes
	paymasterAndData := append(paymasterAddress.Bytes(), encodedData...)
	paymasterAndData = append(paymasterAndData, paymasterSignature...)

	// Update the UserOperation with the properly encoded PaymasterAndData
	userOp.PaymasterAndData = paymasterAndData

	// Update the userOpHash with the new PaymasterAndData value
	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)

	// Sign the updated user operation
	// IMPORTANT: For ERC-4337, the signature format depends on the smart wallet implementation
	// The AVA smart wallet expects an EIP-191 prefixed signature
	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign final UserOp: %w", err)
	}

	return userOp, nil
}
