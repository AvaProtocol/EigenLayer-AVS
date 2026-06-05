package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// chainIDOverrideForTest is the second chain we inject so the routing
// assertions below have something to route TO. Sepolia (11155111) is
// the natural pick — it's the chain the rest of the test environment
// uses, but testutil's GetAggregatorConfig() returns a synthetic
// single-chain config (ChainID=1) by design. The routing tests need
// gateway-mode + at least two configured chains; this constant +
// withGatewayMultiChain() supplies both without depending on whatever
// gateway-dev.yaml happens to advertise.
const chainIDOverrideForTest int64 = 11_155_111

// withGatewayMultiChain promotes a single-chain testutil config to the
// multi-chain gateway shape so wallet_chain_id_test.go's routing
// assertions actually execute. testutil.GetAggregatorConfig() pins
// ChainID=1 (its doc-comment calls this out explicitly: "Tests that
// exercise multi-chain behavior override this on the returned
// config"). We're those tests — set IsGateway, populate Chains with
// the existing default plus a second chain, and prepend the default
// so DefaultChainID derives correctly via chains[0].
func withGatewayMultiChain(cfg *config.Config) *config.Config {
	cfg.IsGateway = true
	cfg.DefaultChainID = cfg.SmartWallet.ChainID
	cfg.Chains = []*config.ChainConfig{
		{
			ChainID:     cfg.SmartWallet.ChainID,
			Name:        "default",
			SmartWallet: cfg.SmartWallet,
		},
		{
			ChainID: chainIDOverrideForTest,
			Name:    "sepolia",
			// Reuse the default SmartWallet for the override chain.
			// GetWallet's mock-derivation path (rpcConn == nil in
			// tests) doesn't actually dial; we only need a valid
			// SmartWalletConfig with an EthRpcUrl set so the
			// production code path takes the right branch.
			SmartWallet: &config.SmartWalletConfig{
				ChainID:            chainIDOverrideForTest,
				EthRpcUrl:          cfg.SmartWallet.EthRpcUrl,
				EthWsUrl:           cfg.SmartWallet.EthWsUrl,
				FactoryAddress:     cfg.SmartWallet.FactoryAddress,
				EntrypointAddress:  cfg.SmartWallet.EntrypointAddress,
				PaymasterAddress:   cfg.SmartWallet.PaymasterAddress,
				WhitelistAddresses: cfg.SmartWallet.WhitelistAddresses,
			},
		},
	}
	return cfg
}

