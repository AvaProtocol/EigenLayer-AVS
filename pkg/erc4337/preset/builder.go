package preset

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/chainio/signer"

	"github.com/AvaProtocol/ap-avs/pkg/eip1559"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/bundler"
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
)

func SendUserOp(
	client *ethclient.Client,
	bundlerClient *bundler.BundlerClient,
	signerKey *ecdsa.PrivateKey,
	owner common.Address,
	callData []byte,
) (string, error) {
	// TODO: Should we use a mutex?
	sender, _ := aa.GetSenderAddress(client, owner, accountSalt)

	initCode := "0x"
	code, err := client.CodeAt(context.Background(), *sender, nil)
	if err != nil {
		return "", err
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
	userOp.Signature, _ = signer.SignMessage(signerKey, dummySigForGasEstimation.Bytes())

	gas, e := bundlerClient.EstimateUserOperationGas(context.Background(), userOp, aa.EntrypointAddress, map[string]any{})
	if gas == nil {
		// TODO: handler retry, this could be rate limit from rpc
		return "", fmt.Errorf("error estimated gas from bundler: %w", e)
	}

	userOp.PreVerificationGas = gas.PreVerificationGas
	userOp.VerificationGasLimit = gas.VerificationGasLimit
	userOp.CallGasLimit = gas.CallGasLimit
	// //userOp.VerificationGas = gas.VerificationGas

	// TODO: Fix this to load properly estimate from voltaire https://github.com/candidelabs/voltaire
	userOp.PreVerificationGas = big.NewInt(10000000)   //gas.PreVerificationGas
	userOp.VerificationGasLimit = big.NewInt(10000000) //gas.VerificationGasLimit
	userOp.CallGasLimit = big.NewInt(10000000)         //gas.CallGasLimit

	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
	userOp.Signature, _ = signer.SignMessage(signerKey, userOpHash.Bytes())

	txResult, e := bundlerClient.SendUserOperation(context.Background(), userOp, aa.EntrypointAddress)

	return txResult, e
}
