package taskengine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type BudgetAuthorizationInput struct {
	Policy    *model.SessionPolicy
	Need      *WorkflowNeed // one run, not all scheduled runs
	Report    *SessionGrantReport
	Schedule  SkillSchedule
	Remainder *GrantRemainder
}

// BuildBudgetAuthorization compares one known run with the real remainder.
// Unknown amounts use the wallet budget as their ceiling; unknown targets do
// not. Missing reads never fall back to the cap installed on the entity.
func BuildBudgetAuthorization(in BudgetAuthorizationInput) *SessionAuthorization {
	_, _, sawWrite, _, _, _, _, _, _ := in.Report.snapshot()
	if (in.Need == nil || !in.Need.HasFundMove) && !sawWrite {
		return &SessionAuthorization{Status: AuthCovered}
	}
	policy := in.Policy
	if policy == nil {
		return &SessionAuthorization{Status: AuthNoGrant, Required: in.Need, Detail: "no usable budget"}
	}
	unavailable := func(err error) *SessionAuthorization {
		return &SessionAuthorization{Status: AuthNotCovered, PolicyID: policy.ID, Required: in.Need, Detail: fmt.Sprintf("%s: %v", BudgetReadUnavailableCode, err)}
	}
	if policy.Status != model.SessionPolicyActive || !policy.Grant.Applied() {
		return unavailable(fmt.Errorf("the budget is not active"))
	}
	rem, err := budgetMergeRemainder(policy, in.Remainder)
	if err != nil {
		return unavailable(err)
	}
	if _, _, err := budgetMergeTotals(policy, rem); err != nil {
		return unavailable(err)
	}
	_, _, saw, missingCalls, nativeMiss, nativeDetail, tokens, native, planned := in.Report.snapshot()
	oneRun := in.Schedule
	oneRun.MaxExecution = 1
	oneRun.ExecutionCount = 0
	oneRun.ExpiredAt = 0
	oneRun.Crons = nil
	// A split ceiling is a sizing rule for workflow grants. In budget mode the
	// owner already chose the ceiling, so a simulated run may inform its warning.
	var need *WorkflowNeed
	if in.Need != nil {
		copyNeed := *in.Need
		copyNeed.CapCeiling = false
		need = &copyNeed
	}
	filled := fillObserved(need, saw, planned, tokens, native, oneRun)
	if filled == nil {
		return &SessionAuthorization{Status: AuthCovered}
	}
	filled.CapNeedsInput = false
	filled.CapCeiling = false
	filled.NativeUnsized = false
	filled.GasUnsized = false
	out := &SessionAuthorization{PolicyID: policy.ID, Required: filled}
	missingTokens := map[common.Address]bool{}
	for _, action := range filled.Actions {
		if action.Target == nil || rem.ERC20[*action.Target] != nil || budgetHasUnmeteredSpend(policy, *action.Target) {
			continue
		}
		for _, selector := range action.Selectors {
			if normalizeSelector(selector) == selectorTransfer || normalizeSelector(selector) == selectorApprove {
				missingTokens[*action.Target] = true
			}
		}
	}
	// Keep explicitly unmetered token capabilities (e.g. legacy WETH with
	// deposit/withdraw) out of numeric shortfall checks.
	clone := *policy
	clone.ERC20SpendCap = nil
	clone.ERC20SpendCaps = nil
	for token, left := range rem.ERC20 {
		copyToken := token
		clone.ERC20SpendCaps = append(clone.ERC20SpendCaps, model.ERC20SpendCap{Token: &copyToken, Amount: left.String()})
	}
	sortSpendCaps(clone.ERC20SpendCaps)
	if rem.Native != nil {
		clone.NativeSpendCap = &model.NativeSpendCap{Amount: rem.Native.String()}
	}
	if priceErr := priceDraftNativeGas(filled, &clone); priceErr != nil {
		out.Status, out.Detail = AuthCapTooLow, priceErr.Error()
		return out
	}
	out.Missing = missingActions(policy.AllowedActions, filled.Actions)
	if saw {
		var err error
		out.Missing, err = budgetActionUnion([][]model.AllowedAction{out.Missing, actionsFromPlanned(missingCalls)}, nil)
		if err != nil {
			out.Status, out.Detail = AuthTargetUnresolved, "a simulated call could not be resolved"
			return out
		}
	}
	for token := range missingTokens {
		out.MissingTokens = append(out.MissingTokens, token)
	}
	sort.Slice(out.MissingTokens, func(i, j int) bool { return out.MissingTokens[i].Hex() < out.MissingTokens[j].Hex() })
	if len(out.Missing) > 0 || len(out.MissingTokens) > 0 || nativeMiss && !isNativeCapMessage(nativeDetail) {
		out.Status, out.Detail = AuthNotCovered, "a planned call or spending token is outside the budget"
		if nativeMiss && !isNativeCapMessage(nativeDetail) {
			out.Detail = nativeDetail
		}
		return out
	}
	if filled.Unresolved {
		out.Status, out.Detail = AuthTargetUnresolved, "a fund-moving target could not be resolved"
		return out
	}
	if rec := firstMissingNativeRecipient(policy, filled); rec != nil {
		out.Status, out.Detail = AuthNotCovered, FormatSessionPolicyRecipientNotAllowed(*rec, policy.ID)
		return out
	}
	checkNeed := *filled
	checkNeed.Caps = nil
	for _, cap := range filled.Caps {
		if cap.Token != nil && rem.ERC20[*cap.Token] != nil {
			checkNeed.Caps = append(checkNeed.Caps, cap)
		}
	}
	if tokenShort, nativeShort := capShortfall(&clone, &checkNeed); tokenShort || nativeShort || nativeMiss && isNativeCapMessage(nativeDetail) {
		out.Status, out.Detail = AuthCapTooLow, "the remaining budget is lower than one run"
		return out
	}
	// A zero remainder blocks even an unsized transfer. Otherwise an unknown
	// amount could be reported covered merely because it has no numeric cap.
	for _, action := range filled.Actions {
		if action.Target == nil {
			continue
		}
		left := rem.ERC20[*action.Target]
		if left == nil || left.Sign() > 0 {
			continue
		}
		for _, selector := range action.Selectors {
			if normalizeSelector(selector) == selectorTransfer || normalizeSelector(selector) == selectorApprove {
				out.Status, out.Detail = AuthCapTooLow, "the token budget is used up"
				return out
			}
		}
	}
	if rem.Native != nil && rem.Native.Sign() == 0 {
		out.Status, out.Detail = AuthCapTooLow, "the native budget is used up"
		return out
	}
	now := in.Schedule.Now
	if now.IsZero() {
		now = time.Now()
	}
	if policy.Expired(now) || filled.ValidUntilMs > policy.ValidUntil {
		out.Status, out.Detail = AuthExpiresTooSoon, "the budget has ended or ends before this workflow"
		if filled.ValidUntilMs > policy.ValidUntil {
			out.NeedsUntil = filled.ValidUntilMs
		}
		return out
	}
	out.Status = AuthCovered
	return out
}

