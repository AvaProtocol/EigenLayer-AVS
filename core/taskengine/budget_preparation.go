package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

const BudgetFeeInsufficientCode = "BUDGET_FEE_INSUFFICIENT_ETH"
const BudgetLegacyGrantPendingCode = "BUDGET_LEGACY_GRANT_PENDING"
const BudgetPreparationLimitCode = "BUDGET_PREPARATION_LIMIT"

type ownerBudgetPrepareFunc func(context.Context, *config.SmartWalletConfig, common.Address, common.Address, *big.Int, preset.OwnerBatch, logger.Logger) (*preset.PreparedOwnerUserOp, error)

// BudgetPolicyPreparation is the stored review, not a usable permission.
// Its operation is priced and unsigned; this slice has no relay path.
type BudgetPolicyPreparation struct {
	ID             string                      `json:"id"`
	Owner          common.Address              `json:"owner"`
	Wallet         common.Address              `json:"wallet"`
	ChainID        int64                       `json:"chainId"`
	CreatedAt      int64                       `json:"createdAt"`
	ExpiresAt      int64                       `json:"expiresAt"`
	State          string                      `json:"state"`
	Operation      *storedOwnerOperation       `json:"operation"`
	UserOpHash     common.Hash                 `json:"userOpHash"`
	MaxFeeWei      string                      `json:"maxFeeWei"`
	Identity       preset.OwnerAccountIdentity `json:"identity"`
	Salt           *big.Int                    `json:"salt"`
	Intent         budgetPreparationIntent     `json:"intent"`
	Base           *model.SessionPolicy        `json:"base,omitempty"`
	Remainder      GrantRemainder              `json:"remainder"`
	EnabledTasks   []json.RawMessage           `json:"enabledTasks"`
	Draft          *BudgetPolicyDraft          `json:"draft"`
	Policy         *model.SessionPolicy        `json:"policy"`
	ApprovalReads  []BudgetApprovalReset       `json:"approvalReads"`
	ApprovalResets []BudgetApprovalReset       `json:"approvalResets"`
	Retirements    []*model.SessionPolicy      `json:"retirements"`
	Budget         *WalletBudget               `json:"budget"`
}

type budgetPreparationIntent struct {
	Budget        *WalletBudgetInput       `json:"budget,omitempty"`
	Add           *PolicyAddition          `json:"add,omitempty"`
	BasePolicyID  *string                  `json:"basePolicyId,omitempty"`
	DropTaskIDs   []string                 `json:"dropTaskIds,omitempty"`
	AgentLabel    string                   `json:"agentLabel"`
	Justification string                   `json:"justification,omitempty"`
	Discovery     *BudgetApprovalDiscovery `json:"approvalDiscovery"`
}

func budgetPreparationPrefix(owner common.Address) []byte {
	return []byte("budgetprep:" + strings.ToLower(owner.Hex()) + ":")
}
func budgetPreparationKey(owner common.Address, id string) []byte {
	return append(budgetPreparationPrefix(owner), id...)
}

