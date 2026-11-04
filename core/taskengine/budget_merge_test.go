package taskengine

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func semanticsBudget() (*model.SessionPolicy, *GrantRemainder) {
	p := budgetPolicy()
	p.Sizing = model.BudgetSizingBudget
	p.BudgetStartedAt = 123
	p.BudgetStartAmounts = []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "1000"}}
	p.BudgetStartNative = &model.NativeSpendCap{Amount: "1000"}
	p.AllowedActions = []model.AllowedAction{{Target: &testTokenAddr, Selectors: []string{selectorTransfer, selectorApprove}}}
	p.ERC20SpendCap = &model.ERC20SpendCap{Token: &testTokenAddr, Amount: "400", GrantedCap: "400"}
	p.NativeSpendCap = &model.NativeSpendCap{Amount: "400", GrantedCap: "400"}
	return p, &GrantRemainder{ERC20: map[common.Address]*big.Int{testTokenAddr: big.NewInt(400)}, Native: big.NewInt(400)}
}

func budgetOwnerInput(p *model.SessionPolicy, total string) *WalletBudgetInput {
	return &WalletBudgetInput{ERC20: []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: total}}, Native: &model.NativeSpendCap{Amount: "1000"}, ValidUntil: p.ValidUntil}
}

func TestBudgetMergePreservesUsageAndAddsOnlyRequestedAmounts(t *testing.T) {
	for _, tc := range []struct {
		name, requested, addition, amount, left, code string
		reset                                         bool
	}{
		{name: "unchanged", requested: "1000", amount: "1000", left: "400"},
		{name: "raise", requested: "1500", amount: "1500", left: "900"},
		{name: "MCP addition", addition: "100", amount: "1100", left: "500"},
		{name: "explicit total takes precedence", requested: "1500", addition: "100", amount: "1500", left: "900"},
		{name: "narrow needs owner", requested: "800", code: BudgetNarrowingCode},
		{name: "owner resets", requested: "800", amount: "800", left: "800", reset: true},
		{name: "owner zero", requested: "0", amount: "0", left: "0", reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, rem := semanticsBudget()
			before, _ := json.Marshal(p)
			beforeRem := rem.clone()
			in := BudgetMergeInput{Current: p, Remainder: rem, ChainID: p.ChainID}
			if tc.requested != "" {
				in.Budget = budgetOwnerInput(p, tc.requested)
				in.Budget.StartNewTotals = tc.reset
			}
			if tc.addition != "" {
				in.Addition = &PolicyAddition{SpendCaps: []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: tc.addition}}}
			}
			draft, err := MergeBudgetPolicy(in)
			if tc.code != "" {
				var refusal *BudgetPolicyError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, tc.code, refusal.Code)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.amount, draft.StartAmounts[0].Amount)
			require.Equal(t, tc.left, draft.Permissions.SpendCaps[0].Amount)
			if tc.reset {
				require.Equal(t, "1000", draft.Permissions.NativeSpendCap.Amount)
			} else {
				require.Equal(t, "400", draft.Permissions.NativeSpendCap.Amount)
			}
			if tc.reset {
				require.Zero(t, draft.StartedAt)
				require.True(t, draft.StartsNewTotals)
			} else {
				require.EqualValues(t, 123, draft.StartedAt)
				require.False(t, draft.StartsNewTotals)
			}
			after, _ := json.Marshal(p)
			require.JSONEq(t, string(before), string(after))
			require.Equal(t, beforeRem, *rem)
		})
	}
}

func TestBudgetOwnerResetRequiresEveryExistingTotal(t *testing.T) {
	for _, missingNative := range []bool{false, true} {
		p, rem := semanticsBudget()
		owner := budgetOwnerInput(p, "500")
		owner.StartNewTotals = true
		if missingNative {
			owner.Native = nil
		} else {
			owner.ERC20 = nil
		}
		_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
		var refusal *BudgetPolicyError
		require.ErrorAs(t, err, &refusal)
		require.Equal(t, BudgetInvalidInputCode, refusal.Code)
	}
}