func budgetNeedForTask(task *avsproto.Task, settings map[string]any, chainID int64, now time.Time) *WorkflowNeed {
	need := DeriveWorkflowNeeds(task, settings, SkillSchedule{MaxExecution: 1, Now: now}, chainID)[chainID]
	if need != nil && task != nil {
		if task.GetTrigger().GetType() == avsproto.TriggerType_TRIGGER_TYPE_MANUAL {
			need.ValidUntilMs = 0
		} else if task.GetExpiredAt() > 0 {
			need.ValidUntilMs = task.GetExpiredAt()
		}
	}
	return need
}

func (n *Engine) buildBudgetAuthorization(policy *model.SessionPolicy, task *avsproto.Task, settings map[string]any, report *SessionGrantReport, schedule SkillSchedule) *SessionAuthorization {
	need := budgetNeedForTask(task, settings, policy.ChainID, schedule.Now)
	if need != nil {
		need.priceFee = n.selfFundedGrantFee(policy.ChainID)
	}
	if policy.Runner == nil {
		return &SessionAuthorization{Status: AuthNotCovered, PolicyID: policy.ID, Required: need, Detail: BudgetReadUnavailableCode + ": budget has no runner"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), occupancyProbeTimeout)
	defer cancel()
	rem, err := n.resolveGrantRemainder(ctx, policy, *policy.Runner)
	if err != nil {
		return &SessionAuthorization{Status: AuthNotCovered, PolicyID: policy.ID, Required: need, Detail: fmt.Sprintf("%s: %v", BudgetReadUnavailableCode, err)}
	}
	return BuildBudgetAuthorization(BudgetAuthorizationInput{Policy: policy, Need: need, Report: report, Schedule: schedule, Remainder: &rem})
}