//nolint:gocyclo // Linear fail-closed preparation: no authority is written between its validation stages.
func (n *Engine) prepareBudgetPolicy(ctx context.Context, user *model.User, in SessionPolicyInput) (*PreparedSessionGrant, error) {
	if err := n.requireServedChain(in.ChainID); err != nil {
		return nil, err
	}
	if err := n.requireMAv2SessionWallet(user, in.ChainID, in.Wallet); err != nil {
		return nil, err
	}
	if in.Budget != nil {
		if err := in.Budget.validate(time.Now()); err != nil {
			return nil, err
		}
	}
	if n.config == nil || !n.config.WalletBudgetPreparationEnabled {
		return nil, budgetHandoffRequired()
	}
	if strings.TrimSpace(in.AgentLabel) == "" || len(in.AgentLabel) > 120 {
		return nil, budgetInputError("agentLabel is required and must fit 120 characters")
	}
	var serviceSigner common.Address
	if common.IsHexAddress(n.config.WalletBudgetDiscoverySigner) {
		serviceSigner = common.HexToAddress(n.config.WalletBudgetDiscoverySigner)
	}
	now := time.Now()
	pairs, err := verifyBudgetDiscovery(in.ApprovalDiscovery, user.Address, in.Wallet, serviceSigner, in.ChainID, now)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// Bound unsigned records per owner atomically, without holding up session
	// operations during RPC pricing. No wallet hold or authority write occurs.
	lock := n.executionMutex("budget-prepare:" + user.Address.Hex())
	lock.Lock()
	defer lock.Unlock()
	if err = n.checkBudgetPreparationCap(user.Address, now); err != nil {
		return nil, err
	}
	current, err := ActiveSessionPolicyForWallet(n.db, in.ChainID, user.Address, in.Wallet)
	if err != nil {
		return nil, err
	}
	if in.BasePolicyID != nil && !policyIDMatches(*in.BasePolicyID, current) {
		return nil, newBaseChanged(current)
	}
	all, err := ListSessionPolicies(n.db, in.ChainID, user.Address)
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		// A deferred signature uses its own session nonce, so consuming the
		// owner nonce cannot invalidate a not-yet-carried legacy install.
		if p.Runner != nil && *p.Runner == in.Wallet && !isWalletBudget(p) && p.Grant != nil && !p.Grant.Applied() && len(p.Grant.OwnerSignature) > 0 && p.Grant.Deadline >= uint64(now.Unix()) {
			return nil, &BudgetPolicyError{Code: BudgetLegacyGrantPendingCode, Detail: "An earlier signed permission may still install. Wait for its signing window to end before migrating to a budget."}
		}
	}
	rem, err := n.resolveGrantRemainder(ctx, current, in.Wallet)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBudgetReadUnavailable, err)
	}
	tasks, err := n.enabledTasksForRunner(user.Address, in.Wallet)
	if err != nil {
		return nil, err
	}
	kept, dropped := tasksExceptDropped(workflowTasks(tasks), in.DropTaskIDs)
	draft, err := MergeBudgetPolicy(BudgetMergeInput{Current: current, Remainder: &rem, Budget: in.Budget, Addition: in.Addition, Tasks: kept, ChainID: in.ChainID, Now: now})
	if err != nil {
		return nil, err
	}
	if err = n.bindNativeRecipientChecks(in.ChainID, &draft.Permissions); err != nil {
		return nil, err
	}
	if err = draft.Permissions.Validate(); err != nil {
		return nil, budgetInputError("invalid resulting budget: %v", err)
	}
	if !budgetDiscoveryCovers(in.ApprovalDiscovery, draft.Permissions) {
		return nil, &BudgetPolicyError{Code: BudgetDiscoveryRequiredCode, Detail: "Approval discovery does not cover every resulting callable target. Discover the complete permission before preparing again."}
	}
	entity, err := NextFreeSessionEntityID(ctx, n.db, in.ChainID, user.Address, in.Wallet, n.occupancyFor(in.ChainID))
	if err != nil {
		return nil, err
	}
	signer, err := n.sessionSignerAddress()
	if err != nil {
		return nil, err
	}
	hooks, err := draft.Permissions.HooksFor(entity)
	if err != nil {
		return nil, err
	}
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: entity, Signer: signer, Hooks: hooks, Global: true})
	if err != nil {
		return nil, err
	}
	spenders := budgetResetSpenders(current, *draft, in.Budget)
	pairs, err = completeBudgetPairs(pairs, current, *draft, spenders)
	if err != nil {
		return nil, err
	}
	var caller aa.ContractCaller
	if len(pairs) > 0 {
		caller, err = n.spendLimitCaller(in.ChainID)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBudgetReadUnavailable, err)
		}
	}
	reads, resets, err := readBudgetApprovals(ctx, caller, in.Wallet, in.ChainID, pairs, spenders)
	if err != nil {
		return nil, err
	}
	calls := make([]aa.Call, 0, len(resets)+2)
	for _, row := range resets {
		data := append(common.FromHex(selectorApprove), common.LeftPadBytes(row.Spender.Bytes(), 32)...)
		data = append(data, make([]byte, 32)...)
		calls = append(calls, aa.Call{Target: row.Token, Value: big.NewInt(0), Data: data})
	}
	// Include still-registered historical entities too. A prior off-chain revoke
	// must not leave its live authority behind the new budget. Never truncate.
	targets, err := onChainTeardownTargets(n.db, in.ChainID, user.Address, in.Wallet)
	if err != nil {
		return nil, err
	}
	retirements := []*model.SessionPolicy{}
	for _, target := range targets {
		cleared, checkErr := n.teardownVerifier()(ctx, in.ChainID, in.Wallet, target.EntityID)
		if checkErr != nil {
			return nil, fmt.Errorf("%w: retirement registration is unavailable", ErrBudgetReadUnavailable)
		}
		if !cleared {
			uninstall, uninstallErr := aa.SessionSignerUninstallFromInstall(target.EntityID, target.Grant.InstallCall)
			if uninstallErr != nil {
				return nil, uninstallErr
			}
			calls = append(calls, aa.Call{Target: in.Wallet, Value: big.NewInt(0), Data: uninstall})
			retirements = append(retirements, target)
		}
	}
	calls = append(calls, aa.Call{Target: in.Wallet, Value: big.NewInt(0), Data: install})
	// The existing owner batch bounds the complete atomic batch. Never split
	// resets and installation across separately signed operations.
	if len(calls) > ownerBatchMaxCalls {
		return nil, budgetInputError("the atomic budget batch exceeds %d calls", ownerBatchMaxCalls)
	}
	wallet, err := n.lookupOwnedWalletRecord(user, in.ChainID, in.Wallet)
	if err != nil {
		return nil, err
	}
	sw := n.ResolveSmartWalletConfig(in.ChainID)
	if sw == nil || wallet == nil {
		return nil, budgetInputError("wallet chain configuration is unavailable")
	}
	selfFunded := *sw
	selfFunded.DisableGasSponsorship = true
	selfFunded.AlchemyPaymasterPolicyID = ""
	prepare := n.budgetOwnerPrepareOverride
	if prepare == nil {
		prepare = preset.PrepareSelfFundedOwnerBatch
	}
	priced, err := prepare(ctx, &selfFunded, user.Address, in.Wallet, wallet.Salt, preset.OwnerBatch{Calls: calls}, n.logger)
	if err != nil {
		var fee *preset.OwnerFundingError
		if errors.As(err, &fee) {
			details := map[string]string{}
			if fee.Required != nil {
				details["neededWei"] = fee.Required.String()
			}
			if fee.Available != nil {
				details["availableWei"] = fee.Available.String()
			}
			return nil, &BudgetPolicyError{Code: BudgetFeeInsufficientCode, Detail: "Add ETH for network fees, then prepare the budget again.", Details: details}
		}
		return nil, fmt.Errorf("budget owner operation could not be prepared: %w", err)
	}
	op, hash, err := validateBudgetOwnerOperation(priced, in.Wallet, in.ChainID, calls)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(ownerPreparationTTL).UnixMilli()
	policyID := strings.ToLower(ulid.Make().String())
	policy := &model.SessionPolicy{ID: policyID, Owner: &user.Address, Runner: &in.Wallet, ChainID: in.ChainID, EntityID: entity, SessionSigner: &signer, AgentLabel: in.AgentLabel, Justification: in.Justification,
		Status: model.SessionPolicyPending, Sizing: model.BudgetSizingBudget, ValidUntil: draft.Permissions.ValidUntilMs, CreatedAt: now.UnixMilli(), BudgetStartedAt: draft.StartedAt, BudgetStartAmounts: draft.StartAmounts, BudgetStartNative: draft.StartNative,
		Grant: &model.SessionGrantAuthorization{InstallCall: install, CarrierNonce: new(big.Int).Set(op.Nonce), Deadline: uint64(expires / 1000)}}
	attachDeclaredPermissions(policy, draft.Permissions)
	view, err := preparedBudgetView(policy, current, *draft, now)
	if err != nil {
		return nil, err
	}
	record := &BudgetPolicyPreparation{ID: strings.ToLower(ulid.Make().String()), Owner: user.Address, Wallet: in.Wallet, ChainID: in.ChainID, CreatedAt: now.UnixMilli(), ExpiresAt: expires, State: ownerSubmissionPrepared,
		Operation: (*storedOwnerOperation)(op), UserOpHash: hash, MaxFeeWei: preset.MaxOwnerGasCost(op).String(), Identity: priced.Identity, Salt: wallet.Salt,
		Intent: budgetPreparationIntent{Budget: in.Budget, Add: in.Addition, BasePolicyID: in.BasePolicyID, DropTaskIDs: in.DropTaskIDs, AgentLabel: in.AgentLabel, Justification: in.Justification, Discovery: in.ApprovalDiscovery},
		Base:   current, Remainder: rem, Draft: draft, Policy: policy, ApprovalReads: reads, ApprovalResets: resets, Retirements: retirements, Budget: view}
	for _, task := range kept {
		data, marshalErr := protojson.Marshal(task)
		if marshalErr != nil {
			return nil, marshalErr
		}
		record.EnabledTasks = append(record.EnabledTasks, json.RawMessage(data))
	}
	// Refuse a changed base instead of returning a review built on another grant.
	latest, err := ActiveSessionPolicyForWallet(n.db, in.ChainID, user.Address, in.Wallet)
	if err != nil {
		return nil, err
	}
	before, _ := json.Marshal(current)
	after, _ := json.Marshal(latest)
	if !bytes.Equal(before, after) {
		return nil, newBaseChanged(latest)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if _, err := verifyBudgetDiscovery(in.ApprovalDiscovery, user.Address, in.Wallet, serviceSigner, in.ChainID, time.Now()); err != nil {
		return nil, err
	}
	if err := n.db.SetWithTTL(budgetPreparationKey(user.Address, record.ID), data, ownerPreparationTTL); err != nil {
		return nil, err
	}
	if in.AffectedOut != nil {
		*in.AffectedOut = dropped
	}
	return &PreparedSessionGrant{BudgetPreparation: record, Policy: policy, SkillChanges: &draft.Changes, EchoPermissions: &draft.Permissions}, nil
}

