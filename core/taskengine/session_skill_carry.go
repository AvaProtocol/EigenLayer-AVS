package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// GrantRemainder is the usable leftover of one grant.
//
// ERC20 contains only tokens that grant capped. A token that is absent was
// not capped and must not be read or treated as spent. Native is nil when
// the grant has no native cap; a non-nil zero is a spent native cap.
type GrantRemainder struct {
	ERC20  map[common.Address]*big.Int
	Native *big.Int
}

func (r GrantRemainder) clone() GrantRemainder {
	out := GrantRemainder{ERC20: map[common.Address]*big.Int{}}
	for token, amt := range r.ERC20 {
		if amt == nil {
			out.ERC20[token] = big.NewInt(0)
			continue
		}
		out.ERC20[token] = new(big.Int).Set(amt)
	}
	if r.Native != nil {
		out.Native = new(big.Int).Set(r.Native)
	}
	return out
}

func storedGrantRemainder(p *model.SessionPolicy) (GrantRemainder, error) {
	out := GrantRemainder{ERC20: map[common.Address]*big.Int{}}
	if p == nil {
		return out, nil
	}
	for _, cap := range policyCaps(p) {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			return GrantRemainder{}, fmt.Errorf("stored cap for %s: %w", cap.Token.Hex(), err)
		}
		out.ERC20[*cap.Token] = amt
	}
	if p.NativeSpendCap != nil {
		amt, err := parseCapAmount(p.NativeSpendCap.Amount)
		if err != nil {
			return GrantRemainder{}, fmt.Errorf("stored native cap: %w", err)
		}
		out.Native = amt
	}
	return out, nil
}

// resolveGrantRemainder returns the leftover a merge may keep. A grant that
// has not been applied contributes its stored caps and does not touch the
// chain: the entity is not installed, so a read of zero would look like a
// spent grant. An applied grant is read from the chain. An error is not
// replaced with the stored cap.
func (n *Engine) resolveGrantRemainder(ctx context.Context, policy *model.SessionPolicy, account common.Address) (GrantRemainder, error) {
	if policy == nil || policy.Grant == nil || !policy.Grant.Applied() {
		return storedGrantRemainder(policy)
	}
	caller, err := n.spendLimitCaller(policy.ChainID)
	if err != nil {
		return GrantRemainder{}, fmt.Errorf("reading the remaining spend limit: %w", err)
	}
	out := GrantRemainder{ERC20: map[common.Address]*big.Int{}}
	for _, cap := range policyCaps(policy) {
		if cap.Token == nil {
			continue
		}
		if ctx.Err() != nil {
			return GrantRemainder{}, fmt.Errorf("reading the remaining spend limit: %w", ctx.Err())
		}
		amt, err := aa.ReadERC20SpendLimit(ctx, caller, policy.EntityID, *cap.Token, account)
		if err != nil {
			return GrantRemainder{}, fmt.Errorf("reading the remaining spend limit: %w", err)
		}
		out.ERC20[*cap.Token] = amt
	}
	if policy.NativeSpendCap != nil {
		if ctx.Err() != nil {
			return GrantRemainder{}, fmt.Errorf("reading the remaining native limit: %w", ctx.Err())
		}
		amt, err := aa.ReadNativeSpendLimit(ctx, caller, policy.EntityID, account)
		if err != nil {
			return GrantRemainder{}, fmt.Errorf("reading the remaining native limit: %w", err)
		}
		out.Native = amt
	}
	return out, nil
}

func (n *Engine) spendLimitCaller(chainID int64) (aa.ContractCaller, error) {
	if n != nil && n.spendLimitCallerOverride != nil {
		return n.spendLimitCallerOverride(chainID)
	}
	if n == nil || !n.sessionChainReads {
		return nil, fmt.Errorf("session resolver is not installed")
	}
	reader := GetChainStateReaderForChain(uint64(chainID))
	if reader == nil {
		return nil, fmt.Errorf("no chain reader for chain %d", chainID)
	}
	return reader, nil
}

