package taskengine

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"sort"
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

// sessionAuthorityShard is the mutex index for one (chainID, owner, runner).
func sessionAuthorityShard(chainID int64, owner, runner common.Address) uint32 {
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
	return hash % sessionAuthorityLockShards
}

// sessionAuthorityLock guards the policy set of one (chainID, owner, runner).
//
// Writers (submit, revoke) take it exclusively; readers (the resolver and the
// contract-write preflight) take it shared, which is what keeps submit's
// store-then-supersede window invisible to an executing workflow.
//
// It is NOT reentrant: a write-lock holder must never call anything that takes
// the read lock — ActiveSessionPolicyForWallet above all.
func sessionAuthorityLock(chainID int64, owner, runner common.Address) *sync.RWMutex {
	return &sessionAuthorityLocks[sessionAuthorityShard(chainID, owner, runner)]
}

// orderedSessionLocks returns one mutex per distinct shard for these chains,
// sorted by shard index. Two chains can share a shard; that mutex is
// returned once, because taking it twice on one goroutine deadlocks.
// Callers that lock more than one shard must use this order. Sorting by
// chain id instead can cross for two runners and deadlock both creates.
func orderedSessionLocks(chainIDs []int64, owner, runner common.Address) []*sync.RWMutex {
	type shardLock struct {
		index uint32
		mu    *sync.RWMutex
	}
	seen := map[*sync.RWMutex]struct{}{}
	ordered := make([]shardLock, 0, len(chainIDs))
	for _, chainID := range chainIDs {
		mu := sessionAuthorityLock(chainID, owner, runner)
		if _, ok := seen[mu]; ok {
			continue
		}
		seen[mu] = struct{}{}
		ordered = append(ordered, shardLock{
			index: sessionAuthorityShard(chainID, owner, runner),
			mu:    mu,
		})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
	out := make([]*sync.RWMutex, len(ordered))
	for i, item := range ordered {
		out[i] = item.mu
	}
	return out
}

// SessionPolicyAmbiguousCode prefixes the refusal when a runner carries more
// than one usable grant. Clients branch on the code, not the prose — same
// contract as SESSION_POLICY_TARGET_NOT_ALLOWED (see session_grant_coverage.go).
//
// Submitting a grant now supersedes the others, so reaching this needs records
// that predate that, or a supersede that failed partway. Both are cleared by
// granting again.
const SessionPolicyAmbiguousCode = "SESSION_POLICY_AMBIGUOUS"

// ErrSessionPolicyAmbiguous is the sentinel for a runner with more than one
// usable grant. The error text still starts with SessionPolicyAmbiguousCode.
// A legacy submit (no basePolicyId) uses errors.Is to treat that state as
// "replace every usable grant". Any other caller fails closed.
var ErrSessionPolicyAmbiguous = errors.New("wallet has more than one usable session policy")

type sessionPolicyAmbiguousError struct {
	wallet common.Address
	ids    [2]string
}

func (e *sessionPolicyAmbiguousError) Error() string {
	return fmt.Sprintf(
		"%s: wallet %s has more than one usable session policy (%s, %s); grant again to replace them, or revoke one before executing",
		SessionPolicyAmbiguousCode, e.wallet.Hex(), e.ids[0], e.ids[1])
}

func (e *sessionPolicyAmbiguousError) Unwrap() error { return ErrSessionPolicyAmbiguous }

// SessionPolicyExpiredCode marks a grant whose TimeRangeModule window has
// closed. Client-fixable by granting again, which is the whole reason it is
// typed: without it the account rejects the operation and the bundler reports
// "User Operation expired or has an invalid time range" with no policy id and
// no indication that a re-grant is the remedy.
const SessionPolicyExpiredCode = "SESSION_POLICY_EXPIRED"

// SessionPolicySignerRetiredCode marks a grant whose installed signer is not
// this gateway's session_signer_private_key — every grant made before that key
// was replaced. Covering the workflow is not enough: the gateway cannot sign
// as that entity, so the grant can never execute. Client-fixable by granting
// again, which installs the current signer; nothing on the gateway side heals
// it.
const SessionPolicySignerRetiredCode = "SESSION_POLICY_SIGNER_RETIRED"

// SessionPolicyEntityOccupiedCode is returned when every candidate entity
// in the bounded occupancy probe is already installed on the account
// (#763 A). Client-fixable only by cleaning leftover entities on chain.
const SessionPolicyEntityOccupiedCode = "SESSION_POLICY_ENTITY_OCCUPIED"

// SessionPolicyChainWindowMismatchCode marks a stored grant whose
// ValidUntil disagrees with the TimeRangeModule window actually
// installed on the entity (#763 C). Storage cannot see this; only a
// chain read can.
const SessionPolicyChainWindowMismatchCode = "SESSION_POLICY_CHAIN_WINDOW_MISMATCH"

// SessionPolicyChainWindowMissingCode marks the special case of that drift
// where the entity has NO TimeRange hook at all: timeRanges() reads zero while
// storage promises a real expiry.
//
// Separate from the mismatch code on purpose. It is a different failure with a
// different remedy, and folding the two renders "no hook installed" as
// "chain validUntil 1970-01-01T00:00:00Z" — which reads as a corrupt timestamp
// and sends whoever is on call hunting for one.
//
// It is also strictly MORE permissive than storage claims rather than less: no
// time-range hook means the grant never expires on chain, where the mismatch
// case usually means it expired early. Refused all the same — a grant that does
// not enforce the expiry the owner approved is not one to sign under — but an
// operator needs to be able to tell the two apart when triaging.
const SessionPolicyChainWindowMissingCode = "SESSION_POLICY_CHAIN_WINDOW_MISSING"

// maxEntityOccupancyProbes bounds consecutive candidates whose occupancy
// could not be read. An unreadable entity is treated as occupied, and a chain
// that answers nothing for this many candidates in a row is a broken read, not
// a long used range: NextFreeSessionEntityID stops there with the last error.
const maxEntityOccupancyProbes = 8

// maxEntityOccupancyWalk bounds how far past entities the chain reports as
// spent the allocator walks. A spent entity is a fact — a signer is installed,
// or its deferred-nonce sequence is above zero and it is unusable forever — so
// skipping it is always safe. The walk is for a gateway whose storage is behind
// the chain: a fresh database against a runner that was granted before. The SDK
// E2E suite boots a new gateway per run, so storage said "next is 1" while the
// chain had spent 1..N, and after eight spent candidates the old cap returned
// SESSION_POLICY_ENTITY_OCCUPIED for good (ava-sdk-js run 38666525933,
// 2026-10-15). maxEntityOccupancyProbes now bounds consecutive candidates that
// could not be READ, which is a broken chain read rather than a long used range.
const maxEntityOccupancyWalk = 256

// occupancyProbeTimeout bounds one occupancy candidate (signer + nonce
// sequence). The loop's overall budget is occupancyWalkBudget.
const occupancyProbeTimeout = 5 * time.Second

// occupancyWalkBudget bounds the whole allocation. A spent candidate is two
// quick reads, so a full walk fits well inside it at normal RPC latency, while
// a hung chain still ends: once the budget is gone every probe fails at once
// and the unread cap returns within maxEntityOccupancyProbes iterations.
const occupancyWalkBudget = 2 * time.Minute

// EntityOccupancyChecker reports whether a validation entity is already
// installed on the account. A failed read must be reported as an error
// (not occupied=false): the allocator treats errors as occupied.
type EntityOccupancyChecker func(ctx context.Context, account common.Address, entity uint32) (occupied bool, err error)

// WindowVerifier reads the TimeRangeModule window for (account, entity).
// validUntil / validAfter are unix seconds (uint48 on chain).
type WindowVerifier func(ctx context.Context, chainID int64, account common.Address, entity uint32) (validUntilSec, validAfterSec uint64, err error)

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
			return nil, &sessionPolicyAmbiguousError{wallet: wallet, ids: [2]string{found.ID, p.ID}}
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

// sessionEntityTaken reports whether any stored policy on this runner
// already occupies entity. Used by submit instead of comparing against
// storage-only NextSessionEntityID, which would false-collide after an
// occupancy-aware prepare skipped on-chain leftovers (#763 A).
func sessionEntityTaken(db storage.Storage, chainID int64, owner, wallet common.Address, entity uint32) (bool, error) {
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return false, err
	}
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != wallet {
			continue
		}
		if p.EntityID == entity {
			return true, nil
		}
	}
	return false, nil
}

