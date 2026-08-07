package taskengine

import (
	"crypto/ecdsa"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// The whole grant flow, without a chain: the gateway prepares a payload, the
// owner signs it, the gateway stores it, and the send path then resolves an
// authorization that carries the install exactly once.
func TestSessionGrantRoundTrip(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	signerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(signerKey.PublicKey)

	prepared, err := PrepareSessionGrant(db, spChain, signer, "p1", SessionGrantRequest{
		Owner:      owner,
		Wallet:     spWallet,
		AgentLabel: "TradingBot",
		// A global grant needs an execution hook, so give it one — this also
		// exercises RequiresExecuteUserOp being derived rather than asserted.
		Hooks: [][]byte{aa.AllowlistExecHook(1)},
	})
	if err != nil {
		t.Fatalf("PrepareSessionGrant: %v", err)
	}
	if prepared.Policy.EntityID != 1 {
		t.Errorf("first grant should take entity 1, got %d", prepared.Policy.EntityID)
	}
	if !prepared.Policy.Grant.RequiresExecuteUserOp {
		t.Error("a grant carrying an execution hook must require executeUserOp wrapping")
	}
	if prepared.Policy.Grant.CarrierNonce == nil {
		t.Fatal("no carrier nonce — it cannot be recomputed later")
	}

	// Nothing is stored until the owner signs.
	if got, _ := ActiveSessionPolicyForWallet(db, spChain, owner, spWallet); got != nil {
		t.Error("preparing a grant must not persist anything")
	}

	sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey) // raw digest
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27

	policy, err := SubmitSessionGrant(db, prepared, sig)
	if err != nil {
		t.Fatalf("SubmitSessionGrant: %v", err)
	}
	if policy.Status != model.SessionPolicyPending {
		t.Errorf("a submitted grant starts pending, got %q", policy.Status)
	}

	// The send path can now resolve it, and the install rides along exactly
	// once: attached while pending, dropped after it is marked applied.
	resolve := NewSessionResolver(db, func(a common.Address) (*ecdsa.PrivateKey, error) {
		if a != signer {
			return nil, fmt.Errorf("unexpected signer %s", a.Hex())
		}
		return signerKey, nil
	}, nil)
	auth, err := resolve(spChain, owner, spWallet)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if auth == nil || !auth.Deferred() {
		t.Fatal("a pending grant's first operation must carry the install")
	}
	if !auth.WrapExecuteUserOp {
		t.Error("the hook-carrying grant must wrap in user-op context")
	}
	// DeferredData must be the ENCODED deferred-action payload the owner
	// signed over (locator ++ deadline ++ installCall), not the raw
	// installValidation calldata. Feeding InstallCall alone is what produced
	// AA23 on first use of a pending grant (including first deploy).
	if string(auth.DeferredData) != string(prepared.DeferredData) {
		t.Errorf("DeferredData is not the encoded deferred action (len got %d, want %d)",
			len(auth.DeferredData), len(prepared.DeferredData))
	}
	if len(auth.DeferredData) < 21+6+4 {
		t.Errorf("DeferredData is too short to be locator+deadline+selector: %d bytes", len(auth.DeferredData))
	}
	if string(auth.DeferredData) == string(policy.Grant.InstallCall) {
		t.Error("DeferredData must not be the raw InstallCall; wrap it with EncodeDeferredActionData")
	}

	if err := MarkSessionGrantApplied(db, policy, "0xdeadbeef"); err != nil {
		t.Fatalf("MarkSessionGrantApplied: %v", err)
	}
	auth, err = resolve(spChain, owner, spWallet)
	if err != nil {
		t.Fatalf("resolve after apply: %v", err)
	}
	if auth == nil {
		t.Fatal("an applied grant still authorizes execution")
	}
	if auth.Deferred() {
		t.Error("the install must not be attached a second time")
	}
}

// A signature from anyone but the owner must be refused before storage. A
// stored bad grant fails later, on chain, naming neither the record nor the
// mismatch.
func TestSubmitSessionGrantRejectsAForeignSignature(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	ownerKey, _ := crypto.GenerateKey()
	strangerKey, _ := crypto.GenerateKey()
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	signer := crypto.PubkeyToAddress(strangerKey.PublicKey)

	prepared, err := PrepareSessionGrant(db, spChain, signer, "p1", SessionGrantRequest{
		Owner: owner, Wallet: spWallet, Hooks: [][]byte{aa.AllowlistExecHook(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := crypto.Sign(prepared.Digest.Bytes(), strangerKey)
	sig[64] += 27

	if _, err := SubmitSessionGrant(db, prepared, sig); err == nil {
		t.Fatal("a grant signed by someone other than the owner must be refused")
	}
	if got, _ := ActiveSessionPolicyForWallet(db, spChain, owner, spWallet); got != nil {
		t.Error("a rejected grant must not be stored")
	}
}

// Two grants prepared concurrently on one wallet both see the same free
// entity. The second submit must lose rather than overwrite the first grant's
// signer on chain.
func TestConcurrentPreparesCannotCollideOnAnEntity(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	ownerKey, _ := crypto.GenerateKey()
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	signerKey, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(signerKey.PublicKey)

	req := SessionGrantRequest{Owner: owner, Wallet: spWallet, Hooks: [][]byte{aa.AllowlistExecHook(1)}}
	first, err := PrepareSessionGrant(db, spChain, signer, "p1", req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareSessionGrant(db, spChain, signer, "p2", req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Policy.EntityID != second.Policy.EntityID {
		t.Fatal("precondition: both prepares should have seen the same free entity")
	}

	sign := func(p *PreparedSessionGrant) []byte {
		s, _ := crypto.Sign(p.Digest.Bytes(), ownerKey)
		s[64] += 27
		return s
	}
	if _, err := SubmitSessionGrant(db, first, sign(first)); err != nil {
		t.Fatalf("the first submit should succeed: %v", err)
	}
	if _, err := SubmitSessionGrant(db, second, sign(second)); err == nil {
		t.Error("the second submit must be refused; its entity was taken while it was being signed")
	}
}

// Revocation splits on whether the install ever reached the chain.
func TestRevokeSessionGrant(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	t.Run("before first use it is free and complete", func(t *testing.T) {
		p := spPolicy("p1", spWallet, 1, model.SessionPolicyPending)
		if err := StoreSessionPolicy(db, p); err != nil {
			t.Fatal(err)
		}
		_, onChain, err := RevokeSessionGrant(db, p)
		if err != nil {
			t.Fatal(err)
		}
		if onChain {
			t.Error("an unapplied grant needs no on-chain cleanup")
		}
		got, _ := ActiveSessionPolicyForWallet(db, spChain, spOwner, spWallet)
		if got != nil {
			t.Error("the record should be gone")
		}
	})

	t.Run("after install it needs uninstallValidation", func(t *testing.T) {
		p := spPolicy("p2", spWallet2, 1, model.SessionPolicyActive)
		p.Grant.AppliedAt = 1_753_000_000_000
		if err := StoreSessionPolicy(db, p); err != nil {
			t.Fatal(err)
		}
		_, onChain, err := RevokeSessionGrant(db, p)
		if err != nil {
			t.Fatal(err)
		}
		if !onChain {
			t.Error("an applied grant leaves a module installed on the account")
		}
		// Retained, not deleted: the cleanup payload is the same tuple as the
		// install, so the record has to survive to reproduce it.
		got, err := ActiveSessionPolicyForWallet(db, spChain, spOwner, spWallet2)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Error("a revoked grant must not authorize anything")
		}
	})
}
