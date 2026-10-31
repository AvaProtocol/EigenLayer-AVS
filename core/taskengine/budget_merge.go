package taskengine

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// BudgetMergeInput contains the trusted remainder snapshot and the owner's
// unsigned intent. Pricing supplies GasHeadroom; clients never choose it here.
type BudgetMergeInput struct {
	Current     *model.SessionPolicy
	Remainder   *GrantRemainder
	Budget      *WalletBudgetInput
	Addition    *PolicyAddition
	Tasks       []*avsproto.Task
	ChainID     int64
	Now         time.Time
	GasHeadroom *big.Int
}

// BudgetPolicyDraft is not a preparation or a usable permission. In particular
// a zero StartedAt means the stored owner handoff must assign the submit time. No
// merge writes storage, signs, retires the base, or activates these totals.
type BudgetPolicyDraft struct {
	Permissions     SessionPermissions
	StartAmounts    []model.ERC20SpendCap
	StartNative     *model.NativeSpendCap
	StartedAt       int64
	StartsNewTotals bool
	Changes         PolicyChanges
}

func MergeBudgetPolicy(in BudgetMergeInput) (*BudgetPolicyDraft, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if in.ChainID <= 0 {
		return nil, budgetInputError("chainId is required")
	}
	if in.Current != nil && in.Current.ChainID != in.ChainID {
		return nil, budgetInputError("the base permission is on another chain")
	}
	if in.Budget == nil && !isWalletBudget(in.Current) {
		return nil, budgetInputError("a first install or migration needs owner budget totals")
	}
	if in.Budget == nil && in.Addition == nil {
		return nil, &BudgetPolicyError{Code: BudgetReplaceRefusedCode, Detail: "a wallet budget needs an additive request or an owner budget edit"}
	}
	if in.Budget != nil {
		if err := in.Budget.validate(in.Now); err != nil {
			return nil, err
		}
	}
	reset := in.Budget != nil && in.Budget.StartNewTotals
	removeOnly := in.Budget != nil && len(in.Budget.RemoveActions) > 0 && !reset && len(in.Budget.ERC20) == 0 && in.Budget.Native == nil
	if removeOnly && (in.Current == nil || !isWalletBudget(in.Current) || in.Addition != nil || in.Budget.ValidUntil != in.Current.ValidUntil || in.GasHeadroom != nil && in.GasHeadroom.Sign() != 0) {
		return nil, budgetInputError("removeActions requires an existing budget and must keep its end and other totals unchanged")
	}
	rem, err := budgetMergeRemainder(in.Current, in.Remainder)
	if err != nil {
		return nil, err
	}
	totals, nativeTotal, err := budgetMergeTotals(in.Current, rem)
	if err != nil {
		return nil, err
	}
	add := PolicyAddition{}
	if in.Addition != nil {
		add = *in.Addition
	}
	requested := map[common.Address]*big.Int{}
	if in.Budget != nil {
		for _, cap := range in.Budget.ERC20 {
			requested[*cap.Token], _ = parseBudgetAmount(cap.Amount)
		}
	}
	if reset && in.Current != nil {
		for token := range rem.ERC20 {
			if requested[token] == nil {
				return nil, budgetInputError("startNewTotals requires a total for existing token %s", token.Hex())
			}
		}
		if in.Current.NativeSpendCap != nil && in.Budget.Native == nil {
			return nil, budgetInputError("startNewTotals requires the existing native total")
		}
	}
	addCaps := map[common.Address]*big.Int{}
	for _, cap := range add.SpendCaps {
		if cap.Token == nil || *cap.Token == (common.Address{}) {
			return nil, budgetInputError("an addition cap has no token")
		}
		amount, err := parseCapAmount(cap.Amount)
		if err != nil {
			return nil, budgetInputError("invalid addition cap: %v", err)
		}
		if addCaps[*cap.Token] == nil {
			addCaps[*cap.Token] = big.NewInt(0)
		}
		addCaps[*cap.Token].Add(addCaps[*cap.Token], amount)
		if addCaps[*cap.Token].BitLen() > 256 {
			return nil, budgetInputError("addition cap exceeds uint256")
		}
	}
	keys := map[common.Address]bool{}
	for token := range totals {
		keys[token] = true
	}
	for token := range requested {
		keys[token] = true
	}
	for token := range addCaps {
		keys[token] = true
	}
	out := &BudgetPolicyDraft{StartsNewTotals: in.Current == nil || !isWalletBudget(in.Current) || reset}
	if !out.StartsNewTotals {
		out.StartedAt = in.Current.BudgetStartedAt
	}
	perms := SessionPermissions{budgetMode: true}
	for token := range keys {
		amount, left, err := mergeBudgetAmount(totals[token], rem.ERC20[token], requested[token], addCaps[token], isWalletBudget(in.Current), reset)
		if err != nil {
			return nil, fmt.Errorf("token %s: %w", token.Hex(), err)
		}
		tokenCopy := token
		out.StartAmounts = append(out.StartAmounts, model.ERC20SpendCap{Token: &tokenCopy, Amount: amount.String()})
		perms.SpendCaps = append(perms.SpendCaps, model.ERC20SpendCap{Token: &tokenCopy, Amount: left.String()})
	}
	var nativeRequested, nativeAdd *big.Int
	if in.Budget != nil && in.Budget.Native != nil {
		nativeRequested, _ = parseBudgetAmount(in.Budget.Native.Amount)
	}
	if add.NativeSpendCap != nil {
		nativeAdd, err = parseCapAmount(add.NativeSpendCap.Amount)
		if err != nil {
			return nil, budgetInputError("invalid native addition: %v", err)
		}
	}
	if in.GasHeadroom != nil && (in.GasHeadroom.Sign() < 0 || in.GasHeadroom.BitLen() > 256) {
		return nil, budgetInputError("invalid priced gas headroom")
	}
	if nativeTotal != nil || nativeRequested != nil || nativeAdd != nil || in.GasHeadroom != nil && in.GasHeadroom.Sign() > 0 {
		amount, left, err := mergeBudgetAmount(nativeTotal, rem.Native, nativeRequested, nativeAdd, isWalletBudget(in.Current), reset)
		if err != nil {
			return nil, fmt.Errorf("native: %w", err)
		}
		if in.GasHeadroom != nil {
			amount.Add(amount, in.GasHeadroom)
			left.Add(left, in.GasHeadroom)
		}
		if amount.BitLen() > 256 || left.BitLen() > 256 {
			return nil, budgetInputError("native total plus gas exceeds uint256")
		}
		out.StartNative = &model.NativeSpendCap{Amount: amount.String()}
		perms.NativeSpendCap = &model.NativeSpendCap{Amount: left.String()}
	}
	var actionGroups [][]model.AllowedAction
	var recipients []*common.Address
	if in.Current != nil {
		actionGroups = append(actionGroups, in.Current.AllowedActions)
		recipients = append(recipients, in.Current.NativeRecipients...)
		perms.AllowContractRecipient = in.Current.AllowContractRecipient
		perms.ValidUntilMs = in.Current.ValidUntil
	}
	actionGroups = append(actionGroups, add.AllowedActions)
	recipients = append(recipients, add.NativeRecipients...)
	perms.AllowContractRecipient = perms.AllowContractRecipient || add.AllowContractRecipient
	if add.ValidUntilMs > perms.ValidUntilMs {
		perms.ValidUntilMs = add.ValidUntilMs
	}
	for _, task := range in.Tasks {
		if task == nil || removeOnly {
			continue
		}
		// Derive only targets/recipients. Run counts never contribute caps.
		sched := SkillSchedule{MaxExecution: 1, Now: in.Now}
		need := DeriveWorkflowNeeds(task, nil, sched, in.ChainID)[in.ChainID]
		if need == nil || !need.HasFundMove {
			continue
		}
		if need.Unresolved {
			if in.Current == nil {
				return nil, &PolicyConflictError{Sentinel: ErrSessionPolicyUnsized, Code: SessionPolicyTargetUnresolvedCode, Detail: "an enabled workflow has an unresolved fund-moving target", AffectedTaskIDs: []string{task.GetId()}}
			}
			continue // an unknown target never widens the carried permission
		}
		actionGroups = append(actionGroups, need.Actions)
		recipients = append(recipients, need.NativeRecipients...)
		if task.GetExpiredAt() > perms.ValidUntilMs {
			perms.ValidUntilMs = task.GetExpiredAt()
		}
	}
	var remove []model.AllowedAction
	if in.Budget != nil {
		remove = in.Budget.RemoveActions
		if len(remove) > 0 && in.Current == nil {
			return nil, budgetInputError("removeActions requires an existing permission")
		}
		if len(remove) > 0 && !removeOnly && !reset {
			return nil, budgetInputError("removeActions must carry existing totals or accompany startNewTotals")
		}
		if removeOnly {
			perms.ValidUntilMs = in.Current.ValidUntil
		} else if reset {
			perms.ValidUntilMs = in.Budget.ValidUntil
		} else {
			if in.Current != nil && in.Budget.ValidUntil < in.Current.ValidUntil {
				return nil, budgetNarrowing("shortening the budget requires startNewTotals")
			}
			if in.Budget.ValidUntil > perms.ValidUntilMs {
				perms.ValidUntilMs = in.Budget.ValidUntil
			}
		}
	}
	perms.AllowedActions, err = budgetActionUnion(actionGroups, remove)
	if err != nil {
		return nil, err
	}
	for _, action := range perms.AllowedActions {
		token := *action.Target
		if !transferApproveOnly(token, perms.AllowedActions) {
			continue
		}
		_, hasCap := keys[token]
		wasUncapped := rem.ERC20[token] == nil && budgetHasUnmeteredSpend(in.Current, token)
		if !hasCap && !wasUncapped {
			return nil, budgetInputError("a new spending token %s needs a budget total", token.Hex())
		}
		if hasCap && wasUncapped && !reset {
			return nil, budgetNarrowing("capping an existing unmetered token requires startNewTotals")
		}
	}
	perms.NativeRecipients = dedupeRecipients(recipients)
	var oldDelegates []model.DelegatedNativeRecipient
	if in.Current != nil {
		oldDelegates = in.Current.DelegatedNativeRecipients
	}
	perms.DelegatedNativeRecipients = carriedDelegatedRecipients(oldDelegates, add.DelegatedNativeRecipients, perms.NativeRecipients)
	sortSpendCaps(perms.SpendCaps)
	sortSpendCaps(out.StartAmounts)
	if err := perms.validate(false); err != nil {
		return nil, budgetInputError("invalid resulting budget: %v", err)
	}
	out.Permissions = perms
	out.Changes = diffGrant(in.Current, &rem, perms, nil, perms.ValidUntilMs)
	return out, nil
}

