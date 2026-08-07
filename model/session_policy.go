package model

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// SessionPolicy is one grant of execution authority on one smart wallet: the
// record behind the grant screen, and the thing the send path resolves before
// it can sign anything.
//
// Shape and rationale: avs-infra Smart_Wallet_MA_v2_Spend_Policy.md §7.4a.
// Two things about it are easy to get wrong:
//
//   - The grant is a BEARER authorization for exactly its committed calldata.
//     It authorizes execution against the user's wallet, so it gets the same
//     handling as a secret: not logged, not returned in list responses,
//     deleted on revoke.
//
//   - InstallCall is stored rather than re-derived. Re-building it from the
//     policy fields at execution time would let a later code change silently
//     alter what the owner signed — the signature would then fail, but only
//     on chain, at execution, for a user who already consented.
type SessionPolicy struct {
	ID      string          `json:"id"`
	Owner   *common.Address `json:"owner"`
	Runner  *common.Address `json:"runner"` // the smart wallet this grants on
	ChainID int64           `json:"chain_id"`

	// EntityID is the validation entity this grant occupies on the account.
	// Unique per ACCOUNT, allocated when the policy is created, never 0 —
	// entity 0 is the owner's fallback signer.
	EntityID uint32 `json:"entity_id"`

	// SessionSigner is the key the gateway signs with as that entity.
	SessionSigner *common.Address `json:"session_signer"`

	AgentLabel    string `json:"agent_label,omitempty"`
	Justification string `json:"justification,omitempty"`

	// AllowedActions and ERC20SpendCap are the grant's declared permissions
	// (master §7.2 P1/P2), stored for the manage screen and for rebuilding
	// the module-cleanup payload at revocation. Display/rebuild data only:
	// what the owner actually authorized is Grant.InstallCall, and these must
	// never be re-encoded into it after signing.
	AllowedActions []AllowedAction `json:"allowed_actions,omitempty"`
	ERC20SpendCap  *ERC20SpendCap  `json:"erc20_spend_cap,omitempty"`

	// Grant is the owner's authorization. Absent once applied is not a valid
	// state: it is retained so revocation can reproduce the module cleanup
	// payload, which is the same (entityId, inputs) tuple as the install.
	Grant *SessionGrantAuthorization `json:"grant,omitempty"`

	// ValidUntil is the grant's own lifetime (the TimeRangeModule hook),
	// distinct from Grant.Deadline which only bounds the signing→first-use
	// window. Unix milliseconds.
	ValidUntil int64 `json:"valid_until,omitempty"`

	Status    SessionPolicyStatus `json:"status"`
	CreatedAt int64               `json:"created_at"`
}

// AllowedAction is one contract the grant's agent may call, scoped to
// function selectors (master §7.2 P1).
type AllowedAction struct {
	Target    *common.Address `json:"target"`
	Selectors []string        `json:"selectors"` // 0x-prefixed 4-byte selectors
}

// ERC20SpendCap is the grant's cumulative token cap (master §7.2 P2).
// GrantedCap preserves the original total for "used X of Y" rendering — the
// on-chain module only exposes the remainder.
type ERC20SpendCap struct {
	Token      *common.Address `json:"token"`
	Amount     string          `json:"amount"`      // smallest unit, decimal string
	GrantedCap string          `json:"granted_cap"` // == Amount at grant time
}

