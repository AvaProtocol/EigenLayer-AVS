package preset

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
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
)

var (
	// Realistic gas limits for UserOp construction (bundler estimation often fails)
	// These values are based on actual ETH transfer and smart wallet operations
	// Last validated: Sept 2025. To update: run representative UserOperations on target network,
	// observe actual gas usage, and adjust these values accordingly
	DEFAULT_CALL_GAS_LIMIT         = big.NewInt(100000) // 100K for smart wallet execute + ETH transfer
	DEFAULT_VERIFICATION_GAS_LIMIT = big.NewInt(150000) // 150K for signature verification
	DEFAULT_PREVERIFICATION_GAS    = big.NewInt(50000)  // 50K for bundler overhead

	// UUPS proxy wallet deployment gas limit
	// Based on real-world data from Base/Sepolia mainnet: UUPS proxy + initialize(owner) requires 1.3M-1.6M gas
	// This covers:
	// - Factory contract execution (~100K)
	// - Proxy deployment (~200K)
	// - Initialization with owner (~100K-300K)
	// - validateUserOp() with AAConfig.controller() call (~200K-500K)
	// - Paymaster validation if present
	// Confirmed via bundler logs: 150K fails with AA13, 1.6M succeeds
	DEPLOYMENT_VERIFICATION_GAS_LIMIT = big.NewInt(1600000) // 1.6M gas for wallet deployment + validation

	callGasLimit         = DEFAULT_CALL_GAS_LIMIT
	verificationGasLimit = DEFAULT_VERIFICATION_GAS_LIMIT
	preVerificationGas   = DEFAULT_PREVERIFICATION_GAS

	// the signature isnt important, only length check
	dummySigForGasEstimation = crypto.Keccak256Hash(common.FromHex("0xdead123"))
	accountSalt              = big.NewInt(0)

	// example tx send to entrypoint: https://sepolia.basescan.org/tx/0x7580ac508a2ac34cf6a4f4346fb6b4f09edaaa4f946f42ecdb2bfd2a633d43af#eventlog
	userOpEventTopic0 = common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")
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
) (*types.Receipt, error) {
	// Configuration for exponential backoff polling
	const (
		maxWaitTime     = 30 * time.Second // Maximum total wait time (bundler should process within 2-5s)
		initialInterval = 1 * time.Second  // Start polling every 1 second
		maxInterval     = 5 * time.Second  // Max polling interval (cap exponential growth)
		backoffFactor   = 1.5              // Multiply interval by 1.5 each retry
	)

	// Try WebSocket subscription first (most efficient for real-time events)
	if wsClient != nil {
		log.Printf("🔍 TRANSACTION WAITING: Attempting WebSocket subscription...")

		query := ethereum.FilterQuery{
			Addresses: []common.Address{entrypoint},
			Topics:    [][]common.Hash{{userOpEventTopic0}, {common.HexToHash(userOpHash)}},
		}

		logs := make(chan types.Log)
		sub, err := wsClient.SubscribeFilterLogs(context.Background(), query, logs)

		if err == nil {
			// WebSocket subscription successful - use it with a polling fallback
			log.Printf("✅ TRANSACTION WAITING: WebSocket subscription active, will poll as fallback")
			defer sub.Unsubscribe()

			startTime := time.Now()
			pollInterval := initialInterval
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			for {
				select {
				case err := <-sub.Err():
					if err != nil {
						log.Printf("⚠️ TRANSACTION WAITING: WebSocket error, falling back to polling: %v", err)
						// Continue with polling below
						goto PollingOnly
					}

				case vLog := <-logs:
					// Got the event via WebSocket - fastest path!
					log.Printf("✅ TRANSACTION WAITING: UserOp confirmed via WebSocket - tx: %s", vLog.TxHash.Hex())
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
						log.Printf("⏰ TRANSACTION WAITING: Timeout after %v - UserOp may still be pending", elapsed)
						return nil, nil
					}

					log.Printf("🔍 TRANSACTION WAITING: Polling for confirmation (elapsed: %v, interval: %v)...",
						elapsed.Round(time.Second), pollInterval)

					receipt, found, err := pollUserOpReceipt(client, entrypoint, userOpHash)
					if err != nil {
						log.Printf("⚠️ TRANSACTION WAITING: Polling error: %v", err)
					}
					if found {
						log.Printf("✅ TRANSACTION WAITING: UserOp confirmed via polling")
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
			log.Printf("⚠️ TRANSACTION WAITING: WebSocket subscription failed, using polling only: %v", err)
		}
	} else {
		log.Printf("🔍 TRANSACTION WAITING: No WebSocket client, using polling only")
	}

PollingOnly:
	// Polling-only mode (WebSocket unavailable or failed)
	log.Printf("🔍 TRANSACTION WAITING: Starting polling-only mode with exponential backoff")

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

// SendUserOp builds, signs, and sends a UserOperation to be executed.
// It then listens on-chain for 60 seconds to wait until the userops is executed.
// If the userops is executed, the transaction Receipt is also returned.
// If paymasterReq is nil, a standard UserOp without paymaster is sent.
// If paymasterReq is provided, it will use the paymaster parameters.
// senderOverride: If provided, use this as the smart account sender.
// saltOverride: If provided (and the account is not yet deployed), use this salt to produce initCode.
func SendUserOp(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	paymasterNonceOverride *big.Int,
) (*userop.UserOperation, *types.Receipt, error) {
	log.Printf("SendUserOp started - owner: %s, bundler: %s", owner.Hex(), smartWalletConfig.BundlerURL)

	var userOp *userop.UserOperation
	var err error
	entrypoint := smartWalletConfig.EntrypointAddress

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

	wsClient, err := ethclient.Dial(smartWalletConfig.EthWsUrl)
	if err != nil {
		return nil, nil, err
	}
	defer wsClient.Close()

	// Step 1: Estimate gas FIRST (before deciding paymaster vs self-funded)
	// Build a temporary UserOp to estimate gas
	var estimatedCallGas, estimatedVerificationGas, estimatedPreVerificationGas *big.Int
	if paymasterReq != nil {
		log.Printf("🔍 GAS ESTIMATION: Estimating gas before paymaster decision...")

		// Build temporary UserOp for estimation (without paymaster)
		tempUserOp, tempErr := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride)
		if tempErr != nil {
			log.Printf("❌ Failed to build temp UserOp for gas estimation: %v", tempErr)
			return nil, nil, tempErr
		}

		// Set nonce for estimation
		tempUserOp.Nonce = aa.MustNonce(client, tempUserOp.Sender, accountSalt)

		// Set dummy signature for estimation
		tempUserOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

		// Estimate gas using bundler
		gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *tempUserOp, aa.EntrypointAddress, map[string]any{})
		if gasErr != nil {
			log.Printf("❌ GAS ESTIMATION FAILED: %v", gasErr)
			log.Printf("   Continuing with hardcoded gas limits...")
		} else if gas != nil {
			estimatedCallGas = gas.CallGasLimit
			estimatedVerificationGas = gas.VerificationGasLimit
			estimatedPreVerificationGas = gas.PreVerificationGas
			log.Printf("✅ GAS ESTIMATION SUCCESS:")
			log.Printf("   CallGasLimit: %s", estimatedCallGas.String())
			log.Printf("   VerificationGasLimit: %s", estimatedVerificationGas.String())
			log.Printf("   PreVerificationGas: %s", estimatedPreVerificationGas.String())
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
			paymasterNonceOverride,      // Use provided paymaster nonce for sequential UserOps
			estimatedCallGas,            // Use estimated gas
			estimatedVerificationGas,    // Use estimated gas
			estimatedPreVerificationGas, // Use estimated gas
		)
	}

	if err != nil {
		log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to build UserOp - %v", err)
		return nil, nil, err
	}

	log.Printf("🔍 DEPLOYED WORKFLOW: UserOp built successfully, sending to bundler - sender: %s", userOp.Sender.Hex())

	// Send the UserOp to the bundler using the shared core logic
	txResult, err := sendUserOpCore(smartWalletConfig, userOp, client, bundlerClient)
	if err != nil {
		return userOp, nil, err
	}

	// 🔍 TRANSACTION WAITING DEBUG: Start waiting for on-chain confirmation
	log.Printf("🔍 TRANSACTION WAITING: Starting to wait for UserOp confirmation")
	log.Printf("  UserOp Hash: %s", txResult)
	log.Printf("  Entrypoint: %s", entrypoint.Hex())

	// Wait for UserOp confirmation using exponential backoff polling
	// This is more efficient than a fixed 3-minute timeout and handles bundler delays gracefully
	receipt, err := waitForUserOpConfirmation(client, wsClient, entrypoint, txResult)
	if err != nil {
		log.Printf("❌ TRANSACTION WAITING: Failed to get confirmation: %v", err)
		return userOp, nil, nil
	}
	if receipt == nil {
		log.Printf("⏰ TRANSACTION WAITING: No confirmation received (bundler may still be processing)")
		return userOp, nil, nil
	}

	log.Printf("✅ DEPLOYED WORKFLOW: Receipt retrieved successfully - status: %d, gas_used: %d, logs_count: %d",
		receipt.Status, receipt.GasUsed, len(receipt.Logs))

	// The polling mechanism in waitForUserOpConfirmation already ensures
	// the transaction is confirmed on-chain before we proceed
	return userOp, receipt, nil
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
		log.Printf("🔍 NONCE DEBUG: Setting nonce to fresh value from chain: %s", freshNonce.String())
		userOp.Nonce = freshNonce
	} else {
		log.Printf("🔍 NONCE DEBUG: Keeping existing nonce: %s (not refreshing to avoid invalidating signatures)", userOp.Nonce.String())
	}

	for retry := 0; retry < maxRetries; retry++ {
		log.Printf("🔄 DEPLOYED WORKFLOW: Attempt %d/%d - Using nonce: %s", retry+1, maxRetries, userOp.Nonce.String())

		// Re-estimate gas with current nonce (only on first attempt or if previous failed due to gas)
		// IMPORTANT: Skip gas re-estimation if paymaster is present, as it would invalidate the paymaster signature
		hasPaymaster := len(userOp.PaymasterAndData) > 0
		if !hasPaymaster && (retry == 0 || (err != nil && strings.Contains(err.Error(), "gas"))) {
			userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

			// Gas estimation debug logging
			log.Printf("GAS ESTIMATION DEBUG: Starting gas estimation for UserOp")
			log.Printf("  Sender: %s", userOp.Sender.Hex())
			log.Printf("  Nonce: %s", userOp.Nonce.String())
			log.Printf("  CallData length: %d bytes", len(userOp.CallData))
			log.Printf("  InitCode length: %d bytes", len(userOp.InitCode))
			log.Printf("  MaxFeePerGas: %s wei", userOp.MaxFeePerGas.String())
			log.Printf("  MaxPriorityFeePerGas: %s wei", userOp.MaxPriorityFeePerGas.String())
			gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *userOp, aa.EntrypointAddress, map[string]any{})
			if gasErr == nil && gas != nil {
				// Gas estimation success logging
				log.Printf("✅ GAS ESTIMATION SUCCESS:")
				log.Printf("  PreVerificationGas: %s wei", gas.PreVerificationGas.String())
				log.Printf("  VerificationGasLimit: %s wei", gas.VerificationGasLimit.String())
				log.Printf("  CallGasLimit: %s wei", gas.CallGasLimit.String())

				// Calculate total gas and cost estimates
				totalGasLimit := new(big.Int).Add(gas.PreVerificationGas, new(big.Int).Add(gas.VerificationGasLimit, gas.CallGasLimit))
				estimatedCost := new(big.Int).Mul(totalGasLimit, userOp.MaxFeePerGas)
				log.Printf("  Total Gas Limit: %s", totalGasLimit.String())
				log.Printf("  Estimated Max Cost: %s wei", estimatedCost.String())

				userOp.PreVerificationGas = gas.PreVerificationGas
				userOp.VerificationGasLimit = gas.VerificationGasLimit
				userOp.CallGasLimit = gas.CallGasLimit
			} else if retry == 0 {
				// Gas estimation failure logging
				log.Printf("❌ GAS ESTIMATION FAILED:")
				log.Printf("  Error: %v", gasErr)
				log.Printf("  Entry Point: %s", aa.EntrypointAddress.Hex())
				// Only fail on first attempt if gas estimation fails
				return "", fmt.Errorf("failed to estimate gas: %w", gasErr)
			} else {
				log.Printf("❌ GAS ESTIMATION FAILED on retry %d: %v", retry+1, gasErr)
			}
		}

		// Sign with current nonce
		userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
		userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
		if err != nil {
			log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to sign UserOp - %v", err)
			return "", fmt.Errorf("failed to sign UserOp: %w", err)
		}

		// Bundler send debug logging
		log.Printf("BUNDLER SEND DEBUG: Preparing to send UserOp to bundler")
		log.Printf("  Final Gas Limits:")
		log.Printf("    CallGasLimit: %s", userOp.CallGasLimit.String())
		log.Printf("    VerificationGasLimit: %s", userOp.VerificationGasLimit.String())
		log.Printf("    PreVerificationGas: %s", userOp.PreVerificationGas.String())
		log.Printf("  Final Gas Prices:")
		log.Printf("    MaxFeePerGas: %s wei", userOp.MaxFeePerGas.String())
		log.Printf("    MaxPriorityFeePerGas: %s wei", userOp.MaxPriorityFeePerGas.String())
		log.Printf("  UserOp Details:")
		log.Printf("    Sender: %s", userOp.Sender.Hex())
		log.Printf("    Nonce: %s", userOp.Nonce.String())
		log.Printf("    InitCode: %d bytes (%s...)", len(userOp.InitCode), func() string {
			if len(userOp.InitCode) > 20 {
				return common.Bytes2Hex(userOp.InitCode[:20])
			}
			return common.Bytes2Hex(userOp.InitCode)
		}())
		log.Printf("    CallData: %d bytes", len(userOp.CallData))
		log.Printf("    PaymasterAndData: %d bytes", len(userOp.PaymasterAndData))
		log.Printf("    Signature: %d bytes", len(userOp.Signature))

		// Calculate total estimated cost for prefund check
		totalGasLimit := new(big.Int).Add(userOp.PreVerificationGas, new(big.Int).Add(userOp.VerificationGasLimit, userOp.CallGasLimit))
		estimatedMaxCost := new(big.Int).Mul(totalGasLimit, userOp.MaxFeePerGas)
		log.Printf("  PREFUND REQUIREMENT: %s wei (total gas * maxFeePerGas)", estimatedMaxCost.String())

		// Check sender balance before sending to bundler
		if balance, balErr := client.BalanceAt(context.Background(), userOp.Sender, nil); balErr == nil {
			log.Printf("  SENDER BALANCE: %s wei", balance.String())
			if balance.Cmp(estimatedMaxCost) >= 0 {
				log.Printf("  ✅ PREFUND CHECK: Sufficient balance (%s >= %s)", balance.String(), estimatedMaxCost.String())
			} else {
				log.Printf("  ❌ PREFUND CHECK: Insufficient balance (%s < %s)", balance.String(), estimatedMaxCost.String())
				log.Printf("  SHORTFALL: Need %s more wei", new(big.Int).Sub(estimatedMaxCost, balance).String())
			}
		} else {
			log.Printf("  ❌ BALANCE CHECK FAILED: %v", balErr)
		}

		// Attempt to send
		txResult, err = bundlerClient.SendUserOperation(context.Background(), *userOp, aa.EntrypointAddress)

		// Bundler send result logging
		if err == nil && txResult != "" {
			log.Printf("✅ BUNDLER SEND SUCCESS:")
			log.Printf("  Attempt: %d/%d", retry+1, maxRetries)
			log.Printf("  Nonce used: %s", userOp.Nonce.String())
			log.Printf("  UserOp hash: %s", txResult)

			// Manually trigger bundling immediately (helps with development/testing)
			// This is only needed for local bundlers that don't auto-bundle frequently
			log.Printf("🔨 MANUAL BUNDLE TRIGGER: Calling debug_bundler_sendBundleNow...")
			triggerErr := bundlerClient.SendBundleNow(context.Background())
			if triggerErr != nil {
				log.Printf("⚠️  Manual bundle trigger failed (non-fatal): %v", triggerErr)
				log.Printf("   Bundler will auto-bundle based on --bundle_interval setting")
			} else {
				log.Printf("✅ Manual bundle trigger successful - bundler should process immediately")
			}

			// Increment nonce for next potential UserOp (prevents nonce collision for sequential txs)
			// This allows the next UserOp to use nonce+1 even if this UserOp hasn't been mined yet
			userOp.Nonce = new(big.Int).Add(userOp.Nonce, big.NewInt(1))
			log.Printf("🔢 NONCE INCREMENT: Next nonce will be %s (for sequential UserOps)", userOp.Nonce.String())

			break
		}

		// Bundler send failure logging
		log.Printf("❌ BUNDLER SEND FAILED:")
		log.Printf("  Attempt: %d/%d", retry+1, maxRetries)
		log.Printf("  Error: %v", err)
		log.Printf("  TxResult: %s", txResult)
		// For nonce errors, refetch nonce and retry
		if err != nil && strings.Contains(err.Error(), "AA25 invalid account nonce") {
			if retry < maxRetries-1 {
				log.Printf("🔄 DEPLOYED WORKFLOW: Nonce conflict detected, refetching fresh nonce")
				freshNonce = aa.MustNonce(client, userOp.Sender, accountSalt)
				userOp.Nonce = freshNonce
				log.Printf("🔄 DEPLOYED WORKFLOW: Updated nonce to: %s", freshNonce.String())
				continue
			}
		}

		// For other errors, don't retry unless it's a transient network error
		if err != nil && !strings.Contains(err.Error(), "AA25 invalid account nonce") &&
			!strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "connection") {
			log.Printf("🚨 DEPLOYED WORKFLOW: Non-retryable error, stopping: %v", err)
			break
		}
	}

	if err != nil || txResult == "" {
		log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to send UserOp to bundler after %d retries - err: %v, txResult: %s", maxRetries, err, txResult)
		return "", fmt.Errorf("error sending transaction to bundler: %w", err)
	}

	log.Printf("✅ DEPLOYED WORKFLOW: UserOp sent successfully - txResult: %s", txResult)
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

