package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/oklog/ulid/v2"
)

// ReconcileBudgetChanges also runs before session sends, including after a
// restart. Failed reads retain the durable hold regardless of feature flags.
func (n *Engine) ReconcileBudgetChanges(ctx context.Context, chain int64, owner, wallet common.Address) error {
	release, e := n.budgetSendLocks.acquire(ctx, budgetGateKey(chain, owner, wallet))
	if e != nil {
		return e
	}
	defer release()
	lock := sessionAuthorityLock(chain, owner, wallet)
	lock.Lock()
	defer lock.Unlock()
	return n.reconcileBudgetWalletLocked(ctx, chain, owner, wallet)
}
func (n *Engine) reconcileBudgetWalletLocked(ctx context.Context, chain int64, owner, wallet common.Address) error {
	holds, e := budgetHoldRows(n.db, chain, owner, wallet)
	if e != nil {
		return e
	}
	for _, hold := range holds {
		if e = n.reconcileBudgetHold(ctx, hold); e != nil {
			return e
		}
	}
	return nil
}
func (n *Engine) reconcileBudgetHold(ctx context.Context, h *BudgetHold) error {
	observation, err := n.budgetBackend().Observe(ctx, h)
	if err != nil {
		return fmt.Errorf("%w: owner inclusion or nonce observation", ErrBudgetReadUnavailable)
	}
	records, winner, err := n.budgetHoldAttempts(h, observation.Hash)
	if err != nil {
		return err
	}
	nonce, ok := new(big.Int).SetString(h.Nonce, 10)
	if !ok || observation.Nonce == nil {
		return fmt.Errorf("invalid budget nonce observation")
	}
	if observation.Hash == (common.Hash{}) || observation.Nonce.Cmp(nonce) <= 0 {
		return nil
	}
	if winner == nil {
		return n.reconcileExternalBudgetNonce(ctx, h, records, observation)
	}
	winner.InclusionBlock = observation.Block
	winner.TransactionHash = observation.TransactionHash
	if observation.Success && !winner.IsCancel {
		winner.State = ownerSubmissionSubmitted
		if proofErr := n.proveBudgetResets(ctx, winner); proofErr != nil {
			winner.Reason = proofErr.Error()
			if n.logger != nil {
				n.logger.Error("Budget activation awaiting reset proof", "preparation_id", winner.ID, "reason", winner.Reason)
			}
			return n.saveBudgetTransition(winner, h)
		}
	}
	return n.settleBudgetHold(h, records, winner, observation)
}

func (n *Engine) budgetHoldAttempts(h *BudgetHold, hash common.Hash) ([]*BudgetPolicyPreparation, *BudgetPolicyPreparation, error) {
	records := make([]*BudgetPolicyPreparation, 0, len(h.Attempts))
	var winner *BudgetPolicyPreparation
	for _, id := range h.Attempts {
		p, err := n.loadBudgetChange(&model.User{Address: h.Owner}, h.Wallet, h.ChainID, id)
		if err != nil {
			return nil, nil, err
		}
		if p.UserOpHash == hash {
			winner = p
		}
		records = append(records, p)
	}
	return records, winner, nil
}

func putBudgetJSON(updates map[string][]byte, key []byte, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	updates[string(key)] = raw
	return nil
}
func putBudgetPolicy(updates map[string][]byte, p *model.SessionPolicy) error {
	return putBudgetJSON(updates, SessionPolicyKey(p.ChainID, *p.Owner, p.ID), p)
}
func putBudgetChange(updates map[string][]byte, p *BudgetPolicyPreparation) error {
	return putBudgetJSON(updates, budgetPreparationKey(p.Owner, p.ID), p)
}