// mergeCarriedSkillGrant sizes the replacement from the current grant.
// Actions, native recipients, and allowContractRecipient only grow, except
// a transfer or approve whose final cap is not positive: that row is omitted
// rather than installed at zero. An unsized or unresolved running task does
// not fail this path.
func mergeCarriedSkillGrant(current *model.SessionPolicy, remainder GrantRemainder, addition PolicyAddition, tasks []*avsproto.Task, chainID int64, now time.Time, expiresIn time.Duration) (SessionPermissions, PolicyChanges, error) {
	addCaps := map[common.Address]*big.Int{}
	for _, cap := range addition.SpendCaps {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation cap %s: %v", ErrSessionPolicyUnsized, cap.Token.Hex(), err)
		}
		if addCaps[*cap.Token] == nil {
			addCaps[*cap.Token] = big.NewInt(0)
		}
		addCaps[*cap.Token].Add(addCaps[*cap.Token], amt)
		if addCaps[*cap.Token].Cmp(maxUint256) > 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation cap %s exceeds uint256", ErrSessionPolicyUnsized, cap.Token.Hex())
		}
	}
	var addNative *big.Int
	if addition.NativeSpendCap != nil {
		amt, err := parseCapAmount(addition.NativeSpendCap.Amount)
		if err != nil {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation native cap: %v", ErrSessionPolicyUnsized, err)
		}
		if amt.Cmp(maxUint256) > 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: new automation native cap exceeds uint256", ErrSessionPolicyUnsized)
		}
		addNative = amt
	}

	var fundNeeds []*WorkflowNeed
	for _, task := range tasks {
		if task == nil {
			continue
		}
		need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), chainID)[chainID]
		if need == nil || !need.HasFundMove {
			continue
		}
		fundNeeds = append(fundNeeds, need)
	}

	// Installing NativeTokenLimitModule is what starts metering payable value
	// and self-funded gas. An unsized payable on a wallet that has no native
	// cap yet cannot be given a number, so the module is not installed.
	// A wallet that already has a native cap keeps that remainder instead.
	if addNative != nil && remainder.Native == nil {
		for _, need := range fundNeeds {
			if !need.NativeUnsized {
				continue
			}
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s (%s): a native cap cannot be added while its payable value cannot be sized",
				ErrSessionNativeCapUnsized, need.TaskID, displayName(need.Name))
		}
	}

	actions := map[string]map[string]struct{}{}
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
	if current != nil {
		addActions(current.AllowedActions)
	}
	addActions(addition.AllowedActions)

	needCaps := map[common.Address]*big.Int{}
	needNative := big.NewInt(0)
	for _, need := range fundNeeds {
		if need.Unresolved {
			continue
		}
		addActions(need.Actions)
		for _, cap := range need.Caps {
			if cap.Token == nil {
				continue
			}
			amt, err := parseCapAmount(cap.Amount)
			if err != nil {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s: %v", ErrSessionPolicyUnsized, need.TaskID, err)
			}
			if needCaps[*cap.Token] == nil {
				needCaps[*cap.Token] = big.NewInt(0)
			}
			needCaps[*cap.Token].Add(needCaps[*cap.Token], amt)
			if needCaps[*cap.Token].Cmp(maxUint256) > 0 {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s cap exceeds uint256", ErrSessionPolicyUnsized, need.TaskID)
			}
		}
		if need.NativeSpendCap != nil && !need.NativeUnsized {
			amt, err := parseCapAmount(need.NativeSpendCap.Amount)
			if err != nil {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s native: %v", ErrSessionPolicyUnsized, need.TaskID, err)
			}
			needNative.Add(needNative, amt)
			if needNative.Cmp(maxUint256) > 0 {
				return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: task %s native cap exceeds uint256", ErrSessionPolicyUnsized, need.TaskID)
			}
		}
	}

	finalCaps := map[common.Address]*big.Int{}
	seen := map[common.Address]struct{}{}
	for token := range remainder.ERC20 {
		seen[token] = struct{}{}
	}
	for token := range needCaps {
		seen[token] = struct{}{}
	}
	for token := range addCaps {
		seen[token] = struct{}{}
	}
	for token := range seen {
		rem := big.NewInt(0)
		if amt := remainder.ERC20[token]; amt != nil {
			rem = amt
		}
		need := big.NewInt(0)
		if amt := needCaps[token]; amt != nil {
			need = amt
		}
		base := rem
		if need.Cmp(base) > 0 {
			base = need
		}
		total := new(big.Int).Set(base)
		if extra := addCaps[token]; extra != nil {
			total = new(big.Int).Add(total, extra)
		}
		if total.Cmp(maxUint256) > 0 {
			return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: cap %s exceeds uint256", ErrSessionPolicyUnsized, token.Hex())
		}
		finalCaps[token] = total
	}

	for target, sels := range actions {
		if !selectorSetIsSpendOnly(sels) {
			continue
		}
		addr := common.HexToAddress(target)
		amt := finalCaps[addr]
		if amt == nil || amt.Sign() <= 0 {
			delete(actions, target)
		}
	}

	mergedActions := actionsFromSet(actions)
	var spendCaps []model.ERC20SpendCap
	for token, amt := range finalCaps {
		token := token
		if amt == nil || amt.Sign() <= 0 {
			continue
		}
		if !actionHasTarget(mergedActions, token) || !transferApproveOnly(token, mergedActions) {
			continue
		}
		spendCaps = append(spendCaps, model.ERC20SpendCap{Token: &token, Amount: amt.String()})
	}
	sortSpendCaps(spendCaps)

	remNative := big.NewInt(0)
	if remainder.Native != nil {
		remNative = remainder.Native
	}
	baseNative := remNative
	if needNative.Cmp(baseNative) > 0 {
		baseNative = needNative
	}
	finalNative := new(big.Int).Set(baseNative)
	if addNative != nil {
		finalNative = new(big.Int).Add(finalNative, addNative)
	}
	if finalNative.Cmp(maxUint256) > 0 {
		return SessionPermissions{}, PolicyChanges{}, fmt.Errorf("%w: native cap exceeds uint256", ErrSessionPolicyUnsized)
	}

	var recipients []*common.Address
	if finalNative.Sign() > 0 {
		if current != nil {
			recipients = append(recipients, current.NativeRecipients...)
		}
		recipients = append(recipients, addition.NativeRecipients...)
		for _, need := range fundNeeds {
			if need.Unresolved {
				continue
			}
			recipients = append(recipients, need.NativeRecipients...)
		}
		recipients = dedupeRecipients(recipients)
	}

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
	if finalNative.Sign() > 0 {
		perms.NativeSpendCap = &model.NativeSpendCap{Amount: finalNative.String()}
	}

	horizon := addition.ValidUntilMs
	if horizon == 0 {
		horizon = now.Add(expiresIn).UnixMilli()
	}
	latest := horizon
	if current != nil && current.ValidUntil > latest {
		latest = current.ValidUntil
	}
	for _, need := range fundNeeds {
		if need.ValidUntilMs > latest {
			latest = need.ValidUntilMs
		}
	}
	floor := now.Add(60 * time.Second).UnixMilli()
	if latest < floor {
		latest = floor
	}
	perms.ValidUntilMs = latest

	remCopy := remainder.clone()
	changes := diffGrant(current, &remCopy, perms, fundNeeds, latest)
	return perms, changes, nil
}

