package taskengine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Skill-page session grants (EigenLayer-AVS #812, carry-forward #815).
//
// One usable grant per runner. When that grant exists, a replacement keeps
// its actions and sizes each cap as max(remaining, what enabled automations
// still need) plus the addition. Remaining is the stored cap until the
// grant's first run, and the on-chain limit after that. The wallet expiry
// is the latest of the current grant, the addition, and enabled automations,
// so a known earlier end does not shorten it. With no current grant, caps
// are the addition plus enabled tasks. An unresolved fund move fails the
// merge. An unsized amount fails unless every named transfer or approve
// already has a positive cap. Simulate's permission verdict is opt-in
// (authorizationMode=report); the default still fails the step.

const (
	SessionPolicyBaseChangedCode = "SESSION_POLICY_BASE_CHANGED"
	SessionPolicyNotCoveringCode = "SESSION_POLICY_NOT_COVERING"

	AuthCovered          = "covered"
	AuthNoGrant          = "no_grant"
	AuthNotCovered       = "not_covered"
	AuthCapTooLow        = "cap_too_low"
	AuthExpiresTooSoon   = "expires_too_soon"
	AuthCapNeedsInput    = "cap_needs_input"
	AuthTargetUnresolved = "target_unresolved"

	// ceilingSharedCapDetail is the covered verdict for a ceiling. The cap
	// is wallet-wide, so a positive amount sized for another automation
	// also satisfies this one.
	ceilingSharedCapDetail = "this ceiling shares the token cap with every automation on the wallet"

	// SessionPolicyTargetUnresolvedCode is a 409. The automation moves
	// funds, and the gateway cannot name the target from the stored
	// workflow. It is not an unsized amount: typing a cap does not fix it.
	SessionPolicyTargetUnresolvedCode = "SESSION_POLICY_TARGET_UNRESOLVED"

	// SessionPolicyNativeUnsizedCode is a 409. The addition would install
	// a native cap, and a running automation's payable value cannot be
	// sized. Pausing that automation, or naming it in dropTaskIds, is the
	// way through. It is not POLICIES_REJECTED: that code is also the
	// catch-all for an unclassified rejection.
	SessionPolicyNativeUnsizedCode = "SESSION_POLICY_NATIVE_UNSIZED"

	selectorTransfer = "0xa9059cbb"
	selectorApprove  = "0x095ea7b3"

	// maxCronWalk bounds a schedule count. Hitting it means the run count
	// is unknown; callers must not invent a horizon. The walk stays
	// iterative because a short sample of cron.Next cannot prove a
	// constant step: a weekday schedule looks regular until the weekend.
	// Prepare and submit run it under the runner lock, so the cap is
	// also the latency bound. A hit fails closed (unsized) rather than
	// under-counting the spend.
	maxCronWalk = 100_000
)

var (
	ErrSessionPolicyBaseChanged = errors.New("the runner's usable grant changed since this request was prepared")
	ErrSessionPolicyNotCovering = errors.New("this grant would leave an enabled automation uncovered")
	// ErrSessionPolicyUnsized is a prepare-merge failure on a wallet with no
	// usable grant: an enabled task still moves a token whose remaining
	// total cannot be computed. A carried grant does not use this error for
	// that task; it keeps the token only when the remainder, a sized need,
	// or the addition already gives it a positive cap.
	ErrSessionPolicyUnsized = errors.New("cannot size the remaining spend for an enabled automation")
	// ErrSessionNativeCapUnsized is a 409 when the addition would install
	// NativeTokenLimitModule and a running automation's payable value cannot
	// be sized. It is not ErrSessionPolicyUnsized: that sentinel is the
	// "set a spend cap" failure, and this one must not be shown as that.
	ErrSessionNativeCapUnsized = errors.New("a native cap cannot be added while a running automation has a payable value that cannot be sized")

	// settingRef matches a whole-string settings reference, including a dotted
	// path such as {{settings.token_amount.address}}. A single segment stays
	// {{settings.recipients}}.
	settingRef = regexp.MustCompile(`^(?:\{\{settings\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)\}\}|\$\{settings\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)\})$`)
	// valueRef matches a whole-string loop element, {{value}} or {{value.a.b}}.
	// It binds only while the loop input is a settings list. A loop over a
	// node output stays unresolved, so coverage cannot invent a target.
	// The compiled split and batch templates are the exception: they are
	// recognized from the stored source, not by binding {{value}}.
	valueRef = regexp.MustCompile(`^\{\{value(?:\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*))?\}\}$`)
)

// PolicyConflictError is a 409 the REST layer copies onto the problem body.
type PolicyConflictError struct {
	Sentinel        error
	Code            string
	Detail          string
	PolicyID        string
	AffectedTaskIDs []string
	Missing         []model.AllowedAction
	Required        *WorkflowNeed
}

func (e *PolicyConflictError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func (e *PolicyConflictError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Sentinel
}

// SkillSchedule is the run window used to size a workflow. Zero
// MaxExecution with no cron end means the run count is unknown.
type SkillSchedule struct {
	MaxExecution   int64
	StartAt        int64
	ExpiredAt      int64
	ExecutionCount int64
	Now            time.Time
	// Crons sizes observed simulate spend when the request did not pass
	// maxExecution. Derive still reads the trigger itself.
	Crons []string
}

// WorkflowNeed is what one workflow still needs on one chain.
type WorkflowNeed struct {
	ChainID          int64
	TaskID           string
	Name             string
	Actions          []model.AllowedAction
	Caps             []model.ERC20SpendCap
	NativeRecipients []*common.Address
	NativeSpendCap   *model.NativeSpendCap
	ValidUntilMs     int64
	HasFundMove      bool
	CapNeedsInput    bool
	// CapCeiling is a named transfer or approve whose amount is not a fixed
	// number. One observed run must not become the cap. A positive cap on
	// the grant is the ceiling the owner signs: it is not a derived total
	// and it is not multiplied by the runs still left. Unknown runs set
	// CapNeedsInput without CapCeiling.
	CapCeiling bool
	// Unresolved is a fund move whose target or recipient could not be
	// read. A merge must not drop that automation, and a grant must not
	// be treated as covering it.
	Unresolved bool
	// NativeUnsized is set when a native amount could not be computed.
	// An observed simulate total can clear it; a merge cannot.
	NativeUnsized bool
}

// PolicyAddition is the automation being set up. Cap amounts are totals
// for that automation; the gateway does not multiply them again.
type PolicyAddition struct {
	AllowedActions         []model.AllowedAction
	SpendCaps              []model.ERC20SpendCap
	NativeRecipients       []*common.Address
	NativeSpendCap         *model.NativeSpendCap
	AllowContractRecipient bool
	// ValidUntilMs is this automation's horizon. Zero means the caller
	// supplies expiresIn instead.
	ValidUntilMs int64
}

// CapChange is one token total on the merged grant.
type CapChange struct {
	Token          common.Address
	Amount         string
	PreviousAmount string // empty when the token is new
	Removed        bool
}

// ExpiryChange is one enabled task whose wallet expiry moved.
type ExpiryChange struct {
	TaskID             string
	Name               string
	ValidUntilMs       int64
	PreviousValidUntil int64
}

// PolicyChanges is the approval-screen diff. Summary is the copy.
type PolicyChanges struct {
	Summary        []string
	BasePolicyID   string
	KeptActions    []model.AllowedAction
	AddedActions   []model.AllowedAction
	RemovedActions []model.AllowedAction
	CapChanges     []CapChange
	ExpiryChanges  []ExpiryChange
}

// SessionAuthorization is the opt-in simulate verdict.
type SessionAuthorization struct {
	Status   string
	PolicyID string
	Missing  []model.AllowedAction
	Required *WorkflowNeed
	Detail   string
}

// SimulateAuth travels on the simulate context. Report false leaves the
// step-failure behavior unchanged and leaves Result nil.
type SimulateAuth struct {
	Report       bool
	MaxExecution int64
	StartAt      int64
	ExpiredAt    int64
	Result       *SessionAuthorization
}

type simulateAuthKey struct{}

// WithSimulateAuth attaches an opt-in simulate verdict request.
func WithSimulateAuth(ctx context.Context, auth *SimulateAuth) context.Context {
	if auth == nil {
		return ctx
	}
	return context.WithValue(ctx, simulateAuthKey{}, auth)
}

// SimulateAuthFrom returns the verdict request on ctx, or nil.
func SimulateAuthFrom(ctx context.Context) *SimulateAuth {
	if ctx == nil {
		return nil
	}
	auth, _ := ctx.Value(simulateAuthKey{}).(*SimulateAuth)
	return auth
}

// SessionGrantReport records grant misses while a simulate VM is in report
// mode. Production sends leave it nil, so they stay fail-closed.
// Token totals decoded from calldata here are the advisory verdict only.
// fillObserved copies an observed transfer or approve onto that verdict
// so a cap is not returned without the call that spent it.
// Studio sends that required object back as the grant addition, so when
// the static walk cannot size a new Skill these observed totals are what
// the owner signs. They have to fit in uint256, the same as a static cap.
// MergeSkillGrant does not read this report; it sizes enabled tasks from
// the stored workflow.
type SessionGrantReport struct {
	mu               sync.Mutex
	Policy           *model.SessionPolicy
	NoGrant          bool
	SawWrite         bool
	Planned          []PlannedCall
	Missing          []PlannedCall
	NativeMiss       bool
	NativeMissDetail string
	TokenSpend       map[common.Address]*big.Int
	NativeSpend      *big.Int
	NativeRecipients []common.Address
}

func (r *SessionGrantReport) observeCalls(planned []PlannedCall) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SawWrite = true
	r.Planned = append(r.Planned, planned...)
	for _, c := range planned {
		if amt, ok := decodeTransferAmount(c.Calldata); ok {
			if r.TokenSpend == nil {
				r.TokenSpend = map[common.Address]*big.Int{}
			}
			if r.TokenSpend[c.Target] == nil {
				r.TokenSpend[c.Target] = big.NewInt(0)
			}
			r.TokenSpend[c.Target].Add(r.TokenSpend[c.Target], amt)
		}
		if c.Value != nil && c.Value.Sign() > 0 {
			if r.NativeSpend == nil {
				r.NativeSpend = big.NewInt(0)
			}
			r.NativeSpend.Add(r.NativeSpend, c.Value)
		}
	}
}

func (r *SessionGrantReport) observeNative(dest common.Address, amount *big.Int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SawWrite = true
	if dest != (common.Address{}) {
		r.NativeRecipients = append(r.NativeRecipients, dest)
	}
	if amount != nil && amount.Sign() > 0 {
		if r.NativeSpend == nil {
			r.NativeSpend = big.NewInt(0)
		}
		r.NativeSpend.Add(r.NativeSpend, new(big.Int).Set(amount))
	}
}

func (r *SessionGrantReport) notePolicy(p *model.SessionPolicy) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.Policy = p
	r.mu.Unlock()
}