func (n *Engine) settleBudgetHold(h *BudgetHold, records []*BudgetPolicyPreparation, winner *BudgetPolicyPreparation, observation *budgetObservation) error {
	updates := map[string][]byte{}
	for _, p := range records {
		p.Reason = ""
		p.TransactionHash = observation.TransactionHash
		p.InclusionBlock = observation.Block
		switch {
		case p.ID != winner.ID:
			p.State = budgetChangeSuperseded
		case !observation.Success:
			p.State = budgetChangeReverted
			p.Reason = "The owner operation reverted; the previous permission remains usable."
		default:
			p.State = budgetChangeActive
		}
		if err := putBudgetChange(updates, p); err != nil {
			return err
		}
		if !p.IsCancel {
			p.Policy.Status = model.SessionPolicyRevoked
			p.Policy.AwaitingBudgetActivation = true
			if err := putBudgetPolicy(updates, p.Policy); err != nil {
				return err
			}
		}
	}
	if observation.Success && !winner.IsCancel {
		if err := n.activateBudgetChange(updates, winner); err != nil {
			return err
		}
	}
	return n.releaseBudgetHold(updates, h)
}

func (n *Engine) activateBudgetChange(updates map[string][]byte, winner *BudgetPolicyPreparation) error {
	existing, err := ListSessionPolicies(n.db, winner.ChainID, winner.Owner)
	if err != nil {
		return err
	}
	for _, old := range existing {
		if old.Runner != nil && *old.Runner == winner.Wallet && old.ID != winner.Policy.ID && old.Usable() {
			old.Status = model.SessionPolicyRevoked
			if putErr := putBudgetPolicy(updates, old); putErr != nil {
				return putErr
			}
		}
	}
	winner.Policy.Status = model.SessionPolicyActive
	winner.Policy.AwaitingBudgetActivation = false
	winner.Policy.Grant.AppliedAt = time.Now().UnixMilli()
	winner.Policy.Grant.AppliedUserOpHash = winner.UserOpHash.Hex()
	// Every budget installs AllowlistExecHook. This also normalizes unsigned
	// 3c reviews prepared before the send/activation integration was deployed.
	winner.Policy.Grant.RequiresExecuteUserOp = true
	if err = putBudgetPolicy(updates, winner.Policy); err != nil {
		return err
	}
	return putBudgetChange(updates, winner)
}

func (n *Engine) releaseBudgetHold(updates map[string][]byte, h *BudgetHold) error {
	h.Released = true
	if err := putBudgetJSON(updates, budgetHoldKey(h), h); err != nil {
		return err
	}
	// Authority activation, all attempt states and hold release commit together.
	// A failed commit cannot accidentally let session sends resume.
	return atomicBudgetWrite(n.db, updates)
}

// An owner may consume the nonce directly. That settles the original hashes,
// but it never activates a different, unreviewed budget. Unknown authority must
// be removed before the hold can end; a later check can then finish recovery.
func (n *Engine) reconcileExternalBudgetNonce(ctx context.Context, h *BudgetHold, records []*BudgetPolicyPreparation, observation *budgetObservation) error {
	v, err := n.budgetBackend().View(ctx, h.ChainID, nil)
	if err != nil {
		return err
	}
	defer v.Close()
	if err = n.verifyBudgetInclusion(ctx, h.ChainID, v.Block(), observation.Block); err != nil {
		return err
	}
	if err = v.CheckAuthorities(ctx, h.Owner, h.Wallet); err != nil {
		for _, p := range records {
			p.Reason = err.Error()
			if saveErr := n.saveBudgetTransition(p, h); saveErr != nil {
				return saveErr
			}
		}
		return nil
	}
	updates := map[string][]byte{}
	if err = n.reconcileExternalBudgetPolicies(ctx, h, v, updates); err != nil {
		return err
	}
	for _, p := range records {
		p.State = budgetChangeSuperseded
		p.Reason = "The owner used this nonce outside Ava. This budget was not activated."
		p.TransactionHash = observation.TransactionHash
		p.InclusionBlock = observation.Block
		if putErr := putBudgetChange(updates, p); putErr != nil {
			return putErr
		}
	}
	if err = v.VerifyCanonical(ctx); err != nil {
		return err
	}
	return n.releaseBudgetHold(updates, h)
}

