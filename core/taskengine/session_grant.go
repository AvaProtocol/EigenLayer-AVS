package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Creating a session grant is two calls, not one.
//
// The owner signs an EIP-712 payload, and that payload cannot exist until the
// gateway has allocated an entity, chosen a signer, and computed the nonce the
// carrying operation will use. So the screen asks for the payload (Prepare),
// the wallet signs it, and the signature comes back (Submit). avs-infra
// Smart_Wallet_MA_v2_Spend_Policy.md §7.5.
//
// Nothing reaches the chain here. The install rides the workflow's first
// operation, which is what makes the grant gasless to authorize and free to
// revoke until it is used.

// SessionGrantRequest is what the grant screen asks for.
type SessionGrantRequest struct {
	Owner  common.Address
	Wallet common.Address // the smart wallet being granted on

	AgentLabel    string
	Justification string

	// Hooks are the encoded permission modules (allowlist, spend cap, time
	// range). They ride the same installValidation call, so the owner's one
	// signature covers them too.
	Hooks [][]byte

	// HooksFor builds the hooks once the entity is known. Hook install data
	// embeds the entity id, and the entity is allocated inside
	// PrepareSessionGrant — so a caller that does not already know its entity
	// (the REST prepare path) supplies this instead of Hooks. When set it
	// wins over Hooks.
	HooksFor func(entityID uint32) ([][]byte, error)

	// Deadline pins the signing-window bound to an absolute uint48 unix-
	// seconds value instead of now+SigningWindow. The submit path needs this:
	// the deadline is inside the signed digest, so reproducing the prepared
	// grant deterministically requires the original value, not a fresh clock
	// reading. Zero means derive from SigningWindow.
	Deadline uint64

	// Selectors scopes the grant. Empty means a global grant, which requires
	// an execution hook — see aa.SessionGrant.
	Selectors [][4]byte

	// ValidUntil is the grant's own lifetime (unix ms), enforced on chain by
	// the TimeRangeModule hook when one is installed.
	ValidUntil int64

	// SigningWindow bounds how long the owner's signature stays usable before
	// the grant's first operation. Distinct from ValidUntil. Zero picks
	// defaultSigningWindow.
	SigningWindow time.Duration

	// AllowSelfAdministration accepts a global grant that carries no execution
	// hook, which can call the account's own installValidation and escalate
	// itself. Present so the case is stateable — test fixtures need it — and
	// deliberately absent from anything the grant screen sends.
	AllowSelfAdministration bool

	// TeardownCheck, when set, reads each storage teardown candidate on chain
	// before packing the replace batch. Entities already clear are dropped and
	// marked TornDownAt — storage belief alone is wrong after the owner ran
	// explicit onChainCleanup (or a prior replace that never marked). Re-
	// uninstalling a cleared entity reverts the whole deferred batch, so the
	// new grant never installs (#731 re-audit).
	//
	// Nil skips the read (offline tests). A read failure fails prepare rather
	// than packing a batch that may permanently brick granting for the wallet.
	TeardownCheck TeardownVerifier
	// TeardownCtx bounds chain reads during prepare. Ignored when TeardownCheck is nil.
	TeardownCtx context.Context
}

// defaultSigningWindow bounds the gap between signing a grant and its first
// use. Long enough that a workflow scheduled for "tomorrow" still fires, short
// enough that a forgotten authorization does not sit in storage indefinitely.
// A dormant workflow whose window lapses needs a re-sign, which is why this is
// not minutes.
const defaultSigningWindow = 30 * 24 * time.Hour

// PreparedSessionGrant is what the wallet signs, plus everything the gateway
// must remember to accept the signature back.
type PreparedSessionGrant struct {
	// Digest is the EIP-712 hash the owner signs. Signed RAW — the account's
	// fallback path skips the replay-safe rewrap during validateUserOp, so
	// this is exactly what eth_signTypedData_v4 must produce.
	Digest common.Hash

	// The fields below are echoed back to Submit. They are returned rather
	// than stashed server-side so a prepare that is never completed leaves no
	// state behind.
	Policy *model.SessionPolicy

	// DeferredData is the encoded action the signature authorizes.
	DeferredData []byte

	// Supersedes lists every on-chain grant this replacement tears down in the
	// deferred batch (usable applied + revoked-applied leftovers). Empty when
	// nothing installed is known — a first grant, or only pending priors.
	//
	// Echoed to Submit so the write can re-check that the set the owner signed
	// away is still the set storage believes is installed. The signature
	// commits to specific entities' teardowns; if the set moved, prepare again.
	Supersedes []SupersededGrant
}

