package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"sort"
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
	// SessionPolicyRecipientNotEOACode is a native recipient that is a
	// contract, or whose code could not be checked before the grant was
	// applied.
	SessionPolicyRecipientNotEOACode = "SESSION_POLICY_RECIPIENT_NOT_EOA"
	// SessionPolicyRecipientNotAcknowledgedCode is a native recipient whose
	// code is an EIP-7702 designation the grant does not acknowledge in
	// delegatedNativeRecipients. It was NOT_EOA until clients needed to tell
	// it from a contract without reading the message.
	SessionPolicyRecipientNotAcknowledgedCode = "SESSION_POLICY_RECIPIENT_NOT_ACKNOWLEDGED"
	SessionPolicyRecipientDelegateChangedCode = "SESSION_POLICY_RECIPIENT_DELEGATE_CHANGED"
	// SessionPolicyRecipientCodeUnreadableCode is an installed grant whose
	// recipient code could not be read. It is not a client failure: the
	// recipient may be fine, and the owner should not be told to change it.
	// Before the grant is applied, the same failure stays
	// SESSION_POLICY_RECIPIENT_NOT_EOA.
	SessionPolicyRecipientCodeUnreadableCode = "SESSION_POLICY_RECIPIENT_CODE_UNREADABLE"
	SessionPolicyTooManyNativeRecipientsCode = "SESSION_POLICY_TOO_MANY_NATIVE_RECIPIENTS"
	SessionPolicyNativeCapExceededCode       = "SESSION_POLICY_NATIVE_CAP_EXCEEDED"

	// Gas units a self-funded operation signs, which NativeTokenLimitModule
	// charges against the native cap at maxFee: verification + call +
	// pre-verification gas. Since #828 the bundler prices verification and
	// pre-verification, so these are measured, not seeds. A send on an
	// installed grant signed 767k on Sepolia (500k call, 225k verification,
	// 42k pre-verification) whatever the grant installs
	// (scripts/spike/native_recipient_cap). The first operation is sized
	// from the grant: grantGasShape.firstOpUnits.
	nativePreflightGasUnitsSteady = 900_000
	// Payable contractWrite (NativeValue) can exceed ethTransfer gas. Not
	// re-measured since A0: a conservative bound, not an estimate.
	// Under-seed is ExceededNativeTokenLimit.
	nativePreflightGasUnitsValueSteady = 1_500_000
	nativePreflightRPCTimeout          = 15 * time.Second
)

// The first operation of a grant carries its install. A re-grant also tears
// down the grants PrepareSessionGrant packs into the same batch (up to
// maxOnChainTeardowns); a first grant deploys the account instead. Signed
// units measured on Sepolia (scripts/spike/native_recipient_cap):
//
//   - install on a deployed wallet: 2.30M, plus 136k per native row and 627k
//     per allowed-action target (20 rows 5.03M);
//   - deploying the account: 382k (20 rows with deploy 5.41M);
//   - tearing down a prior grant: 208k, plus 28.6k per row and 73.6k per
//     target (a 20-row replace 5.81M; four 20-row teardowns 8.58M; a 1-row
//     grant replacing 20 rows with 8 targets 3.81M).
//
// The margin covers the bundler's spread, including the extra calldata of a
// batch with several teardowns. Alchemy priced verification gas at exactly
// 10M on a 20-row, 8-target grant replacing one of the same shape (about
// 11.4M by this model); it mined, but grants that large sit at the
// bundler's ceiling.
const (
	firstOpInstallUnitsBase       = 2_301_000
	firstOpInstallUnitsPerRow     = 136_300
	firstOpInstallUnitsPerTarget  = 627_400
	firstOpDeployUnits            = 382_200
	firstOpTeardownUnitsBase      = 208_500
	firstOpTeardownUnitsPerRow    = 28_600
	firstOpTeardownUnitsPerTarget = 73_600
	firstOpGasMarginPercent       = 120
)

// grantGasShape is what a grant's first operation installs: one allowlist
// row per native recipient and per allowed-action target, and the prior
// grants the same operation tears down.
type grantGasShape struct {
	nativeRows int
	targets    int
	teardowns  []grantGasShape
}

func shapeOfPermissions(p SessionPermissions) grantGasShape {
	return grantGasShape{nativeRows: len(p.NativeRecipients), targets: len(p.AllowedActions)}
}

func shapeOfPolicy(policy *model.SessionPolicy) grantGasShape {
	if policy == nil {
		return grantGasShape{}
	}
	return grantGasShape{nativeRows: len(policy.NativeRecipients), targets: len(policy.AllowedActions)}
}

// teardownShapes prices the prior grants an install tears down. Storage says
// which applied grants are candidates. The chain may have cleared some, and
// PrepareSessionGrant packs at most maxOnChainTeardowns of the rest, so the
// most expensive candidates up to that cap bound any batch it can build.
func teardownShapes(priors []*model.SessionPolicy) []grantGasShape {
	shapes := make([]grantGasShape, 0, len(priors))
	for _, prior := range priors {
		if prior != nil {
			shapes = append(shapes, shapeOfPolicy(prior))
		}
	}
	sort.SliceStable(shapes, func(i, j int) bool { return shapes[i].teardownUnits() > shapes[j].teardownUnits() })
	if len(shapes) > maxOnChainTeardowns {
		shapes = shapes[:maxOnChainTeardowns]
	}
	return shapes
}

