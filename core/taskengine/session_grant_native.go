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
	SessionPolicyRecipientNotAllowedCode      = "SESSION_POLICY_RECIPIENT_NOT_ALLOWED"
	SessionPolicyRecipientNotEOACode          = "SESSION_POLICY_RECIPIENT_NOT_EOA"
	SessionPolicyRecipientDelegateChangedCode = "SESSION_POLICY_RECIPIENT_DELEGATE_CHANGED"
	// SessionPolicyRecipientCodeUnreadableCode is an installed grant whose
	// recipient code could not be read. It is not a client failure: the
	// recipient may be fine, and the owner should not be told to change it.
	// Before the grant is applied, the same failure stays
	// SESSION_POLICY_RECIPIENT_NOT_EOA.
	SessionPolicyRecipientCodeUnreadableCode = "SESSION_POLICY_RECIPIENT_CODE_UNREADABLE"
	SessionPolicyTooManyNativeRecipientsCode = "SESSION_POLICY_TOO_MANY_NATIVE_RECIPIENTS"
	SessionPolicyNativeCapExceededCode       = "SESSION_POLICY_NATIVE_CAP_EXCEEDED"

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
		// allowContractRecipient still skips the code check, before and after
		// the grant is applied. Otherwise:
		//
		//   - Before apply, a recipient with code is allowed only when that
		//     code is an EIP-7702 designation and the owner acknowledged that
		//     exact delegate. A plain contract stays refused, even if listed.
		//     Empty code is allowed; an acknowledgement is not required.
		//   - After apply, unacknowledged recipients are not re-read. A later
		//     delegation must not invalidate an installed grant. Acknowledged
		//     recipients are re-read. Empty code or the same delegate still
		//     sends. Any other code is SESSION_POLICY_RECIPIENT_DELEGATE_CHANGED.
		//
		// A missing reader or a CodeAt error fails closed. Before apply that
		// is NOT_EOA, because prepare is in front of the owner. After apply
		// it is SESSION_POLICY_RECIPIENT_CODE_UNREADABLE. That code is not a
		// client failure: an RPC blip must not be logged as a warning, and
		// it must not tell the owner to change a recipient that is fine.
		//
		// The on-chain native row stays HasSelectorAllowlist=false.
		// AllowlistModule has no empty-calldata flag, and empty calldata has
		// no 4-byte selector, so the row cannot be scoped on chain. The
		// gateway keeps the send empty: an ETH transfer packs
		// PackExecuteMAv2 with empty calldata, and Validate refuses a native
		// recipient that is also an allowedActions target. contractWrite
		// preflight only allows allowedActions targets.
		if mode, ack := nativeRecipientCodeMode(policy, intent.Recipient); mode != nativeCodeSkip {
			if msg := preflightNativeRecipientCode(policy, intent.Recipient, ack, mode, reader); msg != "" {
				return msg
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
	return withPolicyID(msg, policyID)
}

// nativeCodeVerdict is how bytecode at a native recipient compares with the
// delegate the owner acknowledged. Empty code is nativeCodeOK.
type nativeCodeVerdict int

const (
	nativeCodeOK nativeCodeVerdict = iota
	nativeCodePlainContract
	nativeCodeDelegatedUnacked
	nativeCodeDelegateChanged
)

type nativeCodeMode int

const (
	nativeCodeSkip nativeCodeMode = iota
	nativeCodeBeforeApply
	nativeCodeRecheckApplied
)

// classifyNativeRecipientCode decides whether code may receive native ETH.
// ack is the acknowledged delegate, or nil when the owner did not acknowledge
// one. Empty code is allowed. A 7702 designation is allowed only when ack
// equals that delegate. Any other non-empty code is a plain contract, even
// when the recipient is listed in the acknowledgements.
func classifyNativeRecipientCode(code []byte, ack *common.Address) (nativeCodeVerdict, common.Address) {
	if len(code) == 0 {
		return nativeCodeOK, common.Address{}
	}
	delegate, ok := config.EIP7702DesignationDelegate(code)
	if !ok {
		return nativeCodePlainContract, common.Address{}
	}
	if ack == nil || *ack == (common.Address{}) {
		return nativeCodeDelegatedUnacked, delegate
	}
	if *ack != delegate {
		return nativeCodeDelegateChanged, delegate
	}
	return nativeCodeOK, delegate
}

func acknowledgedDelegate(acks []model.DelegatedNativeRecipient, recipient common.Address) *common.Address {
	for _, ack := range acks {
		if ack.Recipient == nil || *ack.Recipient != recipient || ack.Delegate == nil {
			continue
		}
		delegate := *ack.Delegate
		return &delegate
	}
	return nil
}

func nativeRecipientCodeMode(policy *model.SessionPolicy, recipient common.Address) (nativeCodeMode, *common.Address) {
	if policy == nil || policy.AllowContractRecipient {
		return nativeCodeSkip, nil
	}
	ack := acknowledgedDelegate(policy.DelegatedNativeRecipients, recipient)
	if policy.Grant.Applied() {
		if ack == nil {
			return nativeCodeSkip, nil
		}
		return nativeCodeRecheckApplied, ack
	}
	return nativeCodeBeforeApply, ack
}

func preflightNativeRecipientCode(policy *model.SessionPolicy, recipient common.Address, ack *common.Address, mode nativeCodeMode, reader CodeAndFeeReader) string {
	if reader == nil {
		if mode == nativeCodeRecheckApplied {
			return recipientCodeUnreadable(recipient, policy, "no chain reader")
		}
		return fmt.Sprintf("%s: cannot verify native recipient %s is an EOA (no chain reader); fail closed",
			SessionPolicyRecipientNotEOACode, recipient.Hex())
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativePreflightRPCTimeout)
	code, err := reader.CodeAt(ctx, recipient)
	cancel()
	if err != nil {
		if mode == nativeCodeRecheckApplied {
			return recipientCodeUnreadable(recipient, policy, err.Error())
		}
		return fmt.Sprintf("%s: looking up native recipient %s: %v",
			SessionPolicyRecipientNotEOACode, recipient.Hex(), err)
	}
	verdict, onChain := classifyNativeRecipientCode(code, ack)
	if mode == nativeCodeRecheckApplied {
		acknowledged := common.Address{}
		if ack != nil {
			acknowledged = *ack
		}
		return formatAppliedDelegateRefusal(recipient, acknowledged, onChain, verdict, policyIDOf(policy))
	}
	return formatPreflightNativeCodeRefusal(recipient, ack, onChain, verdict, policyIDOf(policy))
}

func recipientCodeUnreadable(recipient common.Address, policy *model.SessionPolicy, detail string) string {
	return withPolicyID(fmt.Sprintf("%s: cannot read code at native recipient %s: %s",
		SessionPolicyRecipientCodeUnreadableCode, recipient.Hex(), detail), policyIDOf(policy))
}

func policyIDOf(policy *model.SessionPolicy) string {
	if policy == nil {
		return ""
	}
	return policy.ID
}

// formatGrantTimeNativeCodeRefusal is the prepare/submit wording. A plain
// contract keeps the contractWrite hint. A designation to any delegate is
// "7702-delegated", including our own SMA delegate.
func formatGrantTimeNativeCodeRefusal(recipient common.Address, ack *common.Address, onChain common.Address, verdict nativeCodeVerdict) error {
	switch verdict {
	case nativeCodeOK:
		return nil
	case nativeCodeDelegatedUnacked:
		return fmt.Errorf("%s: native recipient %s is 7702-delegated to %s; acknowledge that delegate in delegatedNativeRecipients, or set allowContractRecipient (any function on this address, ERC-20 uncapped)",
			SessionPolicyRecipientNotEOACode, recipient.Hex(), onChain.Hex())
	case nativeCodeDelegateChanged:
		acknowledged := common.Address{}
		if ack != nil {
			acknowledged = *ack
		}
		return fmt.Errorf("%s: native recipient %s was acknowledged as delegated to %s; it now designates %s",
			SessionPolicyRecipientDelegateChangedCode, recipient.Hex(), acknowledged.Hex(), onChain.Hex())
	default:
		return fmt.Errorf("%s: native recipient is a contract; send via contractWrite, or set allowContractRecipient (any function on this address, ERC-20 uncapped)",
			SessionPolicyRecipientNotEOACode)
	}
}

func formatPreflightNativeCodeRefusal(recipient common.Address, ack *common.Address, onChain common.Address, verdict nativeCodeVerdict, policyID string) string {
	switch verdict {
	case nativeCodeOK:
		return ""
	case nativeCodeDelegatedUnacked:
		return withPolicyID(fmt.Sprintf("%s: native recipient %s is 7702-delegated to %s; acknowledge that delegate in delegatedNativeRecipients, or re-grant with allowContractRecipient (any function on this address, ERC-20 uncapped)",
			SessionPolicyRecipientNotEOACode, recipient.Hex(), onChain.Hex()), policyID)
	case nativeCodeDelegateChanged:
		acknowledged := common.Address{}
		if ack != nil {
			acknowledged = *ack
		}
		return withPolicyID(fmt.Sprintf("%s: native recipient %s was acknowledged as delegated to %s; it now designates %s",
			SessionPolicyRecipientDelegateChangedCode, recipient.Hex(), acknowledged.Hex(), onChain.Hex()), policyID)
	default:
		return FormatSessionPolicyRecipientNotEOA(recipient, policyID)
	}
}

func formatAppliedDelegateRefusal(recipient, acknowledged, onChain common.Address, verdict nativeCodeVerdict, policyID string) string {
	switch verdict {
	case nativeCodeOK:
		return ""
	case nativeCodeDelegateChanged:
		return withPolicyID(fmt.Sprintf("%s: native recipient %s was acknowledged as delegated to %s; it now designates %s",
			SessionPolicyRecipientDelegateChangedCode, recipient.Hex(), acknowledged.Hex(), onChain.Hex()), policyID)
	default:
		return withPolicyID(fmt.Sprintf("%s: native recipient %s was acknowledged as delegated to %s; its code is no longer that designation",
			SessionPolicyRecipientDelegateChangedCode, recipient.Hex(), acknowledged.Hex()), policyID)
	}
}

func withPolicyID(msg, policyID string) string {
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
