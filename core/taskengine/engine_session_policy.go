package taskengine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/oklog/ulid/v2"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// Engine surface for /policies — ownership-gated wrappers over the session
// grant machinery, one per REST operation. The REST layer maps types and
// errors; everything that decides anything lives here.

// Sentinel errors the REST layer maps to specific statuses.
var (
	// ErrWalletNotOwned: the wallet is not the caller's → 404 (existence is
	// not confirmed to non-owners).
	ErrWalletNotOwned = errors.New("wallet is not owned by the caller")
	// ErrSessionEntityTaken: another grant claimed the entity between prepare
	// and submit → 409, prepare again.
	ErrSessionEntityTaken = errors.New("validation entity was taken while the grant was being signed")
	// ErrSessionPolicyNotFound → 404.
	ErrSessionPolicyNotFound = errors.New("session policy not found")
	// ErrSessionWalletNotMAv2: grants only validate on Modular Account v2 runners.
	// A legacy SimpleAccount (or missing factory on the wallet record) must be
	// refused at prepare/submit so clients never collect a doomed owner signature.
	// Studio filters Auto to MA v2; this is the gateway belt-and-braces half.
	ErrSessionWalletNotMAv2 = errors.New("session grants require a Modular Account v2 smart wallet; legacy SimpleAccount cannot host session policies")
	// ErrSessionPolicySupersedeFailed: the new grant is stored and usable, but
	// revoking a previous one failed, so the runner is left ambiguous and
	// execute will refuse it. Distinct from a plain rejection because the
	// submit DID take effect — the client must not retry it (the entity is
	// spent), only clear the named leftovers.
	ErrSessionPolicySupersedeFailed = errors.New("the grant was stored but replacing the previous grant failed")
)

// SessionPolicyInput carries one grant's declared shape between prepare and
// submit. On submit every field is the client's echo of what prepare
// returned; nothing in it is trusted — the grant is recomputed from it and
// the signature decides.
type SessionPolicyInput struct {
	Wallet        common.Address
	ChainID       int64
	AgentLabel    string
	Justification string
	Permissions   SessionPermissions
}

func (in *SessionPolicyInput) validate() error {
	if in.ChainID <= 0 {
		return fmt.Errorf("chain id %d is not valid", in.ChainID)
	}
	if strings.TrimSpace(in.AgentLabel) == "" {
		return fmt.Errorf("agent label is required")
	}
	return in.Permissions.Validate()
}

// sessionSignerAddress is the signer the gateway assigns to new grants —
// today always the controller (see controllerSessionSigner for why that
// interim holds the model).
func (n *Engine) sessionSignerAddress() (common.Address, error) {
	if n.config == nil || n.config.SmartWallet == nil || n.config.SmartWallet.ControllerPrivateKey == nil {
		return common.Address{}, fmt.Errorf("no controller key configured; cannot assign a session signer")
	}
	return crypto.PubkeyToAddress(n.config.SmartWallet.ControllerPrivateKey.PublicKey), nil
}

// requireOwnedWallet gates every policy operation on wallet ownership. The
// grant authorizes execution against the wallet, so acting on another
// owner's wallet — even just listing — is refused as not-found.
func (n *Engine) requireOwnedWallet(user *model.User, wallet common.Address) error {
	if user == nil {
		return fmt.Errorf("no authenticated user")
	}
	owned, err := n.userOwnsWalletOnAnyChain(user, wallet)
	if err != nil {
		return fmt.Errorf("checking wallet ownership: %w", err)
	}
	if !owned {
		return ErrWalletNotOwned
	}
	return nil
}

// requireMAv2SessionWallet refuses prepare/submit when the target runner is
// not Modular Account v2. Session policies install ERC-6900 modules; a v0.6
// SimpleAccount cannot validate them and would only produce opaque AA23 later.
func (n *Engine) requireMAv2SessionWallet(user *model.User, chainID int64, wallet common.Address) error {
	if err := n.requireOwnedWallet(user, wallet); err != nil {
		return err
	}
	rec, err := n.lookupOwnedWalletRecord(user, chainID, wallet)
	if err != nil {
		return fmt.Errorf("loading wallet %s for session grant: %w", wallet.Hex(), err)
	}
	if rec == nil || rec.Factory == nil || *rec.Factory == (common.Address{}) {
		return fmt.Errorf("%w: no factory recorded for %s", ErrSessionWalletNotMAv2, wallet.Hex())
	}
	if aa.ProviderForFactory(*rec.Factory) != aa.ProviderModularAccountV2 {
		return fmt.Errorf("%w: wallet %s uses factory %s",
			ErrSessionWalletNotMAv2, wallet.Hex(), rec.Factory.Hex())
	}
	return nil
}