func TestBudgetGasHeadroomIsAnExplicitIncrementAcrossMerges(t *testing.T) {
	p, rem := semanticsBudget()
	owner := budgetOwnerInput(p, "1000")
	in := BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID, GasHeadroom: big.NewInt(25)}
	first, err := MergeBudgetPolicy(in)
	require.NoError(t, err)
	require.Equal(t, "1025", first.StartNative.Amount)
	require.Equal(t, "425", first.Permissions.NativeSpendCap.Amount)
	// Preparing again from the same live base cannot compound a draft's gas.
	repeated, err := MergeBudgetPolicy(in)
	require.NoError(t, err)
	require.Equal(t, first, repeated)

	// Model activation, then 100 wei of spending. The next owner input is the
	// combined total shown by the budget read, including the earlier 25 wei.
	p.NativeSpendCap, p.BudgetStartNative = first.Permissions.NativeSpendCap, first.StartNative
	rem.Native = big.NewInt(325)
	owner.Native = &model.NativeSpendCap{Amount: first.StartNative.Amount}
	in.GasHeadroom = big.NewInt(5) // newly priced wei, not the previous reserve
	second, err := MergeBudgetPolicy(in)
	require.NoError(t, err)
	require.Equal(t, "1030", second.StartNative.Amount)
	require.Equal(t, "330", second.Permissions.NativeSpendCap.Amount)
	amount, _ := new(big.Int).SetString(second.StartNative.Amount, 10)
	left, _ := new(big.Int).SetString(second.Permissions.NativeSpendCap.Amount, 10)
	require.Equal(t, "700", new(big.Int).Sub(amount, left).String())
	require.Equal(t, p.BudgetStartedAt, second.StartedAt)

	in.GasHeadroom = nil
	unchanged, err := MergeBudgetPolicy(in)
	require.NoError(t, err)
	require.Equal(t, "1025", unchanged.StartNative.Amount)
	require.Equal(t, "325", unchanged.Permissions.NativeSpendCap.Amount)
	owner.Native.Amount = "1000" // an old pre-gas value is a lower total
	_, err = MergeBudgetPolicy(in)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetNarrowingCode, refusal.Code)
}

func TestBudgetPlainRaiseCarriesOmittedTokenAndNativeTotals(t *testing.T) {
	for _, nativeOnly := range []bool{false, true} {
		p, rem := semanticsBudget()
		other := common.HexToAddress("0x9999")
		p.ERC20SpendCaps = []model.ERC20SpendCap{*p.ERC20SpendCap, {Token: &other, Amount: "50"}}
		p.ERC20SpendCap = nil
		p.BudgetStartAmounts = append(p.BudgetStartAmounts, model.ERC20SpendCap{Token: &other, Amount: "500"})
		rem.ERC20[other] = big.NewInt(50)
		owner := &WalletBudgetInput{ValidUntil: p.ValidUntil}
		if nativeOnly {
			owner.Native = &model.NativeSpendCap{Amount: "1500"}
		} else {
			owner.ERC20 = []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "1500"}}
		}
		draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
		require.NoError(t, err)
		for _, cap := range draft.Permissions.SpendCaps {
			want := "50"
			if *cap.Token == testTokenAddr {
				want = "900"
				if nativeOnly {
					want = "400"
				}
			}
			require.Equal(t, want, cap.Amount)
		}
		if nativeOnly {
			require.Equal(t, "1500", draft.StartNative.Amount)
			require.Equal(t, "900", draft.Permissions.NativeSpendCap.Amount)
		} else {
			require.Equal(t, "1000", draft.StartNative.Amount)
			require.Equal(t, "400", draft.Permissions.NativeSpendCap.Amount)
		}
		require.Equal(t, p.BudgetStartedAt, draft.StartedAt)
		owner.StartNewTotals = true
		_, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
		var refusal *BudgetPolicyError
		require.ErrorAs(t, err, &refusal)
		require.Equal(t, BudgetInvalidInputCode, refusal.Code, "the same partial request is not a complete Change/Renew")
	}
}