// Adding a selector must not hide an existing uncapped spending right. Such a
// right can become capped only in an explicit owner change, even if the same
// change also adds approve or removes a WETH deposit/withdraw selector.
func budgetHasUnmeteredSpend(current *model.SessionPolicy, token common.Address) bool {
	if current == nil {
		return false
	}
	for _, cap := range policyCaps(current) {
		if cap.Token != nil && *cap.Token == token {
			return false
		}
	}
	for _, action := range current.AllowedActions {
		if action.Target == nil || *action.Target != token {
			continue
		}
		for _, selector := range action.Selectors {
			if normalizeSelector(selector) == selectorTransfer || normalizeSelector(selector) == selectorApprove {
				return true
			}
		}
	}
	return false
}

func budgetMergeRemainder(current *model.SessionPolicy, read *GrantRemainder) (GrantRemainder, error) {
	out := GrantRemainder{ERC20: map[common.Address]*big.Int{}}
	if current == nil {
		return out, nil
	}
	if current.Grant.Applied() && read == nil {
		return out, fmt.Errorf("%w: an applied permission requires a fresh remainder snapshot", ErrBudgetReadUnavailable)
	}
	for _, cap := range policyCaps(current) {
		if cap.Token == nil || *cap.Token == (common.Address{}) || out.ERC20[*cap.Token] != nil {
			return out, fmt.Errorf("%w: stored cap has an invalid or repeated token", ErrBudgetReadUnavailable)
		}
		var value *big.Int
		if read != nil {
			value = read.ERC20[*cap.Token]
		} else {
			value, _ = parseBudgetAmount(cap.Amount)
		}
		if value == nil || value.Sign() < 0 || value.BitLen() > 256 {
			return out, fmt.Errorf("%w: missing or invalid token remainder", ErrBudgetReadUnavailable)
		}
		out.ERC20[*cap.Token] = new(big.Int).Set(value)
	}
	if current.NativeSpendCap != nil {
		var value *big.Int
		if read != nil {
			value = read.Native
		} else {
			value, _ = parseBudgetAmount(current.NativeSpendCap.Amount)
		}
		if value == nil || value.Sign() < 0 || value.BitLen() > 256 {
			return out, fmt.Errorf("%w: missing or invalid native remainder", ErrBudgetReadUnavailable)
		}
		out.Native = new(big.Int).Set(value)
	}
	return out, nil
}