// lookupOwnedWalletRecord loads the stored smart-wallet row for (owner, wallet),
// preferring the grant's chainID then falling back to any known chain.
// badger.ErrKeyNotFound is "missing" (nil, nil); any other DB error is returned.
func (n *Engine) lookupOwnedWalletRecord(user *model.User, chainID int64, wallet common.Address) (*model.SmartWallet, error) {
	if n.db == nil || user == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	try := func(id int64) (*model.SmartWallet, error) {
		rec, err := GetWallet(n.db, id, user.Address, wallet.Hex())
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return rec, nil
	}
	if chainID > 0 {
		rec, err := try(chainID)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			return rec, nil
		}
	}
	for _, id := range n.knownChainIDs() {
		if id == chainID {
			continue
		}
		rec, err := try(id)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			return rec, nil
		}
	}
	return nil, nil
}

// PrepareSessionPolicy allocates a grant and returns what the owner signs.
// Nothing is stored; an abandoned prepare leaves no state.
func (n *Engine) PrepareSessionPolicy(user *model.User, in SessionPolicyInput) (*PreparedSessionGrant, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if err := n.requireMAv2SessionWallet(user, in.ChainID, in.Wallet); err != nil {
		return nil, err
	}
	signer, err := n.sessionSignerAddress()
	if err != nil {
		return nil, err
	}
	prepared, err := PrepareSessionGrant(n.db, in.ChainID, signer, strings.ToLower(ulid.Make().String()), SessionGrantRequest{
		Owner:         user.Address,
		Wallet:        in.Wallet,
		AgentLabel:    in.AgentLabel,
		Justification: in.Justification,
		ValidUntil:    in.Permissions.ValidUntilMs,
		HooksFor:      in.Permissions.HooksFor,
	})
	if err != nil {
		return nil, err
	}
	attachDeclaredPermissions(prepared.Policy, in.Permissions)
	return prepared, nil
}

// SubmitSessionPolicy verifies the owner's signature over a deterministic
// re-preparation of the grant, stores it as pending, and REPLACES whatever the
// runner had before. superseded lists the grants it revoked to do so.
//
// The client's echo is input, never truth: the install calldata, digest, and
// carrier nonce are all recomputed here from the declared permissions and the
// pinned (policyID, entityID, deadline). A tampered echo either fails
// signature recovery or is a differently-shaped grant that must survive full
// validation on its own merits — there is no path on which stored bytes
// diverge from what the owner's wallet displayed and signed.
//
// Replacement is unconditional rather than a flag the client sets. The send
// path uses exactly one grant per runner and refuses the rest, so a stacked
// second grant is unusable by construction — an opt-in would only mean the
// default leaves the wallet broken, and every client that never learned the
// flag (scripts, partners) would keep recreating the dual-grant failure.
// Scoping is per runner, matching what the send path resolves; it is
// deliberately NOT per capability, which would permit a pair the send path
// then refuses.
//
// The write lock spans the entity re-check, the store, and the supersede.
// Store-then-supersede is the order that matters on a crash: it can leave two
// usable grants, which fails closed and is repaired by granting again, whereas
// superseding first can leave zero — working authority destroyed for a
// replacement that never landed.
func (n *Engine) SubmitSessionPolicy(
	user *model.User,
	in SessionPolicyInput,
	policyID string,
	entityID uint32,
	deadline uint64,
	ownerSignature []byte,
) (policy *model.SessionPolicy, superseded []string, err error) {
	if err := in.validate(); err != nil {
		return nil, nil, err
	}
	if err := n.requireMAv2SessionWallet(user, in.ChainID, in.Wallet); err != nil {
		return nil, nil, err
	}
	if _, err := ulid.ParseStrict(strings.ToUpper(policyID)); err != nil {
		return nil, nil, fmt.Errorf("policy id %q is not a ULID", policyID)
	}
	if deadline <= uint64(time.Now().Unix()) {
		return nil, nil, fmt.Errorf("the signing window (deadline %d) has expired; prepare the grant again", deadline)
	}
	signer, err := n.sessionSignerAddress()
	if err != nil {
		return nil, nil, err
	}

	lock := sessionAuthorityLock(in.ChainID, user.Address, in.Wallet)
	lock.Lock()
	defer lock.Unlock()

	prepared, err := PrepareSessionGrant(n.db, in.ChainID, signer, strings.ToLower(policyID), SessionGrantRequest{
		Owner:         user.Address,
		Wallet:        in.Wallet,
		AgentLabel:    in.AgentLabel,
		Justification: in.Justification,
		ValidUntil:    in.Permissions.ValidUntilMs,
		HooksFor:      in.Permissions.HooksFor,
		Deadline:      deadline,
	})
	if err != nil {
		return nil, nil, err
	}
	if prepared.Policy.EntityID != entityID {
		return nil, nil, fmt.Errorf("%w: prepared entity %d, next free is now %d",
			ErrSessionEntityTaken, entityID, prepared.Policy.EntityID)
	}
	attachDeclaredPermissions(prepared.Policy, in.Permissions)

	stored, err := SubmitSessionGrant(n.db, prepared, ownerSignature)
	if err != nil {
		return nil, nil, err
	}
	superseded, err = supersedeUsablePolicies(n.db, in.ChainID, user.Address, in.Wallet, stored.ID)
	if err != nil {
		// The grant landed; the runner is ambiguous. Say so rather than
		// returning a 201 the send path will refuse.
		return nil, superseded, fmt.Errorf("%w: %w", ErrSessionPolicySupersedeFailed, err)
	}
	return stored, superseded, nil
}