func (r *SessionGrantReport) noteNoGrant() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.NoGrant = true
	r.mu.Unlock()
}

func (r *SessionGrantReport) noteGrantMiss(msg string, planned []PlannedCall) {
	if r == nil || msg == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if isNativeMessage(msg) {
		r.NativeMiss = true
		r.NativeMissDetail = msg
		return
	}
	if r.Policy != nil && len(r.Policy.AllowedActions) > 0 {
		r.Missing = append(r.Missing, MissingGrantCalls(r.Policy.AllowedActions, planned)...)
		return
	}
	r.Missing = append(r.Missing, planned...)
}

func (r *SessionGrantReport) snapshot() (policy *model.SessionPolicy, noGrant, saw bool, missing []PlannedCall, nativeMiss bool, nativeDetail string, token map[common.Address]*big.Int, nativeSpend *big.Int, planned []PlannedCall) {
	if r == nil {
		return nil, false, false, nil, false, "", nil, nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Policy, r.NoGrant, r.SawWrite, append([]PlannedCall(nil), r.Missing...), r.NativeMiss, r.NativeMissDetail, r.TokenSpend, r.NativeSpend, append([]PlannedCall(nil), r.Planned...)
}

func isNativeMessage(msg string) bool {
	return strings.Contains(msg, SessionPolicyNativeNotAllowedCode) ||
		strings.Contains(msg, SessionPolicyRecipientNotAllowedCode) ||
		strings.Contains(msg, SessionPolicyRecipientNotEOACode) ||
		strings.Contains(msg, SessionPolicyNativeCapExceededCode)
}

func isNativeCapMessage(msg string) bool {
	return strings.Contains(msg, SessionPolicyNativeCapExceededCode)
}

// DeriveWorkflowNeeds walks contractWrite and ethTransfer nodes, including
// loop runners. A loop over the compiled split or on-demand batch template
// is sized from that shape. settings overrides the task's inputVariables;
// nil reads them from the task. Chain 0 buckets onto fallbackChain. A task
// with no runs left returns an empty map.
func DeriveWorkflowNeeds(task *avsproto.Task, settings map[string]any, sched SkillSchedule, fallbackChain int64) map[int64]*WorkflowNeed {
	if task == nil {
		return nil
	}
	if settings == nil {
		settings = settingsFromTask(task)
	}
	if sched.Now.IsZero() {
		sched.Now = time.Now()
	}
	runs, runsKnown := remainingRuns(sched, cronSchedules(task))
	if runsKnown && runs == 0 {
		return nil
	}
	needs := map[int64]*WorkflowNeed{}
	ensure := func(chain int64) *WorkflowNeed {
		if needs[chain] == nil {
			needs[chain] = &WorkflowNeed{
				ChainID:      chain,
				TaskID:       task.GetId(),
				Name:         task.GetName(),
				ValidUntilMs: taskValidUntil(sched, cronSchedules(task)),
			}
		}
		return needs[chain]
	}
	walkFundMoves(task.GetNodes(), settings, task.GetTrigger(), func(site fundSite) {
		chain := site.chain
		if chain <= 0 {
			chain = fallbackChain
		}
		need := ensure(chain)
		need.HasFundMove = true
		// ETH send and payable value have no contract target. Size them
		// before the unresolved-call return, which would drop the cap.
		if site.kind == fundEth || site.kind == fundValue {
			if site.kind == fundEth {
				if site.recipientOK {
					addRecipient(need, site.recipient)
				} else {
					need.Unresolved = true
				}
			}
			if site.amountUnknown || site.loopUnknown || !runsKnown {
				need.CapNeedsInput = true
				need.NativeUnsized = true
				return
			}
			if site.amountOK {
				total, ok := scaleSpend(site.amount, runs, site.loopMult)
				if !ok || !addNative(need, total) {
					need.CapNeedsInput = true
					need.NativeUnsized = true
					need.NativeSpendCap = nil
				}
			}
			return
		}
		if !site.targetOK || !site.selectorOK {
			// A template the walk cannot read still moves funds. Leaving
			// it off the need would let a new grant drop it.
			need.Unresolved = true
			if site.amountUnknown || site.loopUnknown || !runsKnown {
				need.CapNeedsInput = true
			}
			return
		}
		addAction(need, site.target, site.selector)
		spendLimited := site.selector == selectorTransfer || site.selector == selectorApprove
		if site.ceiling && spendLimited {
			// The token is named. The amount is not a total, so it is not
			// stored and one simulation must not fill it in. Unknown runs
			// stay cap_needs_input without that ceiling flag: a typed cap
			// can prepare the grant, and simulate still asks for a run count.
			need.CapNeedsInput = true
			if runsKnown {
				need.CapCeiling = true
			}
			return
		}
		if spendLimited && (site.amountUnknown || site.loopUnknown || !runsKnown) {
			need.CapNeedsInput = true
		} else if spendLimited && site.amountOK && runsKnown {
			total, ok := scaleSpend(site.amount, runs, site.loopMult)
			if !ok || !addCap(need, site.target, total) {
				need.CapNeedsInput = true
				removeCap(need, site.target)
			}
		}
	})
	for _, need := range needs {
		sortNeed(need)
	}
	return needs
}

// MergeSkillGrant unions the addition with what enabled tasks on chainID
// still need. With no current grant, caps start at zero and an unsized or
// unresolved fund move fails closed. An unsized payable is
// SESSION_POLICY_NATIVE_UNSIZED when this merge would install a native cap.
// Any other unsized amount stays ErrSessionPolicyUnsized. With a current
// grant, pass nil remainder only when the grant has not been applied: the
// stored caps are the remainder, because nothing can have been spent. An
// applied grant must pass the chain read. Nil there is refused so a missed
// read cannot be treated as nothing left.
func MergeSkillGrant(current *model.SessionPolicy, addition PolicyAddition, tasks []*avsproto.Task, chainID int64, now time.Time, expiresIn time.Duration) (SessionPermissions, PolicyChanges, error) {
	return MergeSkillGrantWithRemainder(current, nil, addition, tasks, chainID, now, expiresIn)
}

// MergeSkillGrantWithRemainder is MergeSkillGrant with the current grant's
// usable leftover supplied by the caller. remainder is ignored when current
// is nil.
func MergeSkillGrantWithRemainder(current *model.SessionPolicy, remainder *GrantRemainder, addition PolicyAddition, tasks []*avsproto.Task, chainID int64, now time.Time, expiresIn time.Duration) (SessionPermissions, PolicyChanges, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if current != nil {
		if current.Grant.Applied() && remainder == nil {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("applied grant %s has no remaining-limit read", current.ID)
		}
		if remainder == nil {
			stored, err := storedGrantRemainder(current)
			if err != nil {
				return SessionPermissions{}, PolicyChanges{}, err
			}
			remainder = &stored
		}
		return mergeCarriedSkillGrant(current, *remainder, addition, tasks, chainID, now, expiresIn)
	}
	actions := map[string]map[string]struct{}{}
	caps := map[common.Address]*big.Int{}
	var recipients []*common.Address
	native := big.NewInt(0)
	var nativeSet bool
	var taskNeeds []*WorkflowNeed

	addActions := func(list []model.AllowedAction) {
		for _, a := range list {
			if a.Target == nil {
				continue
			}
			key := strings.ToLower(a.Target.Hex())
			if actions[key] == nil {
				actions[key] = map[string]struct{}{}
			}
			for _, s := range a.Selectors {
				actions[key][normalizeSelector(s)] = struct{}{}
			}
		}
	}
	addActions(addition.AllowedActions)
	for _, cap := range addition.SpendCaps {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation cap %s: %v", ErrSessionPolicyUnsized, cap.Token.Hex(), err)
		}
		if caps[*cap.Token] == nil {
			caps[*cap.Token] = big.NewInt(0)
		}
		caps[*cap.Token].Add(caps[*cap.Token], amt)
		if caps[*cap.Token].Cmp(maxUint256) > 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation cap %s exceeds uint256", ErrSessionPolicyUnsized, cap.Token.Hex())
		}
	}
	// Later tasks add their derived caps into caps. A ceiling may use only
	// the addition, so a sibling's total cannot cover it or change with
	// task order.
	additionCaps := cloneCaps(caps)
	recipients = append(recipients, addition.NativeRecipients...)
	if addition.NativeSpendCap != nil {
		amt, err := parseCapAmount(addition.NativeSpendCap.Amount)
		if err != nil {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation native cap: %v", ErrSessionPolicyUnsized, err)
		}
		native.Add(native, amt)
		if native.Cmp(maxUint256) > 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation native cap exceeds uint256", ErrSessionPolicyUnsized)
		}
		nativeSet = true
	}

	var unresolved []*WorkflowNeed
	var nativeUnsized []*WorkflowNeed
	var unsized error
	// nativeSet is the addition. A sized running payable can install the
	// module too, including one that sits after an unsized sibling. The
	// sibling is not merged, but it still decides the error code.
	wouldInstallNative := nativeSet && native.Sign() > 0
	for _, task := range tasks {
		if task == nil {
			continue
		}
		derived := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), chainID)
		need := derived[chainID]
		if need == nil || !need.HasFundMove {
			continue
		}
		if need.Unresolved {
			// Not an amount the caller can type. The previous grant's rows
			// are not copied in to paper over it.
			unresolved = append(unresolved, need)
			continue
		}
		if need.NativeSpendCap != nil && !need.NativeUnsized {
			if amt, err := parseCapAmount(need.NativeSpendCap.Amount); err == nil && amt.Sign() > 0 {
				wouldInstallNative = true
			}
		}
		if need.CapNeedsInput {
			if need.NativeUnsized {
				nativeUnsized = append(nativeUnsized, need)
			}
			// A named transfer with no derived total contributes nothing.
			// Only the addition's positive cap covers it. A sibling task's
			// derived cap does not, and a missing cap still fails closed.
			if need.NativeUnsized || !namedSpendCovered(need, additionCaps) {
				if unsized == nil {
					unsized = fmt.Errorf("%w: task %s (%s)", ErrSessionPolicyUnsized, task.GetId(), displayName(task.GetName()))
				}
				continue
			}
		}
		if len(unresolved) > 0 || unsized != nil || len(nativeUnsized) > 0 {
			continue
		}
		taskNeeds = append(taskNeeds, need)
		addActions(need.Actions)
		for _, cap := range need.Caps {
			if cap.Token == nil {
				continue
			}
			amt, err := parseCapAmount(cap.Amount)
			if err != nil {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s: %v", ErrSessionPolicyUnsized, task.GetId(), err)
			}
			if caps[*cap.Token] == nil {
				caps[*cap.Token] = big.NewInt(0)
			}
			caps[*cap.Token].Add(caps[*cap.Token], amt)
			if caps[*cap.Token].Cmp(maxUint256) > 0 {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s cap exceeds uint256", ErrSessionPolicyUnsized, task.GetId())
			}
		}
		recipients = append(recipients, need.NativeRecipients...)
		if need.NativeSpendCap != nil {
			amt, err := parseCapAmount(need.NativeSpendCap.Amount)
			if err != nil {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s native: %v", ErrSessionPolicyUnsized, task.GetId(), err)
			}
			native.Add(native, amt)
			if native.Cmp(maxUint256) > 0 {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s native cap exceeds uint256", ErrSessionPolicyUnsized, task.GetId())
			}
			nativeSet = true
		}
	}
	if len(unresolved) > 0 {
		conflict := unresolvedTargetConflict(unresolved)
		if current != nil {
			conflict.PolicyID = current.ID
		}
		return SessionPermissions{}, PolicyChanges{}, conflict
	}
	if wouldInstallNative && len(nativeUnsized) > 0 {
		return SessionPermissions{}, PolicyChanges{}, nativeCapUnsized(nil, nativeUnsized)
	}
	if unsized != nil {
		return SessionPermissions{}, PolicyChanges{}, unsized
	}

	mergedActions := actionsFromSet(actions)
	var spendCaps []model.ERC20SpendCap
	for token, amt := range caps {
		token := token
		if !actionHasTarget(mergedActions, token) {
			continue
		}
		if !transferApproveOnly(token, mergedActions) {
			continue
		}
		spendCaps = append(spendCaps, model.ERC20SpendCap{Token: &token, Amount: amt.String()})
	}
	sort.Slice(spendCaps, func(i, j int) bool {
		return strings.ToLower(spendCaps[i].Token.Hex()) < strings.ToLower(spendCaps[j].Token.Hex())
	})

	recipients = dedupeRecipients(recipients)
	perms := SessionPermissions{
		AllowedActions:         mergedActions,
		SpendCaps:              spendCaps,
		NativeRecipients:       recipients,
		AllowContractRecipient: addition.AllowContractRecipient || (current != nil && current.AllowContractRecipient),
	}
	if len(spendCaps) > 0 {
		first := spendCaps[0]
		perms.SpendCap = &first
	}
	for _, action := range mergedActions {
		if action.Target == nil || !transferApproveOnly(*action.Target, mergedActions) {
			continue
		}
		if caps[*action.Target] == nil || caps[*action.Target].Sign() <= 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: %s has no positive remaining cap", ErrSessionPolicyUnsized, action.Target.Hex())
		}
	}
	if len(recipients) > 0 || nativeSet {
		if native.Sign() <= 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: native spend has no positive total", ErrSessionPolicyUnsized)
		}
		perms.NativeSpendCap = &model.NativeSpendCap{Amount: native.String()}
	}

	horizon := addition.ValidUntilMs
	if horizon == 0 {
		horizon = now.Add(expiresIn).UnixMilli()
	}
	latest := horizon
	unknownEnd := false
	for _, need := range taskNeeds {
		if need.ValidUntilMs > latest {
			latest = need.ValidUntilMs
		}
		if need.ValidUntilMs == 0 {
			// An enabled automation with no computable end must not let a
			// new skill pull the wallet's single expiry earlier.
			unknownEnd = true
		}
	}
	if unknownEnd && current != nil && current.ValidUntil > latest {
		latest = current.ValidUntil
	}
	floor := now.Add(60 * time.Second).UnixMilli()
	if latest < floor {
		latest = floor
	}
	perms.ValidUntilMs = latest

	changes := diffGrant(current, nil, perms, taskNeeds, latest)
	return perms, changes, nil
}

