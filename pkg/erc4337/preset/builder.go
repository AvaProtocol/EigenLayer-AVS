package preset

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
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
	BATCH_OVERHEAD_BUFFER_PERCENT  = 20                  // 20% buffer for executeBatchWithValues overhead
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

	// Controls verbose logging for this package. Set to true for detailed debug output.
	VerboseLogs = false

	// example tx send to entrypoint: https://sepolia.basescan.org/tx/0x7580ac508a2ac34cf6a4f4346fb6b4f09edaaa4f946f42ecdb2bfd2a633d43af#eventlog
	userOpEventTopic0 = common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")

	// EnablePaymasterReimbursement controls whether to add ETH reimbursement to paymaster
	// When true: Wraps execute() with executeBatchWithValues() to atomically reimburse paymaster
	// Default: true (reimburse paymaster for gas costs)
	EnablePaymasterReimbursement = true

	// GasReimbursementBufferPercent is the extra buffer percentage added to the reimbursement amount
	// Formula: reimbursement = (total UserOp gas × maxFeePerGas) × (100% + buffer%)
	// Default: 20% buffer to account for gas price fluctuations and execution variations
	GasReimbursementBufferPercent = 20
)

// VerifyingPaymasterRequest contains the parameters needed for paymaster functionality. This use the reference from https://github.com/eth-optimism/paymaster-reference
type VerifyingPaymasterRequest struct {
	PaymasterAddress common.Address
	ValidUntil       *big.Int
	ValidAfter       *big.Int
}

func GetVerifyingPaymasterRequestForDuration(address common.Address, duration time.Duration) *VerifyingPaymasterRequest {
	// Use a larger negative skew to tolerate clock drift between services and the bundler
	// Increased to 2 minutes to handle cases where aggregator clock is ahead of bundler
	const skewSeconds int64 = 120 // 2 minute skew tolerance
	now := time.Now().Unix()
	validAfter := now - skewSeconds
	validUntil := now + int64(duration.Seconds())

	return &VerifyingPaymasterRequest{
		PaymasterAddress: address,
		ValidUntil:       big.NewInt(validUntil),
		ValidAfter:       big.NewInt(validAfter),
	}
}

// waitForUserOpConfirmation waits for a UserOperation to be confirmed on-chain using
// a hybrid approach: WebSocket subscription for real-time events + exponential backoff polling as fallback.
// This handles bundler delays gracefully without blocking for a fixed timeout.
//
// Returns:
// - (*types.Receipt, nil) if UserOp was confirmed successfully
// - (nil, nil) if timeout reached without confirmation (UserOp may still be pending)
// - (nil, error) if an unrecoverable error occurred
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
	const (
		maxWaitTime     = 30 * time.Second // Maximum total wait time (bundler should process within 2-5s)
		initialInterval = 1 * time.Second  // Start polling every 1 second
		maxInterval     = 5 * time.Second  // Max polling interval (cap exponential growth)
		backoffFactor   = 1.5              // Multiply interval by 1.5 each retry
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
						log.Printf("Transaction waiting: websocket error, falling back to polling: %v", err)
						// Continue with polling below
						goto PollingOnly
					}

				case vLog := <-logs:
					// Got the event via WebSocket - fastest path!
					log.Printf("UserOp confirmed via websocket - tx: %s", vLog.TxHash.Hex())
					receipt, err := client.TransactionReceipt(context.Background(), vLog.TxHash)
					if err != nil {
						log.Printf("⚠️ TRANSACTION WAITING: Failed to get receipt for %s: %v", vLog.TxHash.Hex(), err)
						continue
					}
					return receipt, nil

				case <-ticker.C:
					// Periodic polling as fallback (in case WebSocket misses events)
					elapsed := time.Since(startTime)
					if elapsed > maxWaitTime {
						log.Printf("Transaction waiting timeout after %v - UserOp may still be pending", elapsed)
						return nil, nil
					}

					logger.Debug("Transaction waiting: polling (elapsed: %v, interval: %v)", elapsed.Round(time.Second), pollInterval)

					receipt, found, err := pollUserOpReceipt(client, entrypoint, userOpHash)
					if err != nil {
						log.Printf("Transaction waiting polling error: %v", err)
					}
					if found {
						log.Printf("UserOp confirmed via polling")
						return receipt, nil
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
			log.Printf("Transaction waiting: websocket subscription failed, using polling only: %v", err)
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
			log.Printf("⏰ TRANSACTION WAITING: Timeout after %v (%d attempts) - UserOp may still be pending",
				elapsed, attempt)
			return nil, nil
		}

		log.Printf("🔍 TRANSACTION WAITING: Poll attempt #%d (elapsed: %v, interval: %v)...",
			attempt, elapsed.Round(time.Second), pollInterval)

		receipt, found, err := pollUserOpReceipt(client, entrypoint, userOpHash)
		if err != nil {
			log.Printf("⚠️ TRANSACTION WAITING: Polling error: %v", err)
			// Continue polling despite errors (transient network issues)
		}
		if found {
			log.Printf("✅ TRANSACTION WAITING: UserOp confirmed after %v (%d attempts)", elapsed, attempt)
			return receipt, nil
		}

		// Wait before next poll with exponential backoff
		time.Sleep(pollInterval)
		pollInterval = time.Duration(float64(pollInterval) * backoffFactor)
		if pollInterval > maxInterval {
			pollInterval = maxInterval
		}
	}
}

