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
	// Default gas limit used for initial UserOp construction before bundler estimation
	// Gas info is calculated and returned by bundler RPC
	DEFAULT_GAS_LIMIT    = big.NewInt(10000000)
	callGasLimit         = DEFAULT_GAS_LIMIT
	verificationGasLimit = DEFAULT_GAS_LIMIT
	preVerificationGas   = DEFAULT_GAS_LIMIT

	// the signature isnt important, only length check
	dummySigForGasEstimation = crypto.Keccak256Hash(common.FromHex("0xdead123"))
	accountSalt              = big.NewInt(0)

	// example tx send to entrypoint: https://sepolia.basescan.org/tx/0x7580ac508a2ac34cf6a4f4346fb6b4f09edaaa4f946f42ecdb2bfd2a633d43af#eventlog
	userOpEventTopic0 = common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")
	// waiting 180 seconds for the tx hash to propagate to the network (increased for deployed workflows)
	// if we cannot get it after that, we need to consolidate with some indexer to get this data later on
	waitingForBundleTx = 180 * time.Second
)

// VerifyingPaymasterRequest contains the parameters needed for paymaster functionality. This use the reference from https://github.com/eth-optimism/paymaster-reference
type VerifyingPaymasterRequest struct {
	PaymasterAddress common.Address
	ValidUntil       *big.Int
	ValidAfter       *big.Int
}

func GetVerifyingPaymasterRequestForDuration(address common.Address, duration time.Duration) *VerifyingPaymasterRequest {
	// Use a small negative skew to tolerate clock drift between services and the bundler
	const skewSeconds int64 = 60 // 1 minute skew
	now := time.Now().Unix()
	validAfter := now - skewSeconds
	validUntil := now + int64(duration.Seconds())

	return &VerifyingPaymasterRequest{
		PaymasterAddress: address,
		ValidUntil:       big.NewInt(validUntil),
		ValidAfter:       big.NewInt(validAfter),
	}
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
) (*userop.UserOperation, *types.Receipt, error) {
	log.Printf("🚀 DEPLOYED WORKFLOW: SendUserOp started - owner: %s, calldata_length: %d, bundler: %s",
		owner.Hex(), len(callData), smartWalletConfig.BundlerURL)

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

	// Build the userOp based on whether paymaster is requested or not
	if paymasterReq == nil {
		// Standard UserOp without paymaster
		userOp, err = BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride)
	} else {
		// UserOp with paymaster support
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
		)
	}

	if err != nil {
		log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to build UserOp - %v", err)
		return nil, nil, err
	}

	log.Printf("🔍 DEPLOYED WORKFLOW: UserOp built successfully, sending to bundler - sender: %s", userOp.Sender.Hex())

	// Send the UserOp to the bundler with dynamic nonce fetching
	var txResult string
	maxRetries := 3
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return userOp, nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	// Fetch nonce before entering the retry loop
	freshNonce := aa.MustNonce(client, userOp.Sender, accountSalt)
	userOp.Nonce = freshNonce

	for retry := 0; retry < maxRetries; retry++ {
		log.Printf("🔄 DEPLOYED WORKFLOW: Attempt %d/%d - Using nonce: %s", retry+1, maxRetries, userOp.Nonce.String())

		// Re-estimate gas with current nonce (only on first attempt or if previous failed due to gas)
		if retry == 0 || (err != nil && strings.Contains(err.Error(), "gas")) {
			userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

			// Gas estimation debug logging
			log.Printf("GAS ESTIMATION DEBUG: Starting gas estimation for UserOp")
			log.Printf("  Sender: %s", userOp.Sender.Hex())
			log.Printf("  Nonce: %s", userOp.Nonce.String())
			log.Printf("  CallData length: %d bytes", len(userOp.CallData))
			log.Printf("  InitCode length: %d bytes", len(userOp.InitCode))
			log.Printf("  MaxFeePerGas: %s wei", userOp.MaxFeePerGas.String())
			log.Printf("  MaxPriorityFeePerGas: %s wei", userOp.MaxPriorityFeePerGas.String())
			log.Printf("  Bundler URL: %s", smartWalletConfig.BundlerURL)

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
				log.Printf("  Bundler URL: %s", smartWalletConfig.BundlerURL)
				log.Printf("  Entry Point: %s", aa.EntrypointAddress.Hex())
				// Only fail on first attempt if gas estimation fails
				return userOp, nil, fmt.Errorf("failed to estimate gas: %w", gasErr)
			} else {
				log.Printf("❌ GAS ESTIMATION FAILED on retry %d: %v", retry+1, gasErr)
			}
		}

		// Sign with current nonce
		userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
		userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
		if err != nil {
			log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to sign UserOp - %v", err)
			return userOp, nil, fmt.Errorf("failed to sign UserOp: %w", err)
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
		log.Printf("    PaymasterAndData: %d bytes", len(userOp.PaymasterAndData))

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
			log.Printf("  Bundler URL: %s", smartWalletConfig.BundlerURL)
			break
		}

		// Bundler send failure logging
		log.Printf("❌ BUNDLER SEND FAILED:")
		log.Printf("  Attempt: %d/%d", retry+1, maxRetries)
		log.Printf("  Error: %v", err)
		log.Printf("  TxResult: %s", txResult)
		log.Printf("  Bundler URL: %s", smartWalletConfig.BundlerURL)

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
		return userOp, nil, fmt.Errorf("error sending transaction to bundler: %w", err)
	}

	log.Printf("✅ DEPLOYED WORKFLOW: UserOp sent successfully - txResult: %s", txResult)

	// When the userops get run on-chain, the entrypoint contract emits this event:
	// UserOperationEvent (index_topic_1 bytes32 userOpHash, index_topic_2 address sender, index_topic_3 address paymaster,
	//                     uint256 nonce, bool success, uint256 actualGasCost, uint256 actualGasUsed)
	// Topic0 -> 0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f (event signature)
	// Topic1 -> UserOp Hash
	// Topic2 -> Sender
	// Topic3 -> paymaster (if used)
	query := ethereum.FilterQuery{
		Addresses: []common.Address{entrypoint},
		Topics:    [][]common.Hash{{userOpEventTopic0}, {common.HexToHash(txResult)}},
	}

	// Create a channel to receive logs
	logs := make(chan types.Log)

	// Subscribe to the logs
	sub, err := wsClient.SubscribeFilterLogs(context.Background(), query, logs)
	if err != nil {
		log.Printf("Failed to subscribe to logs: %v", err)
		return userOp, nil, nil
	}
	timeout := time.After(waitingForBundleTx)

	defer sub.Unsubscribe()

	for {
		select {
		case err := <-sub.Err():
			if err != nil {
				return userOp, nil, nil
			}
		case vLog := <-logs:
			// Print the transaction hash of the log
			log.Printf("🎯 DEPLOYED WORKFLOW: got the respective transaction hash: %s for userops hash: %s\n", vLog.TxHash.Hex(), txResult)

			receipt, err := client.TransactionReceipt(context.Background(), vLog.TxHash)
			if err != nil {
				log.Printf("🚨 DEPLOYED WORKFLOW ERROR: Failed to get receipt: %v", err)
				continue
			}

			log.Printf("✅ DEPLOYED WORKFLOW: Receipt retrieved successfully - status: %d, gas_used: %d, logs_count: %d",
				receipt.Status, receipt.GasUsed, len(receipt.Logs))

			return userOp, receipt, nil
		case <-timeout:
			log.Printf("⏰ DEPLOYED WORKFLOW WARNING: Transaction receipt timeout - no receipt received within timeout period")
			return userOp, nil, nil
		}
	}
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

	// account not initialized, feed in init code
	if len(code) == 0 {
		initCode, _ = aa.GetInitCode(owner.Hex(), accountSalt)
	}

	maxFeePerGas, maxPriorityFeePerGas, err := eip1559.SuggestFee(client)
	if err != nil {
		return nil, fmt.Errorf("failed to suggest gas fees: %w", err)
	}

	// Initialize UserOp with temporary nonce (will be set dynamically before sending)
	userOp := userop.UserOperation{
		Sender:   *sender,
		Nonce:    big.NewInt(0), // Placeholder - will be set dynamically
		InitCode: common.FromHex(initCode),
		CallData: callData,

		// dummy value, we will estimate gas with bundler rpc
		CallGasLimit:         callGasLimit,
		VerificationGasLimit: verificationGasLimit,
		PreVerificationGas:   preVerificationGas,

		MaxFeePerGas:         maxFeePerGas,
		MaxPriorityFeePerGas: maxPriorityFeePerGas,
		PaymasterAndData:     common.FromHex("0x"),
	}

	// Gas estimation and signing will be done dynamically in the send loop

	return &userOp, nil
}