func TestBudgetMigrationKeepsUnlimitedAndStartsNewUsage(t *testing.T) {
	p, rem := semanticsBudget()
	p.Sizing = model.BudgetSizingWorkflow
	owner := budgetOwnerInput(p, "450")
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	require.NoError(t, err)
	require.True(t, draft.StartsNewTotals)
	require.Zero(t, draft.StartedAt)
	require.Equal(t, "450", draft.StartAmounts[0].Amount)
	require.Equal(t, "450", draft.Permissions.SpendCaps[0].Amount)
	threshold := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	p.ERC20SpendCap.GrantedCap = threshold.String()
	rem.ERC20[testTokenAddr] = new(big.Int).Sub(threshold, big.NewInt(25))
	owner.ERC20[0].Amount = rem.ERC20[testTokenAddr].String()
	_, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetNarrowingCode, refusal.Code)
	owner.ERC20 = nil
	draft, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	require.NoError(t, err)
	require.Equal(t, threshold.String(), draft.StartAmounts[0].Amount)
}

func TestBudgetMergeNeverSizesFromEnabledRunCounts(t *testing.T) {
	p, rem := semanticsBudget()
	receiver := common.HexToAddress("0x9999")
	task := skillWriteTask("pay", "Pay", testTokenAddr.Hex(), transferCalldata(receiver, big.NewInt(100)), p.ChainID, 1_000_000, 0, p.ValidUntil+3600000)
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: &PolicyAddition{}, Tasks: []*avsproto.Task{task}, ChainID: p.ChainID})
	require.NoError(t, err)
	require.Equal(t, "400", draft.Permissions.SpendCaps[0].Amount)
	require.Equal(t, "1000", draft.StartAmounts[0].Amount)
	require.Equal(t, task.GetExpiredAt(), draft.Permissions.ValidUntilMs)
}

func TestBudgetFirstInstallUsesOwnerTotalsAndUnionsWorkflowScope(t *testing.T) {
	p, _ := semanticsBudget()
	owner := budgetOwnerInput(p, "1000")
	owner.Native.Amount = "2000"
	task := skillWriteTask("pay", "Pay", testTokenAddr.Hex(), transferCalldata(testRouterAddr, big.NewInt(100)), p.ChainID, 1_000_000, 0, p.ValidUntil+10000)
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Budget: owner, Tasks: []*avsproto.Task{task}, ChainID: p.ChainID, GasHeadroom: big.NewInt(5)})
	require.NoError(t, err)
	require.True(t, draft.StartsNewTotals)
	require.Zero(t, draft.StartedAt)
	require.Equal(t, "1000", draft.StartAmounts[0].Amount)
	require.Equal(t, "1000", draft.Permissions.SpendCaps[0].Amount)
	require.Equal(t, "2005", draft.StartNative.Amount)
	require.Equal(t, "2005", draft.Permissions.NativeSpendCap.Amount)
	require.Equal(t, task.ExpiredAt, draft.Permissions.ValidUntilMs)
	require.Equal(t, []model.AllowedAction{{Target: &testTokenAddr, Selectors: []string{selectorTransfer}}}, draft.Permissions.AllowedActions)
	_, err = draft.Permissions.HooksFor(7)
	require.NoError(t, err)
}

func TestBudgetExhaustedAndRemovedTokenRowsRemainCapped(t *testing.T) {
	for _, remove := range []bool{false, true} {
		p, rem := semanticsBudget()
		rem.ERC20[testTokenAddr] = big.NewInt(0)
		owner := budgetOwnerInput(p, "1000")
		if remove {
			owner.ERC20 = nil
			owner.Native = nil
			owner.RemoveActions = p.AllowedActions
		}
		draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
		require.NoError(t, err)
		require.Equal(t, "0", draft.Permissions.SpendCaps[0].Amount)
		rows, err := draft.Permissions.allowlistInputs()
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.True(t, rows[0].HasERC20SpendLimit)
		require.Zero(t, rows[0].ERC20SpendLimit.Sign())
		require.True(t, rows[0].HasSelectorAllowlist)
		if remove {
			require.Empty(t, rows[0].Selectors)
			require.Empty(t, draft.Permissions.AllowedActions)
		} else {
			require.Len(t, rows[0].Selectors, 2)
		}
		_, err = draft.Permissions.HooksFor(7)
		require.NoError(t, err)
		legacy := draft.Permissions
		legacy.budgetMode = false
		require.Error(t, legacy.validate(false))
	}
}

