package operator

import (
	"reflect"
	"testing"
	"time"
)

// TestChainTriggerSet_LivenessWatermark exercises the basic
// markHeadSeen/isHeadFresh contract that supportedChainIDs() relies
// on to drop stalled chains from the advertised set.
func TestChainTriggerSet_LivenessWatermark(t *testing.T) {
	set := &ChainTriggerSet{
		ChainID:        1,
		lastHeadSeenAt: time.Now().Add(-10 * time.Second), // recent
	}

	if !set.isHeadFresh(30 * time.Second) {
		t.Fatalf("a 10s-old head should be fresh under a 30s threshold")
	}
	if set.isHeadFresh(5 * time.Second) {
		t.Fatalf("a 10s-old head should NOT be fresh under a 5s threshold")
	}

	// markHeadSeen advances the watermark to now.
	set.lastHeadMu.Lock()
	set.lastHeadSeenAt = time.Now().Add(-1 * time.Minute)
	set.lastHeadMu.Unlock()
	if set.isHeadFresh(30 * time.Second) {
		t.Fatalf("a 1min-old head should not be fresh under 30s")
	}
	set.markHeadSeen()
	if !set.isHeadFresh(30 * time.Second) {
		t.Fatalf("after markHeadSeen, head should be fresh")
	}
}

// hasBlockWork / noBlockWork are injectable stubs for ChainTriggerSet's
// block-task probe, so liveness tests don't need a real (WS-dialing)
// BlockTrigger.
func hasBlockWork() bool { return true }
func noBlockWork() bool  { return false }

// TestSupportedChainIDs_FiltersStalled confirms a chain that is stalled
// *while it has block work* (head older than the staleness threshold but the
// subscription should be producing heads) drops out of the advertised set,
// even though it's still in chainOrder.
func TestSupportedChainIDs_FiltersStalled(t *testing.T) {
	o := &Operator{
		chainTriggers: map[int64]*ChainTriggerSet{
			1: {
				ChainID:        1,
				lastHeadSeenAt: time.Now(), // fresh
				hasBlockTasks:  hasBlockWork,
			},
			8453: {
				ChainID:        8453,
				lastHeadSeenAt: time.Now().Add(-10 * time.Minute), // stale
				hasBlockTasks:  hasBlockWork,                      // has block work → stall counts
			},
		},
		chainOrder: []int64{1, 8453},
	}

	got := o.supportedChainIDs()
	want := []int64{1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supportedChainIDs filter: got %v, want %v", got, want)
	}

	// allConfiguredChainIDs preserves the full set for observability
	// — a stalled chain still shows up in logs / dashboards.
	all := o.allConfiguredChainIDs()
	wantAll := []int64{1, 8453}
	if !reflect.DeepEqual(all, wantAll) {
		t.Fatalf("allConfiguredChainIDs: got %v, want %v", all, wantAll)
	}
}

// TestSupportedChainIDs_IdleChainStaysAdvertised is the core fix: a chain with
// NO block tasks has its head subscription intentionally stopped, so a stale
// watermark is expected — it must STAY advertised (it can still serve event
// tasks, and the gateway only routes the first block task to advertised
// chains; dropping it would deadlock coverage).
func TestSupportedChainIDs_IdleChainStaysAdvertised(t *testing.T) {
	o := &Operator{
		chainTriggers: map[int64]*ChainTriggerSet{
			1: {
				ChainID:        1,
				lastHeadSeenAt: time.Now().Add(-10 * time.Minute), // stale...
				hasBlockTasks:  noBlockWork,                       // ...but idle → not a stall
			},
			8453: {
				ChainID:        8453,
				lastHeadSeenAt: time.Now().Add(-10 * time.Minute), // stale + has work → dropped
				hasBlockTasks:  hasBlockWork,
			},
		},
		chainOrder: []int64{1, 8453},
	}

	got := o.supportedChainIDs()
	want := []int64{1} // idle chain 1 advertised; stalled-with-work chain 8453 dropped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("idle chain advertisement: got %v, want %v", got, want)
	}
}

// TestSupportedChainIDs_SkipsMissingTriggerSet handles the defensive
// nil-check: if a chainOrder entry has no matching ChainTriggerSet
// (e.g. concurrent map mutation, future cleanup), don't advertise it.
func TestSupportedChainIDs_SkipsMissingTriggerSet(t *testing.T) {
	o := &Operator{
		chainTriggers: map[int64]*ChainTriggerSet{
			1: {
				ChainID:        1,
				lastHeadSeenAt: time.Now(),
			},
			// chain 8453 is in chainOrder below but missing here
		},
		chainOrder: []int64{1, 8453},
	}
	got := o.supportedChainIDs()
	if !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("missing trigger set: got %v want [1]", got)
	}
}
