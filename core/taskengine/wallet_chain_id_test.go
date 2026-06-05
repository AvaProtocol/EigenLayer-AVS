package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

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

	cfg := testutil.GetAggregatorConfig()
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

	// Pick an override chain that's a) in knownChainIDs (so the
	// configured-chain branch fires, not the unconfigured fallback)
	// and b) not equal to defaultChain (so we observe the override).
	knownNonDefault := int64(0)
	for _, k := range n.knownChainIDs() {
		if k != defaultChain && k > 0 {
			knownNonDefault = k
			break
		}
	}

	t.Run("configured non-default ChainID wins over default", func(t *testing.T) {
		if knownNonDefault == 0 {
			t.Skipf("test environment has only one configured chain (%d); add a second to exercise override", defaultChain)
		}
		u := &model.User{Address: testutil.TestUser1().Address, ChainID: knownNonDefault}
		if got := n.resolveUserChainID(u); got != knownNonDefault {
			t.Errorf("ChainID=%d: got %d, want %d (configured override must win)", knownNonDefault, got, knownNonDefault)
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
		if knownNonDefault == 0 {
			t.Skipf("need a second configured chain to verify routing; got only default %d", defaultChain)
		}
		user := &model.User{Address: testutil.TestUser1().Address, ChainID: knownNonDefault}
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
		overrideKey := WalletStorageKey(knownNonDefault, user.Address, strings.ToLower(resp.Address))
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