func TestBudgetExhaustedNativeLimitIsNotDropped(t *testing.T) {
	p, rem := semanticsBudget()
	rem.Native = big.NewInt(0)
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: &PolicyAddition{}, ChainID: p.ChainID})
	require.NoError(t, err)
	require.Equal(t, "0", draft.Permissions.NativeSpendCap.Amount)
	require.Equal(t, "1000", draft.StartNative.Amount)
	_, err = draft.Permissions.HooksFor(7)
	require.NoError(t, err)
}

func TestBudgetMergeReadFailureAndOverflowNeverUseStoredCaps(t *testing.T) {
	p, rem := semanticsBudget()
	_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Addition: &PolicyAddition{}, ChainID: p.ChainID})
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	delete(rem.ERC20, testTokenAddr)
	_, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: &PolicyAddition{}, ChainID: p.ChainID})
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	p, rem = semanticsBudget()
	p.BudgetStartAmounts[0].Amount = maxUint256.String()
	_, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: &PolicyAddition{SpendCaps: []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "1"}}}, ChainID: p.ChainID})
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetInvalidInputCode, refusal.Code)
}

func TestBudgetMergeRefusesCorruptStartMetadata(t *testing.T) {
	for _, corrupt := range []func(*model.SessionPolicy){
		func(p *model.SessionPolicy) { p.BudgetStartAmounts[0].Amount = "bad" },
		func(p *model.SessionPolicy) { p.BudgetStartNative.Amount = "bad" },
		func(p *model.SessionPolicy) { p.NativeSpendCap = nil },
		func(p *model.SessionPolicy) {
			p.ERC20SpendCaps = []model.ERC20SpendCap{*p.ERC20SpendCap, *p.ERC20SpendCap}
		},
	} {
		p, rem := semanticsBudget()
		corrupt(p)
		_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: &PolicyAddition{}, ChainID: p.ChainID})
		require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	}
}

func TestBudgetNativeGasHeadroomAndEarlierEnd(t *testing.T) {
	p, rem := semanticsBudget()
	owner := budgetOwnerInput(p, "1000")
	owner.ValidUntil = p.ValidUntil - 1000
	_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetNarrowingCode, refusal.Code)
	owner.StartNewTotals = true
	owner.Native.Amount = "0"
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID, GasHeadroom: big.NewInt(100)})
	require.NoError(t, err)
	require.Equal(t, "100", draft.StartNative.Amount)
	require.Equal(t, "100", draft.Permissions.NativeSpendCap.Amount)
	require.Equal(t, owner.ValidUntil, draft.Permissions.ValidUntilMs)
}

func TestBudgetInputRejectsAmbiguousOrMalformedTotals(t *testing.T) {
	p, rem := semanticsBudget()
	for _, amount := range []string{"", "-1", "+1", "0x10", "1.5", new(big.Int).Add(maxUint256, big.NewInt(1)).String()} {
		owner := budgetOwnerInput(p, amount)
		_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
		var refusal *BudgetPolicyError
		require.True(t, errors.As(err, &refusal), "%s: %v", amount, err)
		require.Equal(t, BudgetInvalidInputCode, refusal.Code)
	}
	owner := budgetOwnerInput(p, "1000")
	owner.ERC20 = append(owner.ERC20, owner.ERC20[0])
	_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	require.Error(t, err)
	owner = budgetOwnerInput(p, "1000")
	owner.ValidUntil = time.Now().Add(-time.Minute).UnixMilli()
	_, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	require.Error(t, err)
}