// SupersededGrant identifies one on-chain grant a prepared replacement removes.
type SupersededGrant struct {
	PolicyID string
	EntityID uint32
}

// PrepareSessionGrant allocates the grant and returns the payload to sign.
//
// The entity allocation here is READ-ONLY and provisional. It becomes real in
// SubmitSessionGrant, which must re-check it inside the write — two prepares
// racing on one wallet would otherwise both see the same free entity, and the
// second install would overwrite the first grant's signer.
func PrepareSessionGrant(
	db storage.Storage,
	chainID int64,
	sessionSigner common.Address,
	policyID string,
	req SessionGrantRequest,
) (*PreparedSessionGrant, error) {
	if policyID == "" {
		return nil, fmt.Errorf("policy id is required")
	}
	if req.Owner == (common.Address{}) || req.Wallet == (common.Address{}) {
		return nil, fmt.Errorf("owner and wallet are required")
	}
	if sessionSigner == (common.Address{}) {
		return nil, fmt.Errorf("session signer is required")
	}

	entity, err := NextSessionEntityID(db, chainID, req.Owner, req.Wallet)
	if err != nil {
		return nil, err
	}

	hooks := req.Hooks
	if req.HooksFor != nil {
		if hooks, err = req.HooksFor(entity); err != nil {
			return nil, fmt.Errorf("building hooks for entity %d: %w", entity, err)
		}
	}

	grant := aa.SessionGrant{
		EntityID:  entity,
		Signer:    sessionSigner,
		Selectors: req.Selectors,
		Hooks:     hooks,
		Global:    len(req.Selectors) == 0,

		AllowSelfAdministration: req.AllowSelfAdministration,
	}
	installCall, err := aa.PackSessionSignerInstall(grant)
	if err != nil {
		return nil, fmt.Errorf("building the grant for wallet %s: %w", req.Wallet.Hex(), err)
	}

	// A grant REPLACES prior authority, and #716 made that true in storage.
	// Here it becomes true on chain: every applied entity on the runner —
	// the current usable grant and any revoked leftovers / landed orphans —
	// is uninstalled in the same deferred batch as the new install, so the
	// account ends holding exactly one entity (#717 AC1), not one per re-grant.
	//
	// Proven on Sepolia for install+one-uninstall (#717 R-A): the batch
	// composes with the NEW entity as the outer validation and needs no second
	// owner signature. N-way is the same composition with more uninstalls.
	deferredCall := installCall
	teardownTargets, err := onChainTeardownTargets(db, chainID, req.Owner, req.Wallet)
	if err != nil {
		return nil, err
	}
	teardownTargets, err = dropClearedTeardownTargets(
		req.TeardownCtx, db, chainID, req.Owner, req.Wallet, teardownTargets, req.TeardownCheck)
	if err != nil {
		return nil, err
	}
	var supersedes []SupersededGrant
	if len(teardownTargets) > 0 {
		// Install FIRST: the operation carrying this is validated by the new
		// entity, which has to exist by the time the outer validation runs.
		calls := make([]aa.Call, 0, 1+len(teardownTargets))
		calls = append(calls, aa.Call{Target: req.Wallet, Value: big.NewInt(0), Data: installCall})
		for _, prior := range teardownTargets {
			uninstallCall, err := aa.SessionSignerUninstallFromInstall(
				prior.EntityID, prior.Grant.InstallCall)
			if err != nil {
				return nil, fmt.Errorf("building the teardown for grant %s (entity %d): %w",
					prior.ID, prior.EntityID, err)
			}
			calls = append(calls, aa.Call{Target: req.Wallet, Value: big.NewInt(0), Data: uninstallCall})
			supersedes = append(supersedes, SupersededGrant{PolicyID: prior.ID, EntityID: prior.EntityID})
		}
		deferredCall, err = aa.PackExecuteBatchMAv2(calls)
		if err != nil {
			return nil, fmt.Errorf("packing the replace batch: %w", err)
		}
	}

	deadline := req.Deadline
	if deadline == 0 {
		window := req.SigningWindow
		if window <= 0 {
			window = defaultSigningWindow
		}
		deadline = uint64(time.Now().Add(window).Unix())
	}

	// The digest commits to the FULL nonce of the operation that will carry
	// the action. It is knowable now only because a freshly allocated entity
	// is a fresh nonce key whose sequence is zero.
	carrierNonce, err := userop.EncodeNonceMAv2(entity,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction, 0)
	if err != nil {
		return nil, fmt.Errorf("encoding the carrier nonce: %w", err)
	}

	digest, err := userop.DeferredActionDigest(
		big.NewInt(chainID), req.Wallet, carrierNonce, deadline, deferredCall)
	if err != nil {
		return nil, fmt.Errorf("building the grant digest: %w", err)
	}

	deferredData, err := userop.EncodeDeferredActionData(
		userop.FallbackSignerLocator(), deadline, deferredCall)
	if err != nil {
		return nil, fmt.Errorf("encoding the deferred action: %w", err)
	}

	owner, wallet, signer := req.Owner, req.Wallet, sessionSigner
	return &PreparedSessionGrant{
		Digest:       digest,
		DeferredData: deferredData,
		Supersedes:   supersedes,
		Policy: &model.SessionPolicy{
			ID:            policyID,
			Owner:         &owner,
			Runner:        &wallet,
			ChainID:       chainID,
			EntityID:      entity,
			SessionSigner: &signer,
			AgentLabel:    req.AgentLabel,
			Justification: req.Justification,
			ValidUntil:    req.ValidUntil,
			Status:        model.SessionPolicyPending,
			CreatedAt:     time.Now().UnixMilli(),
			Grant: &model.SessionGrantAuthorization{
				InstallCall:           deferredCall,
				CarrierNonce:          carrierNonce,
				Deadline:              deadline,
				RequiresExecuteUserOp: grant.HasExecutionHook(),
			},
		},
	}, nil
}

