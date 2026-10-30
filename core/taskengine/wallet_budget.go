package taskengine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

const BudgetReadUnavailableCode = "BUDGET_READ_UNAVAILABLE"

var ErrBudgetReadUnavailable = errors.New("wallet budget remainder unavailable")

type BudgetTokenRow struct {
	Token     common.Address `json:"token"`
	Amount    string         `json:"amount"`
	Remaining string         `json:"remaining"`
	Used      string         `json:"used"`
	Unlimited bool           `json:"unlimited"`
}

type BudgetNativeRow struct {
	Amount    string `json:"amount"`
	Remaining string `json:"remaining"`
	Used      string `json:"used"`
	Unlimited bool   `json:"unlimited"`
	GasWei    string `json:"gasWei,omitempty"`
}

type UnmeteredPermissionKind string

const (
	UnmeteredAaveWithdraw  UnmeteredPermissionKind = "aave_withdraw"
	UnmeteredAaveBorrow    UnmeteredPermissionKind = "aave_borrow"
	UnmeteredWrappedNative UnmeteredPermissionKind = "wrapped_native"
	UnmeteredOther         UnmeteredPermissionKind = "other"
)

type UnmeteredPermission struct {
	Kind      UnmeteredPermissionKind `json:"kind"`
	Target    common.Address          `json:"target"`
	Selectors []string                `json:"selectors"`
	Token     *common.Address         `json:"token,omitempty"`
	Added     bool                    `json:"added"`
}

type RetirementState string

const (
	RetirementRegistered RetirementState = "registered"
	RetirementResidual   RetirementState = "residual"
	RetirementUnverified RetirementState = "unverified"
)

type BudgetRetirement struct {
	EntityID uint32                `json:"entityId"`
	PolicyID string                `json:"policyId"`
	State    RetirementState       `json:"state"`
	ERC20    []model.ERC20SpendCap `json:"-"`
	Native   *model.NativeSpendCap `json:"-"`
}

type WalletBudget struct {
	ChainID              int64                     `json:"chainId"`
	PolicyID             string                    `json:"policyId"`
	Status               model.SessionPolicyStatus `json:"status"`
	Sizing               model.BudgetSizing        `json:"sizing"`
	ValidUntil           int64                     `json:"validUntil"`
	StartedAt            int64                     `json:"startedAt,omitempty"`
	ERC20                []BudgetTokenRow          `json:"erc20"`
	Native               *BudgetNativeRow          `json:"native,omitempty"`
	UnmeteredPermissions []UnmeteredPermission     `json:"unmeteredPermissions"`
	Retirements          []BudgetRetirement        `json:"retirements,omitempty"`
	ReadAt               int64                     `json:"readAt"`
}

// GetWalletBudget keeps the authority lock through the read so a concurrent
// revoke/replacement cannot mix stored metadata from different permissions.
func (n *Engine) GetWalletBudget(ctx context.Context, user *model.User, chainID int64, wallet common.Address) (*WalletBudget, error) {
	if err := n.requireOwnedWallet(user, wallet); err != nil {
		return nil, err
	}
	lock := sessionAuthorityLock(chainID, user.Address, wallet)
	lock.RLock()
	defer lock.RUnlock()
	policy, err := activeSessionPolicyLocked(n.db, chainID, user.Address, wallet)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, ErrSessionPolicyNotFound
	}
	all, err := ListSessionPolicies(n.db, chainID, user.Address)
	if err != nil {
		return nil, err
	}
	var retired []*model.SessionPolicy
	for _, p := range all {
		if p.Runner != nil && *p.Runner == wallet && p.Status == model.SessionPolicyRevoked && p.Grant.Applied() {
			retired = append(retired, p) // TornDownAt proves registration only, not residue.
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, teardownVerifyTimeout)
	defer cancel()
	var client *ethclient.Client
	if cfg := n.ResolveSmartWalletConfig(chainID); cfg != nil && cfg.EthRpcUrl != "" {
		client, err = ethclient.DialContext(readCtx, cfg.EthRpcUrl)
		if err == nil {
			defer client.Close()
		}
	}
	// A pending deferred grant needs no remainder RPC. A dial failure may still
	// produce retirement=unverified; it must not suppress its stored totals.
	var caller aa.ContractCaller
	if client != nil {
		caller = client
	}
	return ReadWalletBudget(readCtx, policy, retired, caller, time.Now())
}