// ListSessionPoliciesForWallet returns the wallet's policies, newest first.
func (n *Engine) ListSessionPoliciesForWallet(user *model.User, chainID int64, wallet common.Address) ([]*model.SessionPolicy, error) {
	if err := n.requireOwnedWallet(user, wallet); err != nil {
		return nil, err
	}
	all, err := ListSessionPolicies(n.db, chainID, user.Address)
	if err != nil {
		return nil, err
	}
	out := make([]*model.SessionPolicy, 0, len(all))
	for _, p := range all {
		if p.Runner != nil && *p.Runner == wallet {
			out = append(out, p)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i] // ULID keys scan oldest-first
	}
	return out, nil
}

// GetSessionPolicyByID returns one policy, or ErrSessionPolicyNotFound.
func (n *Engine) GetSessionPolicyByID(user *model.User, chainID int64, wallet common.Address, policyID string) (*model.SessionPolicy, error) {
	policies, err := n.ListSessionPoliciesForWallet(user, chainID, wallet)
	if err != nil {
		return nil, err
	}
	for _, p := range policies {
		if strings.EqualFold(p.ID, policyID) {
			return p, nil
		}
	}
	return nil, ErrSessionPolicyNotFound
}

// RevokeSessionPolicyByID revokes one policy. deleted reports the pending
// case (record removed outright); cleanupRequired reports that the grant's
// validation is still installed on the account and needs the owner's
// uninstallValidation. When cleanup is required, cleanup carries the
// owner-executable call derived from the retained InstallCall (#717 AC3).
//
// Takes the runner's write lock for the same reason submit does: it changes
// which grant the send path will resolve, and must not interleave with a
// submit deciding the same question.
func (n *Engine) RevokeSessionPolicyByID(user *model.User, chainID int64, wallet common.Address, policyID string) (deleted, cleanupRequired bool, cleanup *OnChainRevokeCleanup, err error) {
	if err := n.requireOwnedWallet(user, wallet); err != nil {
		return false, false, nil, err
	}
	lock := sessionAuthorityLock(chainID, user.Address, wallet)
	lock.Lock()
	defer lock.Unlock()

	policy, err := n.GetSessionPolicyByID(user, chainID, wallet, policyID)
	if err != nil {
		return false, false, nil, err
	}
	cleanupRequired, err = RevokeSessionGrant(n.db, policy)
	if err != nil {
		return false, false, nil, err
	}
	if !cleanupRequired {
		return true, false, nil, nil
	}
	// Storage revoke already landed. Building the call is what closes the
	// "reports into the void" gap: a failure here means the record is
	// revoked but the product has no payload to hand the wallet — still
	// better than refusing the soft revoke, so surface the build error
	// with cleanupRequired so the client knows both halves.
	cleanup, buildErr := BuildOnChainRevokeCleanup(policy)
	if buildErr != nil {
		return false, true, nil, fmt.Errorf("grant revoked off-chain but on-chain cleanup payload could not be built: %w", buildErr)
	}
	return false, true, cleanup, nil
}

// attachDeclaredPermissions records the grant's declared shape on the policy
// for the manage screen and revocation cleanup. Display data — the signed
// truth is Grant.InstallCall.
func attachDeclaredPermissions(policy *model.SessionPolicy, perms SessionPermissions) {
	if policy == nil {
		return
	}
	policy.AllowedActions = perms.AllowedActions
	if perms.SpendCap != nil {
		spendCap := *perms.SpendCap
		spendCap.GrantedCap = spendCap.Amount
		policy.ERC20SpendCap = &spendCap
	}
}
