package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	BudgetChangingCode         = "BUDGET_CHANGING"
	BudgetFeeCeilingCode       = "BUDGET_FEE_CEILING_EXCEEDED"
	BudgetRouteUnsupportedCode = "BUDGET_ROUTE_UNSUPPORTED"
	BudgetAuthorityUnknownCode = "BUDGET_AUTHORITY_UNKNOWN"
	budgetChangeActive         = "active"
	budgetChangeReverted       = "reverted"
	budgetChangeSuperseded     = "superseded"
	budgetDefaultMaxFeeWei     = "5000000000000000" // 0.005 ETH, gateway-configurable.
)

type BudgetBlock struct {
	Number uint64      `json:"number"`
	Hash   common.Hash `json:"hash"`
}
type BudgetHold struct {
	Owner     common.Address `json:"owner"`
	Wallet    common.Address `json:"wallet"`
	ChainID   int64          `json:"chainId"`
	Nonce     string         `json:"nonce"`
	Attempts  []string       `json:"attempts"`
	FromBlock uint64         `json:"fromBlock"`
	Released  bool           `json:"released"`
}
type BudgetPendingChange struct {
	PolicyID      string `json:"policyId"`
	PreparationID string `json:"preparationId"`
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
}

type budgetAtomicWriter interface {
	AtomicWrite(map[string][]byte, [][]byte) error
}

func atomicBudgetWrite(db storage.Storage, values map[string][]byte, deletes ...[]byte) error {
	w, ok := db.(budgetAtomicWriter)
	if !ok {
		return fmt.Errorf("%w: budget authority requires atomic storage", ErrBudgetReadUnavailable)
	}
	if err := w.AtomicWrite(values, deletes); err != nil {
		return fmt.Errorf("%w: durable budget authority update failed: %v", ErrBudgetReadUnavailable, err)
	}
	return nil
}
func budgetHoldPrefix(chain int64, owner, wallet common.Address) []byte {
	return []byte(fmt.Sprintf("budgethold:%d:%s:%s:", chain, strings.ToLower(owner.Hex()), strings.ToLower(wallet.Hex())))
}
func budgetHoldKey(h *BudgetHold) []byte {
	return append(budgetHoldPrefix(h.ChainID, h.Owner, h.Wallet), h.Nonce...)
}
func budgetGateKey(chain int64, owner, wallet common.Address) common.Address {
	return common.BytesToAddress(crypto.Keccak256([]byte(fmt.Sprint(chain)), owner.Bytes(), wallet.Bytes()))
}
func budgetChanging(reason string) error {
	return &BudgetPolicyError{Code: BudgetChangingCode, Detail: reason}
}
func budgetSettled(s string) bool {
	return s == budgetChangeActive || s == budgetChangeReverted || s == budgetChangeSuperseded || s == ownerSubmissionUnsent
}
func (n *Engine) requireBudgetSigning(chain int64) error {
	if n.config == nil || !n.config.WalletBudgetSigningEnabled || !n.config.WalletBudgetPreparationEnabled {
		return budgetHandoffRequired()
	}
	if chain != 11155111 {
		return &BudgetPolicyError{Code: BudgetHandoffRequiredCode, Detail: "Budget signing is enabled only on Sepolia."}
	}
	return nil
}
func (n *Engine) checkBudgetFee(p *BudgetPolicyPreparation) error {
	maxFee := budgetDefaultMaxFeeWei
	if n.config != nil && n.config.WalletBudgetMaxFeeWei != "" {
		maxFee = n.config.WalletBudgetMaxFeeWei
	}
	ceiling, e := parseBudgetAmount(maxFee)
	if e != nil || ceiling.Sign() <= 0 {
		return budgetInputError("invalid budget fee ceiling")
	}
	fee, e := parseBudgetAmount(p.MaxFeeWei)
	if e != nil {
		return e
	}
	if fee.Cmp(ceiling) > 0 {
		return &BudgetPolicyError{Code: BudgetFeeCeilingCode, Detail: "Maximum network fee exceeds the configured budget-operation ceiling.", Details: map[string]string{"neededWei": fee.String(), "availableWei": ceiling.String()}}
	}
	return nil
}
func budgetHoldRows(db storage.Storage, chain int64, owner, wallet common.Address) ([]*BudgetHold, error) {
	rows, e := db.GetByPrefix(budgetHoldPrefix(chain, owner, wallet))
	if e != nil {
		return nil, e
	}
	out := make([]*BudgetHold, 0, len(rows))
	for _, row := range rows {
		var h BudgetHold
		if e = json.Unmarshal(row.Value, &h); e != nil {
			return nil, fmt.Errorf("%w: unreadable wallet hold", ErrBudgetReadUnavailable)
		}
		if !h.Released {
			out = append(out, &h)
		}
	}
	return out, nil
}
func budgetHoldPresent(db storage.Storage, chain int64, owner, wallet common.Address) error {
	hs, e := budgetHoldRows(db, chain, owner, wallet)
	if e != nil {
		return e
	}
	if len(hs) > 0 {
		return budgetChanging("Ava's runs are paused while this wallet's budget change is being settled.")
	}
	return nil
}
func (n *Engine) saveBudgetTransition(p *BudgetPolicyPreparation, h *BudgetHold, policies ...*model.SessionPolicy) error {
	updates := map[string][]byte{}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	updates[string(budgetPreparationKey(p.Owner, p.ID))] = raw
	if h != nil {
		raw, e = json.Marshal(h)
		if e != nil {
			return e
		}
		updates[string(budgetHoldKey(h))] = raw
	}
	for _, policy := range policies {
		raw, e = policy.ToJSON()
		if e != nil {
			return e
		}
		updates[string(SessionPolicyKey(policy.ChainID, *policy.Owner, policy.ID))] = raw
	}
	return atomicBudgetWrite(n.db, updates)
}
func (n *Engine) loadBudgetChange(user *model.User, wallet common.Address, chain int64, id string) (*BudgetPolicyPreparation, error) {
	if e := n.requireOwnedWallet(user, wallet); e != nil {
		return nil, e
	}
	if e := n.requireServedChain(chain); e != nil {
		return nil, e
	}
	if _, e := ulid.ParseStrict(id); e != nil {
		return nil, ErrSessionPolicyNotFound
	}
	raw, e := n.db.GetKey(budgetPreparationKey(user.Address, id))
	if errors.Is(e, badger.ErrKeyNotFound) {
		return nil, ErrSessionPolicyNotFound
	}
	if e != nil {
		return nil, e
	}
	var p BudgetPolicyPreparation
	if e = json.Unmarshal(raw, &p); e != nil {
		return nil, fmt.Errorf("%w: unreadable budget change", ErrBudgetReadUnavailable)
	}
	if p.ID != id || p.Owner != user.Address || p.Wallet != wallet || p.ChainID != chain {
		return nil, ErrSessionPolicyNotFound
	}
	if e := validateStoredBudgetChange(&p); e != nil {
		return nil, e
	}
	return &p, nil
}