// SessionGrantAuthorization is the owner's signed deferred action.
type SessionGrantAuthorization struct {
	// InstallCall is the exact deferred calldata the signature commits to, and
	// must be re-encoded verbatim — the resolver rebuilds the signed payload
	// from it, so any difference invalidates the owner's signature.
	//
	// Usually installValidation. When the grant REPLACES one that was already
	// installed on chain, it is an executeBatch carrying that install AND the
	// prior entity's uninstall, so a single owner signature makes the
	// replacement true on both sides (#717). Readers that want the install
	// alone must unwrap the batch — see aa.InstallValidationWithin.
	InstallCall []byte `json:"install_call"`

	// CarrierNonce is the FULL 256-bit nonce of the operation that will carry
	// the action. The digest commits to it, so the carrying operation must use
	// exactly this value. Computable at grant time only because a fresh entity
	// is a fresh nonce key whose sequence is zero — it cannot be recomputed
	// later.
	CarrierNonce *big.Int `json:"carrier_nonce"`

	// Deadline bounds the window between signing and first execution (uint48
	// unix seconds). Not the grant's lifetime — see SessionPolicy.ValidUntil.
	Deadline uint64 `json:"deadline"`

	OwnerSignature []byte `json:"owner_signature"`

	// AppliedAt is the replay marker. The payload authorizes exactly its
	// committed calldata; attaching it twice must be impossible, and a
	// non-zero AppliedAt is the check. Unix milliseconds.
	AppliedAt         int64  `json:"applied_at,omitempty"`
	AppliedUserOpHash string `json:"applied_userop_hash,omitempty"`

	// RequiresExecuteUserOp is true when the grant installs an EXECUTION hook
	// (an ERC-20 spend cap does; a time range does not). Every operation under
	// such a grant must be executeUserOp-wrapped, including the one carrying
	// this install. Recorded at grant time so the send path does not have to
	// re-parse the install calldata to find out.
	RequiresExecuteUserOp bool `json:"requires_execute_user_op,omitempty"`
}

// SessionPolicyStatus tracks how far a grant has got.
type SessionPolicyStatus string

const (
	// SessionPolicyPending is signed and stored but not yet on chain.
	// Revoking is free here: delete the record, nothing was installed.
	SessionPolicyPending SessionPolicyStatus = "pending"
	// SessionPolicyActive means the install applied on chain. Revoking now
	// needs uninstallValidation.
	SessionPolicyActive SessionPolicyStatus = "active"
	// SessionPolicyRevoked is retained for audit; it grants nothing.
	SessionPolicyRevoked SessionPolicyStatus = "revoked"
)

// Applied reports whether the deferred action has already been consumed.
func (g *SessionGrantAuthorization) Applied() bool {
	return g != nil && g.AppliedAt > 0
}

// Usable reports whether this policy can authorize an operation right now.
func (p *SessionPolicy) Usable() bool {
	if p == nil || p.Grant == nil {
		return false
	}
	return p.Status == SessionPolicyPending || p.Status == SessionPolicyActive
}

// Validate rejects a record that cannot produce a working authorization.
func (p *SessionPolicy) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("session policy has no id")
	}
	if p.Owner == nil || *p.Owner == (common.Address{}) {
		return fmt.Errorf("session policy %s has no owner", p.ID)
	}
	if p.Runner == nil || *p.Runner == (common.Address{}) {
		return fmt.Errorf("session policy %s has no runner", p.ID)
	}
	if p.EntityID == 0 {
		return fmt.Errorf("session policy %s uses entity 0, which is the owner's fallback signer", p.ID)
	}
	if p.SessionSigner == nil || *p.SessionSigner == (common.Address{}) {
		return fmt.Errorf("session policy %s has no session signer", p.ID)
	}
	if p.Grant == nil {
		return fmt.Errorf("session policy %s has no grant", p.ID)
	}
	if len(p.Grant.InstallCall) == 0 {
		return fmt.Errorf("session policy %s has no install calldata", p.ID)
	}
	if len(p.Grant.OwnerSignature) == 0 {
		return fmt.Errorf("session policy %s has no owner signature", p.ID)
	}
	if p.Grant.CarrierNonce == nil {
		return fmt.Errorf("session policy %s has no carrier nonce; it cannot be recomputed", p.ID)
	}
	return nil
}

func (p *SessionPolicy) ToJSON() ([]byte, error) { return json.Marshal(p) }

func (p *SessionPolicy) FromStorageData(body []byte) error {
	return json.Unmarshal(body, p)
}