// ReadWalletBudget is also used by the live probe against a real permission.
// No read mutates policy storage, derives approvals, or activates a permission.
func ReadWalletBudget(ctx context.Context, policy *model.SessionPolicy, retired []*model.SessionPolicy, client aa.ContractCaller, now time.Time) (*WalletBudget, error) {
	if policy == nil || policy.Runner == nil {
		return nil, ErrSessionPolicyNotFound
	}
	out := &WalletBudget{ChainID: policy.ChainID, PolicyID: policy.ID, Status: policy.Status,
		Sizing: policy.EffectiveSizing(), ValidUntil: policy.ValidUntil, ReadAt: now.UnixMilli(),
		ERC20: []BudgetTokenRow{}, UnmeteredPermissions: classifyUnmeteredPermissions(policy)}
	if out.Sizing != model.BudgetSizingWorkflow && out.Sizing != model.BudgetSizingBudget {
		return nil, fmt.Errorf("%w: unknown sizing %q", ErrBudgetReadUnavailable, out.Sizing)
	}
	if out.Sizing == model.BudgetSizingBudget {
		out.StartedAt = policy.BudgetStartedAt
	}
	for _, cap := range policyCaps(policy) {
		if cap.Token == nil {
			return nil, fmt.Errorf("%w: cap without token", ErrBudgetReadUnavailable)
		}
		total := originalCap(cap.Amount, cap.GrantedCap)
		if out.Sizing == model.BudgetSizingBudget {
			total = ""
			for _, start := range policy.BudgetStartAmounts {
				if start.Token != nil && *start.Token == *cap.Token {
					total = start.Amount
					break
				}
			}
		}
		remaining, err := parseBudgetAmount(total)
		if policy.Status != model.SessionPolicyPending {
			remaining, err = aa.ReadERC20SpendLimit(ctx, client, policy.EntityID, *cap.Token, *policy.Runner)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: token %s: %v", ErrBudgetReadUnavailable, cap.Token.Hex(), err)
		}
		amount, used, unlimited, err := budgetAmounts(total, remaining)
		if err != nil {
			return nil, err
		}
		out.ERC20 = append(out.ERC20, BudgetTokenRow{Token: *cap.Token, Amount: amount, Remaining: remaining.String(), Used: used, Unlimited: unlimited})
	}
	if cap := policy.NativeSpendCap; cap != nil {
		total := originalCap(cap.Amount, cap.GrantedCap)
		if out.Sizing == model.BudgetSizingBudget {
			total = ""
			if policy.BudgetStartNative != nil {
				total = policy.BudgetStartNative.Amount
			}
		}
		remaining, err := parseBudgetAmount(total)
		if policy.Status != model.SessionPolicyPending {
			remaining, err = aa.ReadNativeSpendLimit(ctx, client, policy.EntityID, *policy.Runner)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: native: %v", ErrBudgetReadUnavailable, err)
		}
		amount, used, unlimited, err := budgetAmounts(total, remaining)
		if err != nil {
			return nil, err
		}
		out.Native = &BudgetNativeRow{Amount: amount, Remaining: remaining.String(), Used: used, Unlimited: unlimited}
		// No persisted gas breakdown exists yet. Never guess gasWei.
	}
	for _, p := range retired {
		state := inspectRetirement(ctx, client, p)
		if state == "" {
			continue
		}
		row := BudgetRetirement{EntityID: p.EntityID, PolicyID: p.ID, State: state}
		if state == RetirementRegistered {
			row.ERC20 = policyCaps(p)
			row.Native = p.NativeSpendCap
		}
		out.Retirements = append(out.Retirements, row)
	}
	return out, nil
}

func inspectRetirement(ctx context.Context, client aa.ContractCaller, policy *model.SessionPolicy) RetirementState {
	if policy == nil || policy.Runner == nil || policy.Grant == nil {
		return RetirementUnverified
	}
	registered, err := aa.EntityRegisteredOnChain(ctx, client, *policy.Runner, policy.EntityID)
	if err != nil {
		return RetirementUnverified
	}
	if registered {
		return RetirementRegistered
	}
	clear, err := aa.EntityModuleStorageCleared(ctx, client, *policy.Runner, policy.EntityID, policy.Grant.InstallCall)
	if err != nil {
		return RetirementUnverified
	}
	if !clear {
		return RetirementResidual
	}
	return "" // clean entities are omitted
}

func originalCap(amount, granted string) string {
	if granted != "" {
		return granted
	}
	return amount // older stored records predate GrantedCap
}

