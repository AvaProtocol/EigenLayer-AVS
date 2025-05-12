package preset

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
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
	// Dummy value to fullfil validation.
	// Gas info is calculated and return by bundler RPC
	callGasLimit         = big.NewInt(10000000)
	verificationGasLimit = big.NewInt(10000000)
	preVerificationGas   = big.NewInt(10000000)

	// the signature isnt important, only length check
	dummySigForGasEstimation = crypto.Keccak256Hash(common.FromHex("0xdead123"))
	accountSalt              = big.NewInt(0)

	// example tx send to entrypoint: https://sepolia.basescan.org/tx/0x7580ac508a2ac34cf6a4f4346fb6b4f09edaaa4f946f42ecdb2bfd2a633d43af#eventlog
	userOpEventTopic0 = common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")
	// waiting 60 seconds for the tx hash to propagate to the network
	// if we cannot get it after that, we need to consolidate with some indexer to get this data later on
	waitingForBundleTx = 60 * time.Second
)

// VerifyingPaymasterRequest contains the parameters needed for paymaster functionality. This use the reference from https://github.com/eth-optimism/paymaster-reference
type VerifyingPaymasterRequest struct {
	PaymasterAddress common.Address
	ValidUntil       *big.Int
	ValidAfter       *big.Int
}

func GetVerifyingPaymasterRequestForDuration(address common.Address, duration time.Duration) *VerifyingPaymasterRequest {
	currentTime := time.Now().Unix()
	return &VerifyingPaymasterRequest{
		PaymasterAddress: address,
		ValidUntil:       big.NewInt(currentTime + int64(duration.Seconds())),
		// Create validUntil and validAfter values (1 hour from now and current time)
		// Due to clock drift between server, we subtract 3 seconds to be on the safe side
		ValidAfter: big.NewInt(currentTime - 3),
	}
}

// SendUserOp builds, signs, and sends a UserOperation to be executed.
// It then listens on-chain for 60 seconds to wait until the userops is executed.
// If the userops is executed, the transaction Receipt is also returned.
// If paymasterReq is nil, a standard UserOp without paymaster is sent.
// If paymasterReq is provided, it will use the paymaster parameters.
func SendUserOp(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
) (*userop.UserOperation, *types.Receipt, error) {
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
		userOp, err = BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData)
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
		)
	}

	if err != nil {
		return nil, nil, err
	}

	// Send the UserOp to the bundler
	txResult, err := bundlerClient.SendUserOperation(context.Background(), *userOp, aa.EntrypointAddress)
	if err != nil || txResult == "" {
		return userOp, nil, fmt.Errorf("error sending transaction to bundler: %w", err)
	}

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
			log.Printf("got the respective transaction hash: %s for userops hash: %s\n", vLog.TxHash.Hex(), txResult)

			receipt, err := client.TransactionReceipt(context.Background(), vLog.TxHash)
			if err != nil {
				log.Printf("Failed to get receipt: %v", err)
				continue
			}

			return userOp, receipt, nil
		case <-timeout:
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
) (*userop.UserOperation, error) {
	sender, _ := aa.GetSenderAddress(client, owner, accountSalt)

	initCode := "0x"
	code, err := client.CodeAt(context.Background(), *sender, nil)
	if err != nil {
		return nil, err
	}

	// account not initialize, feed in init code
	if len(code) == 0 {
		initCode, _ = aa.GetInitCode(owner.Hex(), accountSalt)
	}

	maxFeePerGas, maxPriorityFeePerGas, err := eip1559.SuggestFee(client)
	if err != nil {
		return nil, fmt.Errorf("failed to suggest gas fees: %w", err)
	}

	nonce := aa.MustNonce(client, *sender, accountSalt)
	userOp := userop.UserOperation{
		Sender:   *sender,
		Nonce:    nonce,
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

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

	gas, e := bundlerClient.EstimateUserOperationGas(context.Background(), userOp, aa.EntrypointAddress, map[string]any{})
	if gas == nil {
		return nil, fmt.Errorf("error estimated gas from bundler: %w", e)
	}

	userOp.PreVerificationGas = gas.PreVerificationGas
	userOp.VerificationGasLimit = gas.VerificationGasLimit
	userOp.CallGasLimit = gas.CallGasLimit
	// userOp.VerificationGas = gas.VerificationGas

	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign UserOp: %w", err)
	}

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
) (*userop.UserOperation, error) {
	// First build the basic user operation
	userOp, err := BuildUserOp(smartWalletConfig, client, bundlerClient, owner, callData)
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