func (n *Engine) reconcileExternalBudgetPolicies(ctx context.Context, h *BudgetHold, v budgetChainView, updates map[string][]byte) error {
	policies, err := ListSessionPolicies(n.db, h.ChainID, h.Owner)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != h.Wallet {
			continue
		}
		registered, readErr := aa.EntityRegisteredOnChain(ctx, v, h.Wallet, p.EntityID)
		if readErr != nil {
			return fmt.Errorf("%w: external nonce retirement read", ErrBudgetReadUnavailable)
		}
		if p.AwaitingBudgetActivation || !registered {
			p.Status = model.SessionPolicyRevoked
			if putErr := putBudgetPolicy(updates, p); putErr != nil {
				return putErr
			}
		}
	}
	return nil
}

func (n *Engine) proveBudgetResets(ctx context.Context, p *BudgetPolicyPreparation) error {
	var number *big.Int
	if !p.ResetRepairRequired {
		number = new(big.Int).SetUint64(p.InclusionBlock.Number)
	}
	view, e := n.budgetBackend().View(ctx, p.ChainID, number)
	if e != nil {
		return fmt.Errorf("reset verification unavailable; Ava's runs remain paused")
	}
	defer view.Close()
	if e = n.verifyBudgetInclusion(ctx, p.ChainID, view.Block(), p.InclusionBlock); e != nil {
		return e
	}
	for _, r := range p.ApprovalResets {
		data := append(crypto.Keccak256([]byte("allowance(address,address)"))[:4], common.LeftPadBytes(p.Wallet.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(r.Spender.Bytes(), 32)...)
		out, e := view.CallContract(ctx, ethereum.CallMsg{To: &r.Token, Data: data}, nil)
		if e != nil || len(out) != 32 {
			return fmt.Errorf("could not confirm reset for token %s, spender %s; Ava's runs remain paused", r.Token.Hex(), r.Spender.Hex())
		}
		if new(big.Int).SetBytes(out).Sign() != 0 {
			p.ResetRepairRequired = true
			return fmt.Errorf("revoke the approval for token %s to spender %s in the approval scan, then check again; Ava's runs remain paused", r.Token.Hex(), r.Spender.Hex())
		}
	}
	return view.VerifyCanonical(ctx)
}

// A canonical older block cannot prove anything about a later inclusion.
// Also bind that inclusion's hash when reading a newer repair/authority view,
// so a reorg cannot turn an orphaned observation into a settled change.
func (n *Engine) verifyBudgetInclusion(ctx context.Context, chain int64, snapshot, inclusion BudgetBlock) error {
	if inclusion.Hash == (common.Hash{}) || snapshot.Number < inclusion.Number {
		return fmt.Errorf("%w: snapshot predates inclusion; Ava's runs remain paused", ErrBudgetReadUnavailable)
	}
	if snapshot.Number == inclusion.Number {
		if snapshot.Hash != inclusion.Hash {
			return fmt.Errorf("%w: inclusion evidence changed; Ava's runs remain paused", ErrBudgetReadUnavailable)
		}
		return nil
	}
	view, err := n.budgetBackend().View(ctx, chain, new(big.Int).SetUint64(inclusion.Number))
	if err != nil {
		return fmt.Errorf("%w: inclusion evidence unavailable; Ava's runs remain paused", ErrBudgetReadUnavailable)
	}
	defer view.Close()
	if view.Block() != inclusion {
		return fmt.Errorf("%w: inclusion evidence changed; Ava's runs remain paused", ErrBudgetReadUnavailable)
	}
	return view.VerifyCanonical(ctx)
}

//nolint:gocyclo // Linear owner-cancel checks; pricing never submits or changes the held nonce.
func (n *Engine) PrepareBudgetCancel(ctx context.Context, user *model.User, wallet common.Address, chain int64, id string) (*BudgetPolicyPreparation, error) {
	if e := n.requireOwnedWallet(user, wallet); e != nil {
		return nil, e
	}
	if e := n.requireBudgetSigning(chain); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	unlockPreparation, e := n.budgetPreparationLocks.acquire(ctx, user.Address)
	if e != nil {
		return nil, e
	}
	defer unlockPreparation()
	if e = n.checkBudgetPreparationCap(user.Address, time.Now()); e != nil {
		return nil, e
	}
	release, e := n.budgetSendLocks.acquire(ctx, budgetGateKey(chain, user.Address, wallet))
	if e != nil {
		return nil, e
	}
	defer release()
	lock := sessionAuthorityLock(chain, user.Address, wallet)
	lock.Lock()
	defer lock.Unlock()
	if e = n.reconcileBudgetWalletLocked(ctx, chain, user.Address, wallet); e != nil {
		return nil, e
	}
	parent, e := n.loadBudgetChange(user, wallet, chain, id)
	if e != nil {
		return nil, e
	}
	if budgetSettled(parent.State) {
		return nil, budgetChanging("This change has already settled.")
	}
	hs, e := budgetHoldRows(n.db, chain, user.Address, wallet)
	if e != nil {
		return nil, e
	}
	var found bool
	for _, h := range hs {
		for _, attempt := range h.Attempts {
			if attempt == id {
				found = true
			}
		}
	}
	if !found {
		return nil, budgetChanging("No owner operation is in flight for this change.")
	}
	sw := *n.ResolveSmartWalletConfig(chain)
	sw.DisableGasSponsorship = true
	sw.AlchemyPaymasterPolicyID = ""
	priced, e := n.ownerBackend().Prepare(ctx, &sw, user.Address, wallet, parent.Salt, preset.OwnerBatch{Calls: budgetCancelCalls(wallet)}, n.logger)
	if e != nil {
		var funding *preset.OwnerFundingError
		if errors.As(e, &funding) {
			return nil, budgetFundingRefusal(funding)
		}
		return nil, e
	}
	op, hash, e := validateBudgetOwnerOperation(priced, wallet, chain, budgetCancelCalls(wallet))
	if e != nil {
		return nil, e
	}
	if op.Nonce.Cmp((*userop.UserOperationV07)(parent.Operation).Nonce) != 0 {
		return nil, budgetChanging("The held owner nonce has already been consumed.")
	}
	raw, e := json.Marshal(parent)
	if e != nil {
		return nil, e
	}
	var p BudgetPolicyPreparation
	if e = json.Unmarshal(raw, &p); e != nil {
		return nil, e
	}
	p.ID = stringsLowerULID()
	p.IsCancel = true
	p.ApprovalResets = []BudgetApprovalReset{}
	p.ApprovalReads = nil
	p.Retirements = nil
	p.ResetRepairRequired = false
	p.SubmittedAt = 0
	p.FinalBlock = BudgetBlock{}
	p.InclusionBlock = BudgetBlock{}
	p.TransactionHash = common.Hash{}
	p.State = ownerSubmissionPrepared
	p.Operation = (*storedOwnerOperation)(op)
	p.UserOpHash = hash
	p.MaxFeeWei = preset.MaxOwnerGasCost(op).String()
	p.Identity = priced.Identity
	p.Reason = ""
	p.ExpiresAt = time.Now().Add(ownerPreparationTTL).UnixMilli()
	p.CreatedAt = time.Now().UnixMilli()
	p.Policy.Grant.CarrierNonce = new(big.Int).Set(op.Nonce)
	if e = n.checkBudgetFee(&p); e != nil {
		return nil, e
	}
	raw, e = json.Marshal(&p)
	if e != nil {
		return nil, e
	}
	if e = n.db.SetWithTTL(budgetPreparationKey(p.Owner, p.ID), raw, ownerPreparationTTL); e != nil {
		return nil, e
	}
	return &p, nil
}
func stringsLowerULID() string { return strings.ToLower(ulid.Make().String()) }