// SubmitSessionGrant records the owner's signature and stores the policy.
//
// The signature is verified against the owner before anything is written. A
// grant that does not recover to the owner would be accepted, stored, and then
// fail during the first operation's validation — on chain, with an error that
// names neither this record nor the mismatch.
func SubmitSessionGrant(db storage.Storage, prepared *PreparedSessionGrant, ownerSignature []byte) (*model.SessionPolicy, error) {
	if prepared == nil || prepared.Policy == nil {
		return nil, fmt.Errorf("nothing prepared to submit")
	}
	if len(ownerSignature) != 65 {
		return nil, fmt.Errorf("owner signature is %d bytes, want 65", len(ownerSignature))
	}
	policy := prepared.Policy

	if err := verifyGrantSignature(prepared.Digest, ownerSignature, *policy.Owner); err != nil {
		return nil, err
	}

	// Re-check the entity inside the write path. PrepareSessionGrant's
	// allocation was provisional: another grant on this wallet may have
	// landed in between, and reusing its entity would overwrite that grant's
	// signer on chain.
	entity, err := NextSessionEntityID(db, policy.ChainID, *policy.Owner, *policy.Runner)
	if err != nil {
		return nil, err
	}
	if entity != policy.EntityID {
		return nil, fmt.Errorf(
			"entity %d was taken while this grant was being signed (next free is %d); prepare it again",
			policy.EntityID, entity)
	}

	// Re-check what this grant REPLACES, for the same reason and with the same
	// remedy. The signature commits to tearing down one specific entity, so a
	// payload prepared against a grant that has since been revoked or replaced
	// would remove something the owner did not agree to remove — or nothing at
	// all, which is worse, because it mines.
	if err := recheckSupersededGrant(db, prepared, policy); err != nil {
		return nil, err
	}

	policy.Grant.OwnerSignature = ownerSignature
	if err := StoreSessionPolicy(db, policy); err != nil {
		return nil, err
	}
	return policy, nil
}