func (n *Engine) checkBudgetPreparationCap(owner common.Address, now time.Time) error {
	rows, err := n.db.GetByPrefix(budgetPreparationPrefix(owner))
	if err != nil {
		return err
	}
	count := 0
	for _, row := range rows {
		var p BudgetPolicyPreparation
		if err := json.Unmarshal(row.Value, &p); err != nil {
			return fmt.Errorf("unreadable budget preparation: %w", err)
		}
		if p.State == ownerSubmissionPrepared && p.ExpiresAt > now.UnixMilli() {
			count++
		}
	}
	if count >= ownerPreparationMaxPerOwner {
		return &BudgetPolicyError{Code: BudgetPreparationLimitCode, Detail: "Too many unsigned budget preparations. Wait for one to expire."}
	}
	return nil
}

func completeBudgetPairs(pairs []BudgetApprovalPair, current *model.SessionPolicy, draft BudgetPolicyDraft, spenders map[common.Address]bool) ([]BudgetApprovalPair, error) {
	tokens := map[common.Address]bool{}
	for _, pair := range pairs {
		tokens[pair.Token] = true
	}
	for _, cap := range draft.StartAmounts {
		if cap.Token != nil {
			tokens[*cap.Token] = true
		}
	}
	if current != nil {
		for _, action := range current.AllowedActions {
			if action.Target == nil {
				continue
			}
			for _, s := range action.Selectors {
				if normalizeSelector(s) == selectorApprove {
					tokens[*action.Target] = true
				}
			}
		}
	}
	unique := make(map[BudgetApprovalPair]bool, len(pairs))
	for _, pair := range pairs {
		unique[pair] = true
	}
	for token := range tokens {
		for spender := range spenders {
			unique[BudgetApprovalPair{Token: token, Spender: spender}] = true
			if len(unique) > budgetDiscoveryMaxPairs {
				return nil, &BudgetPolicyError{Code: BudgetDiscoveryUnavailableCode, Detail: "The complete approval inventory exceeds the preparation limit."}
			}
		}
	}
	out := make([]BudgetApprovalPair, 0, len(unique))
	for pair := range unique {
		out = append(out, pair)
	}
	out, err := canonicalBudgetPairs(out)
	if err != nil {
		return nil, &BudgetPolicyError{Code: BudgetDiscoveryUnavailableCode, Detail: err.Error()}
	}
	return out, nil
}

