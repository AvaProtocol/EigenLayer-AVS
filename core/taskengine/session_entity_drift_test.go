package taskengine

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// #763 A: occupancy-aware allocation.

func TestNextFreeSessionEntityIDNilCheckerIsStorageOnly(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	if err := StoreSessionPolicy(db, spPolicy("p1", spWallet, 3, model.SessionPolicyRevoked)); err != nil {
		t.Fatal(err)
	}
	got, err := NextFreeSessionEntityID(context.Background(), db, spChain, spOwner, spWallet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 {
		t.Errorf("nil checker must return storage max+1, got %d", got)
	}
}

func TestNextFreeSessionEntityIDSkipsOccupied(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	// Storage says next is 1. Chain says 1 and 2 are leftover.
	occupied := map[uint32]bool{1: true, 2: true}
	var seen []uint32
	check := func(_ context.Context, _ common.Address, entity uint32) (bool, error) {
		seen = append(seen, entity)
		return occupied[entity], nil
	}
	got, err := NextFreeSessionEntityID(context.Background(), db, spChain, spOwner, spWallet, check)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("got entity %d, want 3 (1 and 2 occupied on chain)", got)
	}
	if len(seen) != 3 {
		t.Errorf("probed %v, want [1 2 3]", seen)
	}
}

func TestNextFreeSessionEntityIDTreatsReadErrorAsOccupied(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	check := func(_ context.Context, _ common.Address, entity uint32) (bool, error) {
		if entity == 1 {
			return false, errors.New("rpc blip")
		}
		return false, nil
	}
	got, err := NextFreeSessionEntityID(context.Background(), db, spChain, spOwner, spWallet, check)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("a failed read must not be treated as free; got %d, want 2", got)
	}
}

func TestNextFreeSessionEntityIDBoundsTheProbe(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	var probes int
	check := func(_ context.Context, _ common.Address, _ uint32) (bool, error) {
		probes++
		return true, nil
	}
	_, err := NextFreeSessionEntityID(context.Background(), db, spChain, spOwner, spWallet, check)
	if err == nil {
		t.Fatal("expected a typed occupied error after the probe cap")
	}
	if !strings.Contains(err.Error(), SessionPolicyEntityOccupiedCode) {
		t.Fatalf("expected %s, got %v", SessionPolicyEntityOccupiedCode, err)
	}
	if probes != maxEntityOccupancyProbes {
		t.Errorf("probes = %d, want %d", probes, maxEntityOccupancyProbes)
	}
}