func selectorSetIsSpendOnly(sels map[string]struct{}) bool {
	if len(sels) == 0 {
		return false
	}
	for sel := range sels {
		if sel != selectorTransfer && sel != selectorApprove {
			return false
		}
	}
	return true
}

func sortSpendCaps(spendCaps []model.ERC20SpendCap) {
	// Local wrapper so the carried merge sorts the same way as the fresh one.
	if len(spendCaps) < 2 {
		return
	}
	for i := 1; i < len(spendCaps); i++ {
		for j := i; j > 0; j-- {
			left, right := spendCaps[j-1].Token, spendCaps[j].Token
			if left == nil || right == nil {
				break
			}
			if strings.ToLower(left.Hex()) <= strings.ToLower(right.Hex()) {
				break
			}
			spendCaps[j-1], spendCaps[j] = spendCaps[j], spendCaps[j-1]
		}
	}
}

func sameGrantID(a, b *model.SessionPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.EqualFold(a.ID, b.ID)
}

func sameSessionPermissions(a, b SessionPermissions) bool {
	if a.ValidUntilMs != b.ValidUntilMs || a.AllowContractRecipient != b.AllowContractRecipient {
		return false
	}
	if !sameActionSets(a.AllowedActions, b.AllowedActions) {
		return false
	}
	if !sameCapSets(a, b) {
		return false
	}
	if !sameRecipientSets(a.NativeRecipients, b.NativeRecipients) {
		return false
	}
	return sameNativeAmount(a.NativeSpendCap, b.NativeSpendCap)
}