// MarkSessionGrantApplied records that the deferred install reached the chain,
// so it is never attached to a second operation.
func MarkSessionGrantApplied(db storage.Storage, policy *model.SessionPolicy, userOpHash string) error {
	if policy == nil || policy.Grant == nil {
		return fmt.Errorf("no policy to mark applied")
	}
	if policy.Grant.Applied() {
		return nil // already recorded; marking twice is not an error
	}
	policy.Grant.AppliedAt = time.Now().UnixMilli()
	policy.Grant.AppliedUserOpHash = userOpHash
	policy.Status = model.SessionPolicyActive
	return StoreSessionPolicy(db, policy)
}

// MarkSessionGrantAppliedByID is MarkSessionGrantApplied for callers that
// held the policy EARLIER — the resolver's callback fires after an operation
// confirms, and by then the record may have moved. It re-reads under the
// runner's write lock:
//
//   - Record gone: nothing to update (legacy pending deletes). Prefer retaining
//     revoked records so InstallCall survives.
//   - Still pending: normal pending → active transition.
//   - Revoked, not yet Applied: the install landed after supersede/revoke.
//     Record AppliedAt WITHOUT resurrecting usable status — the entity is an
//     on-chain orphan the next replace batch (or explicit cleanup) must remove.
//   - Already applied / other status: no-op.
//
// Safe to take here because the resolver has already released its read lock by
// the time this callback fires — it is invoked after the operation confirms,
// not while authority is being resolved.
func MarkSessionGrantAppliedByID(db storage.Storage, chainID int64, owner, runner common.Address, policyID, userOpHash string) error {
	lock := sessionAuthorityLock(chainID, owner, runner)
	lock.Lock()
	defer lock.Unlock()

	raw, err := db.GetKey(SessionPolicyKey(chainID, owner, policyID))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil // no record — see above
	}
	if err != nil {
		return fmt.Errorf("reading session policy %s: %w", policyID, err)
	}
	policy := &model.SessionPolicy{}
	if err := policy.FromStorageData(raw); err != nil {
		return fmt.Errorf("session policy %s is unreadable: %w", policyID, err)
	}
	if policy.Grant == nil || policy.Grant.Applied() {
		return nil
	}
	switch policy.Status {
	case model.SessionPolicyPending:
		return MarkSessionGrantApplied(db, policy, userOpHash)
	case model.SessionPolicyRevoked:
		// Landed after supersede: entity is on chain; keep revoked, mark applied
		// so onChainTeardownTargets / BuildOnChainRevokeCleanup can see it.
		policy.Grant.AppliedAt = time.Now().UnixMilli()
		policy.Grant.AppliedUserOpHash = userOpHash
		return StoreSessionPolicy(db, policy)
	default:
		return nil
	}
}

// OnChainRevokeCleanup is the owner-executable call that clears an applied
// grant's validation entity and its hooks from the runner.
//
// Production grants are policied: the controller cannot self-uninstall (the
// allowlist's execution hook blocks uninstallValidation — spike R4). The
// proven path is the owner calling this calldata on Target as a plain
// transaction (spike R3) or as a UserOp validated by the owner fallback
// (deferred-uninstall U-B). The gateway does not hold the owner key, so it
// builds the payload and the product surfaces it for the wallet to sign.
type OnChainRevokeCleanup struct {
	EntityID uint32
	Target   common.Address // the runner / smart wallet
	CallData []byte         // uninstallValidation, derived from Grant.InstallCall
	ChainID  int64
}

