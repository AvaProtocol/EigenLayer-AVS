package taskengine

import (
	"context"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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
	task.Nodes[1].Id, task.Nodes[2].Id = "sendTokens", "sendEth"
	report := &SessionGrantReport{}
	report.noteNoGrant()
	usdc := []PlannedCall{{
		Target:   common.HexToAddress(skillUSDC),
		Selector: selectorTransfer,
		Calldata: common.FromHex(transferCalldata(destination, big.NewInt(640_000_000))),
	}}
	report.observeCalls(usdc)
	report.observeRunCalls(skillSepolia, "sendTokens_iter_0", usdc)
	report.observeNative(destination, big.NewInt(50_000_000_000_000_000))
	report.observeRunNative(skillSepolia, "sendEth_iter_0", big.NewInt(50_000_000_000_000_000))

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

	// The markers are the need's own, in address order.
	tokens, native := perRunUnsized(&WorkflowNeed{
		UnsizedTokens: []common.Address{common.HexToAddress(skillUSDT), common.HexToAddress(skillUSDC)},
		NativeUnsized: true,
	})
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDC), common.HexToAddress(skillUSDT)}, tokens)
	require.True(t, native)
	require.Nil(t, withPerRun(nil, &WorkflowNeed{}))
}

// perRunCallNode is node id calling contract with calldata on chain.
func perRunCallNode(id, contract, calldata string, chain int64) *avsproto.TaskNode {
	node := skillWriteTask("pay", "Pay", contract, calldata, chain, 1, 0, 0).Nodes[0]
	node.Id = id
	return node
}

// perRunUnknownTransferNode is node id transferring token on chain with an
// amount the static walk can't read: it comes from an earlier step's output.
func perRunUnknownTransferNode(id, token string, payee common.Address, chain int64) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		Id: id,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ChainId:         chain,
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

// perRunEthNode is node id sending amount wei of ETH to payee on Sepolia.
func perRunEthNode(id string, payee common.Address, amount string) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		Id: id,
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

// perRunTask is a one-run workflow of nodes.
func perRunTask(nodes ...*avsproto.TaskNode) *avsproto.Task {
	return &avsproto.Task{Id: "pay", Name: "Pay", MaxExecution: 1, Nodes: nodes}
}

// perRunObserve records the token transfers node sent on Sepolia in the
// simulated run.
func perRunObserve(report *SessionGrantReport, node, token string, payee common.Address, amounts ...int64) {
	calls := make([]PlannedCall, 0, len(amounts))
	for _, amount := range amounts {
		calls = append(calls, PlannedCall{
			Target:   common.HexToAddress(token),
			Selector: selectorTransfer,
			Calldata: common.FromHex(transferCalldata(payee, big.NewInt(amount))),
		})
	}
	report.observeCalls(calls)
	report.observeRunCalls(skillSepolia, node, calls)
}

// perRunFixedAndUnknown is a fixed transfer of 7 beside a transfer whose
// amount the walk can't read.
func perRunFixedAndUnknown(payee common.Address) *avsproto.Task {
	return perRunTask(
		perRunCallNode("fixed", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia),
		perRunUnknownTransferNode("dynamic", skillUSDC, payee, skillSepolia),
	)
}

// A fixed send of 7 beside a send the simulation sized at 13 is a run of
// 20. The fixed cap alone would propose 7 for it.
func TestSimulatePerRunAddsTheSimulatedSendToAFixedOne(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000056")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000002")
	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, "fixed", skillUSDC, payee, 7)
	perRunObserve(report, "dynamic", skillUSDC, payee, 13)

	got := perRunVerdict(t, skillWorkflow(perRunFixedAndUnknown(payee), runner), runner, 1, report, skillSepolia)
	require.NotNil(t, got.PerRun)
	require.Equal(t, "20", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// When the simulation sent only the fixed part, the other send is still
// unknown: the token has no amount rather than the fixed part's 7.
func TestSimulatePerRunLeavesAPartlyUnknownTokenUnsized(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000057")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000003")
	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, "fixed", skillUSDC, payee, 7)

	got := perRunVerdict(t, skillWorkflow(perRunFixedAndUnknown(payee), runner), runner, 1, report, skillSepolia)
	require.NotNil(t, got.PerRun)
	require.Equal(t, "", perRunCapOf(got.PerRun, skillUSDC))
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDC)}, got.PerRunUnsizedTokens)
	require.True(t, got.PerRun.CapNeedsInput)
}

