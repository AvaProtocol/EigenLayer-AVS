package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
)

// NativeIntentKind is what the inner call does with native ETH.
type NativeIntentKind int

const (
	// NativeSend is empty-calldata execute (ethTransfer / withdraw).
	NativeSend NativeIntentKind = iota
	// NativeValue is a payable contractWrite (selector present).
	NativeValue
)

// NativeIntent is one native-ETH check against a session grant.
type NativeIntent struct {
	Recipient    common.Address // empty-calldata destination; zero if payable write
	Amount       *big.Int
	Kind         NativeIntentKind
	Sponsored    bool
	EstimatedGas *big.Int // wei; nil means use A0 ceilings × MaxFeePerGas
}

// CodeAndFeeReader is injected in unit tests and backed by chain RPC in
// production. Fail closed if either call errors.
type CodeAndFeeReader interface {
	CodeAt(ctx context.Context, addr common.Address) ([]byte, error)
	MaxFeePerGas(ctx context.Context) (*big.Int, error)
}

const (
	SessionPolicyRecipientNotAllowedCode = "SESSION_POLICY_RECIPIENT_NOT_ALLOWED"
	SessionPolicyRecipientNotEOACode     = "SESSION_POLICY_RECIPIENT_NOT_EOA"
	SessionPolicyNativeCapExceededCode   = "SESSION_POLICY_NATIVE_CAP_EXCEEDED"

	// A0-measured gas-unit ceilings for NT cap preflight when the send path
	// has not yet estimated. Do not use the seed sum (500k+100k+700k).
	nativePreflightGasUnitsSteady  = 500_000   // installed grant ethTransfer
	nativePreflightGasUnitsFirstOp = 2_000_000 // deferred hooks, 5-row window
	// Payable contractWrite (NativeValue) can exceed ethTransfer gas. These
	// are conservative bounds, not estimates — under-seed is ExceededNativeTokenLimit.
	nativePreflightGasUnitsValueSteady  = 1_500_000
	nativePreflightGasUnitsValueFirstOp = 3_000_000
	nativePreflightRPCTimeout           = 15 * time.Second
)

// PreflightNativePermission returns a client-parseable error, or "" if the
// grant covers the intent. policy nil is "no usable grant".
func PreflightNativePermission(policy *model.SessionPolicy, intent NativeIntent, reader CodeAndFeeReader) string {
	if intent.Amount == nil {
		intent.Amount = big.NewInt(0)
	}

	if intent.Kind == NativeSend {
		if policy == nil {
			return FormatSessionPolicyNativeNotAllowed(intent.Recipient, "")
		}
		if len(policy.NativeRecipients) == 0 {
			return FormatSessionPolicyNativeNotAllowed(intent.Recipient, policy.ID)
		}
		if !nativeRecipientAllowed(policy, intent.Recipient) {
			return FormatSessionPolicyRecipientNotAllowed(intent.Recipient, policy.ID)
		}
		if !policy.AllowContractRecipient {
			if reader == nil {
				return fmt.Sprintf("%s: cannot verify native recipient %s is an EOA (no chain reader); fail closed",
					SessionPolicyRecipientNotEOACode, intent.Recipient.Hex())
			}
			ctx, cancel := context.WithTimeout(context.Background(), nativePreflightRPCTimeout)
			code, err := reader.CodeAt(ctx, intent.Recipient)
			cancel()
			if err != nil {
				return fmt.Sprintf("%s: looking up native recipient %s: %v",
					SessionPolicyRecipientNotEOACode, intent.Recipient.Hex(), err)
			}
			if len(code) != 0 {
				return FormatSessionPolicyRecipientNotEOA(intent.Recipient, policy.ID)
			}
		}
	}

	if policy == nil || policy.NativeSpendCap == nil {
		// Payable write with no NT module: trust the allowlisted selector.
		return ""
	}

	capWei, err := nativeGrantedCap(policy)
	if err != nil {
		return fmt.Sprintf("%s: %v", SessionPolicyNativeCapExceededCode, err)
	}
	need := new(big.Int).Set(intent.Amount)
	if !intent.Sponsored {
		gasWei := intent.EstimatedGas
		if gasWei == nil {
			if reader == nil {
				return fmt.Sprintf("%s: cannot price gas for a self-funded native cap check (no chain reader); fail closed",
					SessionPolicyNativeCapExceededCode)
			}
			ctx, cancel := context.WithTimeout(context.Background(), nativePreflightRPCTimeout)
			maxFee, feeErr := reader.MaxFeePerGas(ctx)
			cancel()
			if feeErr != nil || maxFee == nil || maxFee.Sign() <= 0 {
				return fmt.Sprintf("%s: cannot price gas: %v", SessionPolicyNativeCapExceededCode, feeErr)
			}
			units := nativePreflightGasUnitsFor(intent.Kind, policy)
			gasWei = new(big.Int).Mul(big.NewInt(int64(units)), maxFee)
		}
		need.Add(need, gasWei)
	}
	if need.Cmp(capWei) > 0 {
		return FormatSessionPolicyNativeCapExceeded(need, capWei, policy.ID)
	}
	return ""
}

