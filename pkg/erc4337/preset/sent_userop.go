package preset

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

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

// SendUserOpAuto sends an operation and reports the result in version-neutral
// terms.
//
// It is the single send entry point. The v0.6 path it used to dispatch to is
// gone: every chain runs Modular Account v2 on EntryPoint v0.7, and a config
// asking for anything else is refused at load (see SmartWalletConfig
// validation) rather than routed to an EntryPoint nothing deploys against.
//
// Sponsorship comes from the chain's Alchemy Gas Manager policy. The
// paymasterReq and executionFeeWei arguments this used to take applied only to
// the v0.6 verifying paymaster and were already ignored here, so they are gone
// rather than left as parameters that quietly mean nothing.
func SendUserOpAuto(
	smartWalletConfig *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	senderOverride *common.Address,
	saltOverride *big.Int,
	lgr logger.Logger,
) (*SentUserOp, *types.Receipt, error) {
	if smartWalletConfig == nil {
		return nil, nil, fmt.Errorf("nil smart wallet config")
	}
	if !smartWalletConfig.UsesModularAccountV2() {
		return nil, nil, fmt.Errorf(
			"chain %d is configured for account provider %q; only %s can execute — "+
				"the v0.6 send path was removed with the EntryPoint v0.7 cutover",
			smartWalletConfig.ChainID, smartWalletConfig.AccountProviderName(),
			config.AccountProviderModularAccountV2)
	}

	// nil auth: SendUserOpMAv2 resolves the session grant itself, AFTER it has
	// resolved the actual sender. Grants are stored per smart-wallet address,
	// and callers do not always pass an override — resolving here against the
	// owner EOA used to find nothing and sign as the fallback entity the
	// gateway cannot validate for.
	op, receipt, err := SendUserOpMAv2(smartWalletConfig, owner, callData, senderOverride, saltOverride, nil, lgr)
	sent, convErr := sentFromV07(op, big.NewInt(smartWalletConfig.ChainID))
	if err != nil {
		// Report the send failure, not the conversion: the send is why this
		// failed, and `sent` is best-effort context for the caller.
		return sent, receipt, err
	}
	if convErr != nil {
		return nil, receipt, convErr
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