// resolveUserChainID is the chain-resolution helper consulted by the
// wallet RPC handlers that accept *model.User. Today that's only
// Engine.GetWallet — SetWallet and ListWallets retain their legacy
// owner-only signatures and always operate on n.defaultChainID();
// plumbing *User through them is a follow-up.
//
// The contract:
//
//   - If user.ChainID is zero, use n.defaultChainID() (gRPC + any
//     caller that didn't populate it).
//   - If user.ChainID is a configured chain, use it (REST callers set
//     this from JWT aud).
//   - If user.ChainID is set but NOT in n.knownChainIDs(), fall back
//     to n.defaultChainID() with a Warn log. This prevents an
//     attacker-supplied JWT aud (AuthExchange does no chain
//     validation) from causing storage writes under an arbitrary
//     bucket w:<unconfigured-chain>:*.
//
// Wallet RPC payloads (GetWalletReq, ListWalletReq, SetWalletReq) do
// NOT carry a chain_id and cannot override this. Adding one to the
// proto in the future must remain non-authoritative — JWT aud wins.
func TestResolveUserChainID(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := withGatewayMultiChain(testutil.GetAggregatorConfig())
	n := New(db, cfg, nil, testutil.GetLogger())
	defaultChain := n.defaultChainID()

	t.Run("nil user falls back to default", func(t *testing.T) {
		if got := n.resolveUserChainID(nil); got != defaultChain {
			t.Errorf("nil user: got chainID %d, want default %d", got, defaultChain)
		}
	})

	t.Run("user with ChainID=0 falls back to default", func(t *testing.T) {
		u := &model.User{Address: testutil.TestUser1().Address}
		if got := n.resolveUserChainID(u); got != defaultChain {
			t.Errorf("ChainID=0: got %d, want default %d", got, defaultChain)
		}
	})

	// withGatewayMultiChain guarantees `chainIDOverrideForTest` is in
	// knownChainIDs() and is distinct from defaultChain. Sanity-check
	// that here so a future change to the helper can't silently
	// re-introduce the single-chain skip path.
	if chainIDOverrideForTest == defaultChain {
		t.Fatalf("test setup broken: override chain %d equals default chain %d", chainIDOverrideForTest, defaultChain)
	}
	var overrideIsKnown bool
	for _, k := range n.knownChainIDs() {
		if k == chainIDOverrideForTest {
			overrideIsKnown = true
			break
		}
	}
	if !overrideIsKnown {
		t.Fatalf("test setup broken: override chain %d not in knownChainIDs %v (withGatewayMultiChain regressed?)", chainIDOverrideForTest, n.knownChainIDs())
	}

	t.Run("configured non-default ChainID wins over default", func(t *testing.T) {
		u := &model.User{Address: testutil.TestUser1().Address, ChainID: chainIDOverrideForTest}
		if got := n.resolveUserChainID(u); got != chainIDOverrideForTest {
			t.Errorf("ChainID=%d: got %d, want %d (configured override must win)", chainIDOverrideForTest, got, chainIDOverrideForTest)
		}
	})

	t.Run("unconfigured ChainID falls back to default (security guard)", func(t *testing.T) {
		// Pick a chain ID that's deliberately NOT one of the
		// gateway's configured chains. AuthExchange mints JWTs with
		// whatever chain the client signed for; without validation a
		// JWT for 99999 would silently route writes into w:99999:*.
		const malicious int64 = 999_999_999
		for _, k := range n.knownChainIDs() {
			if k == malicious {
				t.Fatalf("test setup leaked: %d is configured in the test env, pick a different sentinel", malicious)
			}
		}
		u := &model.User{Address: testutil.TestUser1().Address, ChainID: malicious}
		got := n.resolveUserChainID(u)
		if got != defaultChain {
			t.Errorf("unconfigured chain %d should fall back to default %d, got %d (would allow orphan storage)", malicious, defaultChain, got)
		}
	})

	t.Run("GetWallet writes record under user.ChainID, not defaultChain", func(t *testing.T) {
		// End-to-end assertion: a GetWallet call for a configured
		// non-default chain must persist its wallet record under that
		// chain's storage prefix. Regression guard if a future change
		// stops routing through resolveUserChainID.
		user := &model.User{Address: testutil.TestUser1().Address, ChainID: chainIDOverrideForTest}
		// Use a salt unlikely to collide with anything pre-existing
		// in the in-memory DB.
		const salt = "987654321"
		resp, err := n.GetWallet(user, &avsproto.GetWalletReq{Salt: salt})
		if err != nil {
			t.Fatalf("GetWallet returned err: %v", err)
		}
		if resp == nil || resp.Address == "" {
			t.Fatalf("GetWallet returned no address")
		}
		// Direct storage probe at the per-chain prefix. The override
		// chain bucket must contain the new record.
		overrideKey := WalletStorageKey(chainIDOverrideForTest, user.Address, strings.ToLower(resp.Address))
		exists, err := db.Exist([]byte(overrideKey))
		if err != nil {
			t.Fatalf("storage Exist on %s: %v", overrideKey, err)
		}
		if !exists {
			t.Errorf("wallet missing from override-chain bucket %q — GetWallet didn't route by user.ChainID", overrideKey)
		}
		// Negative side: the default-chain bucket must NOT contain
		// the same record (else routing is no-op).
		defaultKey := WalletStorageKey(defaultChain, user.Address, strings.ToLower(resp.Address))
		if defaultKey != overrideKey {
			defaultExists, err := db.Exist([]byte(defaultKey))
			if err != nil {
				t.Fatalf("storage Exist on %s: %v", defaultKey, err)
			}
			if defaultExists {
				t.Errorf("wallet ALSO landed in default-chain bucket %q — chain routing leaked", defaultKey)
			}
		}
	})
}