// CoverageRefusal reports a concrete gap. A notification-only need returns
// nil. A fund move whose target could not be resolved refuses, so a new
// grant cannot drop that automation. An unsized amount does not refuse
// when the actions are covered. No grant plus any fund move refuses.
func CoverageRefusal(policy *model.SessionPolicy, need *WorkflowNeed) *PolicyConflictError {
	if need == nil || !need.HasFundMove {
		return nil
	}
	if policy == nil || !policy.Usable() {
		return &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          fmt.Sprintf("%s moves funds and the runner has no usable grant", displayName(need.Name)),
			AffectedTaskIDs: nonEmptyID(need.TaskID),
			Required:        need,
		}
	}
	if need.Unresolved {
		conflict := unresolvedTargetConflict([]*WorkflowNeed{need})
		conflict.PolicyID = policy.ID
		return conflict
	}
	missing := missingActions(policy.AllowedActions, need.Actions)
	var nativeMiss bool
	for _, rec := range need.NativeRecipients {
		if rec == nil {
			continue
		}
		if !nativeRecipientAllowed(policy, *rec) {
			nativeMiss = true
			break
		}
	}
	if len(missing) > 0 || nativeMiss {
		return &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          fmt.Sprintf("%s is outside the usable grant", displayName(need.Name)),
			PolicyID:        policy.ID,
			AffectedTaskIDs: nonEmptyID(need.TaskID),
			Missing:         missing,
			Required:        need,
		}
	}
	if short, nativeShort := capShortfall(policy, need); short || nativeShort {
		detail := fmt.Sprintf("%s needs a higher spend cap than the usable grant", displayName(need.Name))
		if nativeShort && !short {
			detail = fmt.Sprintf("%s needs a higher native cap than the usable grant", displayName(need.Name))
		}
		return &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          detail,
			PolicyID:        policy.ID,
			AffectedTaskIDs: nonEmptyID(need.TaskID),
			Required:        need,
		}
	}
	// A live grant that ends before the workflow. An already-closed grant
	// is not a refusal here: submit and carry still need that record to
	// count as the grant occupying its entity. Create and resume call
	// Expired() themselves.
	if need.ValidUntilMs > 0 && policy.ValidUntil > 0 && policy.ValidUntil < need.ValidUntilMs {
		return &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          fmt.Sprintf("%s runs until the grant has expired", displayName(need.Name)),
			PolicyID:        policy.ID,
			AffectedTaskIDs: nonEmptyID(need.TaskID),
			Required:        need,
		}
	}
	return nil
}

// namedSpendCovered reports whether every transfer or approve on need
// already has a positive cap, either derived onto the need or present in
// caps. caps must be the addition alone, not caps accumulated from
// earlier tasks. This applies to every named unsized transfer or approve,
// not only a recognized split or batch. No such action is not covered:
// an unsized native amount stays on today's error path.
func namedSpendCovered(need *WorkflowNeed, caps map[common.Address]*big.Int) bool {
	if need == nil {
		return false
	}
	sawSpend := false
	for _, action := range need.Actions {
		if action.Target == nil || !transferApproveOnly(*action.Target, need.Actions) {
			continue
		}
		sawSpend = true
		if amt := capAmount(need, *action.Target); amt != nil && amt.Sign() > 0 {
			continue
		}
		got := caps[*action.Target]
		if got == nil || got.Sign() <= 0 {
			return false
		}
	}
	return sawSpend
}

func cloneCaps(caps map[common.Address]*big.Int) map[common.Address]*big.Int {
	out := make(map[common.Address]*big.Int, len(caps))
	for token, amt := range caps {
		if amt == nil {
			continue
		}
		out[token] = new(big.Int).Set(amt)
	}
	return out
}

// ceilingGrantCovers is true when every unsized spend token on a ceiling
// need has a positive grant cap. Tokens the need already sized are left
// for capShortfall. Unknown runs are not a ceiling, so simulate stays
// cap_needs_input until the run count is stored.
func ceilingGrantCovers(policy *model.SessionPolicy, need *WorkflowNeed) bool {
	if policy == nil || need == nil || !need.CapCeiling {
		return false
	}
	have := map[common.Address]*big.Int{}
	for _, cap := range policyCaps(policy) {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil || amt.Sign() <= 0 {
			continue
		}
		have[*cap.Token] = amt
	}
	sawSpend := false
	for _, action := range need.Actions {
		if action.Target == nil || !transferApproveOnly(*action.Target, need.Actions) {
			continue
		}
		sawSpend = true
		if amt := capAmount(need, *action.Target); amt != nil && amt.Sign() > 0 {
			continue
		}
		got := have[*action.Target]
		if got == nil || got.Sign() <= 0 {
			return false
		}
	}
	return sawSpend
}

// BuildAuthorization picks one simulate status. Simulation-resolved misses
// win over the static walk when the report saw a write. Required is set
// whenever the workflow moves funds.
//
// sched.Now is the clock. A zero Now means time.Now(). An expired grant is
// expires_too_soon even when the workflow has no end. A live grant that
// ends first stays on the window check. A missing call or a short cap
// still outranks expiry.
func BuildAuthorization(policy *model.SessionPolicy, need *WorkflowNeed, report *SessionGrantReport, sched SkillSchedule) *SessionAuthorization {
	repPolicy, noGrant, saw, missingCalls, nativeMiss, nativeDetail, tokenSpend, nativeSpend, planned := report.snapshot()
	if policy == nil {
		policy = repPolicy
	}
	if (need == nil || !need.HasFundMove) && !saw {
		return &SessionAuthorization{Status: AuthCovered}
	}
	filled := fillObserved(need, saw, planned, tokenSpend, nativeSpend, sched)
	if filled == nil {
		filled = &WorkflowNeed{HasFundMove: true}
	}
	out := &SessionAuthorization{Required: filled}
	if policy != nil {
		out.PolicyID = policy.ID
	}
	if policy == nil || noGrant && policy == nil {
		out.Status = AuthNoGrant
		out.Detail = "no usable grant"
		return out
	}
	var missing []model.AllowedAction
	if saw {
		missing = actionsFromPlanned(missingCalls)
		if nativeMiss && !isNativeCapMessage(nativeDetail) {
			out.Detail = nativeDetail
			out.Status = AuthNotCovered
			out.Missing = missing
			return out
		}
	} else if need != nil {
		missing = missingActions(policy.AllowedActions, need.Actions)
	}
	// A ceiling whose static grant lacks the token is cap_needs_input.
	// An observed call outside the grant still wins as not_covered.
	if len(missing) > 0 && !(filled.CapCeiling && !saw) {
		out.Status = AuthNotCovered
		out.Missing = missing
		out.Detail = "a planned call is outside the usable grant"
		return out
	}
	if filled.Unresolved {
		// One observed iteration must not clear this. The call the report
		// saw can be covered and the status is still target_unresolved.
		out.Status = AuthTargetUnresolved
		out.Detail = "a fund-moving target could not be resolved"
		return out
	}
	if filled.CapNeedsInput && !ceilingGrantCovers(policy, filled) {
		out.Status = AuthCapNeedsInput
		out.Detail = "a spend amount is not a fixed number"
		return out
	}
	if len(missing) > 0 {
		out.Status = AuthNotCovered
		out.Missing = missing
		out.Detail = "a planned call is outside the usable grant"
		return out
	}
	short, nativeShort := capShortfall(policy, filled)
	if nativeMiss && isNativeCapMessage(nativeDetail) {
		nativeShort = true
		out.Detail = nativeDetail
	}
	if short || nativeShort {
		out.Status = AuthCapTooLow
		if out.Detail == "" {
			out.Detail = "a spend cap is lower than the runs still left"
		}
		return out
	}
	now := sched.Now
	if now.IsZero() {
		now = time.Now()
	}
	if policy.Expired(now) {
		out.Status = AuthExpiresTooSoon
		out.Detail = "the grant has expired"
		return out
	}
	if filled.ValidUntilMs > 0 && policy.ValidUntil > 0 && policy.ValidUntil < filled.ValidUntilMs {
		out.Status = AuthExpiresTooSoon
		out.Detail = "the grant ends before this workflow's window"
		return out
	}
	out.Status = AuthCovered
	if filled.CapCeiling {
		out.Detail = ceilingSharedCapDetail
	}
	return out
}

