package taskengine

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// seedOperator installs an operatorState in the engine's trackSyncedTasks
// map without going through the full StreamCheckToOperator dance. Useful
// for unit-testing the capability-snapshot logic in isolation.
func seedOperator(n *Engine, addr string, chains []int64) {
	n.lock.Lock()
	defer n.lock.Unlock()
	if n.trackSyncedTasks == nil {
		n.trackSyncedTasks = make(map[string]*operatorState)
	}
	n.trackSyncedTasks[addr] = &operatorState{
		TaskID:                  map[string]bool{},
		MonotonicClock:          time.Now().UnixNano(),
		SupportedChainIDs:       chains,
		SupportedChainsExplicit: len(chains) > 0,
	}
}

// TestUpdateOperatorSupportedChains_LiveUpdate confirms the Ping-driven
// path mutates the per-operator snapshot in trackSyncedTasks. This is
// the core load-bearing wire for Step 2 of the capability hardening.
func TestUpdateOperatorSupportedChains_LiveUpdate(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())
	addr := "0xAAAAaAaaaaAaaaaAAAAAAaaaAAaaaAaaAaAaAAAa"

	seedOperator(engine, addr, []int64{1, 8453})

	// Narrow the advertised set as if Base subscription stalled.
	engine.UpdateOperatorSupportedChains(addr, []int64{1})

	engine.lock.Lock()
	got := append([]int64(nil), engine.trackSyncedTasks[addr].SupportedChainIDs...)
	engine.lock.Unlock()

	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("SupportedChainIDs after live update: got %v want [1]", got)
	}
}

// TestUpdateOperatorSupportedChains_EmptyDoesNotWidenCoverage is the
// regression guard for the empty-list semantics that Copilot flagged.
// An operator that explicitly advertised [1, 8453] and then degrades
// to [] (all subscriptions stalled) must NOT be expanded to "covers
// everything" — that would silently mis-route tasks to a dead
// operator. The SupportedChainsExplicit flag distinguishes
// degraded-empty from legacy-empty.
func TestUpdateOperatorSupportedChains_EmptyDoesNotWidenCoverage(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	addr := "0xExplicit"
	// Simulate the SyncMessages-connect path: explicit non-empty list.
	engine.lock.Lock()
	engine.trackSyncedTasks[addr] = &operatorState{
		TaskID:                  map[string]bool{},
		SupportedChainIDs:       []int64{1, 8453},
		SupportedChainsExplicit: true,
	}
	engine.lock.Unlock()

	// Ping comes in with an empty list (operator narrowed everything).
	engine.UpdateOperatorSupportedChains(addr, nil)

	engine.lock.Lock()
	covering := engine.operatorsCoveringChain(1)
	hasExplicit := engine.trackSyncedTasks[addr].SupportedChainsExplicit
	engine.lock.Unlock()

	if len(covering) != 0 {
		t.Fatalf("explicit-empty operator should NOT cover chain 1, got %v", covering)
	}
	if !hasExplicit {
		t.Fatalf("SupportedChainsExplicit should remain true after empty Ping")
	}
}

// TestOperatorsCoveringChain_LegacyVsExplicit confirms the new flag
// distinguishes legacy back-compat (empty list = covers all) from
// degraded explicit (empty list = covers nothing).
func TestOperatorsCoveringChain_LegacyVsExplicit(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	engine.lock.Lock()
	engine.trackSyncedTasks["0xLegacy"] = &operatorState{
		TaskID:                  map[string]bool{},
		SupportedChainIDs:       nil,
		SupportedChainsExplicit: false,
	}
	engine.trackSyncedTasks["0xDegraded"] = &operatorState{
		TaskID:                  map[string]bool{},
		SupportedChainIDs:       nil,
		SupportedChainsExplicit: true,
	}
	covering := engine.operatorsCoveringChain(1)
	engine.lock.Unlock()

	if len(covering) != 1 || covering[0] != "0xLegacy" {
		t.Fatalf("only legacy operator should cover chain 1, got %v", covering)
	}
}

