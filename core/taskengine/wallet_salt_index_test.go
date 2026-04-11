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
//   - BackfillWalletSaltIndex (the migration core) processes a mixed set
//     of canonical, stale, and legacy rows correctly under both apply and
//     dry-run modes, with an injected fake deriver — no RPC required.
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

	require.NoError(t, StoreWallet(db, owner, wallet))

	// Primary record present.
	got, err := GetWallet(db, owner, addr.Hex())
	require.NoError(t, err)
	assert.Equal(t, addr.Hex(), got.Address.Hex())

	// Secondary index points at the same address.
	canonical, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
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
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, canonicalAddr, 0)))

	// Now, write a stale wallet with the same (owner, factory, salt).
	// The secondary index must continue to point at the canonical
	// address, not the stale one.
	staleAddr := common.HexToAddress("0x4444444444444444444444444444444444444444")
	stale := mkWallet(owner, factory, staleAddr, 0)
	stale.StaleDerivation = true
	stale.IsHidden = true
	require.NoError(t, StoreWallet(db, owner, stale))

	canonical, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), canonicalAddr.Hex()),
		"index must continue pointing at the canonical wallet, not the stale one")

	// And the stale primary record is still readable.
	got, err := GetWallet(db, owner, staleAddr.Hex())
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
	require.NoError(t, StoreWallet(db, owner, legacy))

	got, err := GetWallet(db, owner, addr.Hex())
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

	_, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	assert.ErrorIs(t, err, badger.ErrKeyNotFound)
}

func TestMarkWalletStale_FlipsFlagsAndPreservesIndex(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, addr, 0)))

	require.NoError(t, MarkWalletStale(db, owner, addr.Hex()))

	got, err := GetWallet(db, owner, addr.Hex())
	require.NoError(t, err)
	assert.True(t, got.StaleDerivation)
	assert.True(t, got.IsHidden)

	// The secondary index was written before the wallet was marked
	// stale, and MarkWalletStale must not erase it (a future fresh
	// derivation will overwrite it via StoreWallet).
	indexed, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
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
	engine := New(db, cfg, nil, testutil.GetLogger())

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")

	// Era 1: store the wallet that was canonical when the factory
	// implementation produced this address.
	era1 := common.HexToAddress("0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f")
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, era1, 0)))

	// Sanity: the index points at era1.
	canonical, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era1.Hex()))

	// Era 2: factory implementation upgraded — new derivation produces
	// a different address.
	era2 := common.HexToAddress("0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e")

	engine.markPreviousCanonicalStaleIfAny(owner, factory, big.NewInt(0), era2)

	// era1 must now be marked stale.
	got, err := GetWallet(db, owner, era1.Hex())
	require.NoError(t, err)
	assert.True(t, got.StaleDerivation, "era1 wallet should be flagged as stale")
	assert.True(t, got.IsHidden, "era1 wallet should be force-hidden")

	// And the secondary index still points at era1 — until the caller
	// follows up with StoreWallet on the era2 wallet, which is what
	// the production GetWallet path does immediately after.
	canonical, err = LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era1.Hex()),
		"index unchanged until the new canonical wallet is stored")

	// Now simulate the StoreWallet that would follow in GetWallet's
	// not-found branch and verify the index flips to era2.
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, era2, 0)))
	canonical, err = LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era2.Hex()),
		"index should now point at the new canonical wallet")
}

func TestMarkPreviousCanonicalStaleIfAny_NoOpWhenIndexMatches(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	engine := New(db, cfg, nil, testutil.GetLogger())

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, addr, 0)))

	// Same address — no upgrade detected, nothing to mark stale.
	engine.markPreviousCanonicalStaleIfAny(owner, factory, big.NewInt(0), addr)

	got, err := GetWallet(db, owner, addr.Hex())
	require.NoError(t, err)
	assert.False(t, got.StaleDerivation)
	assert.False(t, got.IsHidden)
}

