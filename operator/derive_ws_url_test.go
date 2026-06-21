package operator

import "testing"

// Exercises applyWsURLDerivation — the same helper NewOperatorFromConfig
// calls at startup. Tests the production code path directly so removing
// or changing the derivation block in operator.go would break these
// tests immediately (no in-test re-implementation that could mask the
// removal).
//
// Regression guard for the Railway env-var rename in #546: without
// derivation, removing eth_ws_url from the operator config would
// leave c.EthWsUrl empty and operator/operator.go:NewOperator would
// fail the WebSocket dial at startup. Surfaced by Copilot review.
func TestApplyWsURLDerivation_FillsMissingAtAllLayers(t *testing.T) {
	cfg := OperatorConfig{
		EthRpcUrl: "https://api-ethereum-mainnet.example.com/key",
		// EthWsUrl intentionally left empty
	}
	cfg.TargetChain.EthRpcUrl = "https://api-base-mainnet.example.com/key"
	// TargetChain.EthWsUrl intentionally left empty
	cfg.Chains = []OperatorChainConfig{
		{ChainID: 1, Name: "ethereum", EthRpcUrl: "https://api-ethereum-mainnet.example.com/key"},
		{ChainID: 8453, Name: "base", EthRpcUrl: "https://api-base-mainnet.example.com/key"},
	}

	applyWsURLDerivation(&cfg)

	if got, want := cfg.EthWsUrl, "wss://api-ethereum-mainnet.example.com/key"; got != want {
		t.Errorf("top-level EthWsUrl = %q, want %q", got, want)
	}
	if got, want := cfg.TargetChain.EthWsUrl, "wss://api-base-mainnet.example.com/key"; got != want {
		t.Errorf("TargetChain.EthWsUrl = %q, want %q", got, want)
	}
	if got, want := cfg.Chains[0].EthWsUrl, "wss://api-ethereum-mainnet.example.com/key"; got != want {
		t.Errorf("Chains[0].EthWsUrl = %q, want %q", got, want)
	}
	if got, want := cfg.Chains[1].EthWsUrl, "wss://api-base-mainnet.example.com/key"; got != want {
		t.Errorf("Chains[1].EthWsUrl = %q, want %q", got, want)
	}
}

// Explicit eth_ws_url values must win over derivation — operators that
// genuinely need a separate WS endpoint can still set it.
func TestApplyWsURLDerivation_PreservesExplicitValues(t *testing.T) {
	cfg := OperatorConfig{
		EthRpcUrl: "https://eth-rpc.example.com/key",
		EthWsUrl:  "wss://eth-ws.different-host.example.com/key",
	}
	cfg.TargetChain.EthRpcUrl = "https://target-rpc.example.com/key"
	cfg.TargetChain.EthWsUrl = "wss://target-ws.different-host.example.com/key"

	applyWsURLDerivation(&cfg)

	if got, want := cfg.EthWsUrl, "wss://eth-ws.different-host.example.com/key"; got != want {
		t.Errorf("explicit top-level EthWsUrl overwritten: got %q, want %q", got, want)
	}
	if got, want := cfg.TargetChain.EthWsUrl, "wss://target-ws.different-host.example.com/key"; got != want {
		t.Errorf("explicit TargetChain.EthWsUrl overwritten: got %q, want %q", got, want)
	}
}
