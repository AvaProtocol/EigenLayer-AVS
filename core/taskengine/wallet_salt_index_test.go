// Tests for the (owner, factory, salt) → wallet address secondary index
// introduced to detect and hide wallet rows that became orphaned by a
// factory account-implementation upgrade.
//
// What's exercised here:
//
//   - StoreWallet writes both the primary record and the secondary index
//     atomically (and skips the index for stale or legacy rows).
//   - LookupCanonicalWalletAddress reads back what StoreWallet wrote.
//   - MarkWalletStale flips the StaleDerivation/IsHidden flags on the
//     primary record and intentionally does not point the secondary index
//     at a stale row.
//   - markPreviousCanonicalStaleIfAny detects an implementation upgrade
//     and flips the previously canonical wallet to stale.
//   - ListWallets hard-filters StaleDerivation rows from the response,
//     so callers see exactly one canonical wallet per (owner, factory, salt).
package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper to keep test setup terse
func mkWallet(owner, factory, addr common.Address, salt int64) *model.SmartWallet {
	saltBig := big.NewInt(salt)
	return &model.SmartWallet{
		Owner:   &owner,
		Address: &addr,
		Factory: &factory,
		Salt:    saltBig,
	}
}

func TestStoreWallet_WritesPrimaryAndSecondaryIndex(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	wallet := mkWallet(owner, factory, addr, 0)

	require.NoError(t, StoreWallet(db, int64(1), owner, wallet))

	// Primary record present.
	got, err := GetWallet(db, int64(1), owner, addr.Hex())
	require.NoError(t, err)
	assert.Equal(t, addr.Hex(), got.Address.Hex())

	// Secondary index points at the same address.
	canonical, err := LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), addr.Hex()),
		"secondary index should point at the canonical wallet address")
}

func TestStoreWallet_StaleRecordDoesNotUpdateSecondaryIndex(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// First, persist the canonical wallet — index should point at it.
	canonicalAddr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, canonicalAddr, 0)))

	// Now, write a stale wallet with the same (owner, factory, salt).
	// The secondary index must continue to point at the canonical
	// address, not the stale one.
	staleAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	stale := mkWallet(owner, factory, staleAddr, 0)
	stale.StaleDerivation = true
	stale.IsHidden = true
	require.NoError(t, StoreWallet(db, int64(1), owner, stale))

	canonical, err := LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), canonicalAddr.Hex()),
		"index must continue pointing at the canonical wallet, not the stale one")

	// And the stale primary record is still readable.
	got, err := GetWallet(db, int64(1), owner, staleAddr.Hex())
	require.NoError(t, err)
	assert.True(t, got.StaleDerivation)
	assert.True(t, got.IsHidden)
}

func TestStoreWallet_MissingFactorySkipsSecondaryIndex(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	// Legacy row: factory is nil. Should still persist via primary key,
	// just without populating the secondary index.
	legacy := &model.SmartWallet{
		Owner:   &owner,
		Address: &addr,
		Salt:    big.NewInt(0),
	}
	require.NoError(t, StoreWallet(db, int64(1), owner, legacy))

	got, err := GetWallet(db, int64(1), owner, addr.Hex())
	require.NoError(t, err)
	assert.Equal(t, addr.Hex(), got.Address.Hex())

	// No factory was provided, so there is no triple to look up.
	// The lookup helper requires a factory, so we just check that no
	// secondary index entry exists for this owner under any factory.
	items, err := db.GetByPrefix([]byte("wsalt:" + strings.ToLower(owner.Hex())))
	require.NoError(t, err)
	assert.Empty(t, items, "no wsalt: entries should exist for legacy wallet")
}

func TestLookupCanonicalWalletAddress_NotFound(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")

	_, err := LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	assert.ErrorIs(t, err, badger.ErrKeyNotFound)
}