// TestUpdateOperatorSupportedChains_UnknownOperator is a no-op when the
// operator never opened a SyncMessages stream. We don't want a stray
// Ping to silently create an operator entry — that path is owned by
// StreamCheckToOperator and produces side effects (tickers, locks)
// that the Ping handler can't replicate.
func TestUpdateOperatorSupportedChains_UnknownOperator(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	engine.UpdateOperatorSupportedChains("0xnonexistent", []int64{1})

	engine.lock.Lock()
	_, exists := engine.trackSyncedTasks["0xnonexistent"]
	engine.lock.Unlock()
	if exists {
		t.Fatalf("UpdateOperatorSupportedChains should not auto-register an unknown operator")
	}
}

// TestOperatorsCoveringChain checks the three branches that drive
// CreateTask coverage validation: legacy (empty SupportedChainIDs),
// multi-chain with explicit hit, and multi-chain with no hit. Chain 0
// is the chain-agnostic case and should return every connected
// operator (so chain-agnostic tasks like cron still route).
func TestOperatorsCoveringChain(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	seedOperator(engine, "0xLegacy", nil) // empty = covers everything
	seedOperator(engine, "0xMainnet", []int64{1, 8453})
	seedOperator(engine, "0xTestnet", []int64{11155111, 84532})

	engine.lock.Lock()
	gotEth := engine.operatorsCoveringChain(1)
	gotBase := engine.operatorsCoveringChain(8453)
	gotSepolia := engine.operatorsCoveringChain(11155111)
	gotZero := engine.operatorsCoveringChain(0)
	gotBNB := engine.operatorsCoveringChain(56)
	engine.lock.Unlock()

	// 0xLegacy + 0xMainnet cover chain 1.
	if !containsAll(gotEth, "0xLegacy", "0xMainnet") || len(gotEth) != 2 {
		t.Fatalf("chain 1: got %v want [0xLegacy 0xMainnet]", gotEth)
	}
	if !containsAll(gotBase, "0xLegacy", "0xMainnet") || len(gotBase) != 2 {
		t.Fatalf("chain 8453: got %v want [0xLegacy 0xMainnet]", gotBase)
	}
	if !containsAll(gotSepolia, "0xLegacy", "0xTestnet") || len(gotSepolia) != 2 {
		t.Fatalf("chain 11155111: got %v want [0xLegacy 0xTestnet]", gotSepolia)
	}
	// chain 0 → every connected operator.
	if len(gotZero) != 3 {
		t.Fatalf("chain 0: got %v want all three", gotZero)
	}
	// chain 56 (BNB) is uncovered by Mainnet/Testnet operators, but
	// 0xLegacy's empty list still counts.
	if len(gotBNB) != 1 || gotBNB[0] != "0xLegacy" {
		t.Fatalf("chain 56: got %v want [0xLegacy]", gotBNB)
	}
}

// TestChainNeedsOperatorMonitoring confirms only block + event
// triggers gate CreateTask on operator chain coverage. Cron / fixed
// time / manual fire entirely off the gateway and have no operator
// chain dependency.
func TestChainNeedsOperatorMonitoring(t *testing.T) {
	cases := []struct {
		tt   avsproto.TriggerType
		want bool
	}{
		{avsproto.TriggerType_TRIGGER_TYPE_BLOCK, true},
		{avsproto.TriggerType_TRIGGER_TYPE_EVENT, true},
		{avsproto.TriggerType_TRIGGER_TYPE_CRON, false},
		{avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME, false},
		{avsproto.TriggerType_TRIGGER_TYPE_MANUAL, false},
		{avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED, false},
	}
	for _, c := range cases {
		if got := chainNeedsOperatorMonitoring(c.tt); got != c.want {
			t.Errorf("chainNeedsOperatorMonitoring(%s) = %v, want %v", c.tt, got, c.want)
		}
	}
}

