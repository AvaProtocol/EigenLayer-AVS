package taskengine

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

const spChain = int64(11155111)

var (
	spOwner   = common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	spWallet  = common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	spWallet2 = common.HexToAddress("0xD683400781b73a432b2602dA73a5117F4F445744")
	spSigner  = common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
)

func spPolicy(id string, wallet common.Address, entity uint32, status model.SessionPolicyStatus) *model.SessionPolicy {
	owner, w, signer := spOwner, wallet, spSigner
	return &model.SessionPolicy{
		ID: id, Owner: &owner, Runner: &w, ChainID: spChain,
		EntityID: entity, SessionSigner: &signer, Status: status,
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
			CarrierNonce:   big.NewInt(1),
			Deadline:       1785541743,
			OwnerSignature: make([]byte, 65),
		},
	}
}

func spKeyFor(t *testing.T) (func(common.Address) (*ecdsa.PrivateKey, error), *ecdsa.PrivateKey) {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return func(a common.Address) (*ecdsa.PrivateKey, error) {
		if a != spSigner {
			return nil, fmt.Errorf("no key for %s", a.Hex())
		}
		return k, nil
	}, k
}

func TestSessionResolverReturnsTheWalletsGrant(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	keyFor, key := spKeyFor(t)

	pending := spPolicy("p1", spWallet, 1, model.SessionPolicyPending)
	if err := StoreSessionPolicy(db, pending); err != nil {
		t.Fatalf("StoreSessionPolicy: %v", err)
	}
	// A grant on a DIFFERENT wallet the same owner holds must not leak across.
	if err := StoreSessionPolicy(db, spPolicy("p2", spWallet2, 1, model.SessionPolicyActive)); err != nil {
		t.Fatalf("StoreSessionPolicy: %v", err)
	}

	resolve := NewSessionResolver(db, keyFor, nil)
	auth, err := resolve(spChain, spOwner, spWallet)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if auth == nil {
		t.Fatal("expected an authorization for a wallet that has a grant")
	}
	if auth.EntityID != 1 || auth.SignerKey != key {
		t.Errorf("wrong entity/key: %d %v", auth.EntityID, auth.SignerKey == key)
	}
	if auth.PolicyID != "p1" {
		t.Errorf("PolicyID = %q, want p1 (for send-path logging)", auth.PolicyID)
	}
	if auth.CarrierNonce == nil || auth.CarrierNonce.Cmp(pending.Grant.CarrierNonce) != 0 {
		t.Errorf("CarrierNonce = %v, want %v (send path asserts it against op.Nonce)",
			auth.CarrierNonce, pending.Grant.CarrierNonce)
	}
	// Pending means the install has not been applied, so it must ride along.
	if !auth.Deferred() {
		t.Error("a pending grant's first operation must carry the install")
	}
	// DeferredData is the ENCODED deferred-action payload, not the raw
	// installValidation calldata stored on the grant. The account expects
	// locator(21) ++ deadline(6) ++ call; raw InstallCall alone reverts AA23.
	if string(auth.DeferredData) == string(pending.Grant.InstallCall) {
		t.Error("DeferredData must not equal raw InstallCall")
	}
	if len(auth.DeferredData) <= len(pending.Grant.InstallCall) {
		t.Errorf("DeferredData should be InstallCall plus locator+deadline prefix; got %d bytes for a %d-byte install",
			len(auth.DeferredData), len(pending.Grant.InstallCall))
	}
}

// The install is a bearer authorization for exactly its calldata. Replaying it
// would re-run installValidation on an entity that already exists.
func TestSessionResolverDoesNotReplayAnAppliedGrant(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	keyFor, _ := spKeyFor(t)

	p := spPolicy("p1", spWallet, 1, model.SessionPolicyActive)
	p.Grant.AppliedAt = 1_753_000_000_000
	p.Grant.AppliedUserOpHash = "0xdeadbeef"
	if err := StoreSessionPolicy(db, p); err != nil {
		t.Fatalf("StoreSessionPolicy: %v", err)
	}

	auth, err := NewSessionResolver(db, keyFor, nil)(spChain, spOwner, spWallet)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if auth == nil {
		t.Fatal("an applied grant still authorizes execution")
	}
	if auth.Deferred() {
		t.Error("an applied grant must not re-attach its install")
	}
}

func TestSessionResolverNoGrant(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	keyFor, _ := spKeyFor(t)
	auth, err := NewSessionResolver(db, keyFor, nil)(spChain, spOwner, spWallet)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if auth != nil {
		t.Error("a wallet with no grant must resolve to nil, leaving the owner's signer")
	}
}

