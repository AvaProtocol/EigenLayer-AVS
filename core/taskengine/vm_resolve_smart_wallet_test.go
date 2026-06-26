package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
)

// TestResolveSmartWalletForNode_TaskChainIDFallback exercises the
// three-tier resolver (node.chain_id -> task.chain_id -> vm default)
// that the regression fix introduced. The historical bug was that loop
// iterations create nested nodes without a per-iteration chain_id, so
// resolveSmartWalletForNode(0) fell back to v.smartWalletConfig, which
// in gateway mode is populated from chains[0] (Ethereum mainnet by
// convention). For any task targeting a non-mainnet chain, paymaster
// ops dialed the wrong RPC and surfaced as "no contract code at given
// address" (Sentry EIGENLAYER-AVS-1N/1M, user-reported failure
// 2026-05-30 01:55 UTC on Sepolia).
func TestResolveSmartWalletForNode_TaskChainIDFallback(t *testing.T) {
	const (
		mainnetChainID = int64(1)
		sepoliaChainID = int64(11_155_111)
		baseChainID    = int64(8453)
	)

	mainnetCfg := &config.SmartWalletConfig{
		ChainID:          mainnetChainID,
		EthRpcUrl:        "https://mainnet.example/rpc",
		PaymasterAddress: common.HexToAddress("0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f"),
	}
	sepoliaCfg := &config.SmartWalletConfig{
		ChainID:          sepoliaChainID,
		EthRpcUrl:        "https://sepolia.example/rpc",
		PaymasterAddress: common.HexToAddress("0xd856f532F7C032e6b30d76F19187F25A068D6d92"),
	}
	baseCfg := &config.SmartWalletConfig{
		ChainID:   baseChainID,
		EthRpcUrl: "https://base.example/rpc",
	}

	// Resolver behaves like engine.ResolveSmartWalletConfig in gateway
	// mode: returns the per-chain entry if registered, nil otherwise.
	registry := map[int64]*config.SmartWalletConfig{
		mainnetChainID: mainnetCfg,
		sepoliaChainID: sepoliaCfg,
		baseChainID:    baseCfg,
	}
	resolver := func(chainID int64) *config.SmartWalletConfig {
		return registry[chainID]
	}

	newVM := func(vmDefault *config.SmartWalletConfig) *VM {
		vm := NewVM()
		vm.smartWalletConfig = vmDefault
		vm.chainConfigResolver = resolver
		return vm
	}

	// Post-G5: a task carries no chain, so there is no task-chain inheritance.
	// A node's explicit chain resolves (or errors if unconfigured); a 0 node
	// chain falls back only to the VM default config.
	_ = sepoliaCfg
	tests := []struct {
		name        string
		nodeChainID int64
		vmDefault   *config.SmartWalletConfig
		want        *config.SmartWalletConfig
		wantErr     bool
	}{
		{
			name:        "explicit node chain_id resolves",
			nodeChainID: baseChainID,
			vmDefault:   mainnetCfg,
			want:        baseCfg,
		},
		{
			// Gateway mode (resolver present) requires an explicit chain — a
			// 0 node chain_id errors; there is no task chain to inherit.
			name:        "node chain_id 0 errors in gateway mode",
			nodeChainID: 0,
			vmDefault:   mainnetCfg,
			wantErr:     true,
		},
		{
			name:        "explicit unknown node chain_id errors",
			nodeChainID: 999999, // not registered
			vmDefault:   mainnetCfg,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := newVM(tt.vmDefault)
			got, err := vm.resolveSmartWalletForNode(tt.nodeChainID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveSmartWalletForNode(%d): expected error, got chain %d (%s)",
						tt.nodeChainID, chainIDOrZero(got), rpcOrEmpty(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSmartWalletForNode(%d): unexpected error: %v", tt.nodeChainID, err)
			}
			if got != tt.want {
				t.Fatalf("resolveSmartWalletForNode(%d): got chain %d (%s), want chain %d (%s)",
					tt.nodeChainID,
					chainIDOrZero(got), rpcOrEmpty(got),
					chainIDOrZero(tt.want), rpcOrEmpty(tt.want))
			}
		})
	}
}

// TestResolveSmartWalletForNode_NoResolver covers the single-chain
// (non-gateway) shape: chainConfigResolver is nil, so an explicit node
// chain_id returns v.smartWalletConfig — but chain_id <= 0 is still a hard
// error (G5: chain_id is required on chain-aware nodes in all modes).
func TestResolveSmartWalletForNode_NoResolver(t *testing.T) {
	defaultCfg := &config.SmartWalletConfig{ChainID: 1, EthRpcUrl: "https://default/rpc"}
	vm := NewVM()
	vm.smartWalletConfig = defaultCfg

	if got, err := vm.resolveSmartWalletForNode(8453); err != nil || got != defaultCfg {
		t.Fatalf("expected default config when chainConfigResolver is nil, got %v (err %v)", got, err)
	}
	if _, err := vm.resolveSmartWalletForNode(0); err == nil {
		t.Fatalf("expected error for chain_id 0 (explicit chain required), got nil")
	}
}

func chainIDOrZero(c *config.SmartWalletConfig) int64 {
	if c == nil {
		return 0
	}
	return c.ChainID
}

func rpcOrEmpty(c *config.SmartWalletConfig) string {
	if c == nil {
		return ""
	}
	return c.EthRpcUrl
}