//nolint:gocyclo // Validate every owner-operation field before storing a signable review.
func validateBudgetOwnerOperation(priced *preset.PreparedOwnerUserOp, wallet common.Address, chainID int64, calls []aa.Call) (*userop.UserOperationV07, common.Hash, error) {
	if priced == nil || priced.Operation == nil {
		return nil, common.Hash{}, fmt.Errorf("budget pricing returned no operation")
	}
	op := priced.Operation
	packed, err := aa.PackExecuteBatchMAv2(calls)
	if err != nil {
		return nil, common.Hash{}, err
	}
	if op.Sender != wallet || len(op.Signature) > 0 || !bytes.Equal(op.CallData, packed) || op.Paymaster != nil || len(op.PaymasterData) > 0 {
		return nil, common.Hash{}, fmt.Errorf("budget pricing changed the account, batch, signature or funding policy")
	}
	if op.Nonce == nil {
		return nil, common.Hash{}, fmt.Errorf("missing owner nonce")
	}
	entity, options, _, err := userop.DecodeNonceMAv2(op.Nonce)
	if err != nil || entity != 0 || options != userop.ValidationOptionGlobal {
		return nil, common.Hash{}, fmt.Errorf("invalid owner nonce")
	}
	for _, value := range []*big.Int{op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas, op.MaxFeePerGas, op.MaxPriorityFeePerGas} {
		if value == nil || value.Sign() < 0 || value.BitLen() > 128 {
			return nil, common.Hash{}, fmt.Errorf("invalid owner gas quote")
		}
	}
	if preset.MaxOwnerGasCost(op).Sign() == 0 || op.MaxPriorityFeePerGas.Cmp(op.MaxFeePerGas) > 0 {
		return nil, common.Hash{}, fmt.Errorf("invalid owner fee quote")
	}
	hash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(chainID))
	return op, hash, err
}

