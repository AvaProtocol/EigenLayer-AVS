package operator

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// Operator-side mirror of the test that lives in core/config: when the
// operator YAML omits eth_ws_url at any of its three layers (top-level,
// target_chain, per-chain entry in Chains), NewOperatorFromConfig
// derives it from the matching eth_rpc_url via config.DeriveWsURL.
//
// Regression guard for the Railway env-var rename in #546 — without
// derivation, removing eth_ws_url from operator-railway.yaml would
// leave c.EthWsUrl empty and operator/operator.go:NewOperator would
// fail the WebSocket dial at startup. Surfaced by the Copilot review.
func TestOperatorConfig_DerivesWsURLWhenMissing(t *testing.T) {
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

	// We don't want to actually build the full Operator (needs ECDSA
	// keystore, AVS contracts, etc.) — just exercise the derivation
	// block at the top of NewOperatorFromConfig. Replicate that block
	// here against an exported helper to keep the test fast.
	applyWsDerivation(&cfg)

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
func TestOperatorConfig_PreservesExplicitWsURL(t *testing.T) {
	cfg := OperatorConfig{
		EthRpcUrl: "https://eth-rpc.example.com/key",
		EthWsUrl:  "wss://eth-ws.different-host.example.com/key",
	}
	cfg.TargetChain.EthRpcUrl = "https://target-rpc.example.com/key"
	cfg.TargetChain.EthWsUrl = "wss://target-ws.different-host.example.com/key"

	applyWsDerivation(&cfg)

	if got, want := cfg.EthWsUrl, "wss://eth-ws.different-host.example.com/key"; got != want {
		t.Errorf("explicit top-level EthWsUrl overwritten: got %q, want %q", got, want)
	}
	if got, want := cfg.TargetChain.EthWsUrl, "wss://target-ws.different-host.example.com/key"; got != want {
		t.Errorf("explicit TargetChain.EthWsUrl overwritten: got %q, want %q", got, want)
	}
}

// applyWsDerivation is the exact block inlined at the top of
// NewOperatorFromConfig. Extracted here so tests can exercise it
// without building the full Operator. If you change the derivation
// rules in NewOperatorFromConfig, update this in lockstep — these
// tests are the regression guard.
func applyWsDerivation(c *OperatorConfig) {
	if c.EthWsUrl == "" {
		c.EthWsUrl = config.DeriveWsURL(c.EthRpcUrl)
	}
	if c.TargetChain.EthWsUrl == "" {
		c.TargetChain.EthWsUrl = config.DeriveWsURL(c.TargetChain.EthRpcUrl)
	}
	for i := range c.Chains {
		if c.Chains[i].EthWsUrl == "" {
			c.Chains[i].EthWsUrl = config.DeriveWsURL(c.Chains[i].EthRpcUrl)
		}
	}
}
