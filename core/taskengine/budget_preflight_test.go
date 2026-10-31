package taskengine

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func budgetCall(amount int64, approve bool) PlannedCall {
	data := transferCalldata(testRouterAddr, big.NewInt(amount))
	if approve {
		data = "0x095ea7b3" + data[10:]
	}
	return PlannedCall{Target: testTokenAddr, Selector: SelectorFromCalldata(common.FromHex(data)), Calldata: common.FromHex(data)}
}

func TestBudgetPreflightUsesActualBatchSpendAndRemaining(t *testing.T) {
	for _, tc := range []struct {
		name     string
		calls    []PlannedCall
		left     int64
		wantCode avsproto.ErrorCode
	}{
		{"one transfer fits", []PlannedCall{budgetCall(100, false)}, 100, avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED},
		{"stored 400 is not remaining 99", []PlannedCall{budgetCall(100, false)}, 99, avsproto.ErrorCode_BUDGET_USED_UP},
		{"approve and transfer are both charged", []PlannedCall{budgetCall(60, true), budgetCall(60, false)}, 100, avsproto.ErrorCode_BUDGET_USED_UP},
		{"zero approval stays usable", []PlannedCall{budgetCall(0, true)}, 0, avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := semanticsBudget()
			p.NativeSpendCap = nil
			p.BudgetStartNative = nil
			reads := 0
			caller := budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
				reads++
				require.Equal(t, aa.AllowlistModuleAddress(), *call.To)
				return budgetWord(tc.left), nil
			})
			err := CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: p, Calls: tc.calls, Caller: caller})
			require.Equal(t, 1, reads)
			if tc.wantCode == avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED {
				require.NoError(t, err)
				return
			}
			var refusal *StructuredError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, tc.wantCode, refusal.Code)
			require.Equal(t, testTokenAddr.Hex(), refusal.Details["token"])
			require.Equal(t, big.NewInt(tc.left).String(), refusal.Details["remaining"])
		})
	}
}

func TestBudgetPreflightReadFailuresAndExpiryFailClosed(t *testing.T) {
	p, _ := semanticsBudget()
	p.NativeSpendCap = nil
	var noEthClient *ethclient.Client
	var noChainReader *directChainStateReader
	var noFunction budgetCaller
	for _, caller := range []aa.ContractCaller{nil, noEthClient, noChainReader, noFunction, budgetCaller(func(ethereum.CallMsg) ([]byte, error) { return nil, errors.New("RPC down") }), budgetCaller(func(ethereum.CallMsg) ([]byte, error) { return nil, nil })} {
		err := CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: p, Calls: []PlannedCall{budgetCall(10, false)}, Caller: caller})
		require.Equal(t, avsproto.ErrorCode_RPC_NODE_ERROR, GetErrorCode(err))
		require.Contains(t, err.Error(), BudgetReadUnavailableCode)
	}
	p.ValidUntil = time.Now().Add(-time.Minute).UnixMilli()
	err := CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: p, Calls: []PlannedCall{budgetCall(10, false)}})
	var refusal *StructuredError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, avsproto.ErrorCode_BUDGET_ENDED, refusal.Code)
	require.Equal(t, p.ValidUntil, refusal.Details["validUntil"])
}

func TestBudgetNativeTypedNilFeeReaderFailsClosed(t *testing.T) {
	p, _ := semanticsBudget()
	var reader *codeAndFee
	err := CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: p,
		Caller:    budgetCaller(func(ethereum.CallMsg) ([]byte, error) { return budgetWord(100), nil }),
		FeeReader: reader, Calls: []PlannedCall{{Value: big.NewInt(1)}}, Native: NativeIntent{Kind: NativeSend}})
	require.Equal(t, avsproto.ErrorCode_RPC_NODE_ERROR, GetErrorCode(err))
	require.Contains(t, err.Error(), BudgetReadUnavailableCode)
}

func TestBudgetNativePreflightCountsGasAgainstRemaining(t *testing.T) {
	p, _ := semanticsBudget()
	caller := budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
		require.Equal(t, aa.NativeTokenLimitModuleAddress(), *call.To)
		return budgetWord(100), nil
	})
	for _, sponsored := range []bool{false, true} {
		err := CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: p, Calls: []PlannedCall{{Value: big.NewInt(90)}}, Caller: caller, Native: NativeIntent{Kind: NativeSend, Sponsored: sponsored, EstimatedGas: big.NewInt(20)}})
		if sponsored {
			require.NoError(t, err)
		} else {
			var refusal *StructuredError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, avsproto.ErrorCode_BUDGET_USED_UP, refusal.Code)
			require.Equal(t, "110", refusal.Details["needed"])
			require.Equal(t, budgetNativeToken.Hex(), refusal.Details["token"])
		}
	}
	err := CheckBudgetSpend(context.Background(), BudgetSpendCheck{Policy: p, Calls: []PlannedCall{{Value: big.NewInt(1)}}, Caller: caller, Native: NativeIntent{Kind: NativeSend}})
	require.Equal(t, avsproto.ErrorCode_RPC_NODE_ERROR, GetErrorCode(err), "unpriced gas must not be guessed")
}

