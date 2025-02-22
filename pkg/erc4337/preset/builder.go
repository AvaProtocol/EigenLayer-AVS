package preset

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/chainio/signer"
	"github.com/AvaProtocol/ap-avs/core/config"

	"github.com/AvaProtocol/ap-avs/pkg/eip1559"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/bundler"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/userop"
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

// SendUserOp signed and send the userops to bundle for execution. It then listen on-chain for 60secondds to wait until the userops is executed.
// If the userops is executed the transaction Receipt is also return
func SendUserOp(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
) (*userop.UserOperation, *types.Receipt, error) {
	entrypoint := smartWalletConfig.EntrypointAddress
	bundlerClient, err := bundler.NewBundlerClient(smartWalletConfig.BundlerURL)
	if err != nil {
		return nil, nil, err
	}
	var client, wsClient *ethclient.Client

	defer func() {
		if client != nil {
			client.Close()
		}
		if wsClient != nil {
			wsClient.Close()
		}
	}()

	client, err = ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, nil, err
	}

	wsClient, err = ethclient.Dial(smartWalletConfig.EthWsUrl)
	if err != nil {
		return nil, nil, err
	}

	sender, _ := aa.GetSenderAddress(client, owner, accountSalt)

	initCode := "0x"
	code, err := client.CodeAt(context.Background(), *sender, nil)
	if err != nil {
		return nil, nil, err
	}

	// account not initialize, feed in init code
	if len(code) == 0 {
		initCode, _ = aa.GetInitCode(owner.Hex(), accountSalt)
	}

	maxFeePerGas, maxPriorityFeePerGas, err := eip1559.SuggestFee(client)

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
	userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, dummySigForGasEstimation.Bytes())

	gas, e := bundlerClient.EstimateUserOperationGas(context.Background(), userOp, aa.EntrypointAddress, map[string]any{})
	if gas == nil {
		// TODO: handler retry, this could be rate limit from rpc
		return nil, nil, fmt.Errorf("error estimated gas from bundler: %w", e)
	}

	userOp.PreVerificationGas = gas.PreVerificationGas
	userOp.VerificationGasLimit = gas.VerificationGasLimit
	userOp.CallGasLimit = gas.CallGasLimit
	// userOp.VerificationGas = gas.VerificationGas

	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
	userOp.Signature, _ = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())

	txResult, e := bundlerClient.SendUserOperation(context.Background(), userOp, aa.EntrypointAddress)

	if e != nil || txResult == "" {
		return &userOp, nil, fmt.Errorf("error sending transaction to bundler")
	}

	// When the userops got run on-chain, the entrypoint contract emit this event
	// UserOperationEvent (index_topic_1 bytes32 userOpHash, index_topic_2 address sender, index_topic_3 address paymaster, uint256 nonce, bool success, uint256 actualGasCost, uint256 actualGasUsed)
	// Topic0 -> 0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f
	// Topic1 -> UserOp Hash
	// Topic2 -> Sender
	// Topic3 -> paymaster
	query := ethereum.FilterQuery{
		Addresses: []common.Address{entrypoint},
		Topics:    [][]common.Hash{{userOpEventTopic0}, {common.HexToHash(txResult)}},
	}

	// Create a channel to receive logs
	logs := make(chan types.Log)

	// Subscribe to the logs
	sub, err := wsClient.SubscribeFilterLogs(context.Background(), query, logs)
	if err != nil {
		log.Fatalf("Failed to subscribe to logs: %v", err)
	}
	timeout := time.After(waitingForBundleTx)

	defer sub.Unsubscribe()

	for {
		select {
		case err := <-sub.Err():
			if err != nil {
				return &userOp, nil, nil
			}
		case vLog := <-logs:
			// Print the transaction hash of the log
			fmt.Printf("Transaction hash: %s\n", vLog.TxHash.Hex())

			receipt, err := client.TransactionReceipt(context.Background(), vLog.TxHash)
			if err != nil {
				log.Printf("Failed to get receipt: %v", err)
				continue
			}

			return &userOp, receipt, nil
		case <-timeout:
			return &userOp, nil, nil
		}
	}

	return &userOp, nil, nil
}