//nolint:gocyclo // Linear integrity checks; every optional stored field fails closed before use.
func validateStoredBudgetChange(p *BudgetPolicyPreparation) error {
	switch p.State {
	case ownerSubmissionPrepared, ownerSubmissionSubmitting, ownerSubmissionSubmitted, ownerSubmissionRejected,
		budgetChangeActive, budgetChangeReverted, budgetChangeSuperseded, ownerSubmissionUnsent:
	default:
		return fmt.Errorf("%w: unknown budget change state", ErrBudgetReadUnavailable)
	}
	if p.Operation == nil || p.Policy == nil || p.Policy.Grant == nil || p.Draft == nil || p.Budget == nil || p.Policy.Owner == nil || *p.Policy.Owner != p.Owner || p.Policy.SessionSigner == nil || p.Policy.Runner == nil || *p.Policy.Runner != p.Wallet || p.Policy.ChainID != p.ChainID {
		return fmt.Errorf("%w: invalid budget identity", ErrBudgetReadUnavailable)
	}
	op := (*userop.UserOperationV07)(p.Operation)
	if op.Nonce == nil || p.Policy.Grant.CarrierNonce == nil || op.Nonce.Cmp(p.Policy.Grant.CarrierNonce) != 0 {
		return fmt.Errorf("%w: invalid budget nonce", ErrBudgetReadUnavailable)
	}
	var calls []aa.Call
	var e error
	if p.IsCancel {
		calls = budgetCancelCalls(p.Wallet)
	} else {
		calls, e = budgetPreparationCalls(p.Wallet, p.ApprovalResets, p.Retirements, p.Policy.Grant.InstallCall)
	}
	if e != nil {
		return e
	}
	_, hash, e := validateBudgetOwnerOperation(&preset.PreparedOwnerUserOp{Operation: op}, p.Wallet, p.ChainID, calls)
	if e != nil || hash != p.UserOpHash || preset.MaxOwnerGasCost(op).String() != p.MaxFeeWei {
		return fmt.Errorf("%w: budget review integrity check failed", ErrBudgetReadUnavailable)
	}
	if !p.IsCancel && p.Intent.Discovery == nil {
		return fmt.Errorf("%w: missing approval discovery", ErrBudgetReadUnavailable)
	}
	p.Draft.Permissions.budgetMode = true
	return nil
}
func budgetCancelCalls(wallet common.Address) []aa.Call {
	// Empty calldata/value to the wallet's receive function is an owner no-op.
	return []aa.Call{{Target: wallet, Value: big.NewInt(0), Data: []byte{}}}
}