// ExpiryChangeLine is the approval copy for one task. Same calendar year
// omits the year: "Weekly swap: until Dec 28, was Nov 30".
func ExpiryChangeLine(name string, validUntil, previous time.Time) string {
	name = displayName(name)
	until := validUntil.UTC()
	was := previous.UTC()
	if until.Year() == was.Year() {
		return fmt.Sprintf("%s: until %s, was %s", name, until.Format("Jan 2"), was.Format("Jan 2"))
	}
	return fmt.Sprintf("%s: until %s, was %s", name, until.Format("Jan 2, 2006"), was.Format("Jan 2, 2006"))
}

func scheduleFromTask(task *avsproto.Task, now time.Time) SkillSchedule {
	if task == nil {
		return SkillSchedule{Now: now}
	}
	return SkillSchedule{
		MaxExecution:   task.GetMaxExecution(),
		StartAt:        task.GetStartAt(),
		ExpiredAt:      task.GetExpiredAt(),
		ExecutionCount: task.GetExecutionCount(),
		Now:            now,
		Crons:          cronSchedules(task),
	}
}

func remainingRuns(sched SkillSchedule, crons []string) (int64, bool) {
	now := sched.Now
	if now.IsZero() {
		now = time.Now()
	}
	var count int64
	var countKnown bool
	if sched.MaxExecution > 0 {
		count = sched.MaxExecution - sched.ExecutionCount
		if count < 0 {
			count = 0
		}
		countKnown = true
	}
	var cronCount int64
	var cronKnown bool
	if len(crons) > 0 && sched.ExpiredAt > now.UnixMilli() {
		from := now
		if sched.StartAt > from.UnixMilli() {
			from = time.UnixMilli(sched.StartAt)
		}
		n, ok := countCronFires(crons, from, sched.ExpiredAt, maxCronWalk)
		if ok {
			cronCount = n
			cronKnown = true
		}
	}
	switch {
	case cronKnown && countKnown:
		if cronCount < count {
			return cronCount, true
		}
		return count, true
	case cronKnown:
		return cronCount, true
	case countKnown:
		return count, true
	default:
		return 0, false
	}
}

func taskValidUntil(sched SkillSchedule, crons []string) int64 {
	now := sched.Now
	if now.IsZero() {
		now = time.Now()
	}
	if sched.ExpiredAt > now.UnixMilli() {
		return sched.ExpiredAt
	}
	if sched.MaxExecution <= 0 || len(crons) == 0 {
		return 0
	}
	remaining := sched.MaxExecution - sched.ExecutionCount
	if remaining <= 0 || remaining > maxCronWalk {
		return 0
	}
	from := now
	if sched.StartAt > from.UnixMilli() {
		from = time.UnixMilli(sched.StartAt)
	}
	last, ok := nthCronFire(crons, from, remaining)
	if !ok || !last.After(now) {
		return 0
	}
	return last.UnixMilli()
}

func countCronFires(schedules []string, from time.Time, untilMs int64, limit int) (int64, bool) {
	until := time.UnixMilli(untilMs)
	specs := parseCrons(schedules)
	if len(specs) == 0 || !until.After(from) {
		return 0, len(specs) > 0 && !until.After(from)
	}
	cursor := from
	var n int64
	for n < int64(limit) {
		next, ok := earliestNext(specs, cursor)
		if !ok || next.After(until) {
			return n, true
		}
		n++
		cursor = next
	}
	return 0, false
}

func nthCronFire(schedules []string, from time.Time, n int64) (time.Time, bool) {
	specs := parseCrons(schedules)
	if len(specs) == 0 || n <= 0 {
		return time.Time{}, false
	}
	cursor := from
	var last time.Time
	for i := int64(0); i < n; i++ {
		next, ok := earliestNext(specs, cursor)
		if !ok {
			return time.Time{}, false
		}
		last = next
		cursor = next
	}
	return last, true
}

func parseCrons(schedules []string) []interface{ Next(time.Time) time.Time } {
	var specs []interface{ Next(time.Time) time.Time }
	for _, raw := range schedules {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		spec, err := limitsCronParser.Parse(raw)
		if err != nil {
			continue
		}
		specs = append(specs, spec)
	}
	return specs
}

func earliestNext(specs []interface{ Next(time.Time) time.Time }, cursor time.Time) (time.Time, bool) {
	var next time.Time
	for _, spec := range specs {
		t := spec.Next(cursor)
		if t.IsZero() {
			continue
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next, !next.IsZero()
}

func cronSchedules(task *avsproto.Task) []string {
	if task == nil || task.GetTrigger() == nil || task.GetTrigger().GetCron() == nil || task.GetTrigger().GetCron().GetConfig() == nil {
		return nil
	}
	return task.GetTrigger().GetCron().GetConfig().GetSchedules()
}

type fundKind int

const (
	fundCall fundKind = iota
	fundEth
	fundValue
)

type fundSite struct {
	chain         int64
	kind          fundKind
	target        common.Address
	targetOK      bool
	selector      string
	selectorOK    bool
	recipient     common.Address
	recipientOK   bool
	amount        *big.Int
	amountOK      bool
	amountUnknown bool
	loopUnknown   bool
	// ceiling is a named spend whose amount is not a fixed number. The
	// visit records the action and does not store a derived cap.
	ceiling  bool
	loopMult int
}

// fundScope is the settings map plus, inside a settings-list loop, the
// current element. hasValue is false outside that loop, so {{value}} on a
// node-output loop or a node that is not a loop stays unresolved.
type fundScope struct {
	settings map[string]any
	value    any
	hasValue bool
}

func walkFundMoves(nodes []*avsproto.TaskNode, settings map[string]any, trigger *avsproto.TaskTrigger, visit func(fundSite)) {
	scope := fundScope{settings: settings}
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if cw := node.GetContractWrite(); cw != nil {
			collectContractWrite(cw, scope, false, 1, visit)
		}
		if et := node.GetEthTransfer(); et != nil {
			collectEth(et, scope, false, 1, visit)
		}
		if loop := node.GetLoop(); loop != nil {
			list, known := loopElements(loop.GetConfig(), settings)
			if known && len(list) == 0 {
				continue
			}
			if !known {
				if sites, ok := boundNodeOutputLoop(nodes, settings, trigger, loop); ok {
					for _, site := range sites {
						visit(site)
					}
					continue
				}
				if cw := loop.GetContractWrite(); cw != nil {
					collectContractWrite(cw, scope, true, 1, visit)
				}
				if et := loop.GetEthTransfer(); et != nil {
					collectEth(et, scope, true, 1, visit)
				}
				continue
			}
			// One visit per element, and the list length is applied once.
			// Multiplying each visit by len(list) would square the cap.
			for _, elem := range list {
				elemScope := fundScope{settings: settings, value: elem, hasValue: true}
				if cw := loop.GetContractWrite(); cw != nil {
					collectContractWrite(cw, elemScope, false, 1, visit)
				}
				if et := loop.GetEthTransfer(); et != nil {
					collectEth(et, elemScope, false, 1, visit)
				}
			}
		}
	}
}

func collectContractWrite(cw *avsproto.ContractWriteNode, scope fundScope, loopUnknown bool, loopMult int, visit func(fundSite)) {
	cfg := cw.GetConfig()
	if cfg == nil {
		return
	}
	nodeTarget, nodeTargetOK := resolveAddress(cfg.GetContractAddress(), scope)
	calls := cfg.GetMethodCalls()
	if len(calls) == 0 {
		sel, selOK, amt, amtOK, amtUnknown := decodeCall(cfg.GetCallData(), "", nil, scope)
		site := fundSite{
			chain: cfg.GetChainId(), kind: fundCall,
			target: nodeTarget, targetOK: nodeTargetOK,
			selector: sel, selectorOK: selOK,
			amount: amt, amountOK: amtOK, amountUnknown: amtUnknown,
			loopUnknown: loopUnknown, loopMult: loopMult,
		}
		visit(site)
		if v, ok, unknown := resolveAmount(cfg.GetValue(), scope); ok && v.Sign() > 0 || unknown {
			visit(fundSite{
				chain: cfg.GetChainId(), kind: fundValue,
				amount: v, amountOK: ok && v != nil && v.Sign() > 0, amountUnknown: unknown,
				loopUnknown: loopUnknown, loopMult: loopMult,
			})
		}
		return
	}
	for _, mc := range calls {
		target, targetOK := nodeTarget, nodeTargetOK
		if override := strings.TrimSpace(mc.GetContractAddress()); override != "" {
			target, targetOK = resolveAddress(override, scope)
		}
		sel, selOK, amt, amtOK, amtUnknown := decodeCall(mc.GetCallData(), mc.GetMethodName(), mc.GetMethodParams(), scope)
		if !selOK {
			if got, ok := selectorFromABI(cfg.GetContractAbi(), mc.GetMethodName()); ok {
				sel, selOK = got, true
			}
		}
		visit(fundSite{
			chain: cfg.GetChainId(), kind: fundCall,
			target: target, targetOK: targetOK,
			selector: sel, selectorOK: selOK,
			amount: amt, amountOK: amtOK, amountUnknown: amtUnknown,
			loopUnknown: loopUnknown, loopMult: loopMult,
		})
	}
	if v, ok, unknown := resolveAmount(cfg.GetValue(), scope); ok && v.Sign() > 0 || unknown {
		visit(fundSite{
			chain: cfg.GetChainId(), kind: fundValue,
			amount: v, amountOK: ok && v != nil && v.Sign() > 0, amountUnknown: unknown,
			loopUnknown: loopUnknown, loopMult: loopMult,
		})
	}
}

func collectEth(et *avsproto.ETHTransferNode, scope fundScope, loopUnknown bool, loopMult int, visit func(fundSite)) {
	cfg := et.GetConfig()
	if cfg == nil {
		return
	}
	rec, recOK := resolveAddress(cfg.GetDestination(), scope)
	amt, amtOK, amtUnknown := resolveAmount(cfg.GetAmount(), scope)
	visit(fundSite{
		chain: cfg.GetChainId(), kind: fundEth,
		recipient: rec, recipientOK: recOK,
		amount: amt, amountOK: amtOK, amountUnknown: amtUnknown,
		loopUnknown: loopUnknown, loopMult: loopMult,
	})
}

func decodeCall(calldata, method string, params []string, scope fundScope) (sel string, selOK bool, amt *big.Int, amtOK, amtUnknown bool) {
	raw, concrete := resolveString(calldata, scope)
	if concrete {
		data := common.FromHex(raw)
		if len(data) >= 4 && !strings.ContainsAny(raw, "{}$") {
			sel = SelectorFromCalldata(data)
			selOK = sel != "0x00000000"
			if decoded, ok := decodeTransferAmount(data); ok {
				return sel, selOK, decoded, true, false
			}
			if sel == selectorTransfer || sel == selectorApprove {
				return sel, selOK, nil, false, true
			}
			return sel, selOK, nil, false, false
		}
	} else if strings.TrimSpace(calldata) != "" {
		amtUnknown = true
	}
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "transfer":
		sel, selOK = selectorTransfer, true
	case "approve":
		sel, selOK = selectorApprove, true
	default:
		if sel == "" {
			return "", false, nil, false, amtUnknown
		}
		return sel, selOK, nil, false, amtUnknown
	}
	if len(params) >= 2 {
		amt, amtOK, amtUnknown = resolveAmount(params[1], scope)
		return sel, selOK, amt, amtOK, amtUnknown
	}
	return sel, selOK, nil, false, true
}