func TestBudgetAuthorizationUsesOneRunAndClearsCeilings(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		p, rem := semanticsBudget()
		p.NativeSpendCap = nil
		p.BudgetStartNative = nil
		need := &WorkflowNeed{HasFundMove: true, Actions: p.AllowedActions, CapNeedsInput: unknown, CapCeiling: unknown}
		if !unknown {
			need.Caps = []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "100"}}
		}
		in := BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need, Schedule: SkillSchedule{MaxExecution: 1000000}}
		got := BuildBudgetAuthorization(in)
		require.Equal(t, AuthCovered, got.Status, got.Detail)
		require.False(t, got.Required.CapNeedsInput)
		require.False(t, got.Required.CapCeiling)
		rem.ERC20[testTokenAddr] = big.NewInt(0)
		got = BuildBudgetAuthorization(in)
		require.Equal(t, AuthCapTooLow, got.Status)
		in.Remainder = nil
		got = BuildBudgetAuthorization(in)
		require.Equal(t, AuthNotCovered, got.Status)
		require.Contains(t, got.Detail, BudgetReadUnavailableCode)
	}
}

func TestBudgetAuthorizationRetainsTargetAndExpiryChecks(t *testing.T) {
	p, rem := semanticsBudget()
	p.NativeSpendCap = nil
	p.BudgetStartNative = nil
	other := common.HexToAddress("0x9999")
	need := &WorkflowNeed{HasFundMove: true, Actions: []model.AllowedAction{{Target: &other, Selectors: []string{selectorTransfer}}}}
	in := BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need}
	require.Equal(t, AuthNotCovered, BuildBudgetAuthorization(in).Status)
	need.Actions = p.AllowedActions
	need.Unresolved = true
	require.Equal(t, AuthTargetUnresolved, BuildBudgetAuthorization(in).Status)
	need.Unresolved = false
	need.ValidUntilMs = p.ValidUntil + 1000
	got := BuildBudgetAuthorization(in)
	require.Equal(t, AuthExpiresTooSoon, got.Status)
	require.Equal(t, need.ValidUntilMs, got.NeedsUntil)
	need.ValidUntilMs = 0
	p.ValidUntil = time.Now().Add(-time.Minute).UnixMilli()
	require.Equal(t, AuthExpiresTooSoon, BuildBudgetAuthorization(in).Status)
	// The old entry point cannot quietly use stored caps for a budget.
	p.ValidUntil = time.Now().Add(time.Hour).UnixMilli()
	got = BuildAuthorization(p, need, nil, SkillSchedule{})
	require.Equal(t, AuthNotCovered, got.Status)
	require.Contains(t, got.Detail, BudgetReadUnavailableCode)
}

func TestBudgetAuthorizationReportsNewTokenAndPreservesObservedSpend(t *testing.T) {
	p, rem := semanticsBudget()
	p.NativeSpendCap, p.BudgetStartNative = nil, nil
	other := common.HexToAddress("0x9999")
	need := &WorkflowNeed{HasFundMove: true, Actions: []model.AllowedAction{{Target: &other, Selectors: []string{selectorTransfer}}}, Caps: []model.ERC20SpendCap{{Token: &other, Amount: "12"}}}
	got := BuildBudgetAuthorization(BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need})
	require.Equal(t, AuthNotCovered, got.Status)
	require.Equal(t, []common.Address{other}, got.MissingTokens)
	require.Equal(t, "12", got.Required.Caps[0].Amount)

	need = &WorkflowNeed{HasFundMove: true, Actions: p.AllowedActions, CapNeedsInput: true, CapCeiling: true}
	report := &SessionGrantReport{}
	report.observeCalls([]PlannedCall{budgetCall(500, false)})
	got = BuildBudgetAuthorization(BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need, Report: report})
	require.Equal(t, AuthCapTooLow, got.Status, "the split ceiling must not discard a known simulated run")
	require.Equal(t, "500", got.Required.Caps[0].Amount)
	require.False(t, got.Required.CapCeiling)

	p.ERC20SpendCap, p.BudgetStartAmounts = nil, nil
	rem.ERC20 = map[common.Address]*big.Int{}
	got = BuildBudgetAuthorization(BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need, Report: report})
	require.Equal(t, AuthCovered, got.Status, "an explicitly unmetered token is not checked against a nonexistent cap")
	require.Empty(t, got.MissingTokens)
}

func TestBudgetNeedUsesOneRunAndManualHasNoArtificialEnd(t *testing.T) {
	p, rem := semanticsBudget()
	p.NativeSpendCap, p.BudgetStartNative = nil, nil
	task := skillWriteTask("pay", "Pay", testTokenAddr.Hex(), transferCalldata(testRouterAddr, big.NewInt(100)), p.ChainID, 1_000_000, 0, p.ValidUntil+100000)
	need := budgetNeedForTask(task, nil, p.ChainID, time.Now())
	require.Equal(t, "100", need.Caps[0].Amount)
	got := BuildBudgetAuthorization(BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need})
	require.Equal(t, AuthExpiresTooSoon, got.Status)
	task.Trigger = &avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL}
	need = budgetNeedForTask(task, nil, p.ChainID, time.Now())
	require.Zero(t, need.ValidUntilMs)
	got = BuildBudgetAuthorization(BudgetAuthorizationInput{Policy: p, Remainder: rem, Need: need})
	require.Equal(t, AuthCovered, got.Status)
}