// pollUserOpReceipt queries the chain for a UserOp receipt by searching recent blocks for the UserOperationEvent.
// Returns (receipt, found, error) where found=true if the event was found.
func pollUserOpReceipt(
	client *ethclient.Client,
	entrypoint common.Address,
	userOpHash string,
) (*types.Receipt, bool, error) {
	// Query recent blocks for the UserOperationEvent
	// We look back ~20 blocks (~5 minutes on most chains) to handle reorgs
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	currentBlock, err := client.BlockNumber(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get current block: %w", err)
	}

	// Look back 20 blocks (adjust based on chain's block time)
	fromBlock := currentBlock
	if currentBlock > 20 {
		fromBlock = currentBlock - 20
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

	return receipt, true, nil
}

// estimateGasReimbursementAmount computes the ETH amount to reimburse the paymaster.
// Formula: (bundler's estimated gas OR fallback gas + ETH transfer) × 20% buffer × maxFeePerGas
// This uses the bundler's accurate gas estimation when available, otherwise uses conservative fallbacks.
func estimateGasReimbursementAmount(client *ethclient.Client, gasEstimate *bundler.GasEstimation) (*big.Int, error) {
	maxFeePerGas, _, err := eip1559.SuggestFee(client)
	if err != nil {
		// Fallback to 20 gwei if we can't get real-time price
		log.Printf("⚠️  Failed to get gas price, using 20 gwei fallback: %v", err)
		maxFeePerGas = big.NewInt(20_000_000_000) // 20 gwei
	}

	// Dynamic gas calculation: Use bundler's gas estimation for reimbursement
	// Formula: (bundler's estimated gas + ETH transfer) × buffer
	var totalUserOpGas *big.Int

	if gasEstimate != nil {
		// Use bundler's gas estimation (most accurate)
		preVerificationGas := new(big.Int).Set(gasEstimate.PreVerificationGas)
		verificationGas := new(big.Int).Set(gasEstimate.VerificationGasLimit)
		callGas := new(big.Int).Set(gasEstimate.CallGasLimit)
		ethTransferGas := new(big.Int).Set(ETH_TRANSFER_GAS_COST) // ETH transfer for reimbursement (~21K)

		// Total UserOp gas: bundler's estimate + ETH transfer
		totalUserOpGas = new(big.Int).Add(preVerificationGas, verificationGas)
		totalUserOpGas.Add(totalUserOpGas, callGas)
		totalUserOpGas.Add(totalUserOpGas, ethTransferGas)

		log.Printf("💰 Using BUNDLER gas estimation for reimbursement")
		log.Printf("   PreVerificationGas: %s", preVerificationGas.String())
		log.Printf("   VerificationGas: %s", verificationGas.String())
		log.Printf("   CallGas: %s", callGas.String())
		log.Printf("   ETH transfer: %s", ethTransferGas.String())
	} else {
		// Fallback to conservative defaults when bundler estimation fails
		preVerificationGas := new(big.Int).Set(DEFAULT_PREVERIFICATION_GAS) // Bundler overhead (~50K)
		verificationGas := new(big.Int).Set(DEFAULT_VERIFICATION_GAS_LIMIT) // Signature + paymaster validation (~1M)
		callGas := new(big.Int).Set(DEFAULT_CALL_GAS_LIMIT)                 // Original operation (~200K)
		ethTransferGas := new(big.Int).Set(ETH_TRANSFER_GAS_COST)           // ETH transfer for reimbursement (~21K)

		// Total UserOp gas: preVerification + verification + call + ETH transfer
		totalUserOpGas = new(big.Int).Add(preVerificationGas, verificationGas)
		totalUserOpGas.Add(totalUserOpGas, callGas)
		totalUserOpGas.Add(totalUserOpGas, ethTransferGas)

		log.Printf("💰 Using FALLBACK gas estimation for reimbursement")
		log.Printf("   Fallback PreVerificationGas: %s", DEFAULT_PREVERIFICATION_GAS.String())
		log.Printf("   Fallback VerificationGas: %s", DEFAULT_VERIFICATION_GAS_LIMIT.String())
		log.Printf("   Fallback CallGas: %s", DEFAULT_CALL_GAS_LIMIT.String())
	}

	// Apply buffer to the total UserOp gas
	gasBufferMultiplier := big.NewInt(100 + int64(BATCH_OVERHEAD_BUFFER_PERCENT)) // 120 for 20% buffer
	effectiveGas := new(big.Int).Mul(totalUserOpGas, gasBufferMultiplier)
	effectiveGas.Div(effectiveGas, big.NewInt(100))

	baseCost := new(big.Int).Mul(effectiveGas, maxFeePerGas)

	// Apply configured buffer
	bufferMultiplier := big.NewInt(100 + int64(GasReimbursementBufferPercent))
	reimbursement := new(big.Int).Mul(baseCost, bufferMultiplier)
	reimbursement = new(big.Int).Div(reimbursement, big.NewInt(100))

	log.Printf("💰 GAS REIMBURSEMENT ESTIMATION:")
	if gasEstimate != nil {
		log.Printf("   Source: (bundler's estimated gas + ETH transfer) × %d%% buffer", BATCH_OVERHEAD_BUFFER_PERCENT)
	} else {
		log.Printf("   Source: (fallback gas + ETH transfer) × %d%% buffer", BATCH_OVERHEAD_BUFFER_PERCENT)
	}
	log.Printf("   ETH transfer: %s", ETH_TRANSFER_GAS_COST.String())
	log.Printf("   MaxFeePerGas: %s wei (%.2f gwei)", maxFeePerGas.String(), float64(maxFeePerGas.Int64())/1e9)
	log.Printf("   Total UserOp gas: %s", totalUserOpGas.String())
	log.Printf("   Effective gas: %s (total × %d%%)", effectiveGas.String(), BATCH_OVERHEAD_BUFFER_PERCENT)
	log.Printf("   Base cost: %s wei", baseCost.String())
	log.Printf("   Buffer: %d%%", GasReimbursementBufferPercent)
	log.Printf("   Reimbursement: %s wei (%.6f ETH)", reimbursement.String(), float64(reimbursement.Int64())/1e18)

	return reimbursement, nil
}

// wrapWithReimbursement wraps original SimpleAccount.execute() calldata with executeBatchWithValues
// to add a second step that transfers reimbursement ETH to the reimbursement recipient (paymaster owner or paymaster), atomically.
func wrapWithReimbursement(
	client *ethclient.Client,
	originalCallData []byte,
	reimbursementRecipient common.Address,
	gasEstimate *bundler.GasEstimation,
) ([]byte, *big.Int, error) {
	// Estimate reimbursement
	reimbursementAmount, err := estimateGasReimbursementAmount(client, gasEstimate)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to estimate reimbursement: %w", err)
	}

	// Decode execute(dest, value, data) - we need to manually decode since we don't have a public GetAccountABI
	// The execute() function signature is: execute(address dest, uint256 value, bytes calldata func)
	// Calldata format: [4-byte selector][32-byte dest][32-byte value][offset to bytes][length][data...]

	if len(originalCallData) < 4 {
		return nil, nil, fmt.Errorf("calldata too short: %d bytes", len(originalCallData))
	}

	// Skip function selector (4 bytes) and decode the ABI-encoded parameters
	params := originalCallData[4:]
	if len(params) < 96 { // minimum: 32 (dest) + 32 (value) + 32 (offset)
		return nil, nil, fmt.Errorf("calldata params too short: %d bytes", len(params))
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

	log.Printf("🔍 DECODED EXECUTE() PARAMS:")
	log.Printf("   dest: %s", dest.Hex())
	log.Printf("   value: %s wei", value.String())
	log.Printf("   data: %d bytes (cap: %d)", len(data), cap(data))

	// Create batch arrays for executeBatchWithValues
	// [0] = original operation (e.g., withdrawal)
	// [1] = reimbursement to paymaster owner (to compensate for gas paid by paymaster)
	targets := []common.Address{dest, reimbursementRecipient}
	values := []*big.Int{value, reimbursementAmount}

	// Preserve the original call's calldata for the first step and use an empty
	// bytes for the reimbursement step. This ensures the intended contract
	// method is actually executed (e.g., ERC20 transfer), while the second step
	// simply transfers ETH to reimburse gas costs.
	// CRITICAL: Use make([]byte, 0) to avoid Go's ABI encoder padding bug for empty bytes
	calldatas := [][]byte{data, make([]byte, 0)}

	log.Printf("🔄 REIMBURSEMENT WRAPPING (manual ABI encoding):")
	log.Printf("   Original operation:")
	log.Printf("     Target: %s", dest.Hex())
	log.Printf("     Value: %s wei", value.String())
	log.Printf("     Data: %d bytes", len(data))
	log.Printf("   Reimbursement operation:")
	log.Printf("     Target: %s (paymaster owner for gas reimbursement)", reimbursementRecipient.Hex())
	log.Printf("     Value: %s wei (%.6f ETH)", reimbursementAmount.String(), float64(reimbursementAmount.Int64())/1e18)
	log.Printf("     Data: 0 bytes (truly empty!)")

	// Use manual ABI encoding to bypass Go's ABI encoder bug with empty []byte slices
	wrappedCalldata, err := aa.PackExecuteBatchWithValues(targets, values, calldatas)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to pack executeBatchWithValues: %w", err)
	}

	log.Printf("✅ Calldata manually encoded: %d bytes", len(wrappedCalldata))

	return wrappedCalldata, reimbursementAmount, nil
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
	wsClient *ethclient.Client,
	logger logger.Logger,
) (*userop.UserOperation, *types.Receipt, error) {
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

	// Step 0.5: If paymaster reimbursement is enabled, consider wrapping with executeBatchWithValues
	// We will estimate the reimbursement amount and compare it with the smart wallet's ETH balance.
	// If balance is insufficient for reimbursement, auto-skip wrapping to ensure the main call succeeds.
	var wrappedForReimbursement bool
	if paymasterReq != nil && EnablePaymasterReimbursement {
		// Get reimbursement recipient from config (paymaster owner address)
		reimbursementRecipient := smartWalletConfig.PaymasterOwnerAddress
		if reimbursementRecipient == (common.Address{}) {
			reimbursementRecipient = paymasterReq.PaymasterAddress
		}

		// Prepare wrapped candidate and an initial reimbursement estimate (defaults-based)
		wrappedCandidate, initialReimb, wrapErr := wrapWithReimbursement(client, callData, reimbursementRecipient, nil)
		if wrapErr != nil {
			log.Printf("Failed to prepare reimbursement wrapping: %v (skipping wrap; paymaster absorbs gas costs)", wrapErr)
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
					log.Printf("Failed to fetch smart wallet balance (proceeding): %v", balErr)
				}
			}

			// Use initial (fallback) reimbursement estimate; avoid an extra bundler estimation here
			// The final bundler estimation will still occur later for the chosen path
			var reimbToUse = initialReimb

			// Compare balance with reimbursement; auto-skip wrapping if insufficient
			if balance.Cmp(reimbToUse) < 0 {
				log.Printf("Auto-skip reimbursement wrap: insufficient ETH on smart wallet. balance=%s wei < reimburse=%s wei",
					balance.String(), reimbToUse.String())
				wrappedForReimbursement = false
				// Keep original unwrapped callData
			} else {
				callData = wrappedCandidate
				wrappedForReimbursement = true
				log.Printf("Paymaster reimbursement enabled: %s wei, recipient: %s", reimbToUse.String(), reimbursementRecipient.Hex())
			}
		}
	}

	// Step 1: Estimate gas for the FINAL calldata (wrapped or unwrapped)
	// Build a temporary UserOp to estimate gas
	var estimatedCallGas, estimatedVerificationGas, estimatedPreVerificationGas *big.Int
	if paymasterReq != nil {
		// Attempt bundler gas estimation on the FINAL calldata (wrapped or unwrapped)
		// If wrapped, boost the initial callGas limit to help the bundler's binary search converge.
		initialCallGasLimit := callGasLimit
		if wrappedForReimbursement {
			initialCallGasLimit = new(big.Int).Mul(callGasLimit, big.NewInt(3))
		}

		tempUserOp, tempErr := BuildUserOpWithPaymaster(
			smartWalletConfig,
			client,
			bundlerClient,
			owner,
			callData, // WRAPPED calldata when reimbursement is enabled
			paymasterReq.PaymasterAddress,
			paymasterReq.ValidUntil,
			paymasterReq.ValidAfter,
			senderOverride,
			nil,                  // nonceOverride - fetch from chain
			initialCallGasLimit,  // higher starting point if wrapped
			verificationGasLimit, // provisional values
			preVerificationGas,   // provisional values
		)
		if tempErr != nil {
			log.Printf("Failed to build temp UserOp for gas estimation: %v", tempErr)
			return nil, nil, tempErr
		}

		// Set dummy signature for estimation (paymaster signature already set in BuildUserOpWithPaymaster)
		tempUserOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

		// Estimate gas using bundler with the EXACT UserOp structure (including paymaster and wrapped calldata)
		gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *tempUserOp, aa.EntrypointAddress, map[string]any{})
		if gasErr != nil {
			log.Printf("Gas estimation failed: %v (falling back to defaults)", gasErr)
			// Fallback to conservative defaults when estimation fails
			estimatedCallGas = new(big.Int).Mul(DEFAULT_CALL_GAS_LIMIT, big.NewInt(ETH_TRANSFER_GAS_MULTIPLIER))
			estimatedVerificationGas = DEFAULT_VERIFICATION_GAS_LIMIT
			estimatedPreVerificationGas = DEFAULT_PREVERIFICATION_GAS
		} else if gas != nil {
			estimatedCallGas = gas.CallGasLimit
			estimatedVerificationGas = gas.VerificationGasLimit
			estimatedPreVerificationGas = gas.PreVerificationGas
			log.Printf("Gas estimation: callGas=%s, verificationGas=%s, preVerificationGas=%s",
				estimatedCallGas.String(), estimatedVerificationGas.String(), estimatedPreVerificationGas.String())
		}
	}

	// Build the userOp based on whether paymaster is requested or not
	if paymasterReq == nil {
		// Standard UserOp without paymaster
		userOp, err = BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride)
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
			nil,                         // nonceOverride - let it fetch from chain
			estimatedCallGas,            // Use estimated gas
			estimatedVerificationGas,    // Use estimated gas
			estimatedPreVerificationGas, // Use estimated gas
		)
	}

	if err != nil {
		log.Printf("Failed to build UserOp: %v", err)
		return nil, nil, err
	}

	// Run full validation on the final, fully-built userOp to catch signature/paymaster issues early
	if paymasterReq != nil {
		if simErr := bundlerClient.SimulateUserOperation(context.Background(), *userOp, entrypoint); simErr != nil {
			log.Printf("SimulateUserOperation failed on final userOp: %v", simErr)
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
					log.Printf("Local signature check failed: recovered=%s controller=%s", recovered.Hex(), ctrl.Hex())
					return userOp, nil, fmt.Errorf("local signature check failed: recovered %s != controller %s", recovered.Hex(), ctrl.Hex())
				}
			}
		}
	}

	// Send the UserOp to the bundler using the existing sendUserOpCore function
	txResult, err := sendUserOpCore(smartWalletConfig, userOp, client, bundlerClient)
	if err != nil {
		// Fallback: if paymaster path failed with invalid params, retry without paymaster (self-funded)
		if paymasterReq != nil && (strings.Contains(strings.ToLower(err.Error()), "invalid useroperation") || strings.Contains(err.Error(), "-32602")) {
			log.Printf("⚠️ Paymaster send failed with invalid params (-32602). Retrying without paymaster (self-funded)...")
			// Rebuild WITHOUT paymaster and WITHOUT reimbursement wrapping
			userOpNoPM, buildErr := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, originalCallData, senderOverride)
			if buildErr != nil {
				log.Printf("Fallback build without paymaster failed: %v", buildErr)
				return userOp, nil, err
			}
			// Try to send self-funded userOp
			txResult, err = sendUserOpCore(smartWalletConfig, userOpNoPM, client, bundlerClient)
			if err != nil {
				return userOpNoPM, nil, err
			}
			userOp = userOpNoPM
		} else {
			return userOp, nil, err
		}
	}

	// Wait for UserOp confirmation using exponential backoff polling
	// This is more efficient than a fixed 3-minute timeout and handles bundler delays gracefully
	receipt, err := waitForUserOpConfirmation(client, wsClient, entrypoint, txResult, logger)
	if err != nil {
		log.Printf("Failed to get UserOp confirmation (hash=%s): %v", txResult, err)
		return userOp, nil, nil
	}
	if receipt == nil {
		log.Printf("No receipt received for UserOp (hash=%s)", txResult)
		return userOp, nil, nil
	}

	log.Printf("UserOp confirmed: block=%d, txHash=%s, gasUsed=%d", receipt.BlockNumber.Uint64(), receipt.TxHash.Hex(), receipt.GasUsed)

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
	logger logger.Logger,
) (*userop.UserOperation, *types.Receipt, error) {
	log.Printf("SendUserOp started - owner: %s, bundler: %s", owner.Hex(), smartWalletConfig.BundlerURL)

	// Only create WebSocket client if URL is provided
	var wsClient *ethclient.Client
	var err error
	if smartWalletConfig.EthWsUrl != "" {
		wsClient, err = ethclient.Dial(smartWalletConfig.EthWsUrl)
		if err != nil {
			log.Printf("⚠️  WebSocket client creation failed (will use polling): %v", err)
			wsClient = nil
		} else {
			defer wsClient.Close()
		}
	} else {
		log.Printf("ℹ️  No WebSocket URL configured, will use polling for receipt")
		wsClient = nil
	}

	// Use the shared logic for the main UserOp processing
	return sendUserOpShared(smartWalletConfig, owner, callData, paymasterReq, senderOverride, wsClient, nil)
}