func TestBudgetDisableOnlyRemovesNamedActions(t *testing.T) {
	p, rem := semanticsBudget()
	other := common.HexToAddress("0x9999")
	task := skillWriteTask("other", "Other", other.Hex(), transferCalldata(testRouterAddr, big.NewInt(1)), p.ChainID, 1, 0, p.ValidUntil+10000)
	owner := &WalletBudgetInput{ValidUntil: p.ValidUntil, RemoveActions: []model.AllowedAction{{Target: &testTokenAddr, Selectors: []string{selectorApprove}}}}
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, Tasks: []*avsproto.Task{task}, ChainID: p.ChainID})
	require.NoError(t, err)
	require.Equal(t, []model.AllowedAction{{Target: &testTokenAddr, Selectors: []string{selectorTransfer}}}, draft.Permissions.AllowedActions)
	require.Equal(t, p.ValidUntil, draft.Permissions.ValidUntilMs)
	require.Equal(t, "400", draft.Permissions.SpendCaps[0].Amount)
	require.Equal(t, "1000", draft.StartAmounts[0].Amount)
	require.Equal(t, p.BudgetStartedAt, draft.StartedAt)
	_, err = MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID, GasHeadroom: big.NewInt(1)})
	require.Error(t, err, "Disable cannot silently increase the ETH total")
}

func TestBudgetDisableLastUnmeteredCapabilityAllowsNothing(t *testing.T) {
	p, rem := semanticsBudget()
	p.ERC20SpendCap, p.BudgetStartAmounts = nil, nil
	rem.ERC20 = map[common.Address]*big.Int{}
	owner := &WalletBudgetInput{ValidUntil: p.ValidUntil, RemoveActions: p.AllowedActions}
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, ChainID: p.ChainID})
	require.NoError(t, err)
	require.Empty(t, draft.Permissions.AllowedActions)
	require.Equal(t, "400", draft.Permissions.NativeSpendCap.Amount)
	_, err = draft.Permissions.HooksFor(7)
	require.NoError(t, err, "Disable may install a deny-all allowlist")
	legacy := draft.Permissions
	legacy.budgetMode = false
	require.Error(t, legacy.validate(false), "ordinary grants still require a capability")
}

func TestBudgetCappingAnUnmeteredTokenNeedsOwnerReset(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		p, rem := semanticsBudget()
		p.ERC20SpendCap, p.BudgetStartAmounts = nil, nil
		rem.ERC20 = map[common.Address]*big.Int{}
		p.AllowedActions[0].Selectors = []string{selectorTransfer}
		if wrapped {
			p.AllowedActions[0].Selectors = append(p.AllowedActions[0].Selectors, "0xd0e30db0", "0x2e1a7d4d")
		}
		owner := budgetOwnerInput(p, "500")
		if wrapped {
			owner.RemoveActions = []model.AllowedAction{{Target: &testTokenAddr, Selectors: []string{"0xd0e30db0", "0x2e1a7d4d"}}}
		}
		add := &PolicyAddition{AllowedActions: []model.AllowedAction{{Target: &testTokenAddr, Selectors: []string{selectorApprove}}}}
		_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, Addition: add, ChainID: p.ChainID})
		require.Error(t, err, "adding approve must not hide the existing uncapped transfer")
		owner.StartNewTotals = true
		draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Budget: owner, Addition: add, ChainID: p.ChainID})
		require.NoError(t, err)
		require.Equal(t, "500", draft.Permissions.SpendCaps[0].Amount)
		require.Len(t, draft.Permissions.AllowedActions[0].Selectors, 2)
	}
}

func TestBudgetNewTokenRequiresAndKeepsItsOwnTotal(t *testing.T) {
	p, rem := semanticsBudget()
	other := common.HexToAddress("0x9999")
	add := &PolicyAddition{AllowedActions: []model.AllowedAction{{Target: &other, Selectors: []string{selectorTransfer}}}}
	_, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: add, ChainID: p.ChainID})
	require.Error(t, err)
	add.SpendCaps = []model.ERC20SpendCap{{Token: &other, Amount: "25"}}
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: p, Remainder: rem, Addition: add, ChainID: p.ChainID})
	require.NoError(t, err)
	for _, cap := range draft.Permissions.SpendCaps {
		if *cap.Token == other {
			require.Equal(t, "25", cap.Amount)
		} else {
			require.Equal(t, "400", cap.Amount)
		}
	}
}