func decodeTransferAmount(data []byte) (*big.Int, bool) {
	if len(data) < 68 {
		return nil, false
	}
	sel := SelectorFromCalldata(data)
	if sel != selectorTransfer && sel != selectorApprove {
		return nil, false
	}
	amt := new(big.Int).SetBytes(data[36:68])
	if amt.Sign() <= 0 {
		return nil, false
	}
	return amt, true
}

func resolveAddress(raw string, scope fundScope) (common.Address, bool) {
	s, ok := resolveString(raw, scope)
	if !ok || !common.IsHexAddress(s) || strings.ContainsAny(s, "{}$") {
		return common.Address{}, false
	}
	addr := common.HexToAddress(s)
	if addr == (common.Address{}) {
		return common.Address{}, false
	}
	return addr, true
}

func resolveAmount(raw string, scope fundScope) (*big.Int, bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, false
	}
	s, ok := resolveString(raw, scope)
	if !ok {
		return nil, false, true
	}
	if s == "" {
		return nil, false, false
	}
	if strings.EqualFold(s, "max") {
		return nil, false, true
	}
	if n, ok := new(big.Int).SetString(s, 10); ok && n.Sign() == 0 {
		return nil, false, false
	}
	n, err := parseCapAmount(s)
	if err != nil {
		return nil, false, true
	}
	return n, true, false
}

func resolveString(raw string, scope fundScope) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if strings.Contains(raw, "{{") || strings.Contains(raw, "${") {
		if scope.hasValue {
			if v, ok := lookupLoopValue(raw, scope.value); ok {
				return scalarString(v)
			}
		}
		v, ok := lookupSetting(raw, scope.settings)
		if !ok {
			return "", false
		}
		return scalarString(v)
	}
	return raw, true
}

// lookupLoopValue reads {{value}} or {{value.a.b}} from the current loop
// element. A non-scalar {{value}} (an object) is returned as-is; the caller
// leaves it unresolved rather than inventing an address.
func lookupLoopValue(expr string, value any) (any, bool) {
	m := valueRef.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return nil, false
	}
	if m[1] == "" {
		return value, true
	}
	cur := value
	for _, key := range strings.Split(m[1], ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := obj[key]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// scalarString formats a settings leaf. Floats are accepted only when they
// are an exact integer inside the 53-bit mantissa, so a JSON number cannot
// size a cap it cannot represent.
func scalarString(v any) (string, bool) {
	switch n := v.(type) {
	case string:
		return strings.TrimSpace(n), true
	case int:
		return strconv.Itoa(n), true
	case int32:
		return strconv.FormatInt(int64(n), 10), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case uint32:
		return strconv.FormatUint(uint64(n), 10), true
	case uint64:
		return strconv.FormatUint(n, 10), true
	case float32:
		return exactFloatString(float64(n))
	case float64:
		return exactFloatString(n)
	default:
		return "", false
	}
}

func exactFloatString(n float64) (string, bool) {
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
		return "", false
	}
	if n > 1<<53 || n < -(1<<53) {
		return "", false
	}
	return strconv.FormatFloat(n, 'f', 0, 64), true
}