// sendUserOpCore contains the shared retry loop logic for sending UserOps to the bundler.
// This is the core implementation used by both SendUserOp and SendUserOpWithWsClient.
// Returns (txResult, error)
func sendUserOpCore(
	smartWalletConfig *config.SmartWalletConfig,
	userOp *userop.UserOperation,
	client *ethclient.Client,
	bundlerClient *bundler.BundlerClient,
) (string, error) {
	var txResult string
	var err error
	maxRetries := 3

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %w", err)
	}

	// Fetch nonce before entering the retry loop
	// IMPORTANT: Only refresh nonce if it's not already set (e.g., from BuildUserOpWithPaymaster)
	// Changing the nonce invalidates both the UserOp signature and paymaster signature
	// NOTE: A nonce of 0 is valid for new accounts, so we only check for nil (not 0)
	var freshNonce *big.Int
	if userOp.Nonce == nil {
		freshNonce = aa.MustNonce(client, userOp.Sender, accountSalt)
		userOp.Nonce = freshNonce
	}

	for retry := 0; retry < maxRetries; retry++ {
		// Re-estimate gas with current nonce (only on first attempt or if previous failed due to gas)
		// IMPORTANT:
		// - Skip gas re-estimation if paymaster is present (would invalidate paymaster signature)
		hasPaymaster := len(userOp.PaymasterAndData) > 0
		if !hasPaymaster && (retry == 0 || (err != nil && strings.Contains(err.Error(), "gas"))) {
			userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

			gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *userOp, aa.EntrypointAddress, map[string]any{})
			if gasErr == nil && gas != nil {
				userOp.PreVerificationGas = gas.PreVerificationGas
				userOp.VerificationGasLimit = gas.VerificationGasLimit
				userOp.CallGasLimit = gas.CallGasLimit
				log.Printf("Gas estimated: callGas=%s, verificationGas=%s, preVerificationGas=%s",
					gas.CallGasLimit.String(), gas.VerificationGasLimit.String(), gas.PreVerificationGas.String())
			} else if retry == 0 {
				// Use hardcoded gas limits as fallback (same as paymaster version)
				userOp.PreVerificationGas = big.NewInt(50000)     // 50k gas
				userOp.VerificationGasLimit = big.NewInt(1000000) // 1M gas
				userOp.CallGasLimit = big.NewInt(100000)          // 100k gas
				log.Printf("Gas estimation failed: %v (using hardcoded limits)", gasErr)
			} else {
				log.Printf("Gas estimation failed on retry %d: %v", retry+1, gasErr)
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
				log.Printf("Preflight estimation failed: %v", gasErr)
			}
		}

		// Attempt to send
		txResult, err = bundlerClient.SendUserOperation(context.Background(), *userOp, aa.EntrypointAddress)

		// Bundler send result logging
		if err == nil && txResult != "" {
			log.Printf("UserOp sent (attempt %d/%d): hash=%s, nonce=%s, sender=%s", retry+1, maxRetries, txResult, userOp.Nonce.String(), userOp.Sender.Hex())

			// Manually trigger bundling immediately (helps with development/testing)
			// This is only needed for local bundlers that don't auto-bundle frequently
			triggerErr := bundlerClient.SendBundleNow(context.Background())
			if triggerErr != nil {
				log.Printf("Manual bundle trigger failed (non-fatal): %v", triggerErr)
			}

			// Increment nonce for next potential UserOp (prevents nonce collision for sequential txs)
			// This allows the next UserOp to use nonce+1 even if this UserOp hasn't been mined yet
			userOp.Nonce = new(big.Int).Add(userOp.Nonce, big.NewInt(1))

			break
		}

		// Bundler send failure logging
		log.Printf("UserOp send failed (attempt %d/%d): %v", retry+1, maxRetries, err)
		// For nonce errors, refetch nonce and retry
		if err != nil && strings.Contains(err.Error(), "AA25 invalid account nonce") {
			if retry < maxRetries-1 {
				log.Printf("Nonce conflict detected, polling for fresh nonce")

				// Poll for fresh nonce with timeout (similar to transaction waiting pattern)
				startTime := time.Now()
				timeout := 15 * time.Second
				pollInterval := 500 * time.Millisecond

				for time.Since(startTime) < timeout {
					time.Sleep(pollInterval)
					freshNonce = aa.MustNonce(client, userOp.Sender, accountSalt)

					// Check if nonce has actually changed from what we had
					if userOp.Nonce == nil || freshNonce.Cmp(userOp.Nonce) > 0 {
						userOp.Nonce = freshNonce
						log.Printf("Updated nonce to %s (after %v)", freshNonce.String(), time.Since(startTime))
						break
					}
				}

				if time.Since(startTime) >= timeout {
					log.Printf("Nonce polling timeout after %v, using current nonce: %s", timeout, userOp.Nonce.String())
				}

				continue
			}
		}

		// For other errors, don't retry unless it's a transient network error
		if err != nil && !strings.Contains(err.Error(), "AA25 invalid account nonce") &&
			!strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "connection") {
			log.Printf("Non-retryable error, stopping: %v", err)
			break
		}
	}

	if err != nil || txResult == "" {
		log.Printf("Failed to send UserOp to bundler after %d retries (err=%v, txResult=%s)", maxRetries, err, txResult)
		return "", fmt.Errorf("error sending transaction to bundler: %w", err)
	}

	return txResult, nil
}