func sameActionSets(a, b []model.AllowedAction) bool {
	left, right := actionSet(a), actionSet(b)
	if len(left) != len(right) {
		return false
	}
	for target, sels := range left {
		other := right[target]
		if len(sels) != len(other) {
			return false
		}
		for sel := range sels {
			if _, ok := other[sel]; !ok {
				return false
			}
		}
	}
	return true
}

func permissionCaps(p SessionPermissions) []model.ERC20SpendCap {
	if len(p.SpendCaps) > 0 {
		return p.SpendCaps
	}
	if p.SpendCap != nil {
		return []model.ERC20SpendCap{*p.SpendCap}
	}
	return nil
}

func sameCapSets(a, b SessionPermissions) bool {
	left, right := map[common.Address]*big.Int{}, map[common.Address]*big.Int{}
	for _, cap := range permissionCaps(a) {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			return false
		}
		left[*cap.Token] = amt
	}
	for _, cap := range permissionCaps(b) {
		if cap.Token == nil {
			continue
		}
		amt, err := parseCapAmount(cap.Amount)
		if err != nil {
			return false
		}
		right[*cap.Token] = amt
	}
	if len(left) != len(right) {
		return false
	}
	for token, amt := range left {
		other := right[token]
		if other == nil || amt.Cmp(other) != 0 {
			return false
		}
	}
	return true
}

func sameRecipientSets(a, b []*common.Address) bool {
	left, right := map[common.Address]struct{}{}, map[common.Address]struct{}{}
	for _, rec := range a {
		if rec != nil && *rec != (common.Address{}) {
			left[*rec] = struct{}{}
		}
	}
	for _, rec := range b {
		if rec != nil && *rec != (common.Address{}) {
			right[*rec] = struct{}{}
		}
	}
	if len(left) != len(right) {
		return false
	}
	for addr := range left {
		if _, ok := right[addr]; !ok {
			return false
		}
	}
	return true
}

func sameNativeAmount(a, b *model.NativeSpendCap) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, lerr := parseCapAmount(a.Amount)
	right, rerr := parseCapAmount(b.Amount)
	if lerr != nil || rerr != nil {
		return false
	}
	return left.Cmp(right) == 0
}

func newLimitMoved(current *model.SessionPolicy) *PolicyConflictError {
	return newBaseDetail(current, "the remaining limit moved and the grant id did not; prepare again")
}

func newRunningSetChanged(current *model.SessionPolicy) *PolicyConflictError {
	return newBaseDetail(current, "the running set changed and the grant id did not; prepare again")
}

// skillSubmitMergeError keeps a merge failure that the running set did not
// cause. A native-cap refusal always keeps its own text. Any other failure
// keeps its own text when the enabled task ids still match the set prepare
// signed against. A different set is reported as the running set having
// changed; the next prepare returns the real error.
func skillSubmitMergeError(current *model.SessionPolicy, mergeErr error, prepared, now []*avsproto.Task) error {
	if mergeErr == nil || errors.Is(mergeErr, ErrSessionNativeCapUnsized) || sameRunningTaskIDs(prepared, now) {
		return mergeErr
	}
	return newRunningSetChanged(current)
}

func sameRunningTaskIDs(prepared, now []*avsproto.Task) bool {
	left := taskIDCounts(prepared)
	right := taskIDCounts(now)
	if len(left) != len(right) {
		return false
	}
	for id, count := range left {
		if right[id] != count {
			return false
		}
	}
	return true
}

