package taskengine

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// teardownVerifyTimeout bounds the post-apply chain read that checks a
// replaced entity is gone. OnApplied must not hang on a slow or stuck RPC:
// the new grant is already installed, verification is best-effort, and a
// hung dial would stall the send path's applied callback.
const teardownVerifyTimeout = 10 * time.Second

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

// Authority on one runner is a SINGLETON, and these locks are what make that
// true at write time rather than merely checked at read time.
//
// A grant is not additive: the send path resolves exactly one usable policy per
// runner and refuses the rest (see ActiveSessionPolicyForWallet), so a second
// stacked grant cannot be used — it can only break the wallet. Submitting one
// therefore REPLACES the others, and the whole read-decide-write sequence has
// to be indivisible or two writers interleave into the broken state the
// replacement exists to prevent.
//
// An in-process lock is sufficient here, and that is a property of the
// deployment rather than an assumption: the store is an embedded BadgerDB
// opened once per aggregator (aggregator.go, storage.NewWithPath), and Badger
// holds an exclusive lock on its directory — a second writer process cannot
// exist to be raced with. Nothing distributed is needed unless that changes.
const sessionAuthorityLockShards = 64

// sessionAuthorityLocks is sharded so a fixed set of mutexes bounds memory;
// the same runner always maps to the same shard. Unrelated wallets that
// collide on a shard merely serialize, which costs nothing at this rate.
var sessionAuthorityLocks [sessionAuthorityLockShards]sync.RWMutex

// sessionAuthorityLock guards the policy set of one (chainID, owner, runner).
//
// Writers (submit, revoke) take it exclusively; readers (the resolver and the
// contract-write preflight) take it shared, which is what keeps submit's
// store-then-supersede window invisible to an executing workflow.
//
// It is NOT reentrant: a write-lock holder must never call anything that takes
// the read lock — ActiveSessionPolicyForWallet above all.
func sessionAuthorityLock(chainID int64, owner, runner common.Address) *sync.RWMutex {
	// FNV-1a over the raw bytes: allocation-free, and deterministic in the way
	// that matters — the same runner must always reach the same mutex, or two
	// writers would serialize on different locks and not serialize at all.
	//
	// Hashing the address VALUES rather than their hex avoids both the
	// allocation and the question of checksum casing: common.Address is already
	// a canonical 20 bytes, so there is no normalization to get wrong.
	var hash uint32 = 2166136261
	for i := 0; i < 8; i++ {
		hash ^= uint32(byte(chainID >> (8 * i)))
		hash *= 16777619
	}
	for i := 0; i < common.AddressLength; i++ {
		hash ^= uint32(owner[i])
		hash *= 16777619
	}
	for i := 0; i < common.AddressLength; i++ {
		hash ^= uint32(runner[i])
		hash *= 16777619
	}
	return &sessionAuthorityLocks[hash%sessionAuthorityLockShards]
}

// SessionPolicyAmbiguousCode prefixes the refusal when a runner carries more
// than one usable grant. Clients branch on the code, not the prose — same
// contract as SESSION_POLICY_TARGET_NOT_ALLOWED (see session_grant_coverage.go).
//
// Submitting a grant now supersedes the others, so reaching this needs records
// that predate that, or a supersede that failed partway. Both are cleared by
// granting again.
const SessionPolicyAmbiguousCode = "SESSION_POLICY_AMBIGUOUS"

// ActiveSessionPolicyForWallet returns the usable grant for one wallet.
//
// Exactly one usable grant per wallet is expected. More than one is refused
// rather than resolved by picking: two grants mean two entities, and silently
// choosing would sign under one while the caller may have provisioned the
// other — an authority question is not a place for a heuristic. Submit keeps
// the set to one; this stays as the backstop that fails closed if it ever
// isn't.
func ActiveSessionPolicyForWallet(db storage.Storage, chainID int64, owner, wallet common.Address) (*model.SessionPolicy, error) {
	lock := sessionAuthorityLock(chainID, owner, wallet)
	lock.RLock()
	defer lock.RUnlock()
	return activeSessionPolicyLocked(db, chainID, owner, wallet)
}

// activeSessionPolicyLocked is ActiveSessionPolicyForWallet's body, split out
// for callers that already hold the runner's lock.
func activeSessionPolicyLocked(db storage.Storage, chainID int64, owner, wallet common.Address) (*model.SessionPolicy, error) {
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
				"%s: wallet %s has more than one usable session policy (%s, %s); grant again to replace them, or revoke one before executing",
				SessionPolicyAmbiguousCode, wallet.Hex(), found.ID, p.ID)
		}
		found = p
	}
	return found, nil
}

// supersedeUsablePolicies revokes every usable grant on one runner except
// keepID, and reports what it revoked. The caller must hold the runner's write
// lock.
//
// Revoked, never deleted — even for a pending grant, which RevokeSessionGrant
// would delete outright. NextSessionEntityID derives the next entity by
// scanning stored records and counts revoked ones deliberately, so a deleted
// record frees its entity for reuse. Superseding a pending grant whose install
// is already in flight would then hand that entity to the next grant, whose
// installValidation would land on an entity the chain already has. Keeping the
// record keeps the entity spoken for.
func supersedeUsablePolicies(db storage.Storage, chainID int64, owner, runner common.Address, keepID string) ([]string, error) {
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return nil, err
	}
	superseded := make([]string, 0, len(policies))
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != runner || !p.Usable() {
			continue
		}
		if strings.EqualFold(p.ID, keepID) {
			continue
		}
		p.Status = model.SessionPolicyRevoked
		if err := StoreSessionPolicy(db, p); err != nil {
			// Partial supersede: report what landed so the caller can name the
			// rest. Reporting success here would leave the runner ambiguous
			// behind a 201.
			return superseded, fmt.Errorf("superseding session policy %s: %w", p.ID, err)
		}
		superseded = append(superseded, p.ID)
	}
	return superseded, nil
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
	preset.SetSessionResolver(NewSessionResolver(n.db, controllerSessionSigner(n.config), n.teardownVerifier()))
}