// BuildOnChainRevokeCleanup derives the uninstallValidation call that reverses
// an applied grant, using the stored InstallCall — never live permission
// structs. See aa.SessionSignerUninstallFromInstall for why that matters.
//
// Works for already-revoked records too: the retained Grant is what makes a
// later cleanup attempt possible after the storage-only revoke. Returns an
// error when teardown was already recorded (TornDownAt) so callers do not
// advertise a no-op cleanup.
func BuildOnChainRevokeCleanup(policy *model.SessionPolicy) (*OnChainRevokeCleanup, error) {
	if policy == nil {
		return nil, fmt.Errorf("no policy to clean up")
	}
	if policy.Runner == nil {
		return nil, fmt.Errorf("policy %s has no runner", policy.ID)
	}
	if policy.Grant == nil || len(policy.Grant.InstallCall) == 0 {
		return nil, fmt.Errorf("policy %s has no InstallCall to derive uninstall from", policy.ID)
	}
	if !policy.Grant.NeedsOnChainCleanup() {
		return nil, fmt.Errorf("policy %s does not need on-chain cleanup (applied=%v torn_down=%v)",
			policy.ID, policy.Grant.Applied(), policy.Grant.TornDown())
	}
	if policy.EntityID == 0 {
		return nil, fmt.Errorf("policy %s has entity 0; the owner fallback cannot be uninstalled this way", policy.ID)
	}
	callData, err := aa.SessionSignerUninstallFromInstall(policy.EntityID, policy.Grant.InstallCall)
	if err != nil {
		return nil, fmt.Errorf("building uninstall for policy %s entity %d: %w", policy.ID, policy.EntityID, err)
	}
	return &OnChainRevokeCleanup{
		EntityID: policy.EntityID,
		Target:   *policy.Runner,
		CallData: callData,
		ChainID:  policy.ChainID,
	}, nil
}

// RevokeSessionGrant removes a grant.
//
// Applied grants: soft-revoke in storage and require owner uninstallValidation.
// The record is retained so BuildOnChainRevokeCleanup can rebuild the payload.
//
// Pending grants with an InstallCall: also retained as revoked (not deleted).
// A pending install may still be in flight; deleting would drop InstallCall and
// leave a true on-chain orphan with no cleanup path. Retaining lets a late
// MarkSessionGrantAppliedByID set AppliedAt without resurrecting usable
// status. onChainCleanupRequired is false until AppliedAt is set.
//
// Pending without InstallCall (should not occur after submit): deleted.
func RevokeSessionGrant(db storage.Storage, policy *model.SessionPolicy) (onChainCleanupRequired bool, err error) {
	if policy == nil {
		return false, fmt.Errorf("no policy to revoke")
	}
	key := SessionPolicyKey(policy.ChainID, *policy.Owner, policy.ID)
	if policy.Grant == nil {
		return false, db.Delete(key)
	}
	if policy.Grant.Applied() {
		policy.Status = model.SessionPolicyRevoked
		// Already verified clear on chain — soft-revoke only, no cleanup payload.
		return policy.Grant.NeedsOnChainCleanup(), StoreSessionPolicy(db, policy)
	}
	if len(policy.Grant.InstallCall) == 0 {
		return false, db.Delete(key)
	}
	// Pending with payload: retain for possible late install / cleanup.
	policy.Status = model.SessionPolicyRevoked
	return false, StoreSessionPolicy(db, policy)
}

// verifyGrantSignature checks that the signature recovers to the owner.
//
// The digest is signed RAW, not EIP-191 wrapped — the account's fallback 1271
// path skips the replay-safe rewrap during validateUserOp, so recovery here
// must match that exactly or a valid grant would be rejected.
func verifyGrantSignature(digest common.Hash, signature []byte, owner common.Address) error {
	sig := make([]byte, len(signature))
	copy(sig, signature)
	// go-ethereum recovers with v in {0,1}; wallets emit 27/28.
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	pub, err := crypto.SigToPub(digest.Bytes(), sig)
	if err != nil {
		return fmt.Errorf("grant signature is malformed: %w", err)
	}
	if got := crypto.PubkeyToAddress(*pub); got != owner {
		return fmt.Errorf("%w: signed by %s, not the wallet owner %s", ErrGrantSignerMismatch, got.Hex(), owner.Hex())
	}
	return nil
}

// ErrGrantSignerMismatch is returned when a grant's signature recovers to an
// address other than the wallet owner. Exposed as a sentinel so the REST
// layer can map it to a 400 with errors.Is rather than matching error text.
var ErrGrantSignerMismatch = errors.New("grant was signed by the wrong key")

