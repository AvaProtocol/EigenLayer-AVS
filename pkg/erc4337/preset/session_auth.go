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

	// CarrierNonce is the full 256-bit nonce the owner signed over at grant
	// time (entity + deferred options + sequence 0). Optional: when set on a
	// deferred operation, the send path asserts op.Nonce equals this value
	// before estimation so a drift cannot surface as opaque AA23.
	CarrierNonce *big.Int

	// PolicyID is the storage id of the SessionPolicy this authorization
	// came from. Logging only — never affects validation.
	PolicyID string

	// WrapExecuteUserOp opts the operation into user-op context, which every
	// operation under a grant carrying EXECUTION hooks must do. It is derived
	// from the stored grant's contents, not guessed: an ERC-20 spend cap
	// installs an execution hook, a time range does not.
	WrapExecuteUserOp bool

	// OnApplied is how the send path reports that the grant's install reached
	// the chain, so the stored record stops attaching the deferred action.
	// Set by the resolver alongside DeferredData; carried as a callback
	// because the record lives above this package (BadgerDB, taskengine) and
	// the send path must not reach into storage.
	//
	// Invoked with the carrying operation's userOpHash once its receipt is
	// seen — or with "" when the install is discovered indirectly, by finding
	// the carrier nonce already consumed (the only operation that ever uses a
	// grant's deferred key is its install, so a consumed sequence IS the
	// receipt). An error is logged by the caller, never fatal: the install
	// succeeded on-chain, and an unrecorded one heals on the next operation
	// through that same consumed-nonce path.
	OnApplied func(userOpHash string) error
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

// seedPricingGas sets the gas limits an operation is priced with. An operation
// that validates through a session entity leaves verificationGasLimit and
// preVerificationGas at zero, so the bundler computes them; given a non-zero
// value it simulates under it and echoes it back. Both grow with the grant:
// validation runs the grant's hooks (and, on first use, its install), and the
// install rides in the signature. Measured on Sepolia, with an eight-target
// grant on an undeployed account: about 5.7M verification and 194k
// preVerification for the install, then 221k and 111k verification for the
// next two calls; fixed seeds of 900k and 100k reverted AA23 and AA26. The
// owner's fallback signer keeps its measured seeds.
func seedPricingGas(op *userop.UserOperationV07, auth *SessionAuthorization) {
	if auth != nil {
		op.VerificationGasLimit = big.NewInt(0)
		op.PreVerificationGas = big.NewInt(0)
		return
	}
	op.VerificationGasLimit = seedVerificationGas(op)
	op.PreVerificationGas = big.NewInt(initialPreVerificationGas)
}

// SessionResolver answers "under what authority may the gateway execute for
// this wallet?" — returning nil when there is no grant.
//
// A nil result is a hard failure on the MA v2 send path: the gateway cannot
// sign as the owner fallback, and estimating a doomed controller-as-fallback
// UserOp only produces opaque AA23. Callers that supply their own
// SessionAuthorization (tests, spikes) never hit the resolver.
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