func lookupSetting(expr string, settings map[string]any) (any, bool) {
	m := settingRef.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil || settings == nil {
		return nil, false
	}
	path := m[1]
	if path == "" {
		path = m[2]
	}
	var cur any = settings
	for _, key := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// loopElements returns the settings list a loop iterates. An input that is
// not a settings list is unknown here. walkFundMoves may still recognize
// the compiled split or batch template; anything else is visited once
// with {{value}} unbound.
func loopElements(cfg *avsproto.LoopNode_Config, settings map[string]any) ([]any, bool) {
	if cfg == nil {
		return nil, false
	}
	v, ok := lookupSetting(cfg.GetInputVariable(), settings)
	if !ok {
		return nil, false
	}
	switch list := v.(type) {
	case []any:
		return list, true
	case []string:
		out := make([]any, len(list))
		for i, s := range list {
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

func settingsFromTask(task *avsproto.Task) map[string]any {
	if task == nil || task.InputVariables == nil {
		return nil
	}
	v := task.InputVariables["settings"]
	if v == nil {
		return nil
	}
	m, _ := v.AsInterface().(map[string]any)
	return m
}

func addAction(need *WorkflowNeed, target common.Address, selector string) {
	selector = normalizeSelector(selector)
	for i, a := range need.Actions {
		if a.Target != nil && *a.Target == target {
			for _, s := range a.Selectors {
				if normalizeSelector(s) == selector {
					return
				}
			}
			need.Actions[i].Selectors = append(need.Actions[i].Selectors, selector)
			return
		}
	}
	t := target
	need.Actions = append(need.Actions, model.AllowedAction{Target: &t, Selectors: []string{selector}})
}

func addCap(need *WorkflowNeed, token common.Address, amt *big.Int) bool {
	if amt == nil || amt.Sign() <= 0 || amt.Cmp(maxUint256) > 0 {
		return false
	}
	for i, cap := range need.Caps {
		if cap.Token != nil && *cap.Token == token {
			cur, err := parseCapAmount(cap.Amount)
			if err != nil {
				cur = big.NewInt(0)
			}
			cur.Add(cur, amt)
			if cur.Cmp(maxUint256) > 0 {
				return false
			}
			need.Caps[i].Amount = cur.String()
			return true
		}
	}
	t := token
	need.Caps = append(need.Caps, model.ERC20SpendCap{Token: &t, Amount: amt.String()})
	return true
}

func removeCap(need *WorkflowNeed, token common.Address) {
	out := need.Caps[:0]
	for _, cap := range need.Caps {
		if cap.Token != nil && *cap.Token == token {
			continue
		}
		out = append(out, cap)
	}
	need.Caps = out
}

func addNative(need *WorkflowNeed, amt *big.Int) bool {
	if amt == nil || amt.Sign() <= 0 || amt.Cmp(maxUint256) > 0 {
		return false
	}
	if need.NativeSpendCap == nil {
		need.NativeSpendCap = &model.NativeSpendCap{Amount: amt.String()}
		return true
	}
	cur, err := parseCapAmount(need.NativeSpendCap.Amount)
	if err != nil {
		cur = big.NewInt(0)
	}
	cur.Add(cur, amt)
	if cur.Cmp(maxUint256) > 0 {
		return false
	}
	need.NativeSpendCap.Amount = cur.String()
	return true
}

// scaleSpend is per-run amount times the runs still left. A product the
// hooks cannot store is unsized: packing it would wrap, and the summary
// would not be what the chain enforces.
func scaleSpend(perRun *big.Int, runs int64, loopMult int) (*big.Int, bool) {
	if perRun == nil || perRun.Sign() <= 0 || runs <= 0 || perRun.Cmp(maxUint256) > 0 {
		return nil, false
	}
	total := new(big.Int).Mul(perRun, big.NewInt(runs))
	if loopMult > 1 {
		total.Mul(total, big.NewInt(int64(loopMult)))
	}
	if total.Cmp(maxUint256) > 0 {
		return nil, false
	}
	return total, true
}

// selectorFromABI reads the 4-byte selector of method from the node's ABI.
// A swap named exactInputSingle has no calldata; without this it never
// becomes an allowed action.
func selectorFromABI(items []*structpb.Value, method string) (string, bool) {
	method = strings.TrimSpace(method)
	if method == "" || len(items) == 0 {
		return "", false
	}
	rawItems := make([]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		rawItems = append(rawItems, item.AsInterface())
	}
	if len(rawItems) == 0 {
		return "", false
	}
	encoded, err := json.Marshal(rawItems)
	if err != nil {
		return "", false
	}
	parsed, err := abi.JSON(bytes.NewReader(encoded))
	if err != nil {
		return "", false
	}
	m, ok := parsed.Methods[method]
	if !ok || len(m.ID) < 4 {
		return "", false
	}
	return "0x" + hex.EncodeToString(m.ID[:4]), true
}

func addRecipient(need *WorkflowNeed, addr common.Address) {
	for _, have := range need.NativeRecipients {
		if have != nil && *have == addr {
			return
		}
	}
	a := addr
	need.NativeRecipients = append(need.NativeRecipients, &a)
}

func sortNeed(need *WorkflowNeed) {
	if need == nil {
		return
	}
	for i := range need.Actions {
		sort.Strings(need.Actions[i].Selectors)
	}
	sort.Slice(need.Actions, func(i, j int) bool {
		return strings.ToLower(need.Actions[i].Target.Hex()) < strings.ToLower(need.Actions[j].Target.Hex())
	})
	sort.Slice(need.Caps, func(i, j int) bool {
		return strings.ToLower(need.Caps[i].Token.Hex()) < strings.ToLower(need.Caps[j].Token.Hex())
	})
	sort.Slice(need.NativeRecipients, func(i, j int) bool {
		return strings.ToLower(need.NativeRecipients[i].Hex()) < strings.ToLower(need.NativeRecipients[j].Hex())
	})
}

func actionsFromSet(set map[string]map[string]struct{}) []model.AllowedAction {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []model.AllowedAction
	for _, key := range keys {
		addr := common.HexToAddress(key)
		sels := make([]string, 0, len(set[key]))
		for s := range set[key] {
			sels = append(sels, s)
		}
		sort.Strings(sels)
		out = append(out, model.AllowedAction{Target: &addr, Selectors: sels})
	}
	return out
}

func actionHasTarget(actions []model.AllowedAction, token common.Address) bool {
	for _, a := range actions {
		if a.Target != nil && *a.Target == token {
			return true
		}
	}
	return false
}

func transferApproveOnly(token common.Address, actions []model.AllowedAction) bool {
	for _, a := range actions {
		if a.Target == nil || *a.Target != token {
			continue
		}
		if len(a.Selectors) == 0 {
			return false
		}
		for _, s := range a.Selectors {
			n := normalizeSelector(s)
			if n != selectorTransfer && n != selectorApprove {
				return false
			}
		}
	}
	return true
}

func dedupeRecipients(in []*common.Address) []*common.Address {
	seen := map[common.Address]struct{}{}
	var out []*common.Address
	for _, r := range in {
		if r == nil || *r == (common.Address{}) {
			continue
		}
		if _, ok := seen[*r]; ok {
			continue
		}
		seen[*r] = struct{}{}
		addr := *r
		out = append(out, &addr)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Hex()) < strings.ToLower(out[j].Hex())
	})
	return out
}

func diffGrant(current *model.SessionPolicy, remainder *GrantRemainder, perms SessionPermissions, taskNeeds []*WorkflowNeed, newExpiry int64) PolicyChanges {
	var prev []model.AllowedAction
	prevCaps := map[common.Address]string{}
	var prevExpiry int64
	base := ""
	if current != nil {
		base = current.ID
		prev = current.AllowedActions
		prevExpiry = current.ValidUntil
		if remainder != nil {
			for token, amt := range remainder.ERC20 {
				if amt == nil {
					prevCaps[token] = "0"
					continue
				}
				prevCaps[token] = amt.String()
			}
		} else {
			for _, cap := range policyCaps(current) {
				if cap.Token != nil {
					prevCaps[*cap.Token] = cap.Amount
				}
			}
		}
	}
	kept, added, removed := diffActions(prev, perms.AllowedActions)
	var capChanges []CapChange
	for _, cap := range perms.SpendCaps {
		if cap.Token == nil {
			continue
		}
		prevAmt := prevCaps[*cap.Token]
		if prevAmt == cap.Amount {
			delete(prevCaps, *cap.Token)
			continue
		}
		capChanges = append(capChanges, CapChange{Token: *cap.Token, Amount: cap.Amount, PreviousAmount: prevAmt})
		delete(prevCaps, *cap.Token)
	}
	for token, prevAmt := range prevCaps {
		capChanges = append(capChanges, CapChange{Token: token, Amount: "0", PreviousAmount: prevAmt, Removed: true})
	}
	sort.Slice(capChanges, func(i, j int) bool {
		return strings.ToLower(capChanges[i].Token.Hex()) < strings.ToLower(capChanges[j].Token.Hex())
	})

	var expiry []ExpiryChange
	if prevExpiry > 0 && prevExpiry != newExpiry {
		sort.Slice(taskNeeds, func(i, j int) bool {
			if taskNeeds[i].Name == taskNeeds[j].Name {
				return taskNeeds[i].TaskID < taskNeeds[j].TaskID
			}
			return taskNeeds[i].Name < taskNeeds[j].Name
		})
		for _, need := range taskNeeds {
			expiry = append(expiry, ExpiryChange{
				TaskID:             need.TaskID,
				Name:               displayName(need.Name),
				ValidUntilMs:       newExpiry,
				PreviousValidUntil: prevExpiry,
			})
		}
	}

	ch := PolicyChanges{
		BasePolicyID:   base,
		KeptActions:    kept,
		AddedActions:   added,
		RemovedActions: removed,
		CapChanges:     capChanges,
		ExpiryChanges:  expiry,
	}
	ch.Summary = summaryLines(ch, perms)
	return ch
}

func summaryLines(ch PolicyChanges, perms SessionPermissions) []string {
	var lines []string
	for _, e := range ch.ExpiryChanges {
		lines = append(lines, ExpiryChangeLine(e.Name, time.UnixMilli(e.ValidUntilMs), time.UnixMilli(e.PreviousValidUntil)))
	}
	for _, c := range ch.CapChanges {
		switch {
		case c.Removed:
			lines = append(lines, fmt.Sprintf("Cap %s: removed (was %s)", c.Token.Hex(), c.PreviousAmount))
		case c.PreviousAmount != "":
			lines = append(lines, fmt.Sprintf("Cap %s: %s (was %s)", c.Token.Hex(), c.Amount, c.PreviousAmount))
		default:
			lines = append(lines, fmt.Sprintf("Cap %s: %s", c.Token.Hex(), c.Amount))
		}
	}
	if perms.NativeSpendCap != nil {
		lines = append(lines, fmt.Sprintf("Native cap: %s", perms.NativeSpendCap.Amount))
	}
	lines = append(lines, actionLines("Added", ch.AddedActions)...)
	lines = append(lines, actionLines("Removed", ch.RemovedActions)...)
	lines = append(lines, actionLines("Kept", ch.KeptActions)...)
	if lines == nil {
		lines = []string{}
	}
	return lines
}

func actionLines(verb string, actions []model.AllowedAction) []string {
	var lines []string
	for _, a := range actions {
		if a.Target == nil {
			continue
		}
		sels := append([]string(nil), a.Selectors...)
		sort.Strings(sels)
		for _, s := range sels {
			lines = append(lines, fmt.Sprintf("%s %s %s", verb, a.Target.Hex(), normalizeSelector(s)))
		}
	}
	return lines
}

func diffActions(prev, next []model.AllowedAction) (kept, added, removed []model.AllowedAction) {
	prevSet := actionSet(prev)
	nextSet := actionSet(next)
	kept = actionsInBoth(prevSet, nextSet)
	added = actionsOnly(nextSet, prevSet)
	removed = actionsOnly(prevSet, nextSet)
	return kept, added, removed
}

func actionSet(actions []model.AllowedAction) map[string]map[string]struct{} {
	set := map[string]map[string]struct{}{}
	for _, a := range actions {
		if a.Target == nil {
			continue
		}
		key := strings.ToLower(a.Target.Hex())
		if set[key] == nil {
			set[key] = map[string]struct{}{}
		}
		for _, s := range a.Selectors {
			set[key][normalizeSelector(s)] = struct{}{}
		}
	}
	return set
}

func actionsInBoth(a, b map[string]map[string]struct{}) []model.AllowedAction {
	both := map[string]map[string]struct{}{}
	for target, sels := range a {
		for sel := range sels {
			if _, ok := b[target][sel]; ok {
				if both[target] == nil {
					both[target] = map[string]struct{}{}
				}
				both[target][sel] = struct{}{}
			}
		}
	}
	return actionsFromSet(both)
}

func actionsOnly(a, b map[string]map[string]struct{}) []model.AllowedAction {
	only := map[string]map[string]struct{}{}
	for target, sels := range a {
		for sel := range sels {
			if _, ok := b[target][sel]; ok {
				continue
			}
			if only[target] == nil {
				only[target] = map[string]struct{}{}
			}
			only[target][sel] = struct{}{}
		}
	}
	return actionsFromSet(only)
}

func policyCaps(p *model.SessionPolicy) []model.ERC20SpendCap {
	if p == nil {
		return nil
	}
	if len(p.ERC20SpendCaps) > 0 {
		return p.ERC20SpendCaps
	}
	if p.ERC20SpendCap != nil {
		return []model.ERC20SpendCap{*p.ERC20SpendCap}
	}
	return nil
}

func missingActions(allowed, planned []model.AllowedAction) []model.AllowedAction {
	var calls []PlannedCall
	for _, a := range planned {
		if a.Target == nil {
			continue
		}
		for _, s := range a.Selectors {
			calls = append(calls, PlannedCall{Target: *a.Target, Selector: s})
		}
	}
	missing := MissingGrantCalls(allowed, calls)
	return actionsFromPlanned(missing)
}

func actionsFromPlanned(calls []PlannedCall) []model.AllowedAction {
	set := map[string]map[string]struct{}{}
	for _, c := range calls {
		key := strings.ToLower(c.Target.Hex())
		if set[key] == nil {
			set[key] = map[string]struct{}{}
		}
		set[key][normalizeSelector(c.Selector)] = struct{}{}
	}
	return actionsFromSet(set)
}

func capShortfall(policy *model.SessionPolicy, need *WorkflowNeed) (tokenShort, nativeShort bool) {
	if need == nil || policy == nil {
		return false, false
	}
	have := map[common.Address]*big.Int{}
	for _, cap := range policyCaps(policy) {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			continue
		}
		have[*cap.Token] = amt
	}
	for _, cap := range need.Caps {
		if cap.Token == nil {
			continue
		}
		want, err := parseCapAmount(cap.Amount)
		if err != nil {
			continue
		}
		got := have[*cap.Token]
		if got == nil || got.Cmp(want) < 0 {
			tokenShort = true
		}
	}
	if need.NativeSpendCap != nil {
		want, err := parseCapAmount(need.NativeSpendCap.Amount)
		if err == nil {
			if policy.NativeSpendCap == nil {
				nativeShort = true
			} else if got, gerr := parseCapAmount(policy.NativeSpendCap.Amount); gerr != nil || got.Cmp(want) < 0 {
				nativeShort = true
			}
		}
	}
	return tokenShort, nativeShort
}

// fillObserved copies the static need and adds each transfer or approve
// the simulation actually ran. The cap is sized from that spend; the
// action is what makes the cap preparable.
func fillObserved(need *WorkflowNeed, saw bool, planned []PlannedCall, tokenSpend map[common.Address]*big.Int, nativeSpend *big.Int, sched SkillSchedule) *WorkflowNeed {
	if need == nil && !saw {
		return nil
	}
	var filled WorkflowNeed
	if need != nil {
		filled = *need
		filled.Actions = append([]model.AllowedAction(nil), need.Actions...)
		filled.Caps = append([]model.ERC20SpendCap(nil), need.Caps...)
		filled.NativeRecipients = append([]*common.Address(nil), need.NativeRecipients...)
		if need.NativeSpendCap != nil {
			cp := *need.NativeSpendCap
			filled.NativeSpendCap = &cp
		}
	} else {
		filled.HasFundMove = true
	}
	defer sortNeed(&filled)
	if filled.CapCeiling {
		// One observed iteration is not the split or the batch total.
		// Leave the named actions as they are and do not copy the spend.
		filled.CapNeedsInput = true
		return &filled
	}
	for _, call := range planned {
		sel, ok := observedSpendSelector(call)
		if !ok || call.Target == (common.Address{}) {
			continue
		}
		addAction(&filled, call.Target, sel)
		filled.HasFundMove = true
	}
	runs, known := remainingRuns(sched, sched.Crons)
	if !known || runs <= 0 {
		markUncappedSpend(&filled)
		return &filled
	}
	for token, perRun := range tokenSpend {
		if perRun == nil || perRun.Sign() <= 0 {
			continue
		}
		if capAmount(&filled, token) != nil {
			continue
		}
		total, ok := scaleSpend(perRun, runs, 1)
		if !ok || !addCap(&filled, token, total) {
			filled.CapNeedsInput = true
			continue
		}
	}
	if filled.NativeSpendCap == nil && nativeSpend != nil && nativeSpend.Sign() > 0 {
		total, ok := scaleSpend(nativeSpend, runs, 1)
		if !ok || !addNative(&filled, total) {
			filled.CapNeedsInput = true
			filled.NativeUnsized = true
		} else {
			filled.NativeUnsized = false
		}
	}
	if !filled.CapNeedsInput {
		return &filled
	}
	if filled.NativeUnsized {
		return &filled
	}
	// Observed totals replace an unsized static amount only when every
	// transfer/approve target now has a number. Otherwise the caller still
	// has to choose the cap.
	for _, a := range filled.Actions {
		if a.Target == nil || !transferApproveOnly(*a.Target, filled.Actions) {
			continue
		}
		if capAmount(&filled, *a.Target) == nil {
			return &filled
		}
	}
	filled.CapNeedsInput = false
	return &filled
}

// observedSpendSelector is the transfer or approve the simulation ran.
// Any other call stays out of the cap's action: a router selector is not
// a guess for how the token is spent.
func observedSpendSelector(call PlannedCall) (string, bool) {
	if strings.TrimSpace(call.Selector) != "" {
		sel := normalizeSelector(call.Selector)
		if sel == selectorTransfer || sel == selectorApprove {
			return sel, true
		}
	}
	if len(call.Calldata) >= 4 {
		sel := SelectorFromCalldata(call.Calldata)
		if sel == selectorTransfer || sel == selectorApprove {
			return sel, true
		}
	}
	return "", false
}

// markUncappedSpend keeps a transfer or approve that has no number from
// looking like a finished grant.
func markUncappedSpend(need *WorkflowNeed) {
	if need == nil || need.CapNeedsInput {
		return
	}
	for _, action := range need.Actions {
		if action.Target == nil || !transferApproveOnly(*action.Target, need.Actions) {
			continue
		}
		if capAmount(need, *action.Target) == nil {
			need.CapNeedsInput = true
			return
		}
	}
}

func capAmount(need *WorkflowNeed, token common.Address) *big.Int {
	for _, cap := range need.Caps {
		if cap.Token != nil && *cap.Token == token {
			amt, err := parseCapAmount(cap.Amount)
			if err != nil {
				return nil
			}
			return amt
		}
	}
	return nil
}

func displayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Automation"
	}
	return name
}

