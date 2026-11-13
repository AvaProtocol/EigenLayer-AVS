package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// The legacy standard permission observed on Ethereum includes unmetered WETH
// wrap/unwrap and Aave collateral configuration. Studio's first budget also
// includes the catalog's Lido stake route. Validate the complete merged scope.
func TestBudgetLegacyStandardPermissionMigration(t *testing.T) {
	p := budgetPolicy()
	p.ChainID = 1
	p.Sizing = model.BudgetSizingWorkflow
	pool, weth := budgetContracts["aavePool"][1], budgetContracts["wrappedNative"][1]
	router, lido := budgetContracts["swapRouter"][1], budgetContracts["lidoStake"][1]
	tokens := []common.Address{
		common.HexToAddress("0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984"),
		common.HexToAddress("0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599"),
		common.HexToAddress("0x514910771AF9Ca656af840dff83E8264EcF986CA"),
		common.HexToAddress("0x6B175474E89094C44Da98b954EedeAC495271d0F"),
		common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
		common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"),
	}
	unlimited := "340282366920938463463374607431768211455"
	p.ERC20SpendCap = nil
	p.NativeSpendCap = nil
	p.AllowedActions = []model.AllowedAction{
		{Target: &weth, Selectors: []string{selectorApprove, selectorTransfer, functionSelector("deposit()"), functionSelector("withdraw(uint256)")}},
		{Target: &pool, Selectors: []string{
			functionSelector("supply(address,uint256,address,uint16)"), functionSelector("repay(address,uint256,uint256,address)"),
			functionSelector("withdraw(address,uint256,address)"), functionSelector("borrow(address,uint256,uint256,uint16,address)"),
			functionSelector("setUserUseReserveAsCollateral(address,bool)"),
		}},
		{Target: &router, Selectors: []string{functionSelector("exactInputSingle((address,address,uint24,address,uint256,uint256,uint160))")}},
	}
	remainder := &GrantRemainder{ERC20: map[common.Address]*big.Int{}}
	for _, token := range tokens {
		p.ERC20SpendCaps = append(p.ERC20SpendCaps, model.ERC20SpendCap{Token: &token, Amount: unlimited, GrantedCap: unlimited})
		p.AllowedActions = append(p.AllowedActions, model.AllowedAction{Target: &token, Selectors: []string{selectorApprove, selectorTransfer}})
		left, ok := new(big.Int).SetString(unlimited, 10)
		require.True(t, ok)
		remainder.ERC20[token] = left
	}
	in := BudgetMergeInput{
		Current: p, ChainID: 1,
		Remainder: remainder,
		Budget: &WalletBudgetInput{
			ERC20:  append([]model.ERC20SpendCap(nil), p.ERC20SpendCaps...),
			Native: &model.NativeSpendCap{Amount: "10000000000000000"}, ValidUntil: p.ValidUntil,
		},
		Addition: &PolicyAddition{AllowedActions: []model.AllowedAction{{Target: &lido, Selectors: []string{functionSelector("submit(address)")}}}},
	}
	draft, err := MergeBudgetPolicy(in)
	require.NoError(t, err)
	require.NoError(t, draft.Permissions.Validate())
	require.Len(t, draft.Permissions.SpendCaps, len(tokens), "WETH must remain unmetered")
	for _, cap := range draft.Permissions.SpendCaps {
		require.NotEqual(t, weth, *cap.Token)
		require.Equal(t, unlimited, cap.Amount)
	}
	require.Empty(t, missingActions(draft.Permissions.AllowedActions, p.AllowedActions), "migration must keep every legacy action")
	require.NoError(t, refuseUnsupportedBudgetRoutes(1, draft.Permissions))
	withoutStake := in
	withoutStake.Addition = nil
	withoutStake.Budget = &WalletBudgetInput{ERC20: in.Budget.ERC20, ValidUntil: p.ValidUntil}
	plain, err := MergeBudgetPolicy(withoutStake)
	require.NoError(t, err)
	require.Nil(t, plain.Permissions.NativeSpendCap)
	require.NoError(t, refuseUnsupportedBudgetRoutes(1, plain.Permissions), "the legacy migration needs no Lido or native addition")
	// Keeping WETH callable must not weaken the cap/selector invariant.
	in.Budget.ERC20 = append(in.Budget.ERC20, model.ERC20SpendCap{Token: &weth, Amount: "1"})
	_, err = MergeBudgetPolicy(in)
	require.ErrorContains(t, err, "AllowlistModule only meters transfer/approve")
}

