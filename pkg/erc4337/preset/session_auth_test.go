package preset

import (
	"bytes"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

// The nonce selects the validation entity on MA v2 — not the signature. Get
// this wrong and the account validates against a different entity than the one
// that signed, which surfaces as an opaque signature failure.
func TestSessionAuthNonceEntity(t *testing.T) {
	t.Run("no authorization runs as the owner's fallback signer", func(t *testing.T) {
		var auth *SessionAuthorization
		entity, opts := auth.nonceEntity()
		if entity != userop.FallbackSignerEntityID {
			t.Errorf("entity = %d, want the fallback signer %d", entity, userop.FallbackSignerEntityID)
		}
		if opts&userop.ValidationOptionDeferredAction != 0 {
			t.Error("no authorization must not set the deferred bit")
		}
	})

	t.Run("a grant runs as its own entity", func(t *testing.T) {
		auth := &SessionAuthorization{EntityID: 7, SignerKey: testKey(t)}
		entity, opts := auth.nonceEntity()
		if entity != 7 {
			t.Errorf("entity = %d, want 7", entity)
		}
		if opts&userop.ValidationOptionDeferredAction != 0 {
			t.Error("a grant with no deferred action must not set the deferred bit")
		}
	})

	// The bit is what tells the account to apply the install during
	// validation. Without it the deferred payload is ignored and the operation
	// validates against an entity that does not exist yet.
	t.Run("the grant's first operation sets the deferred bit", func(t *testing.T) {
		auth := &SessionAuthorization{
			EntityID: 7, SignerKey: testKey(t),
			DeferredData: []byte{0x01}, OwnerSignature: bytes.Repeat([]byte{0x02}, 65),
		}
		if !auth.Deferred() {
			t.Fatal("Deferred() should be true when both halves are present")
		}
		_, opts := auth.nonceEntity()
		if opts&userop.ValidationOptionDeferredAction == 0 {
			t.Error("the deferred bit must be set on the install-carrying operation")
		}
	})
}

func TestSessionAuthValidate(t *testing.T) {
	sig := bytes.Repeat([]byte{0x02}, 65)
	for _, tt := range []struct {
		name    string
		auth    *SessionAuthorization
		wantErr bool
	}{
		{"nil is fine — the owner signs", nil, false},
		{"valid", &SessionAuthorization{EntityID: 1, SignerKey: testKey(t)}, false},
		{"entity 0 is the owner's", &SessionAuthorization{EntityID: 0, SignerKey: testKey(t)}, true},
		{"no key", &SessionAuthorization{EntityID: 1}, true},
		// Half a deferred action signs as an entity that does not exist yet.
		{"deferred data without a signature", &SessionAuthorization{EntityID: 1, SignerKey: testKey(t), DeferredData: []byte{0x01}}, true},
		{"signature without deferred data", &SessionAuthorization{EntityID: 1, SignerKey: testKey(t), OwnerSignature: sig}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.auth.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Seeds are measured, and each tier covers a cost the one below it does not.
// A seed that is too low AA26s; too high and Rundler refuses the operation for
// falling under its 0.4 verification-efficiency floor.
func TestSeedVerificationGasScalesWithGrantContents(t *testing.T) {
	op := &userop.UserOperationV07{}
	sig := bytes.Repeat([]byte{0x02}, 65)

	none := seedVerificationGasFor(op, nil)
	entity := seedVerificationGasFor(op, &SessionAuthorization{EntityID: 1, SignerKey: testKey(t)})
	bare := seedVerificationGasFor(op, &SessionAuthorization{
		EntityID: 1, SignerKey: testKey(t), DeferredData: []byte{0x01}, OwnerSignature: sig})
	hooks := seedVerificationGasFor(op, &SessionAuthorization{
		EntityID: 1, SignerKey: testKey(t), DeferredData: []byte{0x01}, OwnerSignature: sig,
		WrapExecuteUserOp: true})

	if !(none.Cmp(entity) < 0 && entity.Cmp(bare) < 0 && bare.Cmp(hooks) < 0) {
		t.Errorf("seeds must increase with validation work: none=%s entity=%s deferred=%s hooks=%s",
			none, entity, bare, hooks)
	}
	// §3.4 measured the hook-carrying install AA26-ing at 300k.
	if hooks.Cmp(big.NewInt(300_000)) <= 0 {
		t.Errorf("hook-carrying seed %s is at or below the measured AA26 threshold", hooks)
	}
}

// The resolver is the only way storage reaches the send path. An unset
// resolver must leave every existing caller untouched.
func TestSessionResolver(t *testing.T) {
	t.Cleanup(func() { SetSessionResolver(nil) })

	SetSessionResolver(nil)
	got, err := resolveSession(11155111, common.Address{}, common.Address{})
	if err != nil || got != nil {
		t.Errorf("an unset resolver must return (nil, nil), got (%v, %v)", got, err)
	}

	want := &SessionAuthorization{EntityID: 3, SignerKey: testKey(t)}
	var sawChain int64
	var sawWallet common.Address
	SetSessionResolver(func(chainID int64, owner, wallet common.Address) (*SessionAuthorization, error) {
		sawChain, sawWallet = chainID, wallet
		return want, nil
	})
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	got, err = resolveSession(11155111, common.Address{}, wallet)
	if err != nil {
		t.Fatalf("resolveSession: %v", err)
	}
	if got != want {
		t.Error("resolver result was not returned")
	}
	// The grant is per (wallet, chain) — resolving by owner alone would hand a
	// user's grant on one wallet to a different wallet they also own.
	if sawChain != 11155111 || sawWallet != wallet {
		t.Errorf("resolver saw chain=%d wallet=%s; both must identify the grant", sawChain, sawWallet)
	}
}

func TestSessionAuthEntityMatchesInstallEntity(t *testing.T) {
	// The authorization's entity keys the nonce; the grant's entity is what
	// the install writes. If they disagree the account installs one entity and
	// validates against another.
	const entity = uint32(5)
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	call, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity, Signer: signer, Global: true, AllowSelfAdministration: true,
	})
	if err != nil {
		t.Fatalf("PackSessionSignerInstall: %v", err)
	}
	auth := &SessionAuthorization{EntityID: entity, SignerKey: testKey(t)}
	gotEntity, _ := auth.nonceEntity()

	cfg := call[4:29]
	installEntity := uint32(cfg[20])<<24 | uint32(cfg[21])<<16 | uint32(cfg[22])<<8 | uint32(cfg[23])
	if gotEntity != installEntity {
		t.Errorf("nonce entity %d != installed entity %d", gotEntity, installEntity)
	}
}