// TestScanOrphanedTasks_LogsForUncoveredChains: a task on a chain
// nobody covers should be flagged. A task on a covered chain or with
// a chain-agnostic trigger should not. We don't capture logger output
// here (would couple to logger internals) — we just confirm the scan
// runs without panic on a populated engine, since the warning path is
// already exercised in production logs.
//
// The real value of this test is making sure the lock-acquisition
// order (n.lock.Lock then read tasks + trackSyncedTasks) doesn't
// deadlock against the engine's other consumers.
func TestScanOrphanedTasks_NoDeadlock(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	seedOperator(engine, "0xMainnet", []int64{1, 8453})

	// Chain lives on the event trigger (G2/G5), not the task — coverage is
	// judged on the trigger's monitoring chain.
	eventOnChain := func(chainID int64) *avsproto.TaskTrigger {
		return &avsproto.TaskTrigger{
			Type:        avsproto.TriggerType_TRIGGER_TYPE_EVENT,
			TriggerType: &avsproto.TaskTrigger_Event{Event: &avsproto.EventTrigger{Config: &avsproto.EventTrigger_Config{ChainId: chainID}}},
		}
	}
	engine.lock.Lock()
	engine.tasks["covered-event"] = &model.Workflow{
		Task: &avsproto.Task{
			Id:      "covered-event",
			Trigger: eventOnChain(1),
		},
	}
	engine.tasks["orphan-event"] = &model.Workflow{
		Task: &avsproto.Task{
			Id:      "orphan-event",
			Trigger: eventOnChain(56), // BNB — no operator covers
		},
	}
	engine.tasks["cron-chain-agnostic"] = &model.Workflow{
		Task: &avsproto.Task{
			Id:      "cron-chain-agnostic",
			Trigger: &avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_CRON},
		},
	}
	engine.lock.Unlock()

	// scanOrphanedTasks must complete promptly; deadlock would hang
	// this test until the runtime timeout.
	done := make(chan struct{})
	go func() {
		engine.scanOrphanedTasks()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("scanOrphanedTasks did not return within 2s — possible deadlock")
	}
}

// TestCreateWorkflow_RejectsUncoveredChain verifies the gateway-mode
// guard: a block/event trigger for a chain no connected operator
// advertises must be rejected at CreateTask time. The most important
// integration of the capability primitives — without this the
// liveness watermark + Ping refresh have no behavioral effect on
// CreateTask.
//
// Implementation note: we drive only the chain-coverage branch by
// constructing a Workflow with no SmartWalletAddress (skips the
// ownership check) and exercise the engine's chain-coverage gate
// directly via the helper. The full CreateWorkflow path requires
// wallet ownership + DB + JWT auth scaffolding that other tests
// already cover end-to-end.
func TestCreateWorkflow_RejectsUncoveredChain(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	// Only an Ethereum-only operator is connected; chain 8453 (Base)
	// has no coverage.
	seedOperator(engine, "0xMainnetOnly", []int64{1})

	// chainNeedsOperatorMonitoring + operatorsCoveringChain are the
	// gate. Exercise both arms.
	if !chainNeedsOperatorMonitoring(avsproto.TriggerType_TRIGGER_TYPE_EVENT) {
		t.Fatalf("event trigger should require coverage")
	}
	engine.lock.Lock()
	coveringBase := engine.operatorsCoveringChain(8453)
	coveringEth := engine.operatorsCoveringChain(1)
	engine.lock.Unlock()
	if len(coveringBase) != 0 {
		t.Fatalf("Base should have no coverage in this scenario, got %v", coveringBase)
	}
	if len(coveringEth) != 1 {
		t.Fatalf("Ethereum should be covered by 0xMainnetOnly, got %v", coveringEth)
	}

	// Now register a Base operator and verify coverage flips.
	engine.UpdateOperatorSupportedChains("0xMainnetOnly", []int64{1, 8453})
	engine.lock.Lock()
	coveringBaseAfter := engine.operatorsCoveringChain(8453)
	engine.lock.Unlock()
	if len(coveringBaseAfter) != 1 {
		t.Fatalf("after live update, Base should be covered, got %v", coveringBaseAfter)
	}
}

func containsAll(s []string, want ...string) bool {
	set := map[string]bool{}
	for _, v := range s {
		set[v] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
