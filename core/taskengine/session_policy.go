package taskengine

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
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
			// No grant. The operation stays on the owner's fallback signer,
			// which the gateway cannot sign — but that failure belongs to the
			// caller that asked for an operation it has no authority for, and
			// it reads more clearly than a fabricated authorization.
			return nil, nil
		}
		key, err := signerKeyFor(*policy.SessionSigner)
		if err != nil {
			return nil, fmt.Errorf("session policy %s: %w", policy.ID, err)
		}
		auth := &preset.SessionAuthorization{
			EntityID:          policy.EntityID,
			SignerKey:         key,
			WrapExecuteUserOp: policy.Grant.RequiresExecuteUserOp,
		}
		// The install rides the grant's FIRST operation only. Replaying an
		// applied action would re-run installValidation on an entity that
		// already exists.
		if !policy.Grant.Applied() {
			auth.DeferredData = policy.Grant.InstallCall
			auth.OwnerSignature = policy.Grant.OwnerSignature
		}
		return auth, nil
	}
}