// BuildUserOp builds a UserOperation with the given parameters.
// The client and bundlerClient are used for blockchain interaction.
func BuildUserOp(
	smartWalletConfig *config.SmartWalletConfig,
	client *ethclient.Client,
	bundlerClient *bundler.BundlerClient,
	owner common.Address,
	callData []byte,
	senderOverride *common.Address,
) (*userop.UserOperation, error) {
	// Resolve sender by deriving from owner (salt:0). If an override is provided, it must match
	// the derived address; if not deployed, we will include initCode to auto-deploy instead of erroring.
	derivedSender, err := aa.GetSenderAddress(client, owner, accountSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to derive sender address: %w", err)
	}
	var sender *common.Address = derivedSender
	if senderOverride != nil {
		so := *senderOverride
		if !strings.EqualFold(so.Hex(), derivedSender.Hex()) {
			// Allow override if it's already deployed; otherwise require derived address for initCode path
			codeAtOverride, err := client.CodeAt(context.Background(), so, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to check override sender code: %w", err)
			}
			if len(codeAtOverride) == 0 {
				return nil, fmt.Errorf("sender override %s does not match derived sender %s and override is not deployed", so.Hex(), derivedSender.Hex())
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

	log.Printf("🔍 BUILD USEROP DEBUG: Checking wallet deployment status")
	log.Printf("  Sender: %s", sender.Hex())
	log.Printf("  Code length at sender: %d bytes", len(code))

	// account not initialized, feed in init code
	if len(code) == 0 {
		initCode, _ = aa.GetInitCode(owner.Hex(), accountSalt)
		log.Printf("  ❌ Wallet NOT deployed - generating initCode")
		log.Printf("  InitCode: %s", initCode)
	} else {
		log.Printf("  ✅ Wallet IS deployed - initCode will be empty (0x)")
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
	log.Printf("🔍 VERIFICATION GAS DEBUG:")
	log.Printf("  initCode string: '%s'", initCode)
	log.Printf("  initCode length: %d", len(initCode))
	log.Printf("  initCode == '0x': %v", initCode == "0x")

	if len(initCode) > 0 && initCode != "0x" {
		actualVerificationGasLimit = DEPLOYMENT_VERIFICATION_GAS_LIMIT
		log.Printf("🔧 InitCode present, increasing verificationGasLimit from %s to %s (UUPS proxy deployment)",
			verificationGasLimit.String(), actualVerificationGasLimit.String())
	} else {
		log.Printf("✅ No initCode (wallet deployed), using standard verificationGasLimit: %s", verificationGasLimit.String())
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
	wsClient *ethclient.Client,
	logger logger.Logger,
) (*userop.UserOperation, *types.Receipt, error) {
	log.Printf("SendUserOpWithWsClient started - owner: %s, bundler: %s", owner.Hex(), smartWalletConfig.BundlerURL)

	// Use provided WebSocket client (no defer close - managed globally)
	if wsClient == nil {
		log.Printf("⚠️ TRANSACTION WAITING: No WebSocket client provided, falling back to SendUserOp")
		// Fall back to SendUserOp which will create its own WebSocket client if needed
		return SendUserOp(smartWalletConfig, owner, callData, paymasterReq, senderOverride, logger)
	}

	// Use the shared logic for the main UserOp processing
	return sendUserOpShared(smartWalletConfig, owner, callData, paymasterReq, senderOverride, wsClient, logger)
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
	nonceOverride *big.Int,
	callGasOverride *big.Int,
	verificationGasOverride *big.Int,
	preVerificationGasOverride *big.Int,
) (*userop.UserOperation, error) {
	// First build the basic user operation (auto-deploy if needed). If override is provided,
	// it must match the derived sender from owner.
	userOp, err := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride)
	if err != nil {
		return nil, fmt.Errorf("failed to build base UserOp: %w", err)
	}

	// Override gas limits with estimated values if provided
	// These must be set BEFORE signing with paymaster, as they're part of the hash
	if callGasOverride != nil {
		userOp.CallGasLimit = callGasOverride
		log.Printf("🔍 Gas Override: CallGasLimit set to %s", callGasOverride.String())
	}
	if verificationGasOverride != nil {
		// CRITICAL: Do NOT override verificationGasLimit when we have initCode (deployment scenario)
		// Bundler estimates ~1M but wallet deployment needs 3M (DEPLOYMENT_VERIFICATION_GAS_LIMIT)
		// Overriding with bundler's estimate causes "Invalid UserOp signature" errors
		hasInitCode := len(userOp.InitCode) > 0
		if hasInitCode {
			log.Printf("🔧 Gas Override SKIPPED for VerificationGasLimit: InitCode present, keeping deployment limit %s (bundler suggested %s)",
				userOp.VerificationGasLimit.String(), verificationGasOverride.String())
		} else {
			userOp.VerificationGasLimit = verificationGasOverride
			log.Printf("🔍 Gas Override: VerificationGasLimit set to %s", verificationGasOverride.String())
		}
	}
	if preVerificationGasOverride != nil {
		userOp.PreVerificationGas = preVerificationGasOverride
		log.Printf("🔍 Gas Override: PreVerificationGas set to %s", preVerificationGasOverride.String())
	}

	// Set the correct nonce BEFORE signing (BuildUserOp sets it to 0 as a placeholder)
	var freshNonce *big.Int
	if nonceOverride != nil {
		// Use provided nonce for sequential UserOps (prevents race conditions)
		freshNonce = nonceOverride
		log.Printf("🔍 BuildUserOpWithPaymaster: Using provided nonce %s (sequential UserOps)", freshNonce.String())
	} else {
		// Fetch from chain (first UserOp or standalone)
		freshNonce = aa.MustNonce(client, userOp.Sender, accountSalt)
		log.Printf("🔍 BuildUserOpWithPaymaster: Fetched nonce %s from chain", freshNonce.String())
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

	encodedData, err := arguments.Pack(validUntil, validAfter)
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
	log.Printf("🔐 SIGNATURE DEBUG:")
	log.Printf("   UserOpHash (to be signed): %s", userOpHash.Hex())
	log.Printf("   Controller address: %s", smartWalletConfig.ControllerAddress.Hex())

	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign final UserOp: %w", err)
	}
	log.Printf("   Signature: 0x%s", common.Bytes2Hex(userOp.Signature))

	return userOp, nil
}