func preparedBudgetView(policy, current *model.SessionPolicy, draft BudgetPolicyDraft, now time.Time) (*WalletBudget, error) {
	out := &WalletBudget{ChainID: policy.ChainID, PolicyID: policy.ID, Status: model.SessionPolicyPending, Sizing: model.BudgetSizingBudget, ValidUntil: policy.ValidUntil, StartedAt: draft.StartedAt, ReadAt: now.UnixMilli(), ERC20: []BudgetTokenRow{}, UnmeteredPermissions: classifyUnmeteredPermissions(policy)}
	totals := map[common.Address]string{}
	for _, cap := range draft.StartAmounts {
		totals[*cap.Token] = cap.Amount
	}
	for _, cap := range draft.Permissions.SpendCaps {
		left, err := parseBudgetAmount(cap.Amount)
		if err != nil {
			return nil, err
		}
		amount, used, unlimited, err := budgetAmounts(totals[*cap.Token], left)
		if err != nil {
			return nil, err
		}
		out.ERC20 = append(out.ERC20, BudgetTokenRow{Token: *cap.Token, Amount: amount, Remaining: cap.Amount, Used: used, Unlimited: unlimited})
	}
	if nativeCap := draft.Permissions.NativeSpendCap; nativeCap != nil {
		left, err := parseBudgetAmount(nativeCap.Amount)
		if err != nil {
			return nil, err
		}
		amount, used, unlimited, err := budgetAmounts(draft.StartNative.Amount, left)
		if err != nil {
			return nil, err
		}
		out.Native = &BudgetNativeRow{Amount: amount, Remaining: nativeCap.Amount, Used: used, Unlimited: unlimited}
	}
	for i, row := range out.UnmeteredPermissions {
		out.UnmeteredPermissions[i].Added = current == nil || len(missingActions(current.AllowedActions, []model.AllowedAction{{Target: &row.Target, Selectors: row.Selectors}})) > 0
	}
	return out, nil
}

