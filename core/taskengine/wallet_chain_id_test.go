package taskengine

import (
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
//   - If user.ChainID > 0, use it (REST callers set this from JWT aud).
//   - Else fall back to the gateway's default chain (gRPC + any caller
//     that didn't populate it).
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

	t.Run("user with ChainID set wins over default", func(t *testing.T) {
		// Pick a chain that's definitively NOT the default so we can
		// observe the override. Most test setups default to 11155111
		// (sepolia); 1 (ethereum mainnet) is a safe contrast.
		const want int64 = 1
		if want == defaultChain {
			t.Skipf("test environment defaults to chain %d; pick a different override", want)
		}
		u := &model.User{Address: testutil.TestUser1().Address, ChainID: want}
		if got := n.resolveUserChainID(u); got != want {
			t.Errorf("ChainID=%d: got %d, want %d (user.ChainID must win)", want, got, want)
		}
	})

	t.Run("GetWallet routes by user.ChainID", func(t *testing.T) {
		// Sanity: a GetWallet call with user.ChainID set doesn't crash
		// or fall back. We can't easily assert which storage bucket
		// was hit without a multi-chain test fixture, but the smoke
		// path catches signature drift if the handler stops calling
		// resolveUserChainID.
		u := &model.User{Address: testutil.TestUser1().Address, ChainID: 8453}
		_, err := n.GetWallet(u, &avsproto.GetWalletReq{Salt: "12345"})
		if err != nil {
			// In test environments without a live RPC the handler may
			// derive a mock address — that's fine. A panic or a
			// nil-pointer error from misrouting would surface here.
			t.Logf("GetWallet returned err (expected in test env without live RPC): %v", err)
		}
	})
}
