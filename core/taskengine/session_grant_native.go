package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"

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
			code, err := reader.CodeAt(context.Background(), intent.Recipient)
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
	if intent.Kind == NativeValue && policy.NativeSpendCap == nil {
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
			maxFee, feeErr := reader.MaxFeePerGas(context.Background())
			if feeErr != nil || maxFee == nil || maxFee.Sign() <= 0 {
				return fmt.Sprintf("%s: cannot price gas: %v", SessionPolicyNativeCapExceededCode, feeErr)
			}
			units := nativePreflightGasUnitsSteady
			if policy.Grant == nil || !policy.Grant.Applied() {
				units = nativePreflightGasUnitsFirstOp
			}
			gasWei = new(big.Int).Mul(big.NewInt(int64(units)), maxFee)
		}
		need.Add(need, gasWei)
	}
	if need.Cmp(capWei) > 0 {
		return FormatSessionPolicyNativeCapExceeded(need, capWei, policy.ID)
	}
	return ""
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

// ethCodeAndFee is the production CodeAndFeeReader over an ethclient.
type ethCodeAndFee struct {
	client *ethclient.Client
}

func (e ethCodeAndFee) CodeAt(ctx context.Context, addr common.Address) ([]byte, error) {
	if e.client == nil {
		return nil, fmt.Errorf("no ethclient")
	}
	return e.client.CodeAt(ctx, addr, nil)
}

func (e ethCodeAndFee) MaxFeePerGas(ctx context.Context) (*big.Int, error) {
	if e.client == nil {
		return nil, fmt.Errorf("no ethclient")
	}
	maxFee, _, err := eip1559.SuggestFee(e.client)
	if err != nil {
		return nil, err
	}
	return maxFee, nil
}

func codeAndFeeFromEthClient(client *ethclient.Client) CodeAndFeeReader {
	if client == nil {
		return nil
	}
	return ethCodeAndFee{client: client}
}

func sponsoredFromConfig(cfg *config.SmartWalletConfig) bool {
	return cfg != nil && cfg.SponsorshipPolicyID() != ""
}