// SendUserOpWithWsClient is like SendUserOp but uses a provided WebSocket client for efficient transaction monitoring
func SendUserOpWithWsClient(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	wsClient *ethclient.Client,
	paymasterNonceOverride *big.Int,
) (*userop.UserOperation, *types.Receipt, error) {
	log.Printf("SendUserOpWithWsClient started - owner: %s, bundler: %s", owner.Hex(), smartWalletConfig.BundlerURL)

	var userOp *userop.UserOperation
	var err error
	entrypoint := smartWalletConfig.EntrypointAddress

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

	// Use provided WebSocket client (no defer close - managed globally)
	if wsClient == nil {
		log.Printf("⚠️ TRANSACTION WAITING: No WebSocket client provided, transaction monitoring disabled")
		// Fall back to original SendUserOp behavior
		return SendUserOp(smartWalletConfig, owner, callData, paymasterReq, senderOverride, nil)
	}

	// Step 1: Estimate gas FIRST (before deciding paymaster vs self-funded)
	// Build a temporary UserOp to estimate gas
	var estimatedCallGas, estimatedVerificationGas, estimatedPreVerificationGas *big.Int
	if paymasterReq != nil {
		log.Printf("🔍 GAS ESTIMATION: Estimating gas before paymaster decision...")

		// Build temporary UserOp for estimation (without paymaster)
		tempUserOp, tempErr := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride)
		if tempErr != nil {
			log.Printf("❌ Failed to build temp UserOp for gas estimation: %v", tempErr)
			return nil, nil, tempErr
		}

		// Set nonce for estimation
		tempUserOp.Nonce = aa.MustNonce(client, tempUserOp.Sender, accountSalt)

		// Set dummy signature for estimation
		tempUserOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

		// Estimate gas using bundler
		gas, gasErr := bundlerClient.EstimateUserOperationGas(context.Background(), *tempUserOp, aa.EntrypointAddress, map[string]any{})
		if gasErr != nil {
			log.Printf("❌ GAS ESTIMATION FAILED: %v", gasErr)
			log.Printf("   Continuing with hardcoded gas limits...")
		} else if gas != nil {
			estimatedCallGas = gas.CallGasLimit
			estimatedVerificationGas = gas.VerificationGasLimit
			estimatedPreVerificationGas = gas.PreVerificationGas
			log.Printf("✅ GAS ESTIMATION SUCCESS:")
			log.Printf("   CallGasLimit: %s", estimatedCallGas.String())
			log.Printf("   VerificationGasLimit: %s", estimatedVerificationGas.String())
			log.Printf("   PreVerificationGas: %s", estimatedPreVerificationGas.String())
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
			paymasterNonceOverride,      // Use provided paymaster nonce for sequential UserOps
			estimatedCallGas,            // Use estimated gas
			estimatedVerificationGas,    // Use estimated gas
			estimatedPreVerificationGas, // Use estimated gas
		)
	}

	if err != nil {
		log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to build UserOp - %v", err)
		return nil, nil, err
	}

	log.Printf("🔍 DEPLOYED WORKFLOW: UserOp built successfully, sending to bundler - sender: %s", userOp.Sender.Hex())

	// Send the UserOp to the bundler using the shared core logic
	txResult, err := sendUserOpCore(smartWalletConfig, userOp, client, bundlerClient)
	if err != nil {
		return userOp, nil, err
	}

	// 🔍 TRANSACTION WAITING DEBUG: Start waiting for on-chain confirmation using global WebSocket client
	log.Printf("🔍 TRANSACTION WAITING: Starting to wait for UserOp confirmation (using global client)")
	log.Printf("  UserOp Hash: %s", txResult)
	log.Printf("  Entrypoint: %s", entrypoint.Hex())

	// Wait for UserOp confirmation using exponential backoff polling
	// This is more efficient than a fixed 3-minute timeout and handles bundler delays gracefully
	receipt, err := waitForUserOpConfirmation(client, wsClient, entrypoint, txResult)
	if err != nil {
		log.Printf("❌ TRANSACTION WAITING: Failed to get confirmation: %v", err)
		return userOp, nil, nil
	}
	if receipt == nil {
		log.Printf("⏰ TRANSACTION WAITING: No confirmation received (bundler may still be processing)")
		return userOp, nil, nil
	}

	log.Printf("✅ DEPLOYED WORKFLOW: Receipt retrieved successfully - status: %d, gas_used: %d, logs_count: %d",
		receipt.Status, receipt.GasUsed, len(receipt.Logs))

	// The polling mechanism in waitForUserOpConfirmation already ensures
	// the transaction is confirmed on-chain before we proceed
	return userOp, receipt, nil
}