func TestPrepareSessionGrantSkipsOnChainOccupiedEntity(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	signerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(signerKey.PublicKey)
	check := func(_ context.Context, _ common.Address, entity uint32) (bool, error) {
		return entity == 1, nil
	}
	prepared, err := PrepareSessionGrant(db, spChain, signer, "p-occ", SessionGrantRequest{
		Owner:                   spOwner,
		Wallet:                  spWallet,
		AgentLabel:              "Bot",
		AllowSelfAdministration: true,
		HooksFor: func(entityID uint32) ([][]byte, error) {
			return [][]byte{aa.AllowlistExecHook(entityID)}, nil
		},
		OccupancyCheck: check,
		OccupancyCtx:   context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Policy.EntityID != 2 {
		t.Errorf("prepare allocated entity %d, want 2 (entity 1 occupied on chain)", prepared.Policy.EntityID)
	}
}

// #763 B: revoke without InstallCall still reserves the entity.

func TestRevokeWithoutInstallCallKeepsEntityReserved(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	p := spPolicy("p-empty", spWallet, 4, model.SessionPolicyPending)
	p.Grant = nil
	p.Status = model.SessionPolicyRevoked
	if err := StoreSessionPolicy(db, p); err != nil {
		t.Fatalf("a revoked record without a grant must still store: %v", err)
	}
	next, err := NextSessionEntityID(db, spChain, spOwner, spWallet)
	if err != nil {
		t.Fatal(err)
	}
	if next != 5 {
		t.Errorf("next = %d, want 5 — incomplete revoked records still occupy their entity", next)
	}
}

func TestRevokedIncompleteGrantPassesValidate(t *testing.T) {
	p := spPolicy("p-rev", spWallet, 1, model.SessionPolicyRevoked)
	p.Grant = nil
	p.SessionSigner = nil
	if err := p.Validate(); err != nil {
		t.Fatalf("revoked records may be incomplete so they can keep the entity reserved: %v", err)
	}
}

// #763 C: send path refuses a stored grant whose window disagrees with chain.

func TestSessionResolverRefusesChainWindowMismatch(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	storedUntil := time.Now().Add(365 * 24 * time.Hour).UnixMilli()
	policy := storeExpiryPolicy(t, db, spChain, spOwner, spWallet, signer, storedUntil)

	chainUntil := uint64(time.Now().Add(-48 * time.Hour).Unix()) // the 2026-vs-2027 shape
	verify := func(_ context.Context, _ int64, _ common.Address, entity uint32) (uint64, uint64, error) {
		if entity != policy.EntityID {
			t.Errorf("window read for entity %d, want %d", entity, policy.EntityID)
		}
		return chainUntil, 0, nil
	}
	resolve := newSessionResolver(db, func(common.Address) (*ecdsa.PrivateKey, error) { return key, nil }, nil, verify)
	auth, err := resolve(spChain, spOwner, spWallet)
	if err == nil || auth != nil {
		t.Fatal("a mismatched window must not authorize a send")
	}
	if !strings.Contains(err.Error(), SessionPolicyChainWindowMismatchCode) {
		t.Fatalf("expected %s, got %v", SessionPolicyChainWindowMismatchCode, err)
	}
	if !strings.Contains(err.Error(), policy.ID) {
		t.Fatalf("error should name the policy: %v", err)
	}
}

func TestSessionResolverCachesChainWindowCheck(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	storedUntil := time.Now().Add(24 * time.Hour).UnixMilli()
	storeExpiryPolicy(t, db, spChain, spOwner, spWallet, signer, storedUntil)

	var reads int
	verify := func(_ context.Context, _ int64, _ common.Address, _ uint32) (uint64, uint64, error) {
		reads++
		return uint64(storedUntil / 1000), 0, nil
	}
	resolve := newSessionResolver(db, func(common.Address) (*ecdsa.PrivateKey, error) { return key, nil }, nil, verify)
	if _, err := resolve(spChain, spOwner, spWallet); err != nil {
		t.Fatalf("matching window must resolve: %v", err)
	}
	if reads != 1 {
		t.Fatalf("first resolve should read once, got %d", reads)
	}
	if _, err := resolve(spChain, spOwner, spWallet); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if reads != 1 {
		t.Fatalf("cached match must not re-read the chain, got %d reads", reads)
	}
}

func TestSessionResolverSkipsWindowCheckOnPendingGrant(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	keyFor, _ := spKeyFor(t)
	pending := spPolicy("p-pending", spWallet, 1, model.SessionPolicyPending)
	pending.ValidUntil = time.Now().Add(24 * time.Hour).UnixMilli()
	if err := StoreSessionPolicy(db, pending); err != nil {
		t.Fatal(err)
	}
	verify := func(_ context.Context, _ int64, _ common.Address, _ uint32) (uint64, uint64, error) {
		t.Fatal("pending grants have not installed a window yet")
		return 0, 0, fmt.Errorf("should not be called")
	}
	resolve := newSessionResolver(db, keyFor, nil, verify)
	auth, err := resolve(spChain, spOwner, spWallet)
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil || !auth.Deferred() {
		t.Fatal("pending grant must still resolve with the install attached")
	}
}

func TestFindDriftedSessionGrantsReportsMismatch(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	storedUntil := time.Now().Add(365 * 24 * time.Hour).UnixMilli()
	want := storeExpiryPolicy(t, db, spChain, spOwner, spWallet, signer, storedUntil)

	chainUntil := uint64(1_755_000_000)
	verify := func(_ context.Context, _ int64, _ common.Address, _ uint32) (uint64, uint64, error) {
		return chainUntil, 0, nil
	}
	drifted, err := FindDriftedSessionGrants(context.Background(), db, spChain, verify)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) != 1 {
		t.Fatalf("got %d drifted, want 1", len(drifted))
	}
	if drifted[0].Policy.ID != want.ID {
		t.Errorf("reported %s, want %s", drifted[0].Policy.ID, want.ID)
	}
	if drifted[0].ChainUntilSec != chainUntil {
		t.Errorf("chain until %d, want %d", drifted[0].ChainUntilSec, chainUntil)
	}
	if drifted[0].StoredUntilSec != uint64(storedUntil/1000) {
		t.Errorf("stored until %d, want %d", drifted[0].StoredUntilSec, storedUntil/1000)
	}
}

func TestFindDriftedSessionGrantsIgnoresMatchingWindows(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	storedUntil := time.Now().Add(24 * time.Hour).UnixMilli()
	storeExpiryPolicy(t, db, spChain, spOwner, spWallet, signer, storedUntil)
	verify := func(_ context.Context, _ int64, _ common.Address, _ uint32) (uint64, uint64, error) {
		return uint64(storedUntil / 1000), 0, nil
	}
	drifted, err := FindDriftedSessionGrants(context.Background(), db, spChain, verify)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) != 0 {
		t.Fatalf("matching windows are not drift, got %d", len(drifted))
	}
}