// Submit checks the signed entity, not the allocator's next candidate. An
// unread probe must not turn into a false concurrent-grant conflict (#853).
func verifySessionEntityAvailable(ctx context.Context, db storage.Storage, chainID int64, owner, wallet common.Address, entity uint32, check EntityOccupancyChecker) error {
	if entity == 0 {
		return fmt.Errorf("validation entity must be positive")
	}
	taken, err := sessionEntityTaken(db, chainID, owner, wallet, entity)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("%w: entity %d is stored", ErrSessionEntityTaken, entity)
	}
	if check == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, occupancyProbeTimeout)
	defer cancel()
	occupied, err := check(probeCtx, wallet, entity)
	if err != nil {
		return fmt.Errorf("%w: entity %d on chain %d: %w", ErrSessionEntityUnverified, entity, chainID, err)
	}
	if occupied {
		return fmt.Errorf("%w: entity %d is occupied on chain %d", ErrSessionEntityTaken, entity, chainID)
	}
	return nil
}

// NextFreeSessionEntityID is NextSessionEntityID plus an on-chain occupancy
// probe (#763 A). When check is nil the result is the storage-only id
// (offline tests). A failed occupancy read is occupied, never free.
// Candidates the chain reports as spent are skipped, up to
// maxEntityOccupancyWalk of them, because storage may be behind the chain.
// Candidates that cannot be read are skipped too, but only
// maxEntityOccupancyProbes in a row: after that it returns a typed
// SESSION_POLICY_ENTITY_OCCUPIED error carrying the last read error rather
// than walking a chain that is not answering.
func NextFreeSessionEntityID(
	ctx context.Context,
	db storage.Storage,
	chainID int64,
	owner, wallet common.Address,
	check EntityOccupancyChecker,
) (uint32, error) {
	start, err := NextSessionEntityID(db, chainID, owner, wallet)
	if err != nil {
		return 0, err
	}
	if check == nil {
		return start, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	overall, cancel := context.WithTimeout(ctx, occupancyWalkBudget)
	defer cancel()
	unread, unreadTotal := 0, 0
	var lastOccErr error
	for i := uint32(0); i < maxEntityOccupancyWalk; i++ {
		cand := start + i
		probeCtx, probeCancel := context.WithTimeout(overall, occupancyProbeTimeout)
		occupied, occErr := check(probeCtx, wallet, cand)
		probeCancel()
		if occErr != nil {
			// Fail-closed: unknown is occupied, same as VerifySupersededTeardown.
			lastOccErr = occErr
			unread++
			unreadTotal++
			if unread >= maxEntityOccupancyProbes {
				return 0, fmt.Errorf(
					"%s: no free validation entity on %s: %d candidates in a row could not be read, ending at %d (last occupancy read: %w)",
					SessionPolicyEntityOccupiedCode, wallet.Hex(), unread, cand, occErr)
			}
			continue
		}
		unread = 0
		if !occupied {
			return cand, nil
		}
	}
	// The walk is exhausted. "Every one is spent" is only true when every
	// candidate answered; a read error anywhere in the range travels with the
	// result, because the operator's remedy is different (fix the RPC, not
	// rotate the runner).
	if lastOccErr != nil {
		return 0, fmt.Errorf(
			"%s: no free validation entity on %s in %d candidates starting at %d; %d could not be read (last occupancy read: %w)",
			SessionPolicyEntityOccupiedCode, wallet.Hex(), maxEntityOccupancyWalk, start, unreadTotal, lastOccErr)
	}
	return 0, fmt.Errorf(
		"%s: no free validation entity on %s in %d candidates starting at %d; every one is spent on chain — rotate this runner to a fresh salt",
		SessionPolicyEntityOccupiedCode, wallet.Hex(), maxEntityOccupancyWalk, start)
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
	n.sessionChainReads = true
	preset.SetSessionResolver(newSessionResolver(n.db, configuredSessionSigner(n.config), n.teardownVerifier(), n.windowVerifier()))
	preset.SetEOA7702AccountLookup(func(chainID int64, owner, sender common.Address) (bool, error) {
		if n.db == nil {
			return false, nil
		}
		rec, err := GetWallet(n.db, chainID, owner, sender.Hex())
		if err != nil {
			return false, nil
		}
		return rec.IsEOA7702() && rec.Address != nil && *rec.Address == owner && *rec.Address == sender, nil
	})
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

// entityOccupancyChecker reports whether a validation entity is already
// spoken for on chain (#763 A). Occupied means a non-zero signer OR a
// non-zero deferred nonce sequence — the same definition
// seedEntitiesConsumedOnChain uses. Teardown can zero the signer while
// the EntryPoint sequence stays > 0, and PrepareSessionGrant assumes
// sequence 0.
func (n *Engine) entityOccupancyChecker(chainID int64) EntityOccupancyChecker {
	return func(ctx context.Context, account common.Address, entity uint32) (bool, error) {
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
		if signer != (common.Address{}) {
			return true, nil
		}
		seq, err := aa.EntityDeferredNonceSequence(ctx, client, account, entity)
		if err != nil {
			return false, err
		}
		return seq > 0, nil
	}
}

// occupancyFor is the occupancy checker PrepareSessionPolicy/SubmitSessionPolicy
// pass down. Nil unless InstallSessionResolver has run, so unit tests that
// never install a resolver stay offline.
func (n *Engine) occupancyFor(chainID int64) EntityOccupancyChecker {
	if n == nil || !n.sessionChainReads {
		return nil
	}
	return n.entityOccupancyChecker(chainID)
}

// bindNativeRecipientChecks attaches the session signer (refused as a
// native recipient) and a memoized CodeAt that uses the pooled
// ChainStateReader. A missing session signer key is an error: swallowing it
// would skip the reservation. Does not replace a test-injected CodeAt, but
// wraps it so Validate + HooksFor share one lookup per address. Production
// InstallSessionResolver enables sessionChainReads; without it CodeAt stays
// nil and Validate fail-closes naming the resolver.
func (n *Engine) bindNativeRecipientChecks(chainID int64, perms *SessionPermissions) error {
	if n == nil || perms == nil {
		return nil
	}
	if perms.SessionSigner == nil {
		signer, err := n.sessionSignerAddress()
		if err != nil {
			return fmt.Errorf("cannot bind session signer for native-recipient reservation: %w", err)
		}
		perms.SessionSigner = &signer
	}
	if perms.CodeAt != nil {
		perms.CodeAt = memoizeCodeAt(perms.CodeAt)
		return nil
	}
	if !n.sessionChainReads {
		return nil
	}
	perms.CodeAt = memoizeCodeAt(func(addr common.Address) ([]byte, error) {
		reader := GetChainStateReaderForChain(uint64(chainID))
		if reader == nil {
			return nil, fmt.Errorf("no ChainStateReader registered for chain %d", chainID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return reader.CodeAt(ctx, addr)
	})
	return nil
}

// memoizeCodeAt caches lookups for one request. The map is unsynchronized:
// SessionPermissions copies share the closure, so callers must not Validate
// concurrently on the same bound CodeAt.
func memoizeCodeAt(inner func(common.Address) ([]byte, error)) func(common.Address) ([]byte, error) {
	if inner == nil {
		return nil
	}
	type result struct {
		code []byte
		err  error
	}
	cache := make(map[common.Address]result)
	return func(addr common.Address) ([]byte, error) {
		if got, ok := cache[addr]; ok {
			return got.code, got.err
		}
		code, err := inner(addr)
		cache[addr] = result{code: code, err: err}
		return code, err
	}
}

// windowVerifier reads TimeRangeModule.timeRanges for (account, entity).
func (n *Engine) windowVerifier() WindowVerifier {
	return func(ctx context.Context, chainID int64, account common.Address, entity uint32) (uint64, uint64, error) {
		swCfg := n.ResolveSmartWalletConfig(chainID)
		if swCfg == nil || swCfg.EthRpcUrl == "" {
			return 0, 0, fmt.Errorf("no RPC configured for chain %d", chainID)
		}
		client, err := ethclient.DialContext(ctx, swCfg.EthRpcUrl)
		if err != nil {
			return 0, 0, fmt.Errorf("dialing chain %d: %w", chainID, err)
		}
		defer client.Close()
		return aa.EntityTimeRangeOnChain(ctx, client, account, entity)
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
	return newSessionResolver(db, signerKeyFor, verifyTeardown, nil)
}

func newSessionResolver(
	db storage.Storage,
	signerKeyFor func(signer common.Address) (*ecdsa.PrivateKey, error),
	verifyTeardown TeardownVerifier,
	verifyWindow WindowVerifier,
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
		// Refuse an expired grant here rather than letting the account do it.
		// The grant installs a TimeRangeValidationHook, so once ValidUntil
		// passes the operation is rejected on chain as "User Operation expired
		// or has an invalid time range" — a bundler-shaped error with no
		// policy id, which reads as an infrastructure fault and hides the fact
		// that granting again fixes it.
		//
		// Returning an error (not nil) is deliberate: nil would produce "no
		// session authorization ... requires a session grant", which is wrong
		// and actively misleading when the owner HAS granted and simply needs
		// to renew.
		if policy.Expired(time.Now()) {
			return nil, fmt.Errorf(
				"%s: session policy %s on wallet %s expired at %s; grant again to keep executing",
				SessionPolicyExpiredCode, policy.ID, wallet.Hex(),
				time.UnixMilli(policy.ValidUntil).UTC().Format(time.RFC3339))
		}
		// Applied grants: the stored ValidUntil can disagree with the
		// TimeRangeModule window actually installed on the entity — the
		// entity-reuse drift #763 filed from. A+B close allocation; they
		// cannot heal a record that is already stored wrong. Check once
		// after install and cache the match so this is not paid per UserOp.
		if err := checkAppliedChainWindow(db, policy, verifyWindow); err != nil {
			return nil, err
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
			// Captured now, from the payload the owner signed: every entity the
			// batch uninstalls must be verified gone (N-way replace included).
			replacedEntities, entityErr := aa.UninstalledEntitiesWithin(policy.Grant.InstallCall)
			if entityErr != nil {
				return nil, fmt.Errorf("session policy %s: reading the teardown targets: %w", policy.ID, entityErr)
			}
			auth.OnApplied = func(userOpHash string) error {
				if err := MarkSessionGrantAppliedByID(db, policyChain, policyOwner, policyRunner, policyID, userOpHash); err != nil {
					return err
				}
				if len(replacedEntities) == 0 || verifyTeardown == nil {
					return nil
				}
				// The batch has mined, which says the operation ran — not that
				// the uninstall did anything. A failed check does not fail the
				// send: the new grant is installed and there is nothing to roll
				// back. VerifySupersededTeardown marks TornDownAt when clear.
				ctx, cancel := context.WithTimeout(context.Background(), teardownVerifyTimeout)
				defer cancel()
				ownerAddr := policyOwner
				for _, replacedEntity := range replacedEntities {
					replaced := &model.SessionPolicy{
						ID: policyID, ChainID: policyChain, Owner: &ownerAddr,
						Runner: &policyRunner, EntityID: replacedEntity,
					}
					_ = VerifySupersededTeardown(ctx, db, verifyTeardown, replaced, globalLogger)
				}
				return nil
			}
		}
		return auth, nil
	}
}

// configuredSessionSigner resolves a session signer to its key.
//
// Every policy is signed by the one key configured as
// session_signer_private_key, so this accepts exactly that address and
// refuses anything else. This is the DESIGN, not a placeholder — per-policy
// signer keys are deliberately not planned, because they add key generation,
// custody, and rotation without changing the trust model. Authority is per
// grant regardless of the key: each policy occupies its own validation
// ENTITY, and the entity — not the key — is what carries the hooks, the
// revocation target, and the nonce space that makes grant-time signing work.
// A single shared key means a compromised session signer reaches every grant,
// so a key per policy would buy complexity, not a smaller blast radius.
// Revisit only if that blast radius itself becomes the thing to shrink.
//
// The key is its own setting, not a smart_wallet controller_private_key.
// Controllers are per network, while this signer is shared by every chain:
// reading it from the top-level smart_wallet block, which a gateway points at
// its default chain, put mainnet grants behind a testnet controller.
//
// Refusing an unknown signer is the important half: a policy naming a key we
// cannot produce must fail loudly here, not sign with the wrong one.
func configuredSessionSigner(cfg *config.Config) func(common.Address) (*ecdsa.PrivateKey, error) {
	return func(signer common.Address) (*ecdsa.PrivateKey, error) {
		if cfg == nil || cfg.SessionSignerPrivateKey == nil {
			return nil, fmt.Errorf("no session_signer_private_key configured; cannot sign as session signer %s", signer.Hex())
		}
		configured := crypto.PubkeyToAddress(cfg.SessionSignerPrivateKey.PublicKey)
		if signer != configured {
			return nil, fmt.Errorf(
				"%s: the grant's session signer %s is not this gateway's session signer %s; grant again to keep executing",
				SessionPolicySignerRetiredCode, signer.Hex(), configured.Hex())
		}
		return cfg.SessionSignerPrivateKey, nil
	}
}

// checkAppliedChainWindow compares a stored grant's ValidUntil against the
// TimeRangeModule window on the entity (#763 C). Pending grants skip: the
// install has not landed. Already-checked grants skip: the match is cached
// on ChainWindowChecked. verifyWindow nil skips (offline tests).
//
// A read failure is returned as-is (infra), not as a mismatch. A mismatch
// is the typed SESSION_POLICY_CHAIN_WINDOW_MISMATCH so the send never
// reaches the bundler's opaque expiry string.
func checkAppliedChainWindow(db storage.Storage, policy *model.SessionPolicy, verifyWindow WindowVerifier) error {
	if policy == nil || policy.Grant == nil || !policy.Grant.Applied() {
		return nil
	}
	if policy.Grant.ChainWindowChecked || verifyWindow == nil || policy.ValidUntil <= 0 {
		return nil
	}
	if policy.Owner == nil || policy.Runner == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownVerifyTimeout)
	defer cancel()
	untilSec, _, err := verifyWindow(ctx, policy.ChainID, *policy.Runner, policy.EntityID)
	if err != nil {
		return fmt.Errorf("reading TimeRange for session policy %s entity %d on %s: %w",
			policy.ID, policy.EntityID, policy.Runner.Hex(), err)
	}
	storedSec := uint64(policy.ValidUntil / 1000)
	if untilSec == 0 {
		return fmt.Errorf(
			"%s: session policy %s entity %d on %s has no TimeRange hook installed, "+
				"while storage promises validUntil %s; the grant does not expire on chain. "+
				"Grant again onto a free entity",
			SessionPolicyChainWindowMissingCode, policy.ID, policy.EntityID, policy.Runner.Hex(),
			time.UnixMilli(policy.ValidUntil).UTC().Format(time.RFC3339))
	}
	if untilSec != storedSec {
		return fmt.Errorf(
			"%s: session policy %s entity %d on %s: stored validUntil %s, chain validUntil %s; grant again onto a free entity",
			SessionPolicyChainWindowMismatchCode, policy.ID, policy.EntityID, policy.Runner.Hex(),
			time.UnixMilli(policy.ValidUntil).UTC().Format(time.RFC3339),
			time.Unix(int64(untilSec), 0).UTC().Format(time.RFC3339))
	}
	if markErr := MarkChainWindowChecked(db, policy.ChainID, *policy.Owner, *policy.Runner, policy.ID); markErr != nil {
		// The send is still good — the windows matched. A failed mark
		// only means the next UserOp re-reads the chain.
		if globalLogger != nil {
			globalLogger.Warn("chain window matched but ChainWindowChecked could not be stored",
				"policy", policy.ID, "error", markErr)
		}
	} else {
		policy.Grant.ChainWindowChecked = true
	}
	return nil
}

// MarkChainWindowChecked records that the send path has confirmed the
// entity's installed TimeRange agrees with storage. Idempotent.
func MarkChainWindowChecked(db storage.Storage, chainID int64, owner, runner common.Address, policyID string) error {
	lock := sessionAuthorityLock(chainID, owner, runner)
	lock.Lock()
	defer lock.Unlock()
	raw, err := db.GetKey(SessionPolicyKey(chainID, owner, policyID))
	if err != nil {
		return err
	}
	policy := &model.SessionPolicy{}
	if err := policy.FromStorageData(raw); err != nil {
		return err
	}
	if policy.Grant == nil || policy.Grant.ChainWindowChecked {
		return nil
	}
	policy.Grant.ChainWindowChecked = true
	return StoreSessionPolicy(db, policy)
}

// DriftedSessionGrant is one stored policy whose ValidUntil disagrees
// with the TimeRangeModule window on its entity (#763 sweep).
type DriftedSessionGrant struct {
	Policy         *model.SessionPolicy
	StoredUntilSec uint64
	ChainUntilSec  uint64
	ChainAfterSec  uint64
	ReadErr        error
	// WindowMissing is the ChainUntilSec == 0 case: no TimeRange hook on the
	// entity at all. Broken out so a pre-deploy sweep can count it separately —
	// it is the population the send path will start refusing, and it is not
	// visibly different from "expired in 1970" in the raw numbers.
	WindowMissing bool
}

// FindDriftedSessionGrants walks applied usable grants on chainID and
// reports those whose installed window disagrees with storage. A+B do
// not heal already-stored records; this is how operators find them so
// the owner can re-grant onto a free entity.
//
// verify nil: nothing to compare, empty result.
// A read failure is recorded on the row rather than aborting the sweep.
func FindDriftedSessionGrants(ctx context.Context, db storage.Storage, chainID int64, verify WindowVerifier) ([]DriftedSessionGrant, error) {
	if verify == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	items, err := db.GetByPrefix(SessionPolicyChainPrefix(chainID))
	if err != nil {
		return nil, fmt.Errorf("listing session policies on chain %d: %w", chainID, err)
	}
	out := make([]DriftedSessionGrant, 0)
	for _, item := range items {
		policy := &model.SessionPolicy{}
		if err := policy.FromStorageData(item.Value); err != nil {
			return nil, fmt.Errorf("session policy at %s is unreadable: %w", string(item.Key), err)
		}
		if !policy.Usable() || policy.Grant == nil || !policy.Grant.Applied() || policy.ValidUntil <= 0 {
			continue
		}
		if policy.Runner == nil {
			continue
		}
		storedSec := uint64(policy.ValidUntil / 1000)
		untilSec, afterSec, readErr := verify(ctx, chainID, *policy.Runner, policy.EntityID)
		if readErr != nil {
			out = append(out, DriftedSessionGrant{Policy: policy, StoredUntilSec: storedSec, ReadErr: readErr})
			continue
		}
		if untilSec != storedSec {
			out = append(out, DriftedSessionGrant{
				Policy: policy, StoredUntilSec: storedSec,
				ChainUntilSec: untilSec, ChainAfterSec: afterSec,
				WindowMissing: untilSec == 0,
			})
		}
	}
	return out, nil
}

// maxOnChainTeardowns is how many prior entities one replacement batch
// uninstalls. Bounded by deferred validation gas: each uninstall costs
// ~100k verification gas (Sepolia measurement, #731 review), and the seed
// scales with count. Soft-degrades rather than refusing prepare — leftover
// entities ride the next grant (self-heal), matching #716.
//
// Sized for install+hooks (~700k base) plus a few teardowns with headroom
// under Rundler's efficiency floor after the scaled seed lands.
const maxOnChainTeardowns = 4

// onChainTeardownTargets returns grants on this runner whose validation
// entity is still believed installed and should be removed when the owner
// next grants (#717 AC1).
//
// Included when Grant.NeedsOnChainCleanup():
//   - usable + applied — the current grant being replaced
//   - revoked + applied, not yet torn down — leftovers and late-landing orphans
//
// Excluded: never applied, already TornDownAt, missing InstallCall, entity 0.
//
// If more than maxOnChainTeardowns remain, the first N (stable list order)
// are returned; the rest wait for a later grant. Never refuses prepare.
func onChainTeardownTargets(db storage.Storage, chainID int64, owner, runner common.Address) ([]*model.SessionPolicy, error) {
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return nil, err
	}
	out := make([]*model.SessionPolicy, 0)
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != runner || p.Grant == nil {
			continue
		}
		if !p.Grant.NeedsOnChainCleanup() || len(p.Grant.InstallCall) == 0 || p.EntityID == 0 {
			continue
		}
		if p.Usable() || p.Status == model.SessionPolicyRevoked {
			out = append(out, p)
		}
	}
	// Deliberately NOT capped here. The caller drops candidates the chain says
	// are already clear, and capping first would let cleared slots eat the
	// budget a live leftover needs — a wallet could then spend several grant
	// cycles retiring entities that were already gone. Cap after the filter.
	return out, nil
}