// teardownVerifier reads a validation entity's signer on the chain the grant
// lives on. Dialled per check rather than held open: verification runs once
// per replacement, and the alternative is a pool of long-lived clients for
// every configured chain to serve a call that happens rarely.
func (n *Engine) teardownVerifier() TeardownVerifier {
	return func(ctx context.Context, chainID int64, account common.Address, entity uint32) (bool, error) {
		swCfg := n.ResolveSmartWalletConfig(chainID)
		if swCfg == nil || swCfg.EthRpcUrl == "" {
			return false, fmt.Errorf("no RPC configured for chain %d", chainID)
		}
		client, err := ethclient.DialContext(ctx, swCfg.EthRpcUrl)
		if err != nil {
			return false, fmt.Errorf("dialing chain %d: %w", chainID, err)
		}
		defer client.Close()

		signer, err := aa.EntitySignerOnChain(ctx, client, account, entity)
		if err != nil {
			return false, err
		}
		return signer == (common.Address{}), nil
	}
}

// NewSessionResolver builds the resolver the send path consults, backed by
// storage and a key lookup.
//
// signerKeyFor maps a session-signer ADDRESS to its private key. It is a
// callback rather than a map so key material stays wherever the gateway keeps
// it and never has to live on this record.
// verifyTeardown, when supplied, closes the loop on a replacement: after the
// grant's deferred action reaches the chain it reads back whether the entity
// the batch was supposed to remove is actually gone. nil disables the check —
// tests and any process without a chain client skip it honestly rather than
// assume it passed. See VerifySupersededTeardown for why a receipt is not
// enough.
func NewSessionResolver(
	db storage.Storage,
	signerKeyFor func(signer common.Address) (*ecdsa.PrivateKey, error),
	verifyTeardown TeardownVerifier,
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
			policyRunner := *policy.Runner
			// Captured now, from the payload the owner signed: if this grant
			// replaced an installed one, its batch carries that entity's
			// uninstall, and the entity it names is what must be verified gone.
			replacedEntity, replacedSomething, entityErr := aa.UninstalledEntityWithin(policy.Grant.InstallCall)
			if entityErr != nil {
				return nil, fmt.Errorf("session policy %s: reading the teardown target: %w", policy.ID, entityErr)
			}
			auth.OnApplied = func(userOpHash string) error {
				if err := MarkSessionGrantAppliedByID(db, policyChain, policyOwner, policyRunner, policyID, userOpHash); err != nil {
					return err
				}
				if !replacedSomething || verifyTeardown == nil {
					return nil
				}
				// The batch has mined, which says the operation ran — not that
				// the uninstall did anything. A failed check does not fail the
				// send: the new grant is installed and there is nothing to roll
				// back. VerifySupersededTeardown logs the outcome; we only
				// bound the RPC so a hung dial cannot stall OnApplied.
				replaced := &model.SessionPolicy{
					ID: policyID, ChainID: policyChain, Runner: &policyRunner, EntityID: replacedEntity,
				}
				ctx, cancel := context.WithTimeout(context.Background(), teardownVerifyTimeout)
				defer cancel()
				_ = VerifySupersededTeardown(ctx, db, verifyTeardown, replaced, globalLogger)
				return nil
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

// supersededOnChainGrant returns the runner's grant whose install has actually
// REACHED THE CHAIN, so a replacement can carry its teardown.
//
// Only an applied grant qualifies. A pending one has no on-chain entity — its
// install is still riding an operation, or never will — so batching an
// uninstall for it would tear down something that does not exist. Superseding
// a pending grant stays what #716 made it: a storage-only revoke.
//
// ambiguous reports that the runner carries MORE THAN ONE installed grant, in
// which case no policy is returned and the caller must NOT batch a teardown.
// This is the deliberate half. #716 gave granting a self-healing property — a
// wallet stuck with stacked usable grants is repaired by granting again — and
// refusing here would take that away precisely from the wallets that need it
// most. A replacement can only remove one entity, so it removes none, the
// storage-level supersede still collapses the set to one usable grant, and the
// on-chain leftovers stay exactly as they were. Degraded, not broken: no worse
// than before replacement carried teardown at all.
//
// Clearing those leftovers needs an N-way batch or a sweep, tracked on #717.
func supersededOnChainGrant(db storage.Storage, chainID int64, owner, runner common.Address) (policy *model.SessionPolicy, ambiguous bool, err error) {
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return nil, false, err
	}
	var found *model.SessionPolicy
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != runner || !p.Usable() || !p.Grant.Applied() {
			continue
		}
		if found != nil {
			return nil, true, nil
		}
		found = p
	}
	return found, false, nil
}