// recheckSupersededGrant refuses a replacement whose teardown set moved
// between prepare and submit.
//
// Compares the set the owner signed (prepared.Supersedes) against storage
// candidates that still need cleanup. Dropped-cleared entities must already
// have TornDownAt from prepare's chain read so they no longer appear here.
// If the set moved, prepare again.
func recheckSupersededGrant(db storage.Storage, prepared *PreparedSessionGrant, policy *model.SessionPolicy) error {
	current, err := onChainTeardownTargets(db, policy.ChainID, *policy.Owner, *policy.Runner)
	if err != nil {
		return err
	}
	want := prepared.Supersedes
	if len(want) == 0 && len(current) == 0 {
		return nil
	}
	if len(want) != len(current) {
		return fmt.Errorf(
			"%w: on-chain teardown set changed on %s while signing (prepared %d entities, now %d); prepare again",
			ErrSessionEntityTaken, policy.Runner.Hex(), len(want), len(current))
	}
	// Match by entity id (stable) and policy id. Order may differ if list
	// order shuffled; index by entity.
	byEntity := make(map[uint32]string, len(current))
	for _, p := range current {
		byEntity[p.EntityID] = p.ID
	}
	for _, s := range want {
		id, ok := byEntity[s.EntityID]
		if !ok || !strings.EqualFold(id, s.PolicyID) {
			return fmt.Errorf(
				"%w: prepared teardown of grant %s (entity %d) on %s no longer matches storage; prepare again",
				ErrSessionEntityTaken, s.PolicyID, s.EntityID, policy.Runner.Hex())
		}
	}
	return nil
}