// Revoked grants authorize nothing, but their entity is not reusable: module
// state may survive an incomplete cleanup.
func TestRevokedGrantIsNotUsableButStillConsumesItsEntity(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	keyFor, _ := spKeyFor(t)
	if err := StoreSessionPolicy(db, spPolicy("p1", spWallet, 1, model.SessionPolicyRevoked)); err != nil {
		t.Fatalf("StoreSessionPolicy: %v", err)
	}

	auth, err := NewSessionResolver(db, keyFor, nil)(spChain, spOwner, spWallet)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if auth != nil {
		t.Error("a revoked grant must not authorize anything")
	}
	next, err := NextSessionEntityID(db, spChain, spOwner, spWallet)
	if err != nil {
		t.Fatalf("NextSessionEntityID: %v", err)
	}
	if next != 2 {
		t.Errorf("next entity = %d, want 2 — a revoked entity may hold stranded module state", next)
	}
}

// Two usable grants on one wallet is an authority question, and picking one
// would sign under an entity the caller may not have provisioned.
func TestTwoUsableGrantsOnOneWalletIsRefused(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	keyFor, _ := spKeyFor(t)
	for _, p := range []*model.SessionPolicy{
		spPolicy("p1", spWallet, 1, model.SessionPolicyActive),
		spPolicy("p2", spWallet, 2, model.SessionPolicyPending),
	} {
		if err := StoreSessionPolicy(db, p); err != nil {
			t.Fatalf("StoreSessionPolicy: %v", err)
		}
	}
	if _, err := NewSessionResolver(db, keyFor, nil)(spChain, spOwner, spWallet); err == nil {
		t.Error("expected a refusal when a wallet has two usable grants")
	}
}

// Entity ids are unique per ACCOUNT while the key is per owner, so allocation
// must not be confused by the owner's other wallets.
func TestNextSessionEntityIDIsPerAccount(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	if err := StoreSessionPolicy(db, spPolicy("p1", spWallet, 4, model.SessionPolicyActive)); err != nil {
		t.Fatal(err)
	}
	if err := StoreSessionPolicy(db, spPolicy("p2", spWallet2, 9, model.SessionPolicyActive)); err != nil {
		t.Fatal(err)
	}
	next, err := NextSessionEntityID(db, spChain, spOwner, spWallet)
	if err != nil {
		t.Fatal(err)
	}
	if next != 5 {
		t.Errorf("next entity for %s = %d, want 5 (the other wallet's 9 is irrelevant)", spWallet.Hex(), next)
	}
	first, err := NextSessionEntityID(db, spChain, spOwner, common.HexToAddress("0x1234"))
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Errorf("a fresh wallet's first entity = %d, want 1 (0 is the owner's)", first)
	}
}

func TestSessionPolicyValidateRejectsUnusableRecords(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*model.SessionPolicy)
	}{
		{"entity 0 is the owner's", func(p *model.SessionPolicy) { p.EntityID = 0 }},
		{"no install calldata", func(p *model.SessionPolicy) { p.Grant.InstallCall = nil }},
		{"no owner signature", func(p *model.SessionPolicy) { p.Grant.OwnerSignature = nil }},
		// The digest commits to it and it cannot be recomputed after the fact.
		{"no carrier nonce", func(p *model.SessionPolicy) { p.Grant.CarrierNonce = nil }},
		{"no grant at all", func(p *model.SessionPolicy) { p.Grant = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := spPolicy("p1", spWallet, 1, model.SessionPolicyPending)
			tt.mutate(p)
			if err := p.Validate(); err == nil {
				t.Error("expected Validate to reject this record")
			}
		})
	}
}

// Round-trip through storage: the grant's bytes and big.Int must survive.
func TestSessionPolicyRoundTrip(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	p := spPolicy("p1", spWallet, 3, model.SessionPolicyPending)
	p.Grant.CarrierNonce, _ = new(big.Int).SetString("18446744073709551617", 10) // > uint64
	if err := StoreSessionPolicy(db, p); err != nil {
		t.Fatal(err)
	}
	got, err := ActiveSessionPolicyForWallet(db, spChain, spOwner, spWallet)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("policy not found after storing")
	}
	if got.Grant.CarrierNonce.Cmp(p.Grant.CarrierNonce) != 0 {
		t.Errorf("carrier nonce round-tripped as %s, want %s", got.Grant.CarrierNonce, p.Grant.CarrierNonce)
	}
	if string(got.Grant.InstallCall) != string(p.Grant.InstallCall) {
		t.Error("install calldata did not round-trip")
	}
}
