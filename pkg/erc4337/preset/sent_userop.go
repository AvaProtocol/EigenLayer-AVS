package preset

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// SentUserOp is what an execution path reports about an operation it sent,
// independent of EntryPoint version.
//
// Callers only ever read this handful of fields off the operation — sender,
// nonce, the gas limits, and the hash — so carrying the version-specific
// struct out to them bought nothing and forced every consumer to know which
// UserOperation shape it had.
//
// UserOpHash is computed here rather than left for the caller. That is not
// just convenience: the hash binds to an EntryPoint address, and consumers
// were computing it against smart_wallet.entrypoint_address, which is the
// v0.6 EntryPoint. Doing that to a v0.7 operation yields a plausible-looking
// hash that matches nothing on chain — it would have been reported in
// execution logs and receipts as if it were real.
type SentUserOp struct {
	Sender     common.Address
	Nonce      *big.Int
	EntryPoint common.Address
	UserOpHash common.Hash

	CallGasLimit         *big.Int
	VerificationGasLimit *big.Int
	PreVerificationGas   *big.Int
	MaxFeePerGas         *big.Int

	// Sponsored reports whether a paymaster covered this operation, whichever
	// mechanism did it — the v0.6 verifying paymaster or a v0.7 Gas Manager
	// policy.
	Sponsored bool
}

// SendUserOpAuto sends an operation using whichever account implementation the
// chain is configured for, and reports the result in version-neutral terms.
//
// Dispatch is per CHAIN, not per wallet. That is a deliberate consequence of
// cutting every chain over at once: a wallet created before the cutover is a
// v0.6 SimpleAccount, and on a chain now configured for MA v2 it will no
// longer execute. Those wallets were audited to hold no meaningful balances
// before this was chosen. If that assumption ever stops holding, this is the
// function that has to learn to look at the wallet's factory instead.
//
// paymasterReq and executionFeeWei apply only to the v0.6 path. MA v2
// sponsorship comes from the Gas Manager policy on the chain's config, so
// those arguments are ignored there rather than quietly changing meaning.
func SendUserOpAuto(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	saltOverride *big.Int,
	executionFeeWei *big.Int,
	lgr logger.Logger,
) (*SentUserOp, *types.Receipt, error) {
	if smartWalletConfig == nil {
		return nil, nil, fmt.Errorf("nil smart wallet config")
	}

	if smartWalletConfig.UsesModularAccountV2() {
		op, receipt, err := SendUserOpMAv2(smartWalletConfig, owner, callData, senderOverride, saltOverride, lgr)
		sent, convErr := sentFromV07(op, big.NewInt(smartWalletConfig.ChainID))
		if err != nil {
			// Report the send failure, not the conversion: the send is why
			// this failed, and `sent` is best-effort context for the caller.
			return sent, receipt, err
		}
		if convErr != nil {
			return nil, receipt, convErr
		}
		return sent, receipt, nil
	}

	op, receipt, err := SendUserOp(smartWalletConfig, owner, callData, paymasterReq,
		senderOverride, saltOverride, executionFeeWei, lgr)
	sent := sentFromV06(op, smartWalletConfig.EntrypointAddress, big.NewInt(smartWalletConfig.ChainID))
	if err != nil {
		return sent, receipt, err
	}
	return sent, receipt, nil
}

// SendUserOpAutoWithWsClient is SendUserOpAuto reusing a caller-owned
// WebSocket client for receipt monitoring.
//
// The MA v2 path ignores wsClient and opens its own from the chain config.
// Sharing the aggregator's long-lived socket saved a dial on the v0.6 path;
// threading it through the v0.7 builder is not worth a second signature for
// what is only a receipt watcher.
func SendUserOpAutoWithWsClient(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *VerifyingPaymasterRequest,
	senderOverride *common.Address,
	saltOverride *big.Int,
	wsClient *ethclient.Client,
	executionFeeWei *big.Int,
	lgr logger.Logger,
) (*SentUserOp, *types.Receipt, error) {
	if smartWalletConfig == nil {
		return nil, nil, fmt.Errorf("nil smart wallet config")
	}
	if smartWalletConfig.UsesModularAccountV2() {
		return SendUserOpAuto(smartWalletConfig, owner, callData, paymasterReq,
			senderOverride, saltOverride, executionFeeWei, lgr)
	}
	op, receipt, err := SendUserOpWithWsClient(smartWalletConfig, owner, callData, paymasterReq,
		senderOverride, saltOverride, wsClient, executionFeeWei, lgr)
	sent := sentFromV06(op, smartWalletConfig.EntrypointAddress, big.NewInt(smartWalletConfig.ChainID))
	if err != nil {
		return sent, receipt, err
	}
	return sent, receipt, nil
}

// sentFromV07 converts a v0.7 operation. It returns (nil, nil) for a nil
// operation so a failed send that never built one is not an error here.
func sentFromV07(op *userop.UserOperationV07, chainID *big.Int) (*SentUserOp, error) {
	if op == nil {
		return nil, nil
	}
	entryPoint := EntryPointV07()
	sent := &SentUserOp{
		Sender:               op.Sender,
		Nonce:                op.Nonce,
		EntryPoint:           entryPoint,
		CallGasLimit:         op.CallGasLimit,
		VerificationGasLimit: op.VerificationGasLimit,
		PreVerificationGas:   op.PreVerificationGas,
		MaxFeePerGas:         op.MaxFeePerGas,
		Sponsored:            op.Paymaster != nil,
	}
	// An operation that never got priced cannot be hashed — every gas field
	// participates. Leaving the hash zero is honest; inventing one is not.
	hash, err := op.GetUserOpHash(entryPoint, chainID)
	if err != nil {
		return sent, nil
	}
	sent.UserOpHash = hash
	return sent, nil
}

func sentFromV06(op *userop.UserOperation, entryPoint common.Address, chainID *big.Int) *SentUserOp {
	if op == nil {
		return nil
	}
	return &SentUserOp{
		Sender:               op.Sender,
		Nonce:                op.Nonce,
		EntryPoint:           entryPoint,
		UserOpHash:           op.GetUserOpHash(entryPoint, chainID),
		CallGasLimit:         op.CallGasLimit,
		VerificationGasLimit: op.VerificationGasLimit,
		PreVerificationGas:   op.PreVerificationGas,
		MaxFeePerGas:         op.MaxFeePerGas,
		Sponsored:            len(op.PaymasterAndData) > 0,
	}
}