// dropClearedTeardownTargets removes candidates whose validation entity is
// already clear on chain, and records TornDownAt so they stay out of future
// batches. Re-uninstalling a cleared entity reverts the whole deferred
// install+uninstall batch (Sepolia #731 re-audit).
//
// verify nil: return candidates unchanged (offline tests).
// verify error: fail prepare — packing a maybe-doomed batch is worse.
func dropClearedTeardownTargets(
	ctx context.Context,
	db storage.Storage,
	chainID int64,
	owner, runner common.Address,
	candidates []*model.SessionPolicy,
	verify TeardownVerifier,
) ([]*model.SessionPolicy, error) {
	if verify == nil || len(candidates) == 0 {
		return candidates, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the multi-read so prepare cannot hang on a stuck RPC.
	ctx, cancel := context.WithTimeout(ctx, teardownVerifyTimeout)
	defer cancel()

	kept := make([]*model.SessionPolicy, 0, len(candidates))
	for _, p := range candidates {
		cleared, err := verify(ctx, chainID, runner, p.EntityID)
		if err != nil {
			return nil, fmt.Errorf(
				"checking whether entity %d is still installed on %s before packing teardown: %w; prepare again",
				p.EntityID, runner.Hex(), err)
		}
		if cleared {
			// Owner (or a prior replace) already cleared it — sync storage.
			if markErr := MarkEntityTornDown(db, chainID, owner, runner, p.EntityID); markErr != nil {
				return nil, fmt.Errorf("marking entity %d torn down after on-chain clear: %w", p.EntityID, markErr)
			}
			continue
		}
		kept = append(kept, p)
	}
	return kept, nil
}

// TeardownVerifier reads back whether a validation entity is clear on chain.
// Supplied by the aggregator, which owns the chain client; nil in tests and in
// any process without one, where verification is skipped rather than faked.
type TeardownVerifier func(ctx context.Context, chainID int64, account common.Address, entity uint32) (cleared bool, err error)

// VerifySupersededTeardown checks that the entity a replacement was supposed
// to remove is actually gone on chain, and returns an error when the check
// fails or is inconclusive. On success it records TornDownAt on the storage
// policy that owns that entity so the next replace batch does not re-target it.
//
// This is not belt-and-braces. A replace batch that mines proves the operation
// executed, NOT that the uninstall did anything: the account catches a hook
// module's onUninstall revert and strands the state. Sepolia showed the two
// outcomes are indistinguishable from a receipt — 601,275 gas and success=true
// with the entity still holding its signer and its full spend cap, against
// 636,341 gas and a clean teardown. Without this read, the product would
// report an entity revoked on the strength of a transaction that removed
// nothing.
//
// Callers on the send path must not fail the operation on this error: the new
// grant is already installed and usable, and there is nothing to roll back.
// Logging here is what makes a stranded entity visible for explicit cleanup.
//
// superseded must carry ChainID, Runner, EntityID, and Owner (for the storage
// mark). Policy ID may be the new grant's id — lookup is by entity on the runner.
func VerifySupersededTeardown(
	ctx context.Context,
	db storage.Storage,
	verify TeardownVerifier,
	superseded *model.SessionPolicy,
	logger sdklogging.Logger,
) error {
	if verify == nil || superseded == nil || superseded.Runner == nil {
		return nil // no chain client: skip honestly rather than assume success
	}

	cleared, err := verify(ctx, superseded.ChainID, *superseded.Runner, superseded.EntityID)
	if err != nil {
		// Unknown is not cleared. Say so rather than treating the read as success.
		if logger != nil {
			logger.Warn("could not verify that a replaced grant was torn down on chain",
				"policy", superseded.ID, "runner", superseded.Runner.Hex(),
				"entity", superseded.EntityID, "error", err)
		}
		return err
	}
	strandedPolicyID := superseded.ID
	if db != nil && superseded.Owner != nil {
		if id := policyIDForEntity(db, superseded.ChainID, *superseded.Owner, *superseded.Runner, superseded.EntityID); id != "" {
			strandedPolicyID = id
		}
	}
	if !cleared {
		// Mined, and cleared nothing. The entity is still live authority.
		if logger != nil {
			logger.Error("a replaced grant is still installed on chain after its teardown mined",
				"policy", strandedPolicyID, "runner", superseded.Runner.Hex(),
				"entity", superseded.EntityID,
				"hint", "the account catches onUninstall reverts and strands module state; this entity needs an explicit uninstall")
		}
		return fmt.Errorf("entity %d on %s survived its teardown (policy %s)",
			superseded.EntityID, superseded.Runner.Hex(), strandedPolicyID)
	}

	if db != nil && superseded.Owner != nil {
		if markErr := MarkEntityTornDown(db, superseded.ChainID, *superseded.Owner, *superseded.Runner, superseded.EntityID); markErr != nil {
			if logger != nil {
				logger.Warn("entity verified clear on chain but TornDownAt could not be stored",
					"policy", strandedPolicyID, "runner", superseded.Runner.Hex(),
					"entity", superseded.EntityID, "error", markErr)
			}
			// Failed mark is serious: the next prepare will re-read the chain
			// and drop this entity via dropClearedTeardownTargets, so granting
			// still recovers. Log only.
		}
	}
	return nil
}

// MarkEntityTornDown sets TornDownAt on the policy that owns entity on runner.
// Idempotent when already marked. Takes the runner write lock so a concurrent
// MarkSessionGrantAppliedByID cannot lose its AppliedUserOpHash write.
func MarkEntityTornDown(db storage.Storage, chainID int64, owner, runner common.Address, entity uint32) error {
	lock := sessionAuthorityLock(chainID, owner, runner)
	lock.Lock()
	defer lock.Unlock()

	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != runner || p.EntityID != entity || p.Grant == nil {
			continue
		}
		if p.Grant.TornDown() {
			return nil
		}
		p.Grant.TornDownAt = time.Now().UnixMilli()
		return StoreSessionPolicy(db, p)
	}
	return nil // no matching record — nothing to mark
}

// policyIDForEntity returns the storage id of the grant occupying entity on
// runner, or "" if none. Used for diagnostics when the resolver only knows the
// new grant's id.
func policyIDForEntity(db storage.Storage, chainID int64, owner, runner common.Address, entity uint32) string {
	if db == nil {
		return ""
	}
	policies, err := ListSessionPolicies(db, chainID, owner)
	if err != nil {
		return ""
	}
	for _, p := range policies {
		if p.Runner != nil && *p.Runner == runner && p.EntityID == entity {
			return p.ID
		}
	}
	return ""
}