func budgetMergeTotals(current *model.SessionPolicy, rem GrantRemainder) (map[common.Address]*big.Int, *big.Int, error) {
	if !isWalletBudget(current) {
		totals := cloneCaps(rem.ERC20)
		threshold := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
		if current != nil {
			for _, cap := range policyCaps(current) {
				if cap.Token == nil {
					continue
				}
				original, err := parseBudgetAmount(originalCap(cap.Amount, cap.GrantedCap))
				if err != nil {
					return nil, nil, fmt.Errorf("%w: invalid stored token amount: %v", ErrBudgetReadUnavailable, err)
				}
				if original.Cmp(threshold) >= 0 {
					totals[*cap.Token] = original
				}
			}
		}
		native := cloneBudgetInt(rem.Native)
		if current != nil && current.NativeSpendCap != nil {
			original, err := parseBudgetAmount(originalCap(current.NativeSpendCap.Amount, current.NativeSpendCap.GrantedCap))
			if err != nil {
				return nil, nil, fmt.Errorf("%w: invalid stored native amount: %v", ErrBudgetReadUnavailable, err)
			}
			if original.Cmp(threshold) >= 0 {
				native = original
			}
		}
		return totals, native, nil
	}
	if current.BudgetStartedAt <= 0 {
		return nil, nil, fmt.Errorf("%w: budget has no start time", ErrBudgetReadUnavailable)
	}
	totals := map[common.Address]*big.Int{}
	for _, cap := range current.BudgetStartAmounts {
		if cap.Token == nil || totals[*cap.Token] != nil {
			return nil, nil, fmt.Errorf("%w: invalid budget start tokens", ErrBudgetReadUnavailable)
		}
		value, err := parseBudgetAmount(cap.Amount)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid budget start amount: %v", ErrBudgetReadUnavailable, err)
		}
		totals[*cap.Token] = value
	}
	if len(totals) != len(rem.ERC20) {
		return nil, nil, fmt.Errorf("%w: budget starts do not match capped tokens", ErrBudgetReadUnavailable)
	}
	for token, left := range rem.ERC20 {
		if totals[token] == nil || totals[token].Cmp(left) < 0 {
			return nil, nil, fmt.Errorf("%w: token remainder exceeds or lacks its total", ErrBudgetReadUnavailable)
		}
	}
	var native *big.Int
	if rem.Native == nil && current.BudgetStartNative != nil {
		return nil, nil, fmt.Errorf("%w: native start amount has no matching cap", ErrBudgetReadUnavailable)
	}
	if rem.Native != nil {
		if current.BudgetStartNative == nil {
			return nil, nil, fmt.Errorf("%w: budget has no native start amount", ErrBudgetReadUnavailable)
		}
		var err error
		native, err = parseBudgetAmount(current.BudgetStartNative.Amount)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid native start amount: %v", ErrBudgetReadUnavailable, err)
		}
		if native.Cmp(rem.Native) < 0 {
			return nil, nil, fmt.Errorf("%w: native remainder exceeds its total", ErrBudgetReadUnavailable)
		}
	}
	return totals, native, nil
}