func nonEmptyID(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

func policyIDMatches(want string, current *model.SessionPolicy) bool {
	got := ""
	if current != nil {
		got = current.ID
	}
	return strings.EqualFold(strings.TrimSpace(want), got)
}

func newBaseChanged(current *model.SessionPolicy) *PolicyConflictError {
	id := ""
	if current != nil {
		id = current.ID
	}
	return &PolicyConflictError{
		Sentinel: ErrSessionPolicyBaseChanged,
		Code:     SessionPolicyBaseChangedCode,
		Detail:   "the runner's usable grant changed; prepare again",
		PolicyID: id,
	}
}

func permissionsAsPolicy(in SessionPolicyInput) *model.SessionPolicy {
	p := &model.SessionPolicy{
		AllowedActions:         in.Permissions.AllowedActions,
		ERC20SpendCap:          in.Permissions.SpendCap,
		ERC20SpendCaps:         in.Permissions.SpendCaps,
		NativeRecipients:       in.Permissions.NativeRecipients,
		NativeSpendCap:         in.Permissions.NativeSpendCap,
		AllowContractRecipient: in.Permissions.AllowContractRecipient,
		ValidUntil:             in.Permissions.ValidUntilMs,
		ChainID:                in.ChainID,
		Runner:                 &in.Wallet,
		Status:                 model.SessionPolicyPending,
		Grant:                  &model.SessionGrantAuthorization{},
	}
	return p
}

// classifyRunnerCoverage checks each enabled task against the grant being
// stored. Tasks named in drop are reported and do not block. Any other
// concrete gap blocks the submit. usablePolicyID is the runner's current
// grant when one exists; the unsigned replacement has no id yet.
//
// A cap that fits each task can still be short of their combined remaining
// spend. That check runs only after every individual gap is clear, and it
// skips tasks named in drop. A dropped task is echoed when putting it back
// would make that combined spend exceed the cap.
func classifyRunnerCoverage(in SessionPolicyInput, tasks []*avsproto.Task, now time.Time, usablePolicyID string) (dropped []string, err error) {
	policy := permissionsAsPolicy(in)
	var blocking []string
	var missing []model.AllowedAction
	var required *WorkflowNeed
	var covered []runnerSpend
	var spared []runnerSpend
	code := SessionPolicyNotCoveringCode
	detail := "this grant would leave an enabled automation uncovered"
	for _, task := range tasks {
		if task == nil {
			continue
		}
		derived := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), in.ChainID)
		need := derived[in.ChainID]
		refusal := CoverageRefusal(policy, need)
		if refusal == nil {
			if need != nil && need.HasFundMove {
				row := runnerSpend{id: task.GetId(), need: need}
				if taskDropped(task.GetId(), in.DropTaskIDs) {
					spared = append(spared, row)
				} else {
					covered = append(covered, row)
				}
			}
			continue
		}
		if taskDropped(task.GetId(), in.DropTaskIDs) {
			dropped = append(dropped, task.GetId())
			continue
		}
		blocking = append(blocking, task.GetId())
		missing = append(missing, refusal.Missing...)
		if required == nil {
			required = refusal.Required
		}
		if refusal.Code == SessionPolicyTargetUnresolvedCode {
			code = SessionPolicyTargetUnresolvedCode
			detail = refusal.Detail
		}
	}
	if len(blocking) > 0 {
		if code == SessionPolicyTargetUnresolvedCode && len(blocking) > 1 {
			detail = "enabled automations move funds to a target the grant cannot resolve"
		}
		return dropped, &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            code,
			Detail:          detail,
			PolicyID:        usablePolicyID,
			AffectedTaskIDs: blocking,
			Missing:         missing,
			Required:        required,
		}
	}
	if ids, over := combinedSpendShortfall(policy, covered); over {
		return dropped, &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          combinedSpendShortDetail,
			PolicyID:        usablePolicyID,
			AffectedTaskIDs: ids,
		}
	}
	for _, extra := range spared {
		trial := append(append([]runnerSpend{}, covered...), extra)
		if _, over := combinedSpendShortfall(policy, trial); over {
			dropped = append(dropped, extra.id)
		}
	}
	return dropped, nil
}

const combinedSpendShortDetail = "this grant's spend cap is below the combined remaining spend of enabled automations"

// runnerSpend is one enabled task whose own need fits the grant.
type runnerSpend struct {
	id   string
	need *WorkflowNeed
}

// combinedSpendShortfall reports tasks whose tokens or native value, added
// together, exceed the grant. Ids stay in task order. A sum above uint256
// cannot fit any legal cap.
func combinedSpendShortfall(policy *model.SessionPolicy, tasks []runnerSpend) (ids []string, short bool) {
	tokenSum := map[common.Address]*big.Int{}
	var nativeSum *big.Int
	type spendRow struct {
		id     string
		tokens map[common.Address]struct{}
		native bool
	}
	var rows []spendRow
	for _, task := range tasks {
		if task.need == nil {
			continue
		}
		row := spendRow{id: task.id, tokens: map[common.Address]struct{}{}}
		for _, cap := range task.need.Caps {
			if cap.Token == nil {
				continue
			}
			amt, err := parseCapAmount(cap.Amount)
			if err != nil {
				continue
			}
			sum := tokenSum[*cap.Token]
			if sum == nil {
				sum = new(big.Int)
				tokenSum[*cap.Token] = sum
			}
			sum.Add(sum, amt)
			row.tokens[*cap.Token] = struct{}{}
		}
		if task.need.NativeSpendCap != nil {
			amt, err := parseCapAmount(task.need.NativeSpendCap.Amount)
			if err == nil {
				if nativeSum == nil {
					nativeSum = new(big.Int)
				}
				nativeSum.Add(nativeSum, amt)
				row.native = true
			}
		}
		if len(row.tokens) > 0 || row.native {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, false
	}
	have := map[common.Address]*big.Int{}
	for _, cap := range policyCaps(policy) {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			continue
		}
		have[*cap.Token] = amt
	}
	shortTokens := map[common.Address]bool{}
	for token, sum := range tokenSum {
		if sum.Cmp(maxUint256) > 0 {
			shortTokens[token] = true
			continue
		}
		got := have[token]
		if got == nil || got.Cmp(sum) < 0 {
			shortTokens[token] = true
		}
	}
	nativeShort := false
	if nativeSum != nil && (nativeSum.Cmp(maxUint256) > 0 || policy == nil || policy.NativeSpendCap == nil) {
		nativeShort = true
	} else if nativeSum != nil {
		got, err := parseCapAmount(policy.NativeSpendCap.Amount)
		if err != nil || got.Cmp(nativeSum) < 0 {
			nativeShort = true
		}
	}
	if len(shortTokens) == 0 && !nativeShort {
		return nil, false
	}
	for _, row := range rows {
		hit := row.native && nativeShort
		if !hit {
			for token := range row.tokens {
				if shortTokens[token] {
					hit = true
					break
				}
			}
		}
		if hit {
			ids = append(ids, row.id)
		}
	}
	return ids, true
}

// tasksExceptDropped removes tasks the client named in dropTaskIds. The
// merged grant does not include their spend, and submit must receive the
// same ids or it refuses them as uncovered.
func tasksExceptDropped(tasks []*avsproto.Task, drop []string) (kept []*avsproto.Task, dropped []string) {
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if taskDropped(task.GetId(), drop) {
			if id := strings.TrimSpace(task.GetId()); id != "" {
				dropped = append(dropped, id)
			}
			continue
		}
		kept = append(kept, task)
	}
	return kept, dropped
}

func unresolvedTargetConflict(needs []*WorkflowNeed) *PolicyConflictError {
	var ids []string
	var first *WorkflowNeed
	for _, need := range needs {
		if need == nil {
			continue
		}
		if first == nil {
			first = need
		}
		if id := strings.TrimSpace(need.TaskID); id != "" {
			ids = append(ids, id)
		}
	}
	detail := "moves funds to a target the grant cannot resolve"
	if len(ids) > 1 {
		detail = "enabled automations move funds to a target the grant cannot resolve"
	} else if first != nil {
		detail = fmt.Sprintf("%s moves funds to a target the grant cannot resolve", displayName(first.Name))
	}
	return &PolicyConflictError{
		Sentinel:        ErrSessionPolicyNotCovering,
		Code:            SessionPolicyTargetUnresolvedCode,
		Detail:          detail,
		AffectedTaskIDs: ids,
		Required:        first,
	}
}

