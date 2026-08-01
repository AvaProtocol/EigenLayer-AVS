package preset

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// SessionAuthorization is how an operation runs under a session grant rather
// than as the account's owner.
//
// A stock Modular Account v2 trusts its fallback signer — the user's EOA — and
// the gateway does not hold that key. Everything the gateway executes
// therefore runs as an installed validation entity, and this carries what that
// requires: which entity, which key signs, and (on the grant's first
// operation) the owner's deferred authorization that installs it.
//
// This is deliberately a plain struct rather than a storage lookup. The
// gateway reads a SessionPolicy and builds one of these; the send path never
// touches BadgerDB. See avs-infra Smart_Wallet_MA_v2_Spend_Policy.md §7.4a for
// the record these fields come from.
type SessionAuthorization struct {
	// EntityID is the validation entity the grant installed. It keys the
	// nonce, so it must match the entity the deferred action installs.
	EntityID uint32

	// SignerKey signs the operation as that entity.
	SignerKey *ecdsa.PrivateKey

	// DeferredData and OwnerSignature carry the grant itself. Set on a
	// grant's FIRST operation only: the account applies the install during
	// validation, then validates this same operation against the entity it
	// just installed. Once applied they must never be replayed — the stored
	// record's applied_at is the marker.
	DeferredData   []byte
	OwnerSignature []byte

	// WrapExecuteUserOp opts the operation into user-op context, which every
	// operation under a grant carrying EXECUTION hooks must do. It is derived
	// from the stored grant's contents, not guessed: an ERC-20 spend cap
	// installs an execution hook, a time range does not.
	WrapExecuteUserOp bool
}

// Deferred reports whether this operation carries the grant's install.
func (s *SessionAuthorization) Deferred() bool {
	return s != nil && len(s.DeferredData) > 0 && len(s.OwnerSignature) > 0
}

// Validate rejects an authorization that cannot produce a valid operation.
func (s *SessionAuthorization) Validate() error {
	if s == nil {
		return nil
	}
	if s.EntityID < aa.MinSessionEntityID {
		return fmt.Errorf("session entity %d is reserved (0 is the owner's fallback signer)", s.EntityID)
	}
	if s.SignerKey == nil {
		return fmt.Errorf("session authorization has no signing key")
	}
	// Half a deferred action is worse than none: the operation would be signed
	// as an entity that does not exist yet and fail validation on chain, with
	// nothing pointing at the missing half.
	if len(s.DeferredData) > 0 && len(s.OwnerSignature) == 0 {
		return fmt.Errorf("deferred action has no owner signature")
	}
	if len(s.OwnerSignature) > 0 && len(s.DeferredData) == 0 {
		return fmt.Errorf("owner signature has no deferred action to authorize")
	}
	return nil
}

// nonceEntity returns the entity whose nonce key this operation uses, and the
// nonce options. Without an authorization the owner's fallback signer runs the
// operation, which is the path used to sign a grant directly.
func (s *SessionAuthorization) nonceEntity() (entityID uint32, options uint8) {
	options = userop.ValidationOptionGlobal
	if s == nil {
		return userop.FallbackSignerEntityID, options
	}
	if s.Deferred() {
		options |= userop.ValidationOptionDeferredAction
	}
	return s.EntityID, options
}

// seedVerificationGasFor sizes the verification limit for what this operation
// actually does during validation.
//
// The numbers come from Sepolia, not from theory (avs-infra
// MA_v2_Authority_Model_And_Spike_Results.md §3.2–§3.4). Validation cost grows
// with what the operation installs: a bare deferred install AA26s at 300k
// because module storage is cold, and a hook-carrying install AA26s at 300k
// again because every allowlist entry is its own cold SSTORE. Seeding must
// therefore scale with the grant's contents rather than with a single
// deploying/deployed flag.
//
// Over-seeding is not free either — Rundler refuses an operation whose
// verification efficiency falls under 0.4 — so these are sized to land inside
// that window, and SendUserOpV07WithRetry tightens from the bundler's own
// ratio when they miss.
func seedVerificationGasFor(op *userop.UserOperationV07, auth *SessionAuthorization) *big.Int {
	if auth.Deferred() {
		if auth.WrapExecuteUserOp {
			// Hook-carrying grant: allowlist entries plus the exec hook.
			return big.NewInt(seedVerificationGasDeferredHooks)
		}
		return big.NewInt(seedVerificationGasDeferredBare)
	}
	if auth != nil {
		// Validating as an installed module entity rather than the bytecode
		// fallback signer: an external call plus, on the entity's first use, a
		// cold nonce-key slot.
		return big.NewInt(seedVerificationGasModuleEntity)
	}
	return seedVerificationGas(op)
}

// SessionResolver answers "under what authority may the gateway execute for
// this wallet?" — returning nil when there is no grant, which leaves the
// operation on the owner's fallback signer.
//
// This exists so the send path never reaches into storage. The gateway
// installs one resolver at boot that reads SessionPolicy records; tests and
// the v0.6 path leave it unset and nothing changes.
type SessionResolver func(chainID int64, owner, wallet common.Address) (*SessionAuthorization, error)

var (
	sessionResolverMu sync.RWMutex
	sessionResolver   SessionResolver
)

// SetSessionResolver installs the resolver. Call once at gateway startup.
func SetSessionResolver(r SessionResolver) {
	sessionResolverMu.Lock()
	defer sessionResolverMu.Unlock()
	sessionResolver = r
}

// resolveSession looks up the authorization for a wallet. A resolver error is
// returned rather than swallowed: falling back to the fallback signer would
// mean signing with a key the gateway does not have, and the operation would
// fail on chain with nothing pointing at the storage read that failed.
func resolveSession(chainID int64, owner, wallet common.Address) (*SessionAuthorization, error) {
	sessionResolverMu.RLock()
	r := sessionResolver
	sessionResolverMu.RUnlock()
	if r == nil {
		return nil, nil
	}
	return r(chainID, owner, wallet)
}
