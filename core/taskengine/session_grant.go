package taskengine

import (
	"errors"
	"fmt"
	"math/big"
	"time"

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
		big.NewInt(chainID), req.Wallet, carrierNonce, deadline, installCall)
	if err != nil {
		return nil, fmt.Errorf("building the grant digest: %w", err)
	}

	deferredData, err := userop.EncodeDeferredActionData(
		userop.FallbackSignerLocator(), deadline, installCall)
	if err != nil {
		return nil, fmt.Errorf("encoding the deferred action: %w", err)
	}

	owner, wallet, signer := req.Owner, req.Wallet, sessionSigner
	return &PreparedSessionGrant{
		Digest:       digest,
		DeferredData: deferredData,
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
				InstallCall:           installCall,
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

// RevokeSessionGrant removes a grant.
//
// Before the install reaches the chain this is complete on its own: nothing
// was installed, so deleting the record removes the authority. After it has
// applied, this only stops the gateway from using the grant — the module is
// still installed on the account, and clearing it needs uninstallValidation.
// The record is retained in that case so the cleanup payload survives.
func RevokeSessionGrant(db storage.Storage, policy *model.SessionPolicy) (onChainCleanupRequired bool, err error) {
	if policy == nil {
		return false, fmt.Errorf("no policy to revoke")
	}
	key := SessionPolicyKey(policy.ChainID, *policy.Owner, policy.ID)
	if !policy.Grant.Applied() {
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
