package taskengine

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Session-policy storage and the resolver that connects it to the send path.
//
// A stock Modular Account v2 trusts only its fallback signer — the user's EOA,
// whose key the gateway does not hold. Everything the gateway executes
// therefore runs as an installed validation entity, and the grant that
// installed it lives here.

// ListSessionPolicies returns every policy an owner holds on a chain.
//
// Records that fail to deserialize are an error rather than a silent skip: a
// dropped policy reads downstream as "this wallet has no grant", which sends
// the operation to the owner's fallback signer and fails on chain with nothing
// pointing back at the unreadable record.
func ListSessionPolicies(db storage.Storage, chainID int64, owner common.Address) ([]*model.SessionPolicy, error) {
	items, err := db.GetByPrefix(SessionPolicyPrefix(chainID, owner))
	if err != nil {
		return nil, fmt.Errorf("listing session policies for %s on chain %d: %w", owner.Hex(), chainID, err)
	}
	out := make([]*model.SessionPolicy, 0, len(items))
	for _, item := range items {
		policy := &model.SessionPolicy{}
		if err := policy.FromStorageData(item.Value); err != nil {
			return nil, fmt.Errorf("session policy at %s is unreadable: %w", string(item.Key), err)
		}
		out = append(out, policy)
	}
	return out, nil
}

// StoreSessionPolicy persists a grant.
func StoreSessionPolicy(db storage.Storage, policy *model.SessionPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	body, err := policy.ToJSON()
	if err != nil {
		return fmt.Errorf("serializing session policy %s: %w", policy.ID, err)
	}
	return db.Set(SessionPolicyKey(policy.ChainID, *policy.Owner, policy.ID), body)
}

// ActiveSessionPolicyForWallet returns the usable grant for one wallet.
//
// Exactly one usable grant per wallet is expected. More than one is refused
// rather than resolved by picking: two grants mean two entities, and silently
// choosing would sign under one while the caller may have provisioned the
// other — an authority question is not a place for a heuristic.
func ActiveSessionPolicyForWallet(db storage.Storage, chainID int64, owner, wallet common.Address) (*model.SessionPolicy, error) {
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return nil, err
	}
	var found *model.SessionPolicy
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != wallet || !p.Usable() {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf(
				"wallet %s has more than one usable session policy (%s, %s); revoke one before executing",
				wallet.Hex(), found.ID, p.ID)
		}
		found = p
	}
	return found, nil
}

// NextSessionEntityID picks an unused validation entity for a wallet.
//
// Uniqueness is per ACCOUNT while the storage key is per owner, so this scans
// the owner's policies and filters by runner. It is deliberately NOT safe to
// call outside the transaction that writes the resulting policy: two
// concurrent grants on one wallet would read the same maximum, and the second
// install would overwrite the first grant's signer at the same entity. See
// avs-infra §7.4a.
func NextSessionEntityID(db storage.Storage, chainID int64, owner, wallet common.Address) (uint32, error) {
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return 0, err
	}
	// Revoked policies still count: their entity may hold module state that
	// was never cleaned up, and reusing it would collide with the leftovers.
	next := uint32(1)
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != wallet {
			continue
		}
		if p.EntityID >= next {
			next = p.EntityID + 1
		}
	}
	return next, nil
}

// InstallSessionResolver wires the send path to this engine's session-policy
// storage. Without it every MA v2 operation fails fast with "no session
// authorization" — the gateway cannot sign as the owner fallback, and
// estimating a doomed UserOp only produces opaque AA23.
//
// Called once at aggregator startup, deliberately NOT from New: the resolver
// is process-global (the preset package has no per-engine context), and test
// suites construct many engines whose constructors would silently overwrite
// each other's resolver — the last-built engine would then answer authority
// questions for all of them. The single production engine installs it
// explicitly; tests that need one install their own scoped to their own
// database.
func (n *Engine) InstallSessionResolver() {
	preset.SetSessionResolver(NewSessionResolver(n.db, controllerSessionSigner(n.config)))
}

