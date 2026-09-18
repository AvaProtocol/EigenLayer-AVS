package taskengine

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// SessionPermissions is the declared §7.2 permission set — allowed actions,
// ERC-20 spend cap, expiry — and its translation into the hook entries a
// grant installs. This is the only place that translation lives: the REST
// layer hands over declared permissions, never raw hook bytes, so a client
// cannot smuggle an encoding the grant screen did not show.
//
// v1 requires all three permissions. That is not just product scope: the cap
// installs the allowlist's EXECUTION hook, and per the revoke spike (results
// doc §3.5) that hook is what stops a global session validation from
// self-administering the account. A capless global grant would need
// selector-scoping instead, which is not yet exercised on-chain.
type SessionPermissions struct {
	AllowedActions []model.AllowedAction
	// SpendCap is the one-token alias (OpenAPI erc20SpendCap). SpendCaps is
	// the full per-token list. Validate accepts either; if both are set they
	// must agree (singular matches one list entry).
	SpendCap     *model.ERC20SpendCap
	SpendCaps    []model.ERC20SpendCap
	ValidUntilMs int64
}

// Validate rejects a permission set the grant screen could not have produced.
func (p SessionPermissions) Validate() error {
	if len(p.AllowedActions) == 0 {
		return fmt.Errorf("a grant needs at least one allowed action")
	}
	for i, action := range p.AllowedActions {
		if action.Target == nil || *action.Target == (common.Address{}) {
			return fmt.Errorf("allowed action %d has no target", i)
		}
		if len(action.Selectors) == 0 {
			return fmt.Errorf("allowed action %d on %s has no selectors; an any-function grant is not offered", i, action.Target.Hex())
		}
		for _, s := range action.Selectors {
			if _, err := parseSelector(s); err != nil {
				return fmt.Errorf("allowed action %d: %w", i, err)
			}
		}
	}
	caps, err := p.resolvedSpendCaps()
	if err != nil {
		return err
	}
	if len(caps) == 0 {
		return fmt.Errorf("a grant needs an ERC-20 spend cap")
	}
	seen := make(map[common.Address]struct{}, len(caps))
	for i, cap := range caps {
		if cap.Token == nil || *cap.Token == (common.Address{}) {
			return fmt.Errorf("spend cap %d has no token", i)
		}
		if amount, ok := new(big.Int).SetString(cap.Amount, 10); !ok || amount.Sign() <= 0 {
			return fmt.Errorf("spend cap amount %q is not a positive decimal integer", cap.Amount)
		}
		if _, dup := seen[*cap.Token]; dup {
			return fmt.Errorf("spend cap token %s is listed twice; merge amounts before submit", cap.Token.Hex())
		}
		seen[*cap.Token] = struct{}{}
		covered := false
		for _, action := range p.AllowedActions {
			if action.Target != nil && *action.Target == *cap.Token {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("the cap token %s is not an allowed-action target; cap a token the agent may actually call", cap.Token.Hex())
		}
	}
	if p.ValidUntilMs <= time.Now().UnixMilli() {
		return fmt.Errorf("validUntil is in the past")
	}
	return nil
}

// allowlistInputs is the allowlist half of HooksFor, split out so the
// selector-scoping invariant is observable to a test rather than only asserted
// in comments.
//
// HasSelectorAllowlist is true for EVERY entry, unconditionally, and a good
// deal depends on that: because the module skips its `data.length < 4` check
// only when the flag is false, a selector-scoped entry can never authorize an
// empty-calldata (native value) inner call. The native-ETH refusals in
// ExecuteWithdraw and ETHTransferProcessor.preflightSessionGrant are blanket
// refusals on MA v2 chains precisely because this is unconditional; if a grant
// shape ever sets it false, those refusals become overly broad and must be
// narrowed to match. TestHooksForAlwaysScopesSelectors guards the coupling.
func (p SessionPermissions) allowlistInputs() ([]aa.AllowlistInput, error) {
	caps, err := p.resolvedSpendCaps()
	if err != nil {
		return nil, err
	}
	limitByToken := make(map[common.Address]*big.Int, len(caps))
	for _, cap := range caps {
		if cap.Token == nil {
			continue
		}
		amount, ok := new(big.Int).SetString(cap.Amount, 10)
		if !ok {
			return nil, fmt.Errorf("spend cap amount %q is not a positive decimal integer", cap.Amount)
		}
		limitByToken[*cap.Token] = amount
	}
	inputs := make([]aa.AllowlistInput, 0, len(p.AllowedActions))
	for _, action := range p.AllowedActions {
		selectors := make([][4]byte, 0, len(action.Selectors))
		for _, s := range action.Selectors {
			sel, err := parseSelector(s)
			if err != nil {
				return nil, err
			}
			selectors = append(selectors, sel)
		}
		input := aa.AllowlistInput{
			Target:               *action.Target,
			HasSelectorAllowlist: true,
			Selectors:            selectors,
		}
		if limit, ok := limitByToken[*action.Target]; ok {
			input.HasERC20SpendLimit = true
			input.ERC20SpendLimit = limit
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

// resolvedSpendCaps returns the per-token list. SpendCaps wins when non-empty;
// otherwise SpendCap is the one-element alias. When both are set, the singular
// token+amount must match one list entry.
func (p SessionPermissions) resolvedSpendCaps() ([]model.ERC20SpendCap, error) {
	if len(p.SpendCaps) > 0 {
		if p.SpendCap != nil && p.SpendCap.Token != nil {
			matched := false
			for _, cap := range p.SpendCaps {
				if cap.Token != nil && *cap.Token == *p.SpendCap.Token && cap.Amount == p.SpendCap.Amount {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("erc20SpendCap %s/%s is not an entry of erc20SpendCaps", p.SpendCap.Token.Hex(), p.SpendCap.Amount)
			}
		}
		return p.SpendCaps, nil
	}
	if p.SpendCap != nil {
		return []model.ERC20SpendCap{*p.SpendCap}, nil
	}
	return nil, nil
}

// HooksFor builds the grant's hook entries for its allocated entity:
// the allowlist validation hook (targets, selectors, and the cap's limit in
// one install payload), the allowlist execution hook that enforces the cap,
// and the time-range hook that expires the grant.
func (p SessionPermissions) HooksFor(entityID uint32) ([][]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	inputs, err := p.allowlistInputs()
	if err != nil {
		return nil, err
	}

	allowlistHook, err := aa.AllowlistValidationHook(entityID, inputs)
	if err != nil {
		return nil, fmt.Errorf("building the allowlist hook: %w", err)
	}
	timeRangeHook, err := aa.TimeRangeValidationHook(entityID, uint64(p.ValidUntilMs/1000), 0)
	if err != nil {
		return nil, fmt.Errorf("building the time-range hook: %w", err)
	}
	return [][]byte{allowlistHook, aa.AllowlistExecHook(entityID), timeRangeHook}, nil
}

func parseSelector(s string) ([4]byte, error) {
	var out [4]byte
	raw := strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if len(raw) != 8 {
		return out, fmt.Errorf("selector %q is not 4 bytes", s)
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return out, fmt.Errorf("selector %q is not hex: %w", s, err)
	}
	copy(out[:], b)
	return out, nil
}