// SubmitBudgetPolicyPreparation never rebuilds, re-signs or repairs a priced
// operation. The marker, hold and pending policy are committed before the RPC.
//
//nolint:gocyclo // Ordered submit checks with one durable marker; no alternate relay path.
func (n *Engine) SubmitBudgetPolicyPreparation(ctx context.Context, user *model.User, wallet common.Address, chain int64, id string, signature []byte) (result *BudgetPolicyPreparation, submitErr error) {
	if e := n.requireOwnedWallet(user, wallet); e != nil {
		return nil, e
	}
	gate := budgetGateKey(chain, user.Address, wallet)
	release, e := n.budgetWorkflowLocks.acquire(ctx, gate)
	if e != nil {
		return nil, e
	}
	defer release()
	sendRelease, e := n.budgetSendLocks.acquire(ctx, gate)
	if e != nil {
		return nil, e
	}
	defer sendRelease()
	lock := sessionAuthorityLock(chain, user.Address, wallet)
	lock.Lock()
	unlockAuthority := sync.OnceFunc(lock.Unlock)
	defer unlockAuthority()
	p, e := n.loadBudgetChange(user, wallet, chain, id)
	if e != nil {
		return nil, e
	}
	// Only a loaded, locked preparation can establish whether sending started.
	// Missing/failed loads make no claim. A marker write is treated as uncertain
	// even if storage reports an error: never invite a second operation then.
	submissionStarted := p.State != ownerSubmissionPrepared && p.State != ownerSubmissionUnsent
	defer func() {
		if submitErr != nil {
			submitErr = &BudgetSubmitError{Cause: submitErr, SubmissionStarted: submissionStarted}
		}
	}()
	if p.State == ownerSubmissionPrepared {
		if e = n.requireBudgetSigning(chain); e != nil {
			return nil, e
		}
	}
	framed, e := preset.FrameOwnerSignature(p.UserOpHash, signature, user.Address)
	if e != nil {
		return nil, budgetInputError("invalid owner signature")
	}
	if p.State != ownerSubmissionPrepared {
		if e = n.reconcileBudgetWalletLocked(ctx, chain, user.Address, wallet); e != nil {
			return nil, e
		}
		return n.loadBudgetChange(user, wallet, chain, id)
	}
	if p.ExpiresAt <= time.Now().UnixMilli() {
		return nil, ErrSessionPolicyNotFound
	}
	if e = n.checkBudgetFee(p); e != nil {
		return nil, e
	}
	view, e := n.budgetBackend().View(ctx, chain, nil)
	if e != nil {
		return nil, fmt.Errorf("%w: final budget snapshot: %w", ErrBudgetReadUnavailable, e)
	}
	defer view.Close()
	if e = n.checkBudgetSessionFlights(ctx, p, view); e != nil {
		return nil, e
	}
	if e = view.ValidateOwner(ctx, p); e != nil {
		if errors.Is(e, preset.ErrOwnerStateChanged) {
			return nil, n.refuseUnsentBudget(p, newBaseChanged(p.Base))
		}
		var funding *preset.OwnerFundingError
		if errors.As(e, &funding) {
			return nil, budgetFundingRefusal(funding)
		}
		if errors.Is(e, config.ErrEOA7702ExecuteDisabled) || errors.Is(e, config.ErrEOADelegationMissing) || errors.Is(e, config.ErrSMA7702Unavailable) ||
			errors.Is(e, preset.ErrOwnerAccountUnsupported) || errors.Is(e, preset.ErrOwnerConfiguration) {
			return nil, e
		}
		return nil, fmt.Errorf("%w: owner identity, nonce or fee recheck: %w", ErrBudgetReadUnavailable, e)
	}
	if e = view.CheckAuthorities(ctx, p.Owner, p.Wallet); e != nil {
		return nil, e
	}
	hs, e := budgetHoldRows(n.db, chain, user.Address, wallet)
	if e != nil {
		return nil, e
	}
	var h *BudgetHold
	for _, existing := range hs {
		if existing.Nonce != (*userop.UserOperationV07)(p.Operation).Nonce.String() {
			return nil, budgetChanging("An earlier owner nonce is still being settled.")
		}
		h = existing
	}
	if !p.IsCancel {
		if e = n.recheckBudgetDerivation(ctx, p, view); e != nil {
			return nil, n.refuseUnsentBudget(p, e)
		}
	}
	if h == nil {
		h = &BudgetHold{Owner: p.Owner, Wallet: p.Wallet, ChainID: chain, Nonce: (*userop.UserOperationV07)(p.Operation).Nonce.String(), FromBlock: view.Block().Number}
	}
	if p.IsCancel && len(h.Attempts) == 0 {
		return nil, budgetChanging("No held owner operation remains to cancel.")
	}
	if e = view.VerifyCanonical(ctx); e != nil {
		return nil, e
	}
	// Final reads can be slow. The signing window must still be open when
	// committing the first-send marker, not only when submit began.
	if p.ExpiresAt <= time.Now().UnixMilli() {
		return nil, ErrSessionPolicyNotFound
	}
	p.FinalBlock = view.Block()
	p.SubmittedAt = time.Now().UnixMilli()
	p.State = ownerSubmissionSubmitting
	h.Attempts = append(h.Attempts, p.ID)
	var pending []*model.SessionPolicy
	if !p.IsCancel {
		p.Policy.AwaitingBudgetActivation = true
		if p.Draft.StartsNewTotals {
			p.Policy.BudgetStartedAt = p.SubmittedAt
		}
		pending = append(pending, p.Policy)
	}
	submissionStarted = true
	if e = n.saveBudgetTransition(p, h, pending...); e != nil {
		return nil, e
	}
	// The durable hold now refuses legacy policy prepare, submit and revoke
	// for this wallet. Keep the exact runner gates through relay, but let
	// unrelated wallets on this authority shard read their policies while
	// the bundler answers.
	unlockAuthority()
	op := *(*userop.UserOperationV07)(p.Operation)
	op.Signature = framed
	relayCtx, relayCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer relayCancel()
	hash, sendErr := n.budgetBackend().Send(relayCtx, p, &op)
	// Every send error is potentially includable. Never delete the hold.
	if sendErr != nil {
		if ownerBundlerRejected(sendErr) {
			p.State = ownerSubmissionRejected
		}
		p.Reason = "The operation could not be confirmed as accepted. Ava's runs remain paused; replace or cancel it."
	} else if hash != p.UserOpHash {
		p.Reason = "Relay returned a different hash. Ava's runs remain paused while inclusion is checked."
	} else {
		p.State = ownerSubmissionSubmitted
	}
	if e = n.saveBudgetTransition(p, h); e != nil {
		return nil, e
	}
	return p, nil
}

