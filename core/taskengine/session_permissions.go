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
	SpendCap       *model.ERC20SpendCap
	ValidUntilMs   int64
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
	if p.SpendCap == nil || p.SpendCap.Token == nil {
		return fmt.Errorf("a grant needs an ERC-20 spend cap")
	}
	if amount, ok := new(big.Int).SetString(p.SpendCap.Amount, 10); !ok || amount.Sign() <= 0 {
		return fmt.Errorf("spend cap amount %q is not a positive decimal integer", p.SpendCap.Amount)
	}
	capCovered := false
	for _, action := range p.AllowedActions {
		if action.Target != nil && *action.Target == *p.SpendCap.Token {
			capCovered = true
			break
		}
	}
	if !capCovered {
		return fmt.Errorf("the cap token %s is not an allowed-action target; cap a token the agent may actually call", p.SpendCap.Token.Hex())
	}
	if p.ValidUntilMs <= time.Now().UnixMilli() {
		return fmt.Errorf("validUntil is in the past")
	}
	return nil
}

// HooksFor builds the grant's hook entries for its allocated entity:
// the allowlist validation hook (targets, selectors, and the cap's limit in
// one install payload), the allowlist execution hook that enforces the cap,
// and the time-range hook that expires the grant.
func (p SessionPermissions) HooksFor(entityID uint32) ([][]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	inputs := make([]aa.AllowlistInput, 0, len(p.AllowedActions))
	capAmount, _ := new(big.Int).SetString(p.SpendCap.Amount, 10)
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
		if *action.Target == *p.SpendCap.Token {
			input.HasERC20SpendLimit = true
			input.ERC20SpendLimit = capAmount
		}
		inputs = append(inputs, input)
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