func cloneBudgetInt(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}

// requested takes precedence over a same-token automation addition. Only an
// addition without an explicit owner total adds its amount on top of R.
func mergeBudgetAmount(total, left, requested, addition *big.Int, alreadyBudget, reset bool) (*big.Int, *big.Int, error) {
	if left == nil {
		left = big.NewInt(0)
	}
	if total == nil {
		total = big.NewInt(0)
	}
	amount, installed := new(big.Int).Set(total), new(big.Int).Set(left)
	switch {
	case reset:
		if requested != nil {
			amount.Set(requested)
		} else if addition != nil {
			amount.Set(addition)
		} else {
			amount.Set(left)
		}
		installed.Set(amount)
	case requested != nil && alreadyBudget:
		if requested.Cmp(total) < 0 {
			return nil, nil, budgetNarrowing("lowering a total requires startNewTotals")
		}
		installed.Add(installed, new(big.Int).Sub(requested, total))
		amount.Set(requested)
	case requested != nil:
		threshold := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
		if requested.Cmp(left) < 0 || total.Cmp(threshold) >= 0 && requested.Cmp(threshold) < 0 {
			return nil, nil, budgetNarrowing("migration must keep the remainder and unlimited limits unless startNewTotals is explicit")
		}
		amount.Set(requested)
		installed.Set(requested)
	case addition != nil:
		amount.Add(amount, addition)
		installed.Add(installed, addition)
	}
	if !alreadyBudget && !reset {
		threshold := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
		if total.Cmp(threshold) >= 0 && installed.Cmp(total) < 0 {
			installed.Set(total)
		}
		amount.Set(installed)
	}
	if amount.BitLen() > 256 || installed.BitLen() > 256 {
		return nil, nil, budgetInputError("a merged amount exceeds uint256")
	}
	return amount, installed, nil
}