// One unknown send the run sized does not size another the run never
// reached. The token waits for every send whose amount the walk couldn't
// read.
func TestSimulatePerRunNeedsEveryUnknownSendToRun(t *testing.T) {
	runner := common.HexToAddress("0x000000000000000000000000000000000000005b")
	first := common.HexToAddress("0x0000000000000000000000000000000000000007")
	second := common.HexToAddress("0x0000000000000000000000000000000000000008")
	task := perRunTask(
		perRunCallNode("fixed", skillUSDC, transferCalldata(first, big.NewInt(7)), skillSepolia),
		perRunUnknownTransferNode("first", skillUSDC, first, skillSepolia),
		perRunUnknownTransferNode("second", skillUSDC, second, skillSepolia),
	)

	partial := &SessionGrantReport{}
	partial.noteNoGrant()
	perRunObserve(partial, "fixed", skillUSDC, first, 7)
	perRunObserve(partial, "first", skillUSDC, first, 13)
	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, partial, skillSepolia)
	require.Equal(t, "", perRunCapOf(got.PerRun, skillUSDC), "13 over the fixed 7 does not account for the second send")
	require.Equal(t, []common.Address{common.HexToAddress(skillUSDC)}, got.PerRunUnsizedTokens)

	whole := &SessionGrantReport{}
	whole.noteNoGrant()
	perRunObserve(whole, "fixed", skillUSDC, first, 7)
	perRunObserve(whole, "first", skillUSDC, first, 13)
	perRunObserve(whole, "second", skillUSDC, second, 4)
	got = perRunVerdict(t, skillWorkflow(task, runner), runner, 1, whole, skillSepolia)
	require.Equal(t, "24", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// Fixed sends keep their whole total when the simulation ran fewer of
// them, for example on a branch it didn't take.
func TestSimulatePerRunKeepsFixedSendsTheRunSkipped(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000058")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000004")
	task := perRunTask(
		perRunCallNode("first", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia),
		perRunCallNode("second", skillUSDC, transferCalldata(payee, big.NewInt(5)), skillSepolia),
	)
	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, "first", skillUSDC, payee, 7)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.Equal(t, "12", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// ETH sent follows the same rule as a token.
func TestSimulatePerRunCompletesEthSent(t *testing.T) {
	runner := common.HexToAddress("0x0000000000000000000000000000000000000059")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000005")
	task := perRunTask(perRunEthNode("fixed", payee, "7"), perRunEthNode("dynamic", payee, "{{custom_code1.data}}"))

	sentMore := &SessionGrantReport{}
	sentMore.noteNoGrant()
	sentMore.observeRunNative(skillSepolia, "fixed", big.NewInt(7))
	sentMore.observeRunNative(skillSepolia, "dynamic", big.NewInt(13))
	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, sentMore, skillSepolia)
	require.NotNil(t, got.PerRun.NativeSpendCap)
	require.Equal(t, "20", got.PerRun.NativeSpendCap.Amount)
	require.False(t, got.PerRunNativeUnsized)

	fixedOnly := &SessionGrantReport{}
	fixedOnly.noteNoGrant()
	fixedOnly.observeRunNative(skillSepolia, "fixed", big.NewInt(7))
	got = perRunVerdict(t, skillWorkflow(task, runner), runner, 1, fixedOnly, skillSepolia)
	require.Nil(t, got.PerRun.NativeSpendCap)
	require.True(t, got.PerRunNativeUnsized)
}

// A self-funded MAX sends its balance less the network fee it reserves.
// One run's ETH is the transfer; the grant report keeps the whole balance
// for sizing the grant.
func TestSimulatePerRunCountsWhatASelfFundedMaxSends(t *testing.T) {
	// Do not use t.Parallel: the MAX fixtures share package-global state.
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	storeMaxTestGrant(t, db, "100000000000000000")
	chain, client, url := newMaxTestChain(t, big.NewInt(10_000_000_000_000_000))
	chain.deposit, chain.remaining = big.NewInt(0), big.NewInt(100_000_000_000_000_000)
	processor, node := newMaxTestProcessor(t, client, selfFundedMaxConfig(url))
	processor.vm.WithDb(db).SetSimulation(true)
	report := &SessionGrantReport{}
	processor.vm.sessionGrantReport = report

	step, err := processor.Execute("send-eth", node)
	require.NoError(t, err)
	transfer, metadata := stepMaps(t, step)
	require.NotEmpty(t, metadata["gasReserve"])

	perRun := simulatePerRun(perRunTask(processor.GetTaskNode()), nil, maxTestChain, report, skillNow())
	require.NotNil(t, perRun)
	require.NotNil(t, perRun.NativeSpendCap)
	require.Equal(t, transfer["value"], perRun.NativeSpendCap.Amount)
	require.False(t, perRun.NativeUnsized)
	require.Equal(t, "10000000000000000", report.NativeSpend.String(), "the grant still sizes from the whole balance")
}

