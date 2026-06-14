package operator

import (
	"reflect"
	"testing"
	"time"
)

func TestEffectiveChains_DerivedFromTargetChain(t *testing.T) {
	cfg := &OperatorConfig{}
	cfg.TargetChain.EthRpcUrl = "https://eth.example.com"
	cfg.TargetChain.EthWsUrl = "wss://eth.example.com"

	got := cfg.EffectiveChains()
	want := []OperatorChainConfig{{
		EthRpcUrl: "https://eth.example.com",
		EthWsUrl:  "wss://eth.example.com",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveChains derived from TargetChain: got %+v, want %+v", got, want)
	}
}

func TestEffectiveChains_DerivedFromEigenLayerRPC(t *testing.T) {
	cfg := &OperatorConfig{}
	cfg.EthRpcUrl = "https://eigen.example.com"
	cfg.EthWsUrl = "wss://eigen.example.com"

	got := cfg.EffectiveChains()
	if len(got) != 1 {
		t.Fatalf("want one chain entry, got %d", len(got))
	}
	if got[0].EthRpcUrl != "https://eigen.example.com" {
		t.Errorf("eigen RPC fallback failed: got %q", got[0].EthRpcUrl)
	}
}

func TestEffectiveChains_ChainsTakesPrecedence(t *testing.T) {
	cfg := &OperatorConfig{
		Chains: []OperatorChainConfig{
			{ChainID: 1, Name: "ethereum", EthRpcUrl: "https://eth.example.com"},
			{ChainID: 8453, Name: "base", EthRpcUrl: "https://base.example.com"},
		},
	}
	// TargetChain set to something else — should be ignored when Chains is non-empty.
	cfg.TargetChain.EthRpcUrl = "https://legacy.example.com"

	got := cfg.EffectiveChains()
	if len(got) != 2 {
		t.Fatalf("want 2 chains, got %d", len(got))
	}
	if got[0].ChainID != 1 || got[1].ChainID != 8453 {
		t.Errorf("chain order or IDs wrong: got %+v", got)
	}
}

func TestTriggersForChain_FallbackOnZero(t *testing.T) {
	primary := &ChainTriggerSet{ChainID: 11155111, Name: "sepolia"}
	other := &ChainTriggerSet{ChainID: 8453, Name: "base"}
	o := &Operator{
		chainTriggers: map[int64]*ChainTriggerSet{11155111: primary, 8453: other},
		chainOrder:    []int64{11155111, 8453},
	}

	// chain_id = 0 (legacy aggregator) → first configured chain.
	got, ok := o.triggersForChain(0)
	if !ok || got != primary {
		t.Errorf("chain_id=0 fallback: got %+v (ok=%v), want primary", got, ok)
	}

	// Exact match.
	got, ok = o.triggersForChain(8453)
	if !ok || got != other {
		t.Errorf("chain_id=8453: got %+v (ok=%v), want base", got, ok)
	}

	// Unsupported chain.
	_, ok = o.triggersForChain(1) // operator doesn't monitor ethereum mainnet
	if ok {
		t.Errorf("chain_id=1 (unsupported): got ok=true, want false")
	}
}

func TestSupportedChainIDs_FiltersZero(t *testing.T) {
	// chain_id=0 is a placeholder for "legacy operator with undetected chain"
	// and must not be advertised to the aggregator. lastHeadSeenAt is
	// seeded to now so the liveness filter (added in the capability-
	// hardening change) is neutral here — this test focuses on the
	// chain_id=0 exclusion.
	now := time.Now()
	o := &Operator{
		chainTriggers: map[int64]*ChainTriggerSet{
			0:        {lastHeadSeenAt: now},
			11155111: {lastHeadSeenAt: now},
			8453:     {lastHeadSeenAt: now},
		},
		chainOrder: []int64{0, 11155111, 8453},
	}
	got := o.supportedChainIDs()
	want := []int64{11155111, 8453}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("supportedChainIDs: got %v, want %v", got, want)
	}
}

func TestSupportedChainIDs_Empty(t *testing.T) {
	o := &Operator{}
	got := o.supportedChainIDs()
	if len(got) != 0 {
		t.Errorf("supportedChainIDs on empty operator: got %v, want []", got)
	}
}
