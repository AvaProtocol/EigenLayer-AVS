package taskengine

import (
	"context"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// perRunCapOf is the cap a need holds for token, or "" when it has none.
func perRunCapOf(need *WorkflowNeed, token string) string {
	if need == nil {
		return ""
	}
	want := common.HexToAddress(token)
	for _, cap := range need.Caps {
		if cap.Token != nil && *cap.Token == want {
			return cap.Amount
		}
	}
	return ""
}

// perRunVerdict runs the reported simulate verdict the way the simulate
// handler does, with an optional grant report from the simulated run.
func perRunVerdict(t *testing.T, workflow *model.Workflow, runner common.Address, maxExecution int64, report *SessionGrantReport, chainID int64) *SessionAuthorization {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	engine := deployCheckEngine(db)
	auth := &SimulateAuth{Report: true, MaxExecution: maxExecution}
	vm := &VM{db: db, mu: &sync.Mutex{}, vars: map[string]any{"aa_sender": runner.Hex()}, sessionGrantReport: report}
	owner := common.HexToAddress("0x0000000000000000000000000000000000000051")
	engine.fillSimulateAuthorization(WithSimulateAuth(context.Background(), auth), &model.User{Address: owner}, workflow, vm, chainID)
	require.NotNil(t, auth.Result, "no authorization verdict")
	return auth.Result
}

// Workflow mode sizes Required for every run left. perRun is one run, so a
// budget proposal never starts from the run-count total.
func TestSimulatePerRunIsOneRunInWorkflowMode(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000052")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia, 12, 0, 0)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 12, nil, skillSepolia)
	require.Equal(t, AuthNoGrant, got.Status)
	require.Equal(t, "84", perRunCapOf(got.Required, skillUSDC), "Required keeps the 12-run total")
	require.NotNil(t, got.PerRun)
	require.NotSame(t, got.Required, got.PerRun)
	require.Equal(t, "7", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
	require.False(t, got.PerRunNativeUnsized)
	require.Nil(t, got.PerRun.NativeGasWei, "one run's need carries no self-funded gas")
}

// A budget verdict carries perRun too, from the same one-run sizing.
func TestSimulatePerRunInBudgetMode(t *testing.T) {
	p, _ := semanticsBudget()
	// No runner keeps the verdict off the on-chain remainder read; it is
	// still the budget branch.
	p.Runner = nil
	runner := common.HexToAddress("0x0000000000000000000000000000000000000053")
	task := skillWriteTask("pay", "Pay", testTokenAddr.Hex(), transferCalldata(testRouterAddr, big.NewInt(100)), p.ChainID, 12, 0, 0)
	report := &SessionGrantReport{}
	report.notePolicy(p)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 12, report, p.ChainID)
	require.Equal(t, AuthNotCovered, got.Status)
	require.True(t, strings.Contains(got.Detail, BudgetReadUnavailableCode), "detail = %q", got.Detail)
	require.Equal(t, "100", perRunCapOf(got.Required, testTokenAddr.Hex()), "budget mode already sizes one run")
	require.NotNil(t, got.PerRun)
	require.Equal(t, "100", perRunCapOf(got.PerRun, testTokenAddr.Hex()))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// A re-runnable Sweep has no run count, so Required can't size it. perRun
// sizes each token from what the simulated run sent through the loop over
// sweepPlan's output; a selected token the run didn't send stays unsized.
func TestSimulatePerRunSizesSweepLoopFromTheSimulation(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000054")
	destination := common.HexToAddress(sweepDestination)
	task := sweepTask(t, sweepPlanSource, sweepSettings(skillUSDC, skillUSDT, skillETH))
	task.MaxExecution = 0
	report := &SessionGrantReport{}
	report.noteNoGrant()
	usdc := common.HexToAddress(skillUSDC)
	report.observeCalls([]PlannedCall{{
		Target:   usdc,
		Selector: selectorTransfer,
		Calldata: common.FromHex(transferCalldata(destination, big.NewInt(640_000_000))),
	}})
	report.observeNative(destination, big.NewInt(50_000_000_000_000_000))

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 0, report, skillSepolia)
	require.Equal(t, AuthNoGrant, got.Status)
	require.True(t, got.Required.CapNeedsInput, "Required can't size runs it can't count")
	require.Equal(t, "", perRunCapOf(got.Required, skillUSDC))

	require.NotNil(t, got.PerRun)
	require.Equal(t, "640000000", perRunCapOf(got.PerRun, skillUSDC))
	require.NotNil(t, got.PerRun.NativeSpendCap)
	require.Equal(t, "50000000000000000", got.PerRun.NativeSpendCap.Amount)
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDT)}, got.PerRunUnsizedTokens)
	require.False(t, got.PerRunNativeUnsized)
	require.Contains(t, nativeRecipientSet(got.PerRun), sweepDestination)
}

// A run that sent nothing names every amount it couldn't size, ETH included.
func TestSimulatePerRunMarksUnsizedAmounts(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000055")
	task := sweepTask(t, sweepPlanSource, sweepSettings(skillUSDC, skillETH))
	report := &SessionGrantReport{}
	report.noteNoGrant()

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.NotNil(t, got.PerRun)
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDC)}, got.PerRunUnsizedTokens)
	require.True(t, got.PerRunNativeUnsized)
	require.Nil(t, got.PerRun.NativeSpendCap)

	// The same markers come from the need alone, in address order.
	tokens, native := perRunUnsized(&WorkflowNeed{
		Actions: []model.AllowedAction{skillAction(skillUSDT, selectorTransfer), skillAction(skillUSDC, selectorTransfer)},
		Caps:    []model.ERC20SpendCap{skillCap(skillUSDT, "5")},
	})
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDC)}, tokens)
	require.False(t, native)
	require.Nil(t, withPerRun(nil, &WorkflowNeed{}))
}