func parseBudgetAmount(raw string) (*big.Int, error) {
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.Sign() < 0 || value.BitLen() > 256 {
		return nil, fmt.Errorf("%w: invalid stored amount", ErrBudgetReadUnavailable)
	}
	return value, nil
}

func budgetAmounts(total string, remaining *big.Int) (string, string, bool, error) {
	amount, err := parseBudgetAmount(total)
	if err != nil {
		return "", "", false, err
	}
	used := new(big.Int).Sub(amount, remaining)
	if used.Sign() < 0 {
		used.SetInt64(0)
	}
	threshold := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	return amount.String(), used.String(), amount.Cmp(threshold) >= 0, nil
}

// Snapshot of the catalog entry points used by today's standard permission,
// from @avaprotocol/protocols. Unknown targets/selectors remain disclosed.
//
//go:embed budget_contracts.json
var budgetContractsJSON []byte
var budgetContracts = loadBudgetContracts()

func loadBudgetContracts() map[string]map[int64]common.Address {
	var table map[string]map[int64]common.Address
	if err := json.Unmarshal(budgetContractsJSON, &table); err != nil {
		panic(err)
	}
	return table
}

func functionSelector(signature string) string {
	return "0x" + common.Bytes2Hex(crypto.Keccak256([]byte(signature))[:4])
}

// One classification table, scoped by chain AND target. A selector collision
// on an unknown contract must never make its authority disappear from the read.
func unmeteredKind(policy *model.SessionPolicy, target common.Address, selector string) UnmeteredPermissionKind {
	selector = strings.ToLower(selector)
	for _, cap := range policyCaps(policy) {
		if cap.Token != nil && *cap.Token == target && (selector == "0xa9059cbb" || selector == "0x095ea7b3") {
			return ""
		}
	}
	if target == budgetContracts["aavePool"][policy.ChainID] {
		switch selector {
		case functionSelector("withdraw(address,uint256,address)"):
			return UnmeteredAaveWithdraw
		case functionSelector("borrow(address,uint256,uint256,uint16,address)"):
			return UnmeteredAaveBorrow
		case functionSelector("supply(address,uint256,address,uint16)"), functionSelector("repay(address,uint256,uint256,address)"):
			return ""
		}
	}
	if target == budgetContracts["wrappedNative"][policy.ChainID] && (selector == functionSelector("deposit()") || selector == functionSelector("withdraw(uint256)")) {
		return UnmeteredWrappedNative
	}
	if target == budgetContracts["swapRouter"][policy.ChainID] {
		switch selector {
		case functionSelector("exactInputSingle((address,address,uint24,address,uint256,uint256,uint160))"),
			functionSelector("exactInput((bytes,address,uint256,uint256))"),
			functionSelector("exactOutputSingle((address,address,uint24,address,uint256,uint256,uint160))"),
			functionSelector("exactOutput((bytes,address,uint256,uint256))"),
			functionSelector("multicall(bytes[])"), functionSelector("multicall(uint256,bytes[])"),
			functionSelector("refundETH()"), functionSelector("unwrapWETH9(uint256,address)"):
			return ""
		}
	}
	if target == budgetContracts["lidoStake"][policy.ChainID] && policy.NativeSpendCap != nil && selector == functionSelector("submit(address)") {
		return ""
	}
	return UnmeteredOther
}

func classifyUnmeteredPermissions(policy *model.SessionPolicy) []UnmeteredPermission {
	rows := []UnmeteredPermission{}
	for _, action := range policy.AllowedActions {
		if action.Target == nil {
			continue
		}
		for _, selector := range action.Selectors {
			kind := unmeteredKind(policy, *action.Target, selector)
			if kind == "" {
				continue
			}
			index := -1
			for i := range rows {
				if rows[i].Target == *action.Target && rows[i].Kind == kind {
					index = i
					break
				}
			}
			if index < 0 {
				row := UnmeteredPermission{Kind: kind, Target: *action.Target, Selectors: []string{}}
				switch kind {
				case UnmeteredWrappedNative:
					token := *action.Target
					row.Token = &token
				case UnmeteredAaveWithdraw, UnmeteredAaveBorrow, UnmeteredOther:
				default:
					panic(fmt.Sprintf("unknown unmetered kind %q", kind))
				}
				rows = append(rows, row)
				index = len(rows) - 1
			}
			rows[index].Selectors = append(rows[index].Selectors, selector)
		}
	}
	return rows
}