func (n *Engine) refuseUnsentBudget(p *BudgetPolicyPreparation, refusal error) error {
	if !errors.Is(refusal, ErrSessionPolicyBaseChanged) {
		return refusal
	}
	p.State, p.Reason = ownerSubmissionUnsent, refusal.Error()
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	remaining := time.Until(time.UnixMilli(p.ExpiresAt))
	if remaining <= 0 {
		return refusal
	}
	// No marker or relay occurred. A stale unsigned review may still expire;
	// it never joins or releases another attempt's durable hold.
	if err = n.db.SetWithTTL(budgetPreparationKey(p.Owner, p.ID), raw, remaining); err != nil {
		return fmt.Errorf("%w: storing the pre-send refusal", ErrBudgetReadUnavailable)
	}
	return refusal
}

func budgetFundingRefusal(fee *preset.OwnerFundingError) error {
	details := map[string]string{}
	if fee.Required != nil {
		details["neededWei"] = fee.Required.String()
	}
	if fee.Available != nil {
		details["availableWei"] = fee.Available.String()
	}
	return &BudgetPolicyError{Code: BudgetFeeInsufficientCode, Detail: "Add ETH for network fees, then prepare the budget again.", Details: details}
}

//nolint:gocyclo // Rebuild and compare each stored derivation at the same canonical snapshot.
func (n *Engine) recheckBudgetDerivation(ctx context.Context, p *BudgetPolicyPreparation, v budgetChainView) error {
	stale := func() error { return newBaseChanged(p.Base) }
	current, e := activeSessionPolicyLocked(n.db, p.ChainID, p.Owner, p.Wallet)
	if e != nil {
		return e
	}
	a, _ := json.Marshal(current)
	b, _ := json.Marshal(p.Base)
	if string(a) != string(b) {
		return fmt.Errorf("%w (base policy changed)", stale())
	}
	all, e := ListSessionPolicies(n.db, p.ChainID, p.Owner)
	if e != nil {
		return e
	}
	if legacyDeadline := latestOutstandingLegacyGrantDeadline(all, p.Wallet, time.Now()); legacyDeadline > 0 {
		return &BudgetPolicyError{Code: BudgetLegacyGrantPendingCode, Detail: "An outstanding legacy signature can still install.", LegacyGrantDeadline: int64(legacyDeadline) * 1000}
	}
	rem, e := readBudgetRemainder(ctx, current, p.Wallet, v)
	if e != nil {
		return e
	}
	oldRem, _ := json.Marshal(p.Remainder)
	newRem, _ := json.Marshal(rem)
	if string(oldRem) != string(newRem) {
		return fmt.Errorf("%w (remaining amounts changed)", stale())
	}
	tasks, e := n.enabledTasksForRunner(p.Owner, p.Wallet)
	if e != nil {
		return e
	}
	kept, _ := tasksExceptDropped(workflowTasks(tasks), p.Intent.DropTaskIDs)
	rawTasks := make([]json.RawMessage, 0, len(kept))
	for _, task := range kept {
		raw, marshalErr := protojson.Marshal(task)
		if marshalErr != nil {
			return marshalErr
		}
		rawTasks = append(rawTasks, raw)
	}
	canonicalTasks := func(rows []json.RawMessage) string {
		out := make([]string, len(rows))
		for i, row := range rows {
			var value interface{}
			_ = json.Unmarshal(row, &value)
			raw, _ := json.Marshal(value)
			out[i] = string(raw)
		}
		sort.Strings(out)
		return strings.Join(out, "\n")
	}
	if canonicalTasks(rawTasks) != canonicalTasks(p.EnabledTasks) {
		return fmt.Errorf("%w (enabled tasks changed)", stale())
	}
	draft, e := MergeBudgetPolicy(BudgetMergeInput{Current: current, Remainder: &rem, Budget: p.Intent.Budget, Addition: p.Intent.Add, Tasks: kept, ChainID: p.ChainID, Now: time.Now()})
	if e != nil {
		return e
	}
	signer, signerErr := n.sessionSignerAddress()
	if signerErr != nil {
		return signerErr
	}
	draft.Permissions.SessionSigner = &signer
	draft.Permissions.CodeAt = memoizeCodeAt(func(address common.Address) ([]byte, error) {
		code, readErr := v.CodeAt(ctx, address)
		if readErr != nil {
			return nil, fmt.Errorf("%w: native recipient code: %w", ErrBudgetReadUnavailable, readErr)
		}
		return code, nil
	})
	if e = draft.Permissions.Validate(); e != nil {
		return e
	}
	a, _ = json.Marshal(draft)
	b, _ = json.Marshal(p.Draft)
	if string(a) != string(b) {
		return fmt.Errorf("%w (derived permissions changed)", stale())
	}
	if e = refuseUnsupportedBudgetRoutes(p.ChainID, draft.Permissions); e != nil {
		return e
	}
	pairs, e := canonicalBudgetPairs(p.Intent.Discovery.Candidates)
	if e != nil {
		return e
	}
	spenders := budgetResetSpenders(current, *draft, p.Intent.Budget)
	pairs, e = completeBudgetPairs(pairs, current, *draft, spenders)
	if e != nil {
		return e
	}
	reads, resets, e := readBudgetApprovals(ctx, v, p.Wallet, p.ChainID, pairs, spenders)
	if e != nil {
		return e
	}
	a, _ = json.Marshal(reads)
	b, _ = json.Marshal(p.ApprovalReads)
	if string(a) != string(b) {
		return fmt.Errorf("%w (approval resets changed)", stale())
	}
	a, _ = json.Marshal(resets)
	b, _ = json.Marshal(p.ApprovalResets)
	if string(a) != string(b) {
		return stale()
	}
	taken, e := sessionEntityTaken(n.db, p.ChainID, p.Owner, p.Wallet, p.Policy.EntityID)
	if e != nil {
		return e
	}
	if taken {
		return ErrSessionEntityTaken
	}
	if (*userop.UserOperationV07)(p.Operation).Factory == nil {
		occupied, e := aa.EntityRegisteredOnChain(ctx, v, p.Wallet, p.Policy.EntityID)
		if e != nil {
			return fmt.Errorf("%w: entity registration: %w", ErrBudgetReadUnavailable, e)
		}
		if occupied {
			return ErrSessionEntityTaken
		}
	}
	return n.recheckBudgetRetirements(ctx, p, v)
}