//nolint:gocyclo // Corrupt stored reviews fail closed before mapping any optional nested fields.
func (n *Engine) GetBudgetPolicyPreparation(user *model.User, wallet common.Address, chainID int64, id string) (*BudgetPolicyPreparation, error) {
	if err := n.requireOwnedWallet(user, wallet); err != nil {
		return nil, err
	}
	if err := n.requireServedChain(chainID); err != nil {
		return nil, err
	}
	if n.config == nil || !n.config.WalletBudgetPreparationEnabled {
		return nil, budgetHandoffRequired()
	}
	data, err := n.db.GetKey(budgetPreparationKey(user.Address, id))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, ErrSessionPolicyNotFound
	}
	if err != nil {
		return nil, err
	}
	var record BudgetPolicyPreparation
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("%w: unreadable budget preparation", ErrBudgetReadUnavailable)
	}
	if record.Owner != user.Address || record.Wallet != wallet || record.ChainID != chainID || record.ExpiresAt <= time.Now().UnixMilli() {
		return nil, ErrSessionPolicyNotFound
	}
	if record.ID != id || record.State != ownerSubmissionPrepared || record.Operation == nil || record.Policy == nil || record.Draft == nil || record.Budget == nil {
		return nil, fmt.Errorf("%w: invalid stored budget preparation", ErrBudgetReadUnavailable)
	}
	if record.Policy.Owner == nil || *record.Policy.Owner != user.Address || record.Policy.Runner == nil || *record.Policy.Runner != wallet || record.Policy.ChainID != chainID || record.Policy.SessionSigner == nil || record.Policy.Grant == nil {
		return nil, fmt.Errorf("%w: invalid stored budget identity", ErrBudgetReadUnavailable)
	}
	op := (*userop.UserOperationV07)(record.Operation)
	if op.Nonce == nil || op.CallGasLimit == nil || op.VerificationGasLimit == nil || op.PreVerificationGas == nil || op.MaxFeePerGas == nil || op.MaxPriorityFeePerGas == nil || len(op.Signature) != 0 || op.Sender != wallet || op.Paymaster != nil || len(op.PaymasterData) > 0 {
		return nil, fmt.Errorf("%w: invalid stored budget operation", ErrBudgetReadUnavailable)
	}
	targets, values, datas, err := aa.UnpackExecuteCalldata(op.CallData)
	if err != nil || len(targets) == 0 || len(targets) > ownerBatchMaxCalls {
		return nil, fmt.Errorf("%w: invalid stored budget batch", ErrBudgetReadUnavailable)
	}
	calls := make([]aa.Call, len(targets))
	for i, target := range targets {
		calls[i] = aa.Call{Target: target, Value: values[i], Data: datas[i]}
	}
	_, hash, err := validateBudgetOwnerOperation(&preset.PreparedOwnerUserOp{Operation: op}, wallet, chainID, calls)
	if err != nil || hash != record.UserOpHash || preset.MaxOwnerGasCost(op).String() != record.MaxFeeWei {
		return nil, fmt.Errorf("%w: stored budget operation does not match its review", ErrBudgetReadUnavailable)
	}
	// Runtime validators are rebound at submit; only declared data is persisted.
	record.Draft.Permissions.budgetMode = true
	return &record, nil
}