// NewSessionResolver builds the resolver the send path consults, backed by
// storage and a key lookup.
//
// signerKeyFor maps a session-signer ADDRESS to its private key. It is a
// callback rather than a map so key material stays wherever the gateway keeps
// it and never has to live on this record.
func NewSessionResolver(
	db storage.Storage,
	signerKeyFor func(signer common.Address) (*ecdsa.PrivateKey, error),
) preset.SessionResolver {
	return func(chainID int64, owner, wallet common.Address) (*preset.SessionAuthorization, error) {
		policy, err := ActiveSessionPolicyForWallet(db, chainID, owner, wallet)
		if err != nil {
			return nil, err
		}
		if policy == nil {
			// No grant. SendUserOpMAv2 fails fast on nil rather than estimating
			// a controller-as-fallback UserOp that always AA23s.
			return nil, nil
		}
		key, err := signerKeyFor(*policy.SessionSigner)
		if err != nil {
			return nil, fmt.Errorf("session policy %s: %w", policy.ID, err)
		}
		auth := &preset.SessionAuthorization{
			EntityID:          policy.EntityID,
			SignerKey:         key,
			PolicyID:          policy.ID,
			WrapExecuteUserOp: policy.Grant.RequiresExecuteUserOp,
		}
		// The install rides the grant's FIRST operation only. Replaying an
		// applied action would re-run installValidation on an entity that
		// already exists. OnApplied is how the send path closes that loop:
		// it fires when the install is known to be on-chain (receipt in
		// hand, or the carrier nonce found consumed), and the ByID variant
		// re-reads the record so a grant revoked mid-flight is never
		// resurrected by a stale pointer.
		//
		// DeferredData must be EncodeDeferredActionData(locator, deadline,
		// installCall) — NOT the raw installValidation calldata. The account
		// unpacks the deferred signature as locator(21) ++ deadline(6) ++
		// call; feeding InstallCall alone makes validation revert AA23 with
		// no useful reason, which is exactly what first-use (and first-use
		// that also deploys the counterfactual account) used to hit.
		if !policy.Grant.Applied() {
			encoded, encErr := userop.EncodeDeferredActionData(
				userop.FallbackSignerLocator(),
				policy.Grant.Deadline,
				policy.Grant.InstallCall,
			)
			if encErr != nil {
				return nil, fmt.Errorf("session policy %s: encoding deferred action: %w", policy.ID, encErr)
			}
			auth.DeferredData = encoded
			auth.OwnerSignature = policy.Grant.OwnerSignature
			if policy.Grant.CarrierNonce != nil {
				auth.CarrierNonce = new(big.Int).Set(policy.Grant.CarrierNonce)
			}
			policyID, policyChain, policyOwner := policy.ID, policy.ChainID, *policy.Owner
			auth.OnApplied = func(userOpHash string) error {
				return MarkSessionGrantAppliedByID(db, policyChain, policyOwner, policyID, userOpHash)
			}
		}
		return auth, nil
	}
}

// controllerSessionSigner resolves a session signer to its key.
//
// Every policy is signed by the gateway's controller key, so this accepts
// exactly that address and refuses anything else. This is the DESIGN, not a
// placeholder — per-policy signer keys are deliberately not planned, because
// they add key generation, custody, and rotation without changing the trust
// model. Authority is per grant regardless of the key: each policy occupies
// its own validation ENTITY, and the entity — not the key — is what carries
// the hooks, the revocation target, and the nonce space that makes grant-time
// signing work. A single shared key means a compromised controller reaches
// every grant, but that is already true of the controller today, so a key per
// policy would buy complexity, not a smaller blast radius. Revisit only if
// that blast radius itself becomes the thing to shrink.
//
// Refusing an unknown signer is the important half: a policy naming a key we
// cannot produce must fail loudly here, not sign with the wrong one.
func controllerSessionSigner(cfg *config.Config) func(common.Address) (*ecdsa.PrivateKey, error) {
	return func(signer common.Address) (*ecdsa.PrivateKey, error) {
		if cfg == nil || cfg.SmartWallet == nil || cfg.SmartWallet.ControllerPrivateKey == nil {
			return nil, fmt.Errorf("no controller key configured; cannot sign as session signer %s", signer.Hex())
		}
		controller := crypto.PubkeyToAddress(cfg.SmartWallet.ControllerPrivateKey.PublicKey)
		if signer != controller {
			return nil, fmt.Errorf(
				"session signer %s is not the gateway controller %s; every policy signs with the shared controller key by design",
				signer.Hex(), controller.Hex())
		}
		return cfg.SmartWallet.ControllerPrivateKey, nil
	}
}