func TestMarkWalletStale_FlipsFlagsAndPreservesIndex(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, addr, 0)))

	require.NoError(t, MarkWalletStale(db, int64(1), owner, addr.Hex()))

	got, err := GetWallet(db, int64(1), owner, addr.Hex())
	require.NoError(t, err)
	assert.True(t, got.StaleDerivation)
	assert.True(t, got.IsHidden)

	// The secondary index was written before the wallet was marked
	// stale, and MarkWalletStale must not erase it (a future fresh
	// derivation will overwrite it via StoreWallet).
	indexed, err := LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(indexed.Hex(), addr.Hex()))
}

// markPreviousCanonicalStaleIfAny is the engine-side helper that runs on
// the GetWallet / ListWallets write paths, detects an implementation
// upgrade by comparing the secondary index to the freshly derived address,
// and marks the previously canonical wallet stale.
func TestMarkPreviousCanonicalStaleIfAny_DetectsUpgrade(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	cfg.SmartWallet.ChainID = int64(1)
	engine := New(db, cfg, nil, testutil.GetLogger())

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")

	// Era 1: store the wallet that was canonical when the factory
	// implementation produced this address.
	era1 := common.HexToAddress("0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f")
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, era1, 0)))

	// Sanity: the index points at era1.
	canonical, err := LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era1.Hex()))

	// Era 2: factory implementation upgraded — new derivation produces
	// a different address.
	era2 := common.HexToAddress("0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e")

	engine.markPreviousCanonicalStaleIfAny(int64(1), owner, factory, big.NewInt(0), era2)

	// era1 must now be marked stale.
	got, err := GetWallet(db, int64(1), owner, era1.Hex())
	require.NoError(t, err)
	assert.True(t, got.StaleDerivation, "era1 wallet should be flagged as stale")
	assert.True(t, got.IsHidden, "era1 wallet should be force-hidden")

	// And the secondary index still points at era1 — until the caller
	// follows up with StoreWallet on the era2 wallet, which is what
	// the production GetWallet path does immediately after.
	canonical, err = LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era1.Hex()),
		"index unchanged until the new canonical wallet is stored")

	// Now simulate the StoreWallet that would follow in GetWallet's
	// not-found branch and verify the index flips to era2.
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, era2, 0)))
	canonical, err = LookupCanonicalWalletAddress(db, int64(1), owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era2.Hex()),
		"index should now point at the new canonical wallet")
}

func TestMarkPreviousCanonicalStaleIfAny_NoOpWhenIndexMatches(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	cfg.SmartWallet.ChainID = int64(1)
	engine := New(db, cfg, nil, testutil.GetLogger())

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, addr, 0)))

	// Same address — no upgrade detected, nothing to mark stale.
	engine.markPreviousCanonicalStaleIfAny(int64(1), owner, factory, big.NewInt(0), addr)

	got, err := GetWallet(db, int64(1), owner, addr.Hex())
	require.NoError(t, err)
	assert.False(t, got.StaleDerivation)
	assert.False(t, got.IsHidden)
}

func TestListWallets_HardFiltersStaleRows(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	cfg.SmartWallet.ChainID = int64(1)
	engine := New(db, cfg, nil, testutil.GetLogger())

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := cfg.SmartWallet.FactoryAddress

	// Two wallets for different salts, then mark one stale and verify
	// it disappears from the list response entirely.
	a := common.HexToAddress("0xAAAAaaaaAAAAaaaaAAAAaaaaAAAAaaaaAAAAaaaa")
	b := common.HexToAddress("0xBBBBbbbbBBBBbbbbBBBBbbbbBBBBbbbbBBBBbbbb")
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, a, 100)))
	require.NoError(t, StoreWallet(db, int64(1), owner, mkWallet(owner, factory, b, 101)))

	// Flag b as stale.
	require.NoError(t, MarkWalletStale(db, int64(1), owner, b.Hex()))

	resp, err := engine.ListWallets(owner, &avsproto.ListWalletReq{})
	require.NoError(t, err)

	addresses := make(map[string]bool)
	for _, w := range resp.Items {
		addresses[strings.ToLower(w.Address)] = true
	}
	assert.True(t, addresses[strings.ToLower(a.Hex())], "non-stale wallet should be returned")
	assert.False(t, addresses[strings.ToLower(b.Hex())],
		"stale wallet must be hard-filtered out of the response")
}