func taskDropped(id string, drop []string) bool {
	for _, d := range drop {
		if strings.EqualFold(strings.TrimSpace(d), id) {
			return true
		}
	}
	return false
}

// enabledTasksForRunner lists this owner and runner's task-index keys.
// It does not scan the whole database.
func (n *Engine) enabledTasksForRunner(owner, wallet common.Address) ([]*model.Workflow, error) {
	if n == nil || n.db == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	var prefixes []string
	for _, b := range n.chainSmartWalletPrefixesBytes(owner, wallet) {
		prefixes = append(prefixes, string(b))
	}
	keys, err := n.db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, err
	}
	var out []*model.Workflow
	for _, key := range keys {
		parsed, perr := ParseUserTaskKey([]byte(key))
		if perr != nil && !errors.Is(perr, ErrLegacyKey) {
			return nil, fmt.Errorf("task index %s: %w", key, perr)
		}
		statusValue, err := n.db.GetKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("task index %s: %w", key, err)
		}
		statusInt, convErr := strconv.Atoi(string(statusValue))
		if convErr != nil {
			return nil, fmt.Errorf("task index %s: %w", key, convErr)
		}
		if avsproto.TaskStatus(statusInt) != avsproto.TaskStatus_Enabled {
			continue
		}
		raw, err := n.db.GetKey(n.findTaskKey(parsed.TaskID, avsproto.TaskStatus_Enabled))
		if err != nil {
			return nil, fmt.Errorf("loading enabled task %s: %w", parsed.TaskID, err)
		}
		task := model.NewWorkflow()
		if err := task.FromStorageData(raw); err != nil {
			return nil, fmt.Errorf("decoding enabled task %s: %w", parsed.TaskID, err)
		}
		task.Id = parsed.TaskID
		if task.Status != avsproto.TaskStatus_Enabled {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

// authorizationRank orders simulate statuses when a workflow spans chains.
// An unresolved target on one chain must not hide a missing grant or an
// observed call the grant does not allow on another. One chain still
// decides inside BuildAuthorization, which returns no_grant first.
func authorizationRank(status string) int {
	switch status {
	case AuthNotCovered:
		return 7
	case AuthNoGrant:
		return 6
	case AuthTargetUnresolved:
		return 5
	case AuthCapNeedsInput:
		return 4
	case AuthCapTooLow:
		return 3
	case AuthExpiresTooSoon:
		return 2
	default:
		return 1
	}
}

// fillSimulateAuthorization writes the opt-in verdict onto the context
// the handler allocated. Enforce mode leaves it unset.
func (n *Engine) fillSimulateAuthorization(ctx context.Context, user *model.User, task *model.Workflow, vm *VM, chainID int64) {
	auth := SimulateAuthFrom(ctx)
	if auth == nil || !auth.Report || task == nil || task.Task == nil {
		return
	}
	sched := scheduleFromTask(task.Task, time.Now())
	if auth.MaxExecution > 0 {
		sched.MaxExecution = auth.MaxExecution
	}
	if auth.StartAt != 0 {
		sched.StartAt = auth.StartAt
	}
	if auth.ExpiredAt != 0 {
		sched.ExpiredAt = auth.ExpiredAt
	}
	var settings map[string]any
	if vm != nil && vm.vars != nil {
		if raw, ok := vm.vars["settings"].(map[string]any); ok {
			settings = raw
		}
	}
	if chainID <= 0 && n != nil {
		chainID = n.defaultChainID()
	}
	needs := DeriveWorkflowNeeds(task.Task, settings, sched, chainID)
	var report *SessionGrantReport
	if vm != nil {
		report = vm.sessionGrantReport
	}
	if len(needs) == 0 {
		auth.Result = BuildAuthorization(nil, nil, report, sched)
		return
	}
	var worst *SessionAuthorization
	for chain, need := range needs {
		if need == nil || !need.HasFundMove {
			continue
		}
		var policy *model.SessionPolicy
		if report != nil && chain == chainID {
			policy, _, _, _, _, _, _, _, _ = report.snapshot()
		}
		if policy == nil && vm != nil && vm.db != nil && user != nil {
			if sender := getAASenderAddress(vm); sender != nil {
				got, err := ActiveSessionPolicyForWallet(vm.db, chain, user.Address, *sender)
				if err != nil {
					auth.Result = &SessionAuthorization{Status: AuthNoGrant, Detail: err.Error(), Required: need}
					return
				}
				policy = got
			}
		}
		var chainReport *SessionGrantReport
		if chain == chainID || len(needs) == 1 {
			chainReport = report
		}
		got := BuildAuthorization(policy, need, chainReport, sched)
		if worst == nil || authorizationRank(got.Status) > authorizationRank(worst.Status) {
			worst = got
		}
	}
	if worst == nil {
		worst = BuildAuthorization(nil, nil, report, sched)
	}
	auth.Result = worst
}

func (n *Engine) sessionPolicyDeployCheckEnabled() bool {
	return n != nil && n.config != nil && n.config.SessionPolicyDeployCheck
}

// enforceSessionPolicyDeployCheck refuses create and resume when the flag
// is on and the runner's grant does not cover this workflow. The flag
// defaults off, so a nil engine config is a no-op.
//
// An expired grant on a fund-moving workflow is SESSION_POLICY_EXPIRED,
// including when the workflow has no computable end. That check stays in
// this caller. CoverageRefusal is also submit and carry, and an expired
// grant still has to count there. A grant with no recorded expiry is not
// expired. A notification-only workflow never reaches the check.
//
// Chains are visited in chain-id order. The first expired grant owns the
// response: its policy id and its chain's need. An earlier chain's coverage
// gap must not stay in required or missing, or a client renewing that
// policy id would sign the wrong actions.
//
// Needs are derived before any lock: the cron walk reads only the task
// being saved. On success the returned function holds sessionAuthorityLock
// for every Modular Account v2 chain this workflow moves funds on, and the
// caller must persist the task before releasing it. A concurrent submit on
// one of those chains then sees the new task. The function is safe to call
// more than once. A refusal or a storage error releases the locks before
// returning.
func (n *Engine) enforceSessionPolicyDeployCheck(user *model.User, task *model.Workflow) (func(), error) {
	noop := func() {}
	if !n.sessionPolicyDeployCheckEnabled() || task == nil || task.Task == nil {
		return noop, nil
	}
	needs := DeriveWorkflowNeeds(task.Task, nil, scheduleFromTask(task.Task, time.Now()), n.defaultChainID())
	chains := make([]int64, 0, len(needs))
	for chainID, need := range needs {
		if need != nil && need.HasFundMove {
			chains = append(chains, chainID)
		}
	}
	if len(chains) == 0 {
		return noop, nil
	}
	if !common.IsHexAddress(task.SmartWalletAddress) {
		return noop, fmt.Errorf("%w: workflow has no runner", ErrSessionPolicyNotCovering)
	}
	if user == nil {
		return noop, fmt.Errorf("%w: workflow has no owner", ErrSessionPolicyNotCovering)
	}
	runner := common.HexToAddress(task.SmartWalletAddress)
	sort.Slice(chains, func(i, j int) bool { return chains[i] < chains[j] })

	type checkedChain struct {
		chain int64
		need  *WorkflowNeed
	}
	var mav2 []checkedChain
	for _, chainID := range chains {
		cfg := n.ResolveSmartWalletConfig(chainID)
		if cfg == nil {
			if n.logger != nil {
				n.logger.Warn("session policy deploy check: chain has no smart wallet config",
					"chain_id", chainID, "task_id", task.GetId())
			}
			return noop, fmt.Errorf("%w: chain %d has no smart wallet config", ErrSessionPolicyNotCovering, chainID)
		}
		if !cfg.UsesModularAccountV2() {
			continue
		}
		mav2 = append(mav2, checkedChain{chain: chainID, need: needs[chainID]})
	}
	if len(mav2) == 0 {
		return noop, nil
	}

	// Shards are shared. Two chains can hash to one mutex, and locking
	// that mutex twice on this goroutine deadlocks. Distinct shards are
	// taken in shard-index order so two multi-chain creates cannot cross.
	chainIDs := make([]int64, len(mav2))
	for i, item := range mav2 {
		chainIDs[i] = item.chain
	}
	locks := orderedSessionLocks(chainIDs, user.Address, runner)
	var held []*sync.RWMutex
	released := false
	unlock := func() {
		if released {
			return
		}
		released = true
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
		held = nil
	}
	for _, mu := range locks {
		mu.Lock()
		held = append(held, mu)
	}

	var blocking []string
	var missing []model.AllowedAction
	var required *WorkflowNeed
	var policyID string
	var expiredUntil int64
	refusals := 0
	now := time.Now()
	for _, item := range mav2 {
		// The write lock is already held. ActiveSessionPolicyForWallet
		// would take the same mutex's read lock and deadlock.
		policy, err := activeSessionPolicyLocked(n.db, item.chain, user.Address, runner)
		if err != nil {
			unlock()
			return noop, err
		}
		// Expired() is false for a nil policy and for ValidUntil == 0.
		// Those stay on CoverageRefusal, which is also what submit and
		// carry use. An already-closed grant is "grant again".
		if item.need != nil && item.need.HasFundMove && policy.Expired(now) {
			refusals++
			if item.need.TaskID != "" {
				blocking = append(blocking, item.need.TaskID)
			}
			// The first expired grant replaces whatever an earlier
			// coverage gap already stored. Later chains keep their
			// task ids, but they do not take this policy's need.
			if expiredUntil == 0 {
				expiredUntil = policy.ValidUntil
				policyID = policy.ID
				required = item.need
			}
			continue
		}
		refusal := CoverageRefusal(policy, item.need)
		if refusal == nil {
			continue
		}
		refusals++
		if item.need != nil && item.need.TaskID != "" {
			blocking = append(blocking, item.need.TaskID)
		}
		missing = append(missing, refusal.Missing...)
		if required == nil {
			required = refusal.Required
		}
		if expiredUntil == 0 && refusal.PolicyID != "" {
			policyID = refusal.PolicyID
		}
	}
	if refusals == 0 {
		return unlock, nil
	}
	unlock()
	code := SessionPolicyNotCoveringCode
	detail := "this workflow's fund-moving steps are outside the runner's usable grant"
	if expiredUntil > 0 {
		code = SessionPolicyExpiredCode
		detail = fmt.Sprintf(
			"the runner's grant expired at %s; grant again before deploying this workflow",
			time.UnixMilli(expiredUntil).UTC().Format(time.RFC3339),
		)
		// missing was collected from other chains' coverage gaps. Those
		// actions belong to a different grant than policyID.
		missing = nil
	}
	return noop, &PolicyConflictError{
		Sentinel:        ErrSessionPolicyNotCovering,
		Code:            code,
		Detail:          detail,
		PolicyID:        policyID,
		AffectedTaskIDs: blocking,
		Missing:         missing,
		Required:        required,
	}
}