// perRunUnknownTransferNode transfers token with an amount the static walk
// can't read: it comes from an earlier step's output.
func perRunUnknownTransferNode(token string, payee common.Address) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ChainId:         skillSepolia,
					ContractAddress: token,
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{
						MethodName:   "transfer",
						MethodParams: []string{payee.Hex(), "{{custom_code1.data}}"},
					}},
				},
			},
		},
	}
}

// perRunEthNode sends amount wei of ETH to payee on Sepolia.
func perRunEthNode(payee common.Address, amount string) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		TaskType: &avsproto.TaskNode_EthTransfer{
			EthTransfer: &avsproto.ETHTransferNode{
				Config: &avsproto.ETHTransferNode_Config{
					Destination: payee.Hex(),
					Amount:      amount,
					ChainId:     skillSepolia,
				},
			},
		},
	}
}

// perRunObserve records the token transfers the simulated run sent.
func perRunObserve(report *SessionGrantReport, token string, payee common.Address, amounts ...int64) {
	calls := make([]PlannedCall, 0, len(amounts))
	for _, amount := range amounts {
		calls = append(calls, PlannedCall{
			Target:   common.HexToAddress(token),
			Selector: selectorTransfer,
			Calldata: common.FromHex(transferCalldata(payee, big.NewInt(amount))),
		})
	}
	report.observeCalls(calls)
}

// A fixed send of 7 beside a send the simulation sized at 13 is a run of
// 20. The fixed cap alone would propose 7 for it.
func TestSimulatePerRunAddsTheSimulatedSendToAFixedOne(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000056")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000002")
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia, 1, 0, 0)
	task.Nodes = append(task.Nodes, perRunUnknownTransferNode(skillUSDC, payee))
	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, skillUSDC, payee, 7, 13)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.NotNil(t, got.PerRun)
	require.Equal(t, "20", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// When the simulation sent only the fixed part, the other send is still
// unknown: the token has no amount rather than the fixed part's 7.
func TestSimulatePerRunLeavesAPartlyUnknownTokenUnsized(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000057")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000003")
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia, 1, 0, 0)
	task.Nodes = append(task.Nodes, perRunUnknownTransferNode(skillUSDC, payee))
	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, skillUSDC, payee, 7)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.NotNil(t, got.PerRun)
	require.Equal(t, "", perRunCapOf(got.PerRun, skillUSDC))
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDC)}, got.PerRunUnsizedTokens)
	require.True(t, got.PerRun.CapNeedsInput)
}

// Fixed sends keep their whole total when the simulation ran fewer of
// them, for example on a branch it didn't take.
func TestSimulatePerRunKeepsFixedSendsTheRunSkipped(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000058")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000004")
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia, 1, 0, 0)
	second := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(5)), skillSepolia, 1, 0, 0)
	task.Nodes = append(task.Nodes, second.Nodes...)
	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, skillUSDC, payee, 7)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.Equal(t, "12", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// ETH sent follows the same rule as a token.
func TestSimulatePerRunCompletesEthSent(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000059")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000005")
	task := &avsproto.Task{
		Id:           "eth",
		Name:         "Send ETH",
		MaxExecution: 1,
		Nodes:        []*avsproto.TaskNode{perRunEthNode(payee, "7"), perRunEthNode(payee, "{{custom_code1.data}}")},
	}

	sentMore := &SessionGrantReport{}
	sentMore.noteNoGrant()
	sentMore.observeNative(payee, big.NewInt(7))
	sentMore.observeNative(payee, big.NewInt(13))
	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, sentMore, skillSepolia)
	require.NotNil(t, got.PerRun.NativeSpendCap)
	require.Equal(t, "20", got.PerRun.NativeSpendCap.Amount)
	require.False(t, got.PerRunNativeUnsized)

	fixedOnly := &SessionGrantReport{}
	fixedOnly.noteNoGrant()
	fixedOnly.observeNative(payee, big.NewInt(7))
	got = perRunVerdict(t, skillWorkflow(task, runner), runner, 1, fixedOnly, skillSepolia)
	require.Nil(t, got.PerRun.NativeSpendCap)
	require.True(t, got.PerRunNativeUnsized)
}

// A two-chain workflow reports one run on the simulated network every
// time, and equally severe chain verdicts are chosen the same way each time.
func TestSimulatePerRunIsForTheSimulatedNetwork(t *testing.T) {
	const base = int64(8453)
	const baseUSDC = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	runner := common.HexToAddress("0x000000000000000000000000000000000000005a")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000006")
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia, 1, 0, 0)
	other := skillWriteTask("pay", "Pay", baseUSDC, transferCalldata(payee, big.NewInt(9)), base, 1, 0, 0)
	task.Nodes = append(task.Nodes, other.Nodes...)

	for attempt := 0; attempt < 32; attempt++ {
		got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, nil, skillSepolia)
		require.Equal(t, AuthNoGrant, got.Status)
		require.Equal(t, skillSepolia, got.Required.ChainID, "attempt %d: the tie goes to the simulated network", attempt)
		require.Equal(t, "7", perRunCapOf(got.PerRun, skillUSDC), "attempt %d", attempt)
		require.Equal(t, "", perRunCapOf(got.PerRun, baseUSDC), "attempt %d", attempt)
	}

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, nil, base)
	require.Equal(t, base, got.Required.ChainID)
	require.Equal(t, "9", perRunCapOf(got.PerRun, baseUSDC))
	require.Equal(t, "", perRunCapOf(got.PerRun, skillUSDC))
}