func TestListWallets_HardFiltersStaleRows(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	engine := New(db, cfg, nil, testutil.GetLogger())

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := cfg.SmartWallet.FactoryAddress

	// Two wallets for different salts, then mark one stale and verify
	// it disappears from the list response entirely.
	a := common.HexToAddress("0xAAAAaaaaAAAAaaaaAAAAaaaaAAAAaaaaAAAAaaaa")
	b := common.HexToAddress("0xBBBBbbbbBBBBbbbbBBBBbbbbBBBBbbbbBBBBbbbb")
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, a, 100)))
	require.NoError(t, StoreWallet(db, owner, mkWallet(owner, factory, b, 101)))

	// Flag b as stale.
	require.NoError(t, MarkWalletStale(db, owner, b.Hex()))

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

// ----------------------------------------------------------------------
// BackfillWalletSaltIndex (migration core) tests with a fake deriver.
// ----------------------------------------------------------------------

// fakeDeriver returns predetermined addresses keyed by (factory, salt).
// Used to simulate the on-chain factory.getAddress() result without
// actually dialing an RPC.
type fakeDeriver struct {
	addresses map[string]common.Address
	errors    map[string]error
}

func newFakeDeriver() *fakeDeriver {
	return &fakeDeriver{
		addresses: map[string]common.Address{},
		errors:    map[string]error{},
	}
}

func (f *fakeDeriver) key(owner, factory common.Address, salt *big.Int) string {
	return strings.ToLower(owner.Hex()) + "|" + strings.ToLower(factory.Hex()) + "|" + salt.String()
}

func (f *fakeDeriver) set(owner, factory common.Address, salt *big.Int, addr common.Address) {
	f.addresses[f.key(owner, factory, salt)] = addr
}

func (f *fakeDeriver) derive(owner common.Address, factory common.Address, salt *big.Int) (common.Address, error) {
	k := f.key(owner, factory, salt)
	if err, ok := f.errors[k]; ok {
		return common.Address{}, err
	}
	if addr, ok := f.addresses[k]; ok {
		return addr, nil
	}
	return common.Address{}, nil // zero address means "not configured"
}

func TestBackfillWalletSaltIndex_AllCanonical(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr := common.HexToAddress("0x3333333333333333333333333333333333333333")

	// Bypass StoreWallet's automatic index write so the migration has
	// something to do — write the primary record directly via Set so
	// the secondary index is empty before the backfill runs.
	w := mkWallet(owner, factory, addr, 0)
	body, err := w.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte(WalletStorageKey(owner, addr.Hex())), body))

	deriver := newFakeDeriver()
	deriver.set(owner, factory, big.NewInt(0), addr)

	stats, err := BackfillWalletSaltIndex(db, deriver.derive, WalletSaltIndexBackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Total)
	assert.Equal(t, 1, stats.CanonicalConfirmed)
	assert.Equal(t, 1, stats.SecondaryIndexWritten)
	assert.Equal(t, 0, stats.NewlyMarkedStale)

	// And the secondary index now exists.
	canonical, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), addr.Hex()))
}