// BuildUserOpWithPaymaster creates a UserOperation with paymaster support.
// It handles the process of building the UserOp, signing it, and setting the appropriate PaymasterAndData field.
// It works same way as BuildUserOp but with the extra field PaymasterAndData set. The protocol is defined in https://eips.ethereum.org/EIPS/eip-4337#paymasters
// Currently, we use the VerifyingPaymaster contract as the paymaster. We set a signer when initialize the paymaster contract.
// The signer is also the controller private key. It's the only way to generate the signature for paymaster.
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
) (*userop.UserOperation, error) {
	// First build the basic user operation (auto-deploy if needed). If override is provided,
	// it must match the derived sender from owner.
	userOp, err := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData, senderOverride)
	if err != nil {
		return nil, fmt.Errorf("failed to build base UserOp: %w", err)
	}

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
	paymasterHash, err := paymasterContract.GetHash(nil, paymasterUserOp, validUntil, validAfter)

	if err != nil {
		return nil, fmt.Errorf("failed to get paymaster hash: %w", err)
	}

	// Sign the paymaster hash with the controller's private key
	paymasterSignature, err := signer.SignMessage(smartWalletConfig.ControllerPrivateKey, paymasterHash[:])

	if err != nil {
		return nil, fmt.Errorf("failed to sign paymaster hash: %w", err)
	}

	// Define ABI types for timestamps
	uint48Type, _ := abi.NewType("uint48", "", nil)
	timestampArgs := abi.Arguments{
		abi.Argument{Type: uint48Type},
		abi.Argument{Type: uint48Type},
	}

	// Pack timestamps according to ABI encoding rules
	encodedTimestamps, err := timestampArgs.Pack(
		validUntil,
		validAfter,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to ABI encode timestamps: %w", err)
	}

	// Create PaymasterAndData
	paymasterAndData := append(paymasterAddress.Bytes(), encodedTimestamps...)
	paymasterAndData = append(paymasterAndData, paymasterSignature...)

	// Update the UserOperation with the properly encoded PaymasterAndData
	userOp.PaymasterAndData = paymasterAndData

	// Update the userOpHash with the new PaymasterAndData value
	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)

	// Sign the updated user operation
	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign final UserOp: %w", err)
	}

	return userOp, nil
}