func taskIDCounts(tasks []*avsproto.Task) map[string]int {
	counts := map[string]int{}
	for _, task := range tasks {
		if task == nil || task.GetId() == "" {
			continue
		}
		counts[task.GetId()]++
	}
	return counts
}

func newLimitAndSetChanged(current *model.SessionPolicy) *PolicyConflictError {
	return newBaseDetail(current, "the remaining limit moved and the running set changed; prepare again")
}

func newBaseDetail(current *model.SessionPolicy, detail string) *PolicyConflictError {
	id := ""
	if current != nil {
		id = current.ID
	}
	return &PolicyConflictError{
		Sentinel: ErrSessionPolicyBaseChanged,
		Code:     SessionPolicyBaseChangedCode,
		Detail:   detail,
		PolicyID: id,
	}
}

// usableCoverageSentinel is not a contract the grant allows. It exists so a
// spent-out allowlist is not empty: an empty allowlist is not a miss.
var usableCoverageSentinel = common.HexToAddress("0x0000000000000000000000000000000000000001")

// usableCoveragePolicy is the current grant with spent-out caps removed.
// A transfer or approve whose remainder is zero is not coverage, so dropping
// it from the replacement is not a regression. A router row is not a cap and
// stays. Native is the same: a zero native cap does not cover recipients.
func usableCoveragePolicy(policy *model.SessionPolicy, rem GrantRemainder) *model.SessionPolicy {
	if policy == nil {
		return nil
	}
	clone := *policy
	var actions []model.AllowedAction
	for _, action := range policy.AllowedActions {
		if action.Target == nil {
			continue
		}
		if transferApproveOnly(*action.Target, policy.AllowedActions) {
			amt := rem.ERC20[*action.Target]
			if amt == nil || amt.Sign() <= 0 {
				continue
			}
		}
		actions = append(actions, action)
	}
	// MissingGrantCalls treats an empty allowlist as "nothing is missing".
	// A grant whose every spend row was spent out would then still look
	// like coverage. One stand-in target keeps the dropped token a miss
	// without matching any real contract.
	if len(actions) == 0 && len(policy.AllowedActions) > 0 {
		sentinel := usableCoverageSentinel
		actions = []model.AllowedAction{{
			Target:    &sentinel,
			Selectors: []string{"0x00000000"},
		}}
	}
	clone.AllowedActions = actions
	var caps []model.ERC20SpendCap
	for token, amt := range rem.ERC20 {
		token := token
		if amt == nil || amt.Sign() <= 0 {
			continue
		}
		if !actionHasTarget(actions, token) {
			continue
		}
		caps = append(caps, model.ERC20SpendCap{Token: &token, Amount: amt.String()})
	}
	sortSpendCaps(caps)
	clone.ERC20SpendCaps = caps
	clone.ERC20SpendCap = nil
	if len(caps) > 0 {
		first := caps[0]
		clone.ERC20SpendCap = &first
	}
	if rem.Native == nil || rem.Native.Sign() <= 0 {
		clone.NativeSpendCap = nil
		clone.NativeRecipients = nil
	} else {
		clone.NativeSpendCap = &model.NativeSpendCap{Amount: rem.Native.String()}
	}
	return &clone
}

func coverageRegressed(current, proposed *model.SessionPolicy, need *WorkflowNeed) bool {
	if CoverageRefusal(current, need) != nil {
		return false
	}
	return CoverageRefusal(proposed, need) != nil
}

