package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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

	newVM := func(taskChainID int64, vmDefault *config.SmartWalletConfig) *VM {
		vm := NewVM()
		vm.smartWalletConfig = vmDefault
		vm.chainConfigResolver = resolver
		if taskChainID != 0 {
			vm.task = &model.Workflow{
				Task: &avsproto.Task{ChainId: taskChainID},
			}
		}
		return vm
	}

	tests := []struct {
		name        string
		nodeChainID int64
		taskChainID int64
		vmDefault   *config.SmartWalletConfig
		want        *config.SmartWalletConfig
		wantErr     bool
	}{
		{
			name:        "node chain_id takes precedence over task chain_id",
			nodeChainID: baseChainID,
			taskChainID: sepoliaChainID,
			vmDefault:   mainnetCfg,
			want:        baseCfg,
		},
		{
			name:        "node chain_id 0 falls back to task chain_id (loop iteration case)",
			nodeChainID: 0,
			taskChainID: sepoliaChainID,
			vmDefault:   mainnetCfg, // populated from chains[0] = mainnet — the bug fixture
			want:        sepoliaCfg,
		},
		{
			name:        "both 0 falls back to vm default (single-chain mode shape)",
			nodeChainID: 0,
			taskChainID: 0,
			vmDefault:   mainnetCfg,
			want:        mainnetCfg,
		},
		{
			// An explicit, unresolvable node chain_id is a hard error — we do
			// NOT silently retarget it onto the task chain (the G4 footgun).
			name:        "explicit unknown node chain_id errors instead of falling back to task",
			nodeChainID: 999999, // not registered
			taskChainID: sepoliaChainID,
			vmDefault:   mainnetCfg,
			wantErr:     true,
		},
		{
			name:        "explicit unknown node chain_id errors even with no task chain",
			nodeChainID: 999998,
			taskChainID: 0,
			vmDefault:   mainnetCfg,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := newVM(tt.taskChainID, tt.vmDefault)
			got, err := vm.resolveSmartWalletForNode(tt.nodeChainID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveSmartWalletForNode(%d) with task=%d: expected error, got chain %d (%s)",
						tt.nodeChainID, tt.taskChainID, chainIDOrZero(got), rpcOrEmpty(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSmartWalletForNode(%d) with task=%d: unexpected error: %v",
					tt.nodeChainID, tt.taskChainID, err)
			}
			if got != tt.want {
				t.Fatalf("resolveSmartWalletForNode(%d) with task=%d: got chain %d (%s), want chain %d (%s)",
					tt.nodeChainID, tt.taskChainID,
					chainIDOrZero(got), rpcOrEmpty(got),
					chainIDOrZero(tt.want), rpcOrEmpty(tt.want))
			}
		})
	}
}

// TestResolveSmartWalletForNode_NoResolver covers the single-chain
// (non-gateway) shape: chainConfigResolver is nil, so the resolver
// must always return v.smartWalletConfig regardless of the node or
// task chain_id values.
func TestResolveSmartWalletForNode_NoResolver(t *testing.T) {
	defaultCfg := &config.SmartWalletConfig{ChainID: 1, EthRpcUrl: "https://default/rpc"}
	vm := NewVM()
	vm.smartWalletConfig = defaultCfg
	vm.task = &model.Workflow{Task: &avsproto.Task{ChainId: 11_155_111}}

	if got, err := vm.resolveSmartWalletForNode(8453); err != nil || got != defaultCfg {
		t.Fatalf("expected default config when chainConfigResolver is nil, got %v (err %v)", got, err)
	}
	if got, err := vm.resolveSmartWalletForNode(0); err != nil || got != defaultCfg {
		t.Fatalf("expected default config when chainConfigResolver is nil (chain_id 0), got %v (err %v)", got, err)
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
