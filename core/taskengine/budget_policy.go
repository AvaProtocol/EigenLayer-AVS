package taskengine

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

const (
	BudgetHandoffRequiredCode = "BUDGET_HANDOFF_REQUIRED"
	BudgetReplaceRefusedCode  = "BUDGET_REPLACE_REFUSED"
	BudgetNarrowingCode       = "BUDGET_NARROWING_NEEDS_OWNER_CHANGE"
	BudgetInvalidInputCode    = "BUDGET_INVALID_INPUT"
)

// WalletBudgetInput describes owner totals, never a workflow's run count.
// Only the future stored owner-operation preparation may sign this intent.
type WalletBudgetInput struct {
	ERC20          []model.ERC20SpendCap
	Native         *model.NativeSpendCap
	ValidUntil     int64
	StartNewTotals bool
	RemoveActions  []model.AllowedAction
}

type BudgetPolicyError struct {
	Code   string
	Detail string
}

func (e *BudgetPolicyError) Error() string { return e.Code + ": " + e.Detail }

func budgetInputError(format string, args ...interface{}) error {
	return &BudgetPolicyError{Code: BudgetInvalidInputCode, Detail: fmt.Sprintf(format, args...)}
}

func budgetNarrowing(detail string) error {
	return &BudgetPolicyError{Code: BudgetNarrowingCode, Detail: detail}
}

func budgetHandoffRequired() error {
	return &BudgetPolicyError{Code: BudgetHandoffRequiredCode, Detail: "Budget changes require a stored owner-signed operation. That signing path is not enabled yet."}
}

// Explicit budget input must never reach the legacy EIP-712 grant builder.
// This is deliberately not an enableable flag: slices 3c/3d must first add the
// stored owner operation, atomic approval resets, hold and activation path.
func (n *Engine) refuseLegacyBudgetInput(user *model.User, in SessionPolicyInput) error {
	if in.Budget == nil {
		return nil
	}
	if err := n.requireServedChain(in.ChainID); err != nil {
		return err
	}
	if err := n.requireMAv2SessionWallet(user, in.ChainID, in.Wallet); err != nil {
		return err
	}
	if err := in.Budget.validate(time.Now()); err != nil {
		return err
	}
	return budgetHandoffRequired()
}

// Called under the authority lock at both prepare and submit, including a
// legacy request that omits basePolicyId or any hint that this is a budget.
func (n *Engine) refuseBudgetStatelessReplacement(user *model.User, in SessionPolicyInput) error {
	policies, err := ListSessionPolicies(n.db, in.ChainID, user.Address)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != in.Wallet || !p.Usable() || p.EffectiveSizing() != model.BudgetSizingBudget {
			continue
		}
		if in.Addition == nil && in.Budget == nil {
			return &BudgetPolicyError{Code: BudgetReplaceRefusedCode, Detail: "A stateless full replacement cannot replace a wallet budget."}
		}
		return budgetHandoffRequired()
	}
	return nil
}

func isWalletBudget(policy *model.SessionPolicy) bool {
	return policy != nil && policy.EffectiveSizing() == model.BudgetSizingBudget
}

func (in WalletBudgetInput) validate(now time.Time) error {
	removeOnly := len(in.RemoveActions) > 0 && !in.StartNewTotals && len(in.ERC20) == 0 && in.Native == nil
	if in.ValidUntil <= 0 || !removeOnly && in.ValidUntil <= now.UnixMilli() || in.ValidUntil/1000 >= 1<<48 {
		return budgetInputError("validUntil must be a future Unix-millisecond time representable by the time-range module")
	}
	seen := map[common.Address]bool{}
	for _, cap := range in.ERC20 {
		if cap.Token == nil || *cap.Token == (common.Address{}) {
			return budgetInputError("a budget token address is required")
		}
		if seen[*cap.Token] {
			return budgetInputError("budget token %s appears more than once", cap.Token.Hex())
		}
		seen[*cap.Token] = true
		if _, err := parseBudgetAmount(cap.Amount); err != nil {
			return budgetInputError("invalid total for token %s", cap.Token.Hex())
		}
	}
	if in.Native != nil {
		if _, err := parseBudgetAmount(in.Native.Amount); err != nil {
			return budgetInputError("invalid native total")
		}
	}
	return nil
}
