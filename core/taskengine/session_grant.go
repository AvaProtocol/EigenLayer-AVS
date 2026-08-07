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

	// Supersedes is the grant this one replaces ON CHAIN, when the deferred
	// action carries its uninstall. Empty when there was nothing installed to
	// remove — a first grant, or a prior grant still pending, which has no
	// on-chain entity.
	//
	// Echoed to Submit so the write can re-check that the grant it is about to
	// remove is still the one the owner signed away. The signature commits to
	// a specific entity's teardown; if a different grant occupies that entity
	// by then, the payload is stale in the same way a taken entity is.
	Supersedes *SupersededGrant
}

// SupersededGrant identifies the on-chain grant a prepared replacement removes.
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

	// A grant REPLACES the runner's previous one, and #716 made that true in
	// storage. Here it becomes true on chain: when a prior grant is actually
	// installed, the owner's one signature carries its uninstall alongside the
	// new install, so the account ends up holding exactly one entity rather
	// than one per grant ever issued.
	//
	// Proven on Sepolia (#717): the batch composes with the NEW entity as the
	// outer validation and needs no second owner signature. §3.6's refusal was
	// specific to removing the validation that was validating the operation —
	// a replace removes a different one.
	deferredCall := installCall
	superseded, ambiguous, err := supersededOnChainGrant(db, chainID, req.Owner, req.Wallet)
	if err != nil {
		return nil, err
	}
	// ambiguous: more than one grant is installed. Install only, so granting
	// still repairs the wallet in storage (#716) instead of refusing the very
	// wallets that need repairing. The leftovers stay on chain.
	if superseded != nil && !ambiguous {
		uninstallCall, err := aa.SessionSignerUninstallFromInstall(
			superseded.EntityID, superseded.Grant.InstallCall)
		if err != nil {
			return nil, fmt.Errorf("building the teardown for the grant being replaced: %w", err)
		}
		// Install FIRST: the operation carrying this is validated by the new
		// entity, which has to exist by the time the outer validation runs.
		deferredCall, err = aa.PackExecuteBatchMAv2([]aa.Call{
			{Target: req.Wallet, Value: big.NewInt(0), Data: installCall},
			{Target: req.Wallet, Value: big.NewInt(0), Data: uninstallCall},
		})
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
	var supersedes *SupersededGrant
	if superseded != nil {
		supersedes = &SupersededGrant{PolicyID: superseded.ID, EntityID: superseded.EntityID}
	}
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
// confirms, and by then the record may have moved. It re-reads and only
// transitions pending → active:
//
//   - Record gone: the grant was revoked (pending revokes delete outright)
//     while its install was in flight. Nothing to update — though if the
//     operation mined, the entity now exists on-chain with no record behind
//     it, an orphan only the owner's uninstallValidation can clear. Reported
//     as nil because there is no state left to make consistent.
//   - Status not pending: never overwrite. Re-storing a stale in-memory
//     policy here could resurrect a revoked grant as active, which is why
//     this exists instead of handing the callback the old pointer.
//
// The re-read and the write happen under the runner's write lock, because
// re-reading alone is not enough: a submit that supersedes this grant between
// the read and the store would be overwritten by the store, resurrecting a
// grant the user just replaced and leaving the runner with two usable ones.
// Replacement is routine, so that window is reachable in normal use.
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
		return nil // no record — revoked while in flight; see above
	}
	if err != nil {
		return fmt.Errorf("reading session policy %s: %w", policyID, err)
	}
	policy := &model.SessionPolicy{}
	if err := policy.FromStorageData(raw); err != nil {
		return fmt.Errorf("session policy %s is unreadable: %w", policyID, err)
	}
	if policy.Status != model.SessionPolicyPending || policy.Grant.Applied() {
		return nil
	}
	return MarkSessionGrantApplied(db, policy, userOpHash)
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
// later cleanup attempt possible after the storage-only revoke.
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
// Before the install reaches the chain this is complete on its own: nothing
// was installed, so deleting the record removes the authority. After it has
// applied, this only stops the gateway from using the grant — the module is
// still installed on the account, and clearing it needs uninstallValidation.
// The record is retained in that case so the cleanup payload survives;
// BuildOnChainRevokeCleanup rebuilds it from Grant.InstallCall.
func RevokeSessionGrant(db storage.Storage, policy *model.SessionPolicy) (onChainCleanupRequired bool, err error) {
	if policy == nil {
		return false, fmt.Errorf("no policy to revoke")
	}
	key := SessionPolicyKey(policy.ChainID, *policy.Owner, policy.ID)
	if policy.Grant == nil || !policy.Grant.Applied() {
		return false, db.Delete(key)
	}
	policy.Status = model.SessionPolicyRevoked
	return true, StoreSessionPolicy(db, policy)
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

// recheckSupersededGrant refuses a replacement whose target moved between
// prepare and submit.
//
// Three ways it can move, all treated alike: the grant was revoked outright,
// it was itself replaced by another submit, or a grant now occupies the entity
// that is different from the one prepared against. In every case the owner
// signed away a specific entity's authority and that is no longer what the
// payload would do, so it is refused the way a taken entity is — prepare
// again, which rebuilds the batch against whatever is actually installed.
func recheckSupersededGrant(db storage.Storage, prepared *PreparedSessionGrant, policy *model.SessionPolicy) error {
	current, ambiguous, err := supersededOnChainGrant(db, policy.ChainID, *policy.Owner, *policy.Runner)
	if err != nil {
		return err
	}
	if ambiguous {
		// Same reasoning as prepare: a stacked wallet is repaired, not refused.
		// The payload installs only, so there is no teardown target to re-check.
		return nil
	}

	switch {
	case prepared.Supersedes == nil && current == nil:
		return nil // nothing to replace then, nothing now
	case prepared.Supersedes == nil && current != nil:
		return fmt.Errorf(
			"%w: grant %s (entity %d) was installed on %s while this one was being signed, "+
				"so this payload would leave it in place; prepare again to replace it",
			ErrSessionEntityTaken, current.ID, current.EntityID, policy.Runner.Hex())
	case prepared.Supersedes != nil && current == nil:
		return fmt.Errorf(
			"%w: the grant this replaces (%s, entity %d) is no longer installed on %s; "+
				"prepare again",
			ErrSessionEntityTaken, prepared.Supersedes.PolicyID, prepared.Supersedes.EntityID,
			policy.Runner.Hex())
	case !strings.EqualFold(prepared.Supersedes.PolicyID, current.ID) ||
		prepared.Supersedes.EntityID != current.EntityID:
		return fmt.Errorf(
			"%w: this replaces grant %s (entity %d) but %s now holds %s (entity %d); prepare again",
			ErrSessionEntityTaken, prepared.Supersedes.PolicyID, prepared.Supersedes.EntityID,
			policy.Runner.Hex(), current.ID, current.EntityID)
	}
	return nil
}

// TeardownVerifier reads back whether a validation entity is clear on chain.
// Supplied by the aggregator, which owns the chain client; nil in tests and in
// any process without one, where verification is skipped rather than faked.
type TeardownVerifier func(ctx context.Context, chainID int64, account common.Address, entity uint32) (cleared bool, err error)

// VerifySupersededTeardown checks that the entity a replacement was supposed
// to remove is actually gone on chain, and returns an error when the check
// fails or is inconclusive. It logs that outcome; it does not write storage.
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
// Logging here is what makes a stranded entity visible until the #717 sweep
// can mark and clear leftovers. db is reserved for that mark and is unused
// today.
func VerifySupersededTeardown(
	ctx context.Context,
	db storage.Storage,
	verify TeardownVerifier,
	superseded *model.SessionPolicy,
	logger sdklogging.Logger,
) error {
	_ = db // reserved for marking stranded entities once the #717 sweep lands
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
	if cleared {
		return nil
	}

	// Mined, and cleared nothing. The entity is still live authority on the
	// account even though storage has it revoked.
	if logger != nil {
		logger.Error("a replaced grant is still installed on chain after its teardown mined",
			"policy", superseded.ID, "runner", superseded.Runner.Hex(),
			"entity", superseded.EntityID,
			"hint", "the account catches onUninstall reverts and strands module state; this entity needs an explicit uninstall")
	}
	return fmt.Errorf("entity %d on %s survived its teardown (policy %s)",
		superseded.EntityID, superseded.Runner.Hex(), superseded.ID)
}