func nativePreflightGasUnitsFor(kind NativeIntentKind, policy *model.SessionPolicy) int64 {
	firstOp := policy == nil || policy.Grant == nil || !policy.Grant.Applied()
	if kind == NativeValue {
		if firstOp {
			return nativePreflightGasUnitsValueFirstOp
		}
		return nativePreflightGasUnitsValueSteady
	}
	if firstOp {
		return nativePreflightGasUnitsFirstOp
	}
	return nativePreflightGasUnitsSteady
}

func nativeRecipientAllowed(policy *model.SessionPolicy, recipient common.Address) bool {
	want := strings.ToLower(recipient.Hex())
	for _, rec := range policy.NativeRecipients {
		if rec != nil && strings.ToLower(rec.Hex()) == want {
			return true
		}
	}
	return false
}

func nativeGrantedCap(policy *model.SessionPolicy) (*big.Int, error) {
	if policy == nil || policy.NativeSpendCap == nil {
		return nil, fmt.Errorf("no nativeSpendCap")
	}
	s := policy.NativeSpendCap.GrantedCap
	if s == "" {
		s = policy.NativeSpendCap.Amount
	}
	return parseCapAmount(s)
}

// FormatSessionPolicyNativeNotAllowed is "this grant has no native-send
// permission". After Track A the owner re-grants with nativeRecipients.
func FormatSessionPolicyNativeNotAllowed(recipient common.Address, policyID string) string {
	msg := SessionPolicyNativeNotAllowedCode +
		": this grant cannot send ETH to " + recipient.Hex() +
		" — re-grant with nativeRecipients (a nativeSpendCap alone is a payable-value cap, not an ETH send)"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}

func FormatSessionPolicyRecipientNotAllowed(recipient common.Address, policyID string) string {
	msg := SessionPolicyRecipientNotAllowedCode +
		": recipient " + recipient.Hex() +
		" is not in nativeRecipients — re-grant to add this address"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}

func FormatSessionPolicyRecipientNotEOA(recipient common.Address, policyID string) string {
	msg := SessionPolicyRecipientNotEOACode +
		": native recipient " + recipient.Hex() +
		" is a contract; send via contractWrite, or re-grant with allowContractRecipient"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}

func FormatSessionPolicyNativeCapExceeded(need, cap *big.Int, policyID string) string {
	msg := SessionPolicyNativeCapExceededCode +
		": this send exceeds the ETH cap (need " + need.String() + " wei, granted " + cap.String() +
		" wei) — re-grant with a higher cap, or send less / sponsor"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}

// codeAndFee is the production CodeAndFeeReader. CodeAt prefers the pooled
// ChainStateReader; MaxFeePerGas uses the signed-op formula (tip + 2*baseFee)
// off an ethclient, matching send_v07 / NativeTokenLimitModule.
type codeAndFee struct {
	reader ChainStateReader
	eth    *ethclient.Client
}

// NewCodeAndFeeReader builds a production reader. Either argument may be
// nil; both nil returns nil. MaxFeePerGas requires eth (the signed-op
// formula); CodeAt uses reader if set, else eth.
func NewCodeAndFeeReader(reader ChainStateReader, eth *ethclient.Client) CodeAndFeeReader {
	if reader == nil && eth == nil {
		return nil
	}
	return codeAndFee{reader: reader, eth: eth}
}

func (c codeAndFee) CodeAt(ctx context.Context, addr common.Address) ([]byte, error) {
	if c.reader != nil {
		return c.reader.CodeAt(ctx, addr)
	}
	if c.eth == nil {
		return nil, fmt.Errorf("no chain reader")
	}
	return c.eth.CodeAt(ctx, addr, nil)
}

func (c codeAndFee) MaxFeePerGas(ctx context.Context) (*big.Int, error) {
	if c.eth == nil {
		return nil, fmt.Errorf("no ethclient for signed-op maxFeePerGas")
	}
	return eip1559.SignedOpMaxFeePerGas(ctx, c.eth)
}

func codeAndFeeFromEthClient(client *ethclient.Client) CodeAndFeeReader {
	return NewCodeAndFeeReader(nil, client)
}

func sponsoredFromConfig(cfg *config.SmartWalletConfig) bool {
	return cfg != nil && cfg.SponsorshipPolicyID() != ""
}