// classifyCarriedCoverage refuses a submit only when the new grant covers
// less of an enabled task than the current grant's usable remainder did.
// A task the current grant already does not cover does not freeze the wallet.
func classifyCarriedCoverage(current *model.SessionPolicy, rem GrantRemainder, in SessionPolicyInput, tasks []*avsproto.Task, now time.Time) (dropped []string, err error) {
	usable := usableCoveragePolicy(current, rem)
	proposed := permissionsAsPolicy(in)
	usableID := ""
	if current != nil {
		usableID = current.ID
	}
	var blocking []string
	var missing []model.AllowedAction
	var required *WorkflowNeed
	var covered []runnerSpend
	var spared []runnerSpend
	detail := "this grant would leave an enabled automation uncovered"
	for _, task := range tasks {
		if task == nil {
			continue
		}
		need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), in.ChainID)[in.ChainID]
		if need == nil || !need.HasFundMove {
			continue
		}
		if !coverageRegressed(usable, proposed, need) {
			if CoverageRefusal(proposed, need) == nil {
				row := runnerSpend{id: task.GetId(), need: need}
				if taskDropped(task.GetId(), in.DropTaskIDs) {
					spared = append(spared, row)
				} else {
					covered = append(covered, row)
				}
			}
			continue
		}
		refusal := CoverageRefusal(proposed, need)
		if taskDropped(task.GetId(), in.DropTaskIDs) {
			dropped = append(dropped, task.GetId())
			continue
		}
		blocking = append(blocking, task.GetId())
		if refusal != nil {
			missing = append(missing, refusal.Missing...)
			if required == nil {
				required = refusal.Required
				if refusal.Detail != "" {
					detail = refusal.Detail
				}
			}
		}
	}
	if len(blocking) > 0 {
		return dropped, &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          detail,
			PolicyID:        usableID,
			AffectedTaskIDs: blocking,
			Missing:         missing,
			Required:        required,
		}
	}
	if ids, over := combinedSpendShortfall(proposed, covered); over {
		return dropped, &PolicyConflictError{
			Sentinel:        ErrSessionPolicyNotCovering,
			Code:            SessionPolicyNotCoveringCode,
			Detail:          combinedSpendShortDetail,
			PolicyID:        usableID,
			AffectedTaskIDs: ids,
		}
	}
	for _, extra := range spared {
		trial := append(append([]runnerSpend{}, covered...), extra)
		if _, over := combinedSpendShortfall(proposed, trial); over {
			dropped = append(dropped, extra.id)
		}
	}
	return dropped, nil
}

// skillPrepareSnapshot is the addition and the tasks prepare merged, so submit
// can derive the grant again from a fresh chain read. The client echoes the
// merged permissions, not the addition. Prepare does not store a grant.
// The cache that holds these is bounded; see skillPrepareCache.
type skillPrepareSnapshot struct {
	owner      common.Address
	wallet     common.Address
	chainID    int64
	addition   PolicyAddition
	expiresIn  time.Duration
	preparedAt time.Time
	remainder  *GrantRemainder
	tasks      []*avsproto.Task
	savedAt    time.Time
}

func (s *skillPrepareSnapshot) matches(owner common.Address, in SessionPolicyInput) bool {
	return s != nil && s.chainID == in.ChainID && s.owner == owner && s.wallet == in.Wallet
}

func cloneTasks(tasks []*avsproto.Task) []*avsproto.Task {
	out := make([]*avsproto.Task, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		cloned, ok := proto.Clone(task).(*avsproto.Task)
		if !ok || cloned == nil {
			continue
		}
		out = append(out, cloned)
	}
	return out
}

func explainSkillDrift(current *model.SessionPolicy, snap *skillPrepareSnapshot, fresh *GrantRemainder, newTasks []*avsproto.Task, signed SessionPermissions) *PolicyConflictError {
	if current == nil || snap == nil {
		return newRunningSetChanged(current)
	}
	// newTasks + the remainder prepare signed against. If this still matches,
	// the running set is not what moved the grant.
	byNewTasks, _, errTasks := MergeSkillGrantWithRemainder(current, snap.remainder, snap.addition, newTasks, snap.chainID, snap.preparedAt, snap.expiresIn)
	// old tasks + the remainder submit just read. If this still matches, the
	// remaining limit is not what moved the grant.
	byNewRem, _, errRem := MergeSkillGrantWithRemainder(current, fresh, snap.addition, snap.tasks, snap.chainID, snap.preparedAt, snap.expiresIn)
	tasksMoved := errTasks != nil || !sameSessionPermissions(byNewTasks, signed)
	remMoved := errRem != nil || !sameSessionPermissions(byNewRem, signed)
	switch {
	case remMoved && tasksMoved:
		return newLimitAndSetChanged(current)
	case remMoved:
		return newLimitMoved(current)
	default:
		return newRunningSetChanged(current)
	}
}