// shapeOfNeed is one automation's own install, for a runner with no grant.
func shapeOfNeed(need *WorkflowNeed) grantGasShape {
	if need == nil {
		return grantGasShape{}
	}
	return grantGasShape{nativeRows: len(need.NativeRecipients), targets: len(need.Actions)}
}

// teardownUnits is what uninstalling this grant adds to another's first op.
func (s grantGasShape) teardownUnits() int64 {
	return firstOpTeardownUnitsBase +
		int64(s.nativeRows)*firstOpTeardownUnitsPerRow +
		int64(s.targets)*firstOpTeardownUnitsPerTarget
}

// firstOpUnits is the gas units the grant's first operation signs: its
// install, and either its teardowns or, for a first grant, the deploy. A
// payable first operation adds what a payable steady operation adds.
func (s grantGasShape) firstOpUnits(kind NativeIntentKind) int64 {
	units := int64(firstOpInstallUnitsBase) +
		int64(s.nativeRows)*firstOpInstallUnitsPerRow +
		int64(s.targets)*firstOpInstallUnitsPerTarget
	if len(s.teardowns) == 0 {
		units += firstOpDeployUnits
	}
	for _, prior := range s.teardowns {
		units += prior.teardownUnits()
	}
	units = units * firstOpGasMarginPercent / 100
	if kind == NativeValue {
		units += nativePreflightGasUnitsValueSteady - nativePreflightGasUnitsSteady
	}
	return units
}

// installPremiumUnits is how much more the first operation signs than a
// steady one. It is the same for a send and a payable call.
func (s grantGasShape) installPremiumUnits() int64 {
	return s.firstOpUnits(NativeSend) - nativePreflightGasUnitsSteady
}

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
	if policy == nil || policy.Grant == nil || !policy.Grant.Applied() {
		return shapeOfPolicy(policy).firstOpUnits(kind)
	}
	if kind == NativeValue {
		return nativePreflightGasUnitsValueSteady
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

// NativeRecipientRefusal is prepare or submit refusing a native recipient
// for its code. Error() is the refusal's text. REST copies the code and the
// addresses onto the problem, so a client can name the recipient without
// reading the text. The recipient can be another enabled task's: the merged
// grant carries every running automation's recipients.
type NativeRecipientRefusal struct {
	// Code is SESSION_POLICY_RECIPIENT_NOT_EOA,
	// SESSION_POLICY_RECIPIENT_NOT_ACKNOWLEDGED, or
	// SESSION_POLICY_RECIPIENT_DELEGATE_CHANGED.
	Code      string
	Recipient common.Address
	// Delegate is the delegate the recipient's designation names now. Nil
	// for a plain contract.
	Delegate *common.Address
	// AcknowledgedDelegate is the delegate the grant acknowledged. Set only
	// for SESSION_POLICY_RECIPIENT_DELEGATE_CHANGED.
	AcknowledgedDelegate *common.Address
	message              string
}

func (e *NativeRecipientRefusal) Error() string { return e.message }

// formatGrantTimeNativeCodeRefusal is the prepare/submit wording. A plain
// contract keeps the contractWrite hint. A designation to any delegate is
// "7702-delegated", including our own SMA delegate.
func formatGrantTimeNativeCodeRefusal(recipient common.Address, ack *common.Address, onChain common.Address, verdict nativeCodeVerdict) error {
	switch verdict {
	case nativeCodeOK:
		return nil
	case nativeCodeDelegatedUnacked:
		delegate := onChain
		return &NativeRecipientRefusal{
			Code:      SessionPolicyRecipientNotAcknowledgedCode,
			Recipient: recipient,
			Delegate:  &delegate,
			message: fmt.Sprintf("%s: native recipient %s is 7702-delegated to %s; acknowledge that delegate in delegatedNativeRecipients, or set allowContractRecipient (any function on this address, ERC-20 uncapped)",
				SessionPolicyRecipientNotAcknowledgedCode, recipient.Hex(), onChain.Hex()),
		}
	case nativeCodeDelegateChanged:
		acknowledged := common.Address{}
		if ack != nil {
			acknowledged = *ack
		}
		delegate := onChain
		return &NativeRecipientRefusal{
			Code:                 SessionPolicyRecipientDelegateChangedCode,
			Recipient:            recipient,
			Delegate:             &delegate,
			AcknowledgedDelegate: &acknowledged,
			message: fmt.Sprintf("%s: native recipient %s was acknowledged as delegated to %s; it now designates %s",
				SessionPolicyRecipientDelegateChangedCode, recipient.Hex(), acknowledged.Hex(), onChain.Hex()),
		}
	default:
		return &NativeRecipientRefusal{
			Code:      SessionPolicyRecipientNotEOACode,
			Recipient: recipient,
			message: fmt.Sprintf("%s: native recipient %s is a contract; send via contractWrite, or set allowContractRecipient (any function on this address, ERC-20 uncapped)",
				SessionPolicyRecipientNotEOACode, recipient.Hex()),
		}
	}
}

func formatPreflightNativeCodeRefusal(recipient common.Address, ack *common.Address, onChain common.Address, verdict nativeCodeVerdict, policyID string) string {
	switch verdict {
	case nativeCodeOK:
		return ""
	case nativeCodeDelegatedUnacked:
		return withPolicyID(fmt.Sprintf("%s: native recipient %s is 7702-delegated to %s; acknowledge that delegate in delegatedNativeRecipients, or re-grant with allowContractRecipient (any function on this address, ERC-20 uncapped)",
			SessionPolicyRecipientNotAcknowledgedCode, recipient.Hex(), onChain.Hex()), policyID)
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