// A token sent on another network is not part of this network's run, in a
// verdict for either network.
func TestSimulatePerRunReadsOnlyTheSimulatedNetworksSends(t *testing.T) {
	const base = int64(8453)
	const baseUSDC = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	runner := common.HexToAddress("0x000000000000000000000000000000000000005c")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000009")
	task := perRunTask(
		perRunUnknownTransferNode("sepolia", skillUSDC, payee, skillSepolia),
		perRunUnknownTransferNode("base", baseUSDC, payee, base),
	)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger()).WithDb(db)
	vm.AddVar("aa_sender", runner.Hex())
	report := &SessionGrantReport{}
	vm.sessionGrantReport = report
	owner := common.HexToAddress("0x0000000000000000000000000000000000000051")
	for _, sent := range []struct {
		node   *avsproto.TaskNode
		chain  int64
		token  string
		amount int64
	}{{task.Nodes[0], skillSepolia, skillUSDC, 7}, {task.Nodes[1], base, baseUSDC, 9}} {
		processor := NewContractWriteProcessor(vm, nil, &config.SmartWalletConfig{ChainID: sent.chain}, owner)
		processor.CommonProcessor.SetTaskNode(sent.node)
		require.NoError(t, processor.preflightSessionGrantError([]PlannedCall{{
			Target:   common.HexToAddress(sent.token),
			Selector: selectorTransfer,
			Calldata: common.FromHex(transferCalldata(payee, big.NewInt(sent.amount))),
		}}))
	}

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.Equal(t, "7", perRunCapOf(got.PerRun, skillUSDC))
	require.Equal(t, "", perRunCapOf(got.PerRun, baseUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)

	got = perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, base)
	require.Equal(t, "9", perRunCapOf(got.PerRun, baseUSDC))
	require.Equal(t, "", perRunCapOf(got.PerRun, skillUSDC))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// An unknown transfer stays listed when the token's contract has another
// method too, as WETH has deposit.
func TestSimulatePerRunListsAnUnknownTransferBesideAnotherMethod(t *testing.T) {
	runner := common.HexToAddress("0x000000000000000000000000000000000000005d")
	payee := common.HexToAddress("0x000000000000000000000000000000000000000a")
	task := perRunTask(
		perRunCallNode("deposit", skillWETH, "0xd0e30db0", skillSepolia),
		perRunUnknownTransferNode("send", skillWETH, payee, skillSepolia),
	)

	got := perRunVerdict(t, skillWorkflow(task, runner), runner, 1, nil, skillSepolia)
	require.Equal(t, "", perRunCapOf(got.PerRun, skillWETH))
	require.Equal(t, []common.Address{common.HexToAddress(skillWETH)}, got.PerRunUnsizedTokens)

	report := &SessionGrantReport{}
	report.noteNoGrant()
	perRunObserve(report, "send", skillWETH, payee, 5)
	got = perRunVerdict(t, skillWorkflow(task, runner), runner, 1, report, skillSepolia)
	require.Equal(t, "5", perRunCapOf(got.PerRun, skillWETH))
	require.Empty(t, got.PerRunUnsizedTokens)
}

// A two-chain workflow reports one run on the simulated network every
// time, and equally severe chain verdicts are chosen the same way each time.
func TestSimulatePerRunIsForTheSimulatedNetwork(t *testing.T) {
	const base = int64(8453)
	const baseUSDC = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	runner := common.HexToAddress("0x000000000000000000000000000000000000005a")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000006")
	task := perRunTask(
		perRunCallNode("sepolia", skillUSDC, transferCalldata(payee, big.NewInt(7)), skillSepolia),
		perRunCallNode("base", baseUSDC, transferCalldata(payee, big.NewInt(9)), base),
	)

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

// A loop iteration's sends belong to its loop node.
func TestWorkflowNodeIDOfALoopIteration(t *testing.T) {
	require.Equal(t, "sendTokens", workflowNodeID("sendTokens_iter_3"))
	require.Equal(t, "sendTokens", workflowNodeID("sendTokens"))
	require.Equal(t, "send_iter_x", workflowNodeID("send_iter_x"))
	require.Equal(t, "", workflowNodeID(""))
}