// BuildUserOpWithPaymaster creates a UserOperation with paymaster support.
// It handles the process of building the UserOp, signing it, and setting the appropriate PaymasterAndData field.
// It works same way as BuildUserOp but with the extra field PaymasterAndData set. The protocol is defined in https://eips.ethereum.org/EIPS/eip-4337#paymasters
// Currently, we use the VerifyingPaymaster contract as the paymaster. We set a signer when initialize the paymaster contract.
// The signer is also the controller private key. It's the only way to generate the signature for paymaster.
// nonceOverride: if provided (not nil), uses this nonce instead of fetching from chain. Use this for sequential UserOps.
// paymasterNonceOverride: if provided (not nil), uses this paymaster nonce instead of fetching from chain. Use this for sequential UserOps.
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
	paymasterNonceOverride *big.Int,
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
		userOp.VerificationGasLimit = verificationGasOverride
		log.Printf("🔍 Gas Override: VerificationGasLimit set to %s", verificationGasOverride.String())
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

	// Get the paymaster's nonce for this sender
	// IMPORTANT: The paymaster maintains its own nonce per sender which is internally included
	// in the GetHash() call. The paymaster contract reads senderNonce[sender] from storage during
	// GetHash() and includes it in the hash computation. This means:
	// 1. The nonce value we fetch here is for logging/debugging only - it's NOT passed to GetHash()
	// 2. GetHash() will use whatever nonce is currently in the contract's storage
	// 3. If RPC nodes are out of sync, the nonce we see might differ from what GetHash() uses
	// 4. AA33 errors can occur if the bundler's RPC sees a different nonce than our RPC during validation
	var paymasterNonce *big.Int
	if paymasterNonceOverride != nil {
		// Use provided paymaster nonce for sequential UserOps (prevents nonce collisions)
		paymasterNonce = paymasterNonceOverride
		log.Printf("🔍 BuildUserOpWithPaymaster: Using provided paymaster nonce %s (sequential UserOps)", paymasterNonce.String())
	} else {
		// Fetch from chain using direct RPC call (Go binding has issues)
		// Method signature: senderNonce(address) -> uint256
		// Method ID: 0x9c90b443
		paddedSender := common.LeftPadBytes(userOp.Sender.Bytes(), 32)
		callData := "0x9c90b443" + hexutil.Encode(paddedSender)[2:]

		// Use CallContract method for direct contract calls
		result, err := client.CallContract(context.Background(), ethereum.CallMsg{
			To:   &paymasterAddress,
			Data: hexutil.MustDecode(callData),
		}, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get paymaster nonce via RPC: %w", err)
		}

		paymasterNonce = new(big.Int).SetBytes(result)
		log.Printf("🔍 BuildUserOpWithPaymaster: Fetched paymaster nonce %s from chain (via RPC)", paymasterNonce.String())
	}

	log.Printf("🔍 PAYMASTER NONCE DEBUG:")
	log.Printf("   Sender: %s", userOp.Sender.Hex())
	log.Printf("   Paymaster nonce: %s", paymasterNonce.String())

	// Get the hash to sign from the PayMaster contract
	// IMPORTANT: The GetHash function signature is (userOp, validUntil, validAfter) per the contract ABI
	paymasterHash, err := paymasterContract.GetHash(nil, paymasterUserOp, validUntil, validAfter)

	if err != nil {
		return nil, fmt.Errorf("failed to get paymaster hash: %w", err)
	}

	log.Printf("🔍 PAYMASTER SIGNATURE DEBUG:")
	log.Printf("   Paymaster address: %s", paymasterAddress.Hex())
	log.Printf("   validAfter: %s (timestamp: %d)", validAfter.String(), validAfter.Int64())
	log.Printf("   validUntil: %s (timestamp: %d)", validUntil.String(), validUntil.Int64())
	log.Printf("   Paymaster hash (from contract): 0x%x", paymasterHash)

	// Sign the paymaster hash with the controller's private key
	paymasterSignature, err := signer.SignMessage(smartWalletConfig.ControllerPrivateKey, paymasterHash[:])

	if err != nil {
		return nil, fmt.Errorf("failed to sign paymaster hash: %w", err)
	}

	// Pack timestamps as raw uint48 values (6 bytes each, 12 bytes total)
	// The bundler expects raw bytes, NOT ABI-encoded 32-byte padded values
	// Total: address(20) + uint48(6) + uint48(6) + signature(65) = 97 bytes

	// Convert timestamps to 6-byte big-endian representation (uint48)
	validUntilBytes := make([]byte, 6)
	validAfterBytes := make([]byte, 6)

	// validUntil to 6 bytes (big-endian)
	validUntilU64 := validUntil.Uint64()
	for i := 0; i < 6; i++ {
		validUntilBytes[5-i] = byte(validUntilU64 >> (8 * i))
	}

	// validAfter to 6 bytes (big-endian)
	validAfterU64 := validAfter.Uint64()
	for i := 0; i < 6; i++ {
		validAfterBytes[5-i] = byte(validAfterU64 >> (8 * i))
	}

	// Combine timestamps: validUntil FIRST, validAfter SECOND
	encodedTimestamps := append(validUntilBytes, validAfterBytes...)

	log.Printf("🔍 TIMESTAMP PACKING DEBUG:")
	log.Printf("   Raw uint48 (validUntil, validAfter): 0x%x (%d bytes)", encodedTimestamps, len(encodedTimestamps))

	// Create PaymasterAndData: address (20) + uint48 validUntil (6) + uint48 validAfter (6) + signature (65) = 97 bytes
	paymasterAndData := append(paymasterAddress.Bytes(), encodedTimestamps...)
	paymasterAndData = append(paymasterAndData, paymasterSignature...)

	log.Printf("🔍 PAYMASTER AND DATA DEBUG:")
	log.Printf("   Total length: %d bytes (expected: 97)", len(paymasterAndData))
	log.Printf("   PaymasterAndData: 0x%x", paymasterAndData)

	// Update the UserOperation with the properly encoded PaymasterAndData
	userOp.PaymasterAndData = paymasterAndData

	// Update the userOpHash with the new PaymasterAndData value
	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)

	// Sign the updated user operation
	// IMPORTANT: For ERC-4337, the signature format depends on the smart wallet implementation
	// The AVA smart wallet expects an EIP-191 prefixed signature
	log.Printf("🔐 SIGNATURE DEBUG:")
	log.Printf("   UserOpHash (to be signed): %s", userOpHash.Hex())
	log.Printf("   Controller address: %s", crypto.PubkeyToAddress(smartWalletConfig.ControllerPrivateKey.PublicKey).Hex())

	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign final UserOp: %w", err)
	}
	log.Printf("   Signature: 0x%s", common.Bytes2Hex(userOp.Signature))

	return userOp, nil
}