// The Sepolia e2e fixture is in exactly this state right now: entity 3 has a
// signer installed and its stored grant runs to 2027, but the account carries
// NO TimeRange hook for it — timeRanges() reads zero. Measured against
// production, not hypothesised.
//
// It must be refused (a grant that does not enforce the expiry the owner
// approved is not one to sign under), but with its own code: folding it into
// the mismatch case prints "chain validUntil 1970-01-01T00:00:00Z", which reads
// as a corrupt timestamp rather than "the hook was never installed".
func TestSessionResolverRefusesMissingChainWindowDistinctly(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	storedUntil := time.Now().Add(365 * 24 * time.Hour).UnixMilli()
	policy := storeExpiryPolicy(t, db, spChain, spOwner, spWallet, signer, storedUntil)

	verify := func(_ context.Context, _ int64, _ common.Address, _ uint32) (uint64, uint64, error) {
		return 0, 0, nil // no hook installed
	}
	resolve := newSessionResolver(db, func(common.Address) (*ecdsa.PrivateKey, error) { return key, nil }, nil, verify)
	auth, err := resolve(spChain, spOwner, spWallet)
	if err == nil || auth != nil {
		t.Fatal("a grant whose entity has no TimeRange hook must not authorize a send")
	}
	if !strings.Contains(err.Error(), SessionPolicyChainWindowMissingCode) {
		t.Fatalf("expected %s, got %v", SessionPolicyChainWindowMissingCode, err)
	}
	if strings.Contains(err.Error(), "1970") {
		t.Fatalf("must not render the absent hook as an epoch date: %v", err)
	}
	if !strings.Contains(err.Error(), policy.ID) {
		t.Fatalf("error should name the policy: %v", err)
	}
}

// The sweep has to count the missing-hook rows separately: that is the
// population the send path starts refusing on deploy, and in raw numbers it is
// indistinguishable from a window that expired at the epoch.
func TestFindDriftedSessionGrantsFlagsMissingWindow(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	storeExpiryPolicy(t, db, spChain, spOwner, spWallet, signer,
		time.Now().Add(365*24*time.Hour).UnixMilli())

	verify := func(_ context.Context, _ int64, _ common.Address, _ uint32) (uint64, uint64, error) {
		return 0, 0, nil
	}
	drifted, err := FindDriftedSessionGrants(context.Background(), db, spChain, verify)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(drifted) != 1 {
		t.Fatalf("expected 1 drifted grant, got %d", len(drifted))
	}
	if !drifted[0].WindowMissing {
		t.Fatal("a zero chain window must be reported as WindowMissing")
	}
}