func budgetActionUnion(groups [][]model.AllowedAction, remove []model.AllowedAction) ([]model.AllowedAction, error) {
	rows := map[common.Address]map[string]bool{}
	for _, group := range groups {
		for _, action := range group {
			if action.Target == nil || *action.Target == (common.Address{}) || len(action.Selectors) == 0 {
				return nil, budgetInputError("an action requires a target and selectors")
			}
			if rows[*action.Target] == nil {
				rows[*action.Target] = map[string]bool{}
			}
			for _, selector := range action.Selectors {
				if _, err := parseSelector(selector); err != nil {
					return nil, budgetInputError("invalid selector: %v", err)
				}
				rows[*action.Target][normalizeSelector(selector)] = true
			}
		}
	}
	for _, action := range remove {
		if action.Target == nil || *action.Target == (common.Address{}) || len(action.Selectors) == 0 {
			return nil, budgetInputError("removeActions requires a target and selectors")
		}
		for _, selector := range action.Selectors {
			if _, err := parseSelector(selector); err != nil {
				return nil, budgetInputError("invalid removal selector: %v", err)
			}
			delete(rows[*action.Target], normalizeSelector(selector))
		}
	}
	var out []model.AllowedAction
	for target, set := range rows {
		if len(set) == 0 {
			continue
		}
		selectors := make([]string, 0, len(set))
		for selector := range set {
			selectors = append(selectors, selector)
		}
		sort.Strings(selectors)
		copyTarget := target
		out = append(out, model.AllowedAction{Target: &copyTarget, Selectors: selectors})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Target.Hex()) < strings.ToLower(out[j].Target.Hex())
	})
	return out, nil
}