func TestBackfillWalletSaltIndex_DetectsAndMarksStale(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")

	era1 := common.HexToAddress("0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f")
	era2 := common.HexToAddress("0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e")
	era3 := common.HexToAddress("0xF0708e63dc30F15C93c7E5ede061AED6D4aaAe03")

	// All three eras stored as separate primary records (this is the
	// production state we observed on Base).
	for _, addr := range []common.Address{era1, era2, era3} {
		w := mkWallet(owner, factory, addr, 0)
		body, err := w.ToJSON()
		require.NoError(t, err)
		require.NoError(t, db.Set([]byte(WalletStorageKey(owner, addr.Hex())), body))
	}

	// Simulate today's factory implementation: salt 0 derives to era3.
	deriver := newFakeDeriver()
	deriver.set(owner, factory, big.NewInt(0), era3)

	stats, err := BackfillWalletSaltIndex(db, deriver.derive, WalletSaltIndexBackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, 3, stats.Total)
	assert.Equal(t, 1, stats.CanonicalConfirmed, "only the era3 row matches the live derivation")
	assert.Equal(t, 1, stats.SecondaryIndexWritten)
	assert.Equal(t, 2, stats.NewlyMarkedStale, "era1 and era2 should be flagged stale")

	// era1 and era2 are now stale.
	for _, addr := range []common.Address{era1, era2} {
		got, gerr := GetWallet(db, owner, addr.Hex())
		require.NoError(t, gerr)
		assert.True(t, got.StaleDerivation, "%s should be stale", addr.Hex())
		assert.True(t, got.IsHidden, "%s should be hidden", addr.Hex())
	}
	// era3 is still canonical.
	got, err := GetWallet(db, owner, era3.Hex())
	require.NoError(t, err)
	assert.False(t, got.StaleDerivation)

	// Secondary index points at era3.
	canonical, err := LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(canonical.Hex(), era3.Hex()))
}

func TestBackfillWalletSaltIndex_DryRunWritesNothing(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")
	stored := common.HexToAddress("0xAAAAaaaaAAAAaaaaAAAAaaaaAAAAaaaaAAAAaaaa")
	live := common.HexToAddress("0xBBBBbbbbBBBBbbbbBBBBbbbbBBBBbbbbBBBBbbbb")

	// Stored row's factory now derives to a different address, so this
	// row would be marked stale in apply mode.
	w := mkWallet(owner, factory, stored, 0)
	body, err := w.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte(WalletStorageKey(owner, stored.Hex())), body))

	deriver := newFakeDeriver()
	deriver.set(owner, factory, big.NewInt(0), live)

	stats, err := BackfillWalletSaltIndex(db, deriver.derive, WalletSaltIndexBackfillOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, 1, stats.NewlyMarkedStale, "dry-run should still report what it would do")

	// But the actual record is unchanged.
	got, err := GetWallet(db, owner, stored.Hex())
	require.NoError(t, err)
	assert.False(t, got.StaleDerivation, "dry-run must not flip the stale flag")
	assert.False(t, got.IsHidden)

	// And no secondary index entry was written.
	_, err = LookupCanonicalWalletAddress(db, owner, factory, big.NewInt(0))
	assert.ErrorIs(t, err, badger.ErrKeyNotFound)
}

func TestBackfillWalletSaltIndex_SkipsLegacyAndDeriveErrors(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// 1. Legacy row: factory is nil — should be counted as
	//    SkippedMissingFactory.
	legacyAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	legacy := &model.SmartWallet{Owner: &owner, Address: &legacyAddr, Salt: big.NewInt(0)}
	legacyBody, err := legacy.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte(WalletStorageKey(owner, legacyAddr.Hex())), legacyBody))

	// 2. Row whose deriver returns an error — should be counted as
	//    SkippedDeriveError.
	errAddr := common.HexToAddress("0x8888888888888888888888888888888888888888")
	errWallet := mkWallet(owner, factory, errAddr, 7)
	errBody, err := errWallet.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte(WalletStorageKey(owner, errAddr.Hex())), errBody))

	deriver := newFakeDeriver()
	deriver.errors[deriver.key(owner, factory, big.NewInt(7))] = assertAnError{}

	stats, err := BackfillWalletSaltIndex(db, deriver.derive, WalletSaltIndexBackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, stats.Total)
	assert.Equal(t, 1, stats.SkippedMissingFactory)
	assert.Equal(t, 1, stats.SkippedDeriveError)
	assert.Equal(t, 0, stats.CanonicalConfirmed)
	assert.Equal(t, 0, stats.NewlyMarkedStale)
}

// assertAnError is a tiny error type for the SkippedDeriveError test.
type assertAnError struct{}

func (assertAnError) Error() string { return "rpc unavailable" }