func TestBudgetCatalogCollateralAndStakeRoutes(t *testing.T) {
	pool, lido := budgetContracts["aavePool"][1], budgetContracts["lidoStake"][1]
	collateral, stake := functionSelector("setUserUseReserveAsCollateral(address,bool)"), functionSelector("submit(address)")
	for _, tc := range []struct {
		name     string
		chain    int64
		target   common.Address
		selector string
		native   string
		allowed  bool
	}{
		{"catalog collateral", 1, pool, collateral, "", true},
		{"catalog stake with native limit", 1, lido, stake, "1", true},
		{"catalog stake with zero native limit", 1, lido, stake, "0", true},
		{"stake without native limit", 1, lido, stake, "", false},
		{"uncatalogued collateral", 1, common.HexToAddress("0xbad"), collateral, "", false},
		{"uncatalogued stake", 1, common.HexToAddress("0xbad"), stake, "1", false},
		{"other Lido selector", 1, lido, collateral, "1", false},
		{"wrong chain collateral", testPolicyChain, pool, collateral, "", false},
		{"wrong chain stake", testPolicyChain, lido, stake, "1", false},
		{"collateral on WETH", 1, budgetContracts["wrappedNative"][1], collateral, "", false},
		{"stake on pool", 1, pool, stake, "1", false},
		{"stake on WETH", 1, budgetContracts["wrappedNative"][1], stake, "1", false},
		{"uppercase collateral", 1, pool, strings.ToUpper(collateral), "", true},
		{"uppercase stake", 1, lido, strings.ToUpper(stake), "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := SessionPermissions{AllowedActions: []model.AllowedAction{{Target: &tc.target, Selectors: []string{tc.selector}}}}
			if tc.native != "" {
				p.NativeSpendCap = &model.NativeSpendCap{Amount: tc.native}
			}
			err := refuseUnsupportedBudgetRoutes(tc.chain, p)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, BudgetRouteUnsupportedCode)
			}
		})
	}
	policy := &model.SessionPolicy{ChainID: 1, AllowedActions: []model.AllowedAction{{Target: &pool, Selectors: []string{collateral}}}}
	require.Equal(t, UnmeteredOther, unmeteredKind(policy, pool, collateral), "collateral changes must remain disclosed as unmetered")
}

func TestBudgetLidoNativeRecipientDoesNotGrantUnrestrictedSelectors(t *testing.T) {
	lido := budgetContracts["lidoStake"][1]
	for _, amount := range []string{"0", "10000000000000000"} {
		t.Run(amount, func(t *testing.T) {
			p := SessionPermissions{budgetMode: true, NativeSpendCap: &model.NativeSpendCap{Amount: amount},
				NativeRecipients: []*common.Address{&lido}, AllowContractRecipient: true, ValidUntilMs: budgetPolicy().ValidUntil}
			require.NoError(t, p.Validate())
			rows, err := p.allowlistInputs()
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.False(t, rows[0].HasSelectorAllowlist, "a native recipient would allow every Lido selector")
			require.False(t, rows[0].HasERC20SpendLimit)
			require.ErrorContains(t, refuseUnsupportedBudgetRoutes(1, p), BudgetRouteUnsupportedCode)
		})
	}
	for _, target := range []common.Address{budgetContracts["aavePool"][1], budgetContracts["swapRouter"][1], budgetContracts["wrappedNative"][1]} {
		p := SessionPermissions{NativeSpendCap: &model.NativeSpendCap{Amount: "1"}, NativeRecipients: []*common.Address{&target}, AllowContractRecipient: true}
		require.NoError(t, refuseUnsupportedBudgetRoutes(1, p), "previously admitted native fallback targets remain admitted")
	}
}

func TestBudgetLidoZeroNativeLimitKeepsBothEnforcementHooks(t *testing.T) {
	lido := budgetContracts["lidoStake"][1]
	p := SessionPermissions{budgetMode: true, NativeSpendCap: &model.NativeSpendCap{Amount: "0"},
		AllowedActions: []model.AllowedAction{{Target: &lido, Selectors: []string{functionSelector("submit(address)")}}}, ValidUntilMs: budgetPolicy().ValidUntil}
	require.NoError(t, refuseUnsupportedBudgetRoutes(1, p))
	hooks, err := p.HooksFor(123)
	require.NoError(t, err)
	nativeHooks := 0
	for _, hook := range hooks {
		if common.BytesToAddress(hook[:20]) == aa.NativeTokenLimitModuleAddress() {
			nativeHooks++
		}
	}
	require.Equal(t, 2, nativeHooks, "an exhausted budget keeps native validation and execution hooks")
}