func (n *Engine) recheckBudgetRetirements(ctx context.Context, p *BudgetPolicyPreparation, v budgetChainView) error {
	targets, err := onChainTeardownTargets(n.db, p.ChainID, p.Owner, p.Wallet)
	if err != nil {
		return err
	}
	retirements := []*model.SessionPolicy{}
	for _, target := range targets {
		registered, readErr := aa.EntityRegisteredOnChain(ctx, v, p.Wallet, target.EntityID)
		if readErr != nil {
			return fmt.Errorf("%w: retirement registration: %w", ErrBudgetReadUnavailable, readErr)
		}
		if registered {
			retirements = append(retirements, target)
		}
	}
	// Compare the actual teardown bytes, not mutable reporting metadata.
	before, err := budgetPreparationCalls(p.Wallet, p.ApprovalResets, p.Retirements, p.Policy.Grant.InstallCall)
	if err != nil {
		return err
	}
	after, err := budgetPreparationCalls(p.Wallet, p.ApprovalResets, retirements, p.Policy.Grant.InstallCall)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		return fmt.Errorf("%w (retirement set changed)", newBaseChanged(p.Base))
	}
	return nil
}

func readBudgetRemainder(ctx context.Context, p *model.SessionPolicy, wallet common.Address, caller aa.ContractCaller) (GrantRemainder, error) {
	if p == nil || p.Grant == nil || !p.Grant.Applied() {
		return storedGrantRemainder(p)
	}
	r := GrantRemainder{ERC20: map[common.Address]*big.Int{}}
	for _, cap := range policyCaps(p) {
		if cap.Token != nil {
			a, e := aa.ReadERC20SpendLimit(ctx, caller, p.EntityID, *cap.Token, wallet)
			if e != nil {
				return r, fmt.Errorf("%w: remaining token budget: %w", ErrBudgetReadUnavailable, e)
			}
			r.ERC20[*cap.Token] = a
		}
	}
	if p.NativeSpendCap != nil {
		a, e := aa.ReadNativeSpendLimit(ctx, caller, p.EntityID, wallet)
		if e != nil {
			return r, fmt.Errorf("%w: remaining native budget: %w", ErrBudgetReadUnavailable, e)
		}
		r.Native = a
	}
	return r, nil
}

func budgetPending(p *BudgetPolicyPreparation) *BudgetPendingChange {
	return &BudgetPendingChange{PolicyID: p.Policy.ID, PreparationID: p.ID, State: p.State, Reason: p.Reason}
}
func (n *Engine) pendingBudgetChange(user *model.User, chain int64, wallet common.Address) (*BudgetPolicyPreparation, error) {
	hs, e := budgetHoldRows(n.db, chain, user.Address, wallet)
	if e != nil {
		return nil, e
	}
	var latest *BudgetPolicyPreparation
	for _, h := range hs {
		for _, id := range h.Attempts {
			p, e := n.loadBudgetChange(user, wallet, chain, id)
			if e != nil {
				return nil, e
			}
			if !budgetSettled(p.State) && (latest == nil || p.SubmittedAt > latest.SubmittedAt) {
				latest = p
			}
		}
	}
	return latest, nil
}
