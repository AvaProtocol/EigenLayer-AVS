package taskengine

import (
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// storeExpiryPolicy writes one usable grant on a runner with the given
// lifetime, so the resolver has something to find.
func storeExpiryPolicy(t *testing.T, db storage.Storage, chainID int64, owner, runner, signer common.Address, validUntil int64) *model.SessionPolicy {
	t.Helper()
	policy := &model.SessionPolicy{
		ID:            "01testexpiry0000000000000",
		ChainID:       chainID,
		Owner:         &owner,
		Runner:        &runner,
		SessionSigner: &signer,
		EntityID:      7,
		ValidUntil:    validUntil,
		Status:        model.SessionPolicyActive,
		Grant: &model.SessionGrantAuthorization{
			InstallCall: []byte{0x01},
			// Applied: the install already landed, so the resolver takes the
			// short path and does not need deferred-action encoding.
			AppliedAt: time.Now().UnixMilli(),
			Deadline:  uint64(time.Now().Add(time.Hour).Unix()),
			// Validate() requires both; the resolver never re-reads them on an
			// applied grant (they exist so a revoke can rebuild the cleanup
			// payload), so any non-empty value works here.
			OwnerSignature: []byte{0x02},
			CarrierNonce:   big.NewInt(1),
		},
	}
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatalf("StoreSessionPolicy: %v", err)
	}
	return policy
}

// An expired grant must be refused by the resolver with a typed error, not
// handed to the send path. Letting it through means the account rejects the
// operation and the bundler answers "User Operation expired or has an invalid
// time range" — no policy id, and nothing telling the owner to re-grant.
func TestSessionResolverRefusesExpiredGrant(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	chainID := int64(11155111)
	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	runner := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	signerKeyFor := func(common.Address) (*ecdsa.PrivateKey, error) { return key, nil }

	t.Run("expired grant is refused with the typed code", func(t *testing.T) {
		expiredAt := time.Now().Add(-48 * time.Hour).UnixMilli()
		policy := storeExpiryPolicy(t, db, chainID, owner, runner, signer, expiredAt)
		defer func() { _ = db.Delete(SessionPolicyKey(chainID, owner, policy.ID)) }()

		resolve := NewSessionResolver(db, signerKeyFor, nil)
		auth, err := resolve(chainID, owner, runner)
		if err == nil {
			t.Fatal("expected an expired grant to be refused")
		}
		if auth != nil {
			t.Fatal("no authorization may be returned for an expired grant")
		}
		if !strings.Contains(err.Error(), SessionPolicyExpiredCode) {
			t.Fatalf("expected %s, got %v", SessionPolicyExpiredCode, err)
		}
		// The policy id is the whole point of refusing here instead of on chain.
		if !strings.Contains(err.Error(), policy.ID) {
			t.Fatalf("error should name the policy so the owner knows what to renew: %v", err)
		}
	})

	t.Run("live grant still resolves", func(t *testing.T) {
		liveUntil := time.Now().Add(24 * time.Hour).UnixMilli()
		policy := storeExpiryPolicy(t, db, chainID, owner, runner, signer, liveUntil)
		defer func() { _ = db.Delete(SessionPolicyKey(chainID, owner, policy.ID)) }()

		resolve := NewSessionResolver(db, signerKeyFor, nil)
		auth, err := resolve(chainID, owner, runner)
		if err != nil {
			t.Fatalf("a live grant must resolve: %v", err)
		}
		if auth == nil {
			t.Fatal("expected an authorization for a live grant")
		}
		if auth.EntityID != policy.EntityID {
			t.Fatalf("entity %d != %d", auth.EntityID, policy.EntityID)
		}
	})
}

// ValidUntil==0 means "no recorded expiry" (records predating the field), not
// "expired at the epoch". Treating zero as expired would refuse every legacy
// grant on the first send.
func TestSessionPolicyExpiredTreatsZeroAsNoExpiry(t *testing.T) {
	now := time.Now()

	if (&model.SessionPolicy{ValidUntil: 0}).Expired(now) {
		t.Fatal("ValidUntil=0 must not count as expired")
	}
	if (&model.SessionPolicy{ValidUntil: now.Add(time.Hour).UnixMilli()}).Expired(now) {
		t.Fatal("a future ValidUntil must not count as expired")
	}
	if !(&model.SessionPolicy{ValidUntil: now.Add(-time.Hour).UnixMilli()}).Expired(now) {
		t.Fatal("a past ValidUntil must count as expired")
	}
}

// Usable() stays status-only on purpose: an expired record still occupies its
// validation entity, and supersede/ambiguity rely on seeing it. If this ever
// starts returning false for an expired grant, NextSessionEntityID can hand
// that entity out again while the account still has it installed — which is
// exactly the drift that made a 2027 grant fail on chain with a 2026 window.
func TestUsableIgnoresExpiryOnPurpose(t *testing.T) {
	expired := &model.SessionPolicy{
		Status:     model.SessionPolicyActive,
		ValidUntil: time.Now().Add(-time.Hour).UnixMilli(),
		Grant:      &model.SessionGrantAuthorization{InstallCall: []byte{0x01}},
	}
	if !expired.Usable() {
		t.Fatal("Usable() must stay status-only so an expired record keeps its entity reserved")
	}
	if !expired.Expired(time.Now()) {
		t.Fatal("the same record must still report Expired()")
	}
}
