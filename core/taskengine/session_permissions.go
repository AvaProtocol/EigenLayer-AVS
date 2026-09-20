package taskengine

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// MaxNativeRecipients is the product cap Validate and the SDK merge enforce.
// Keep in sync with OpenAPI nativeRecipients maxItems on PreparePolicyRequest
// and SubmitPolicyRequest. A0: 20-row first-op install mined at 1.5M VGL, but
// 20-row deferred replace AA23'd 3/3 at 2.2M. Singleton re-grant is replace,
// so the max follows replace. Five rows stay inside the 700k + 45k/row seed
// window.
const MaxNativeRecipients = 5

// SessionPermissions is the declared permission set — allowed actions,
// ERC-20 spend cap(s), expiry — and its translation into the hook entries a
// grant installs. This is the only place that translation lives: the REST
// layer hands over declared permissions, never raw hook bytes, so a client
// cannot smuggle an encoding the grant screen did not show.
//
// A grant needs allowedActions and/or nativeRecipients, a matching spend
// cap for each class, and a future validUntil. ERC-20 caps install the
// allowlist EXECUTION hook, which is what stops a global session validation
// from self-administering the account (revoke spike §3.5). Native-only
// grants still install AllowlistExecHook (K5) via empty-selector rows.
//
// AllowlistModule's ERC-20 spend limit only meters transfer(address,uint256)
// and approve(address,uint256). deposit/withdraw (and any other selector) on
// a capped token revert InvalidCalldataLength / SelectorNotAllowed. Validate
// therefore refuses to cap a token whose allowedActions are not a subset of
// those two selectors — Uniswap wrap/unwrap on WETH cannot also cap WETH.
type SessionPermissions struct {
	AllowedActions []model.AllowedAction
	// SpendCap is the one-token alias (OpenAPI erc20SpendCap). SpendCaps is
	// the full per-token list. Validate accepts either; if both are set they
	// must agree (singular matches one list entry).
	SpendCap     *model.ERC20SpendCap
	SpendCaps    []model.ERC20SpendCap
	ValidUntilMs int64

	NativeRecipients       []*common.Address
	NativeSpendCap         *model.NativeSpendCap
	AllowContractRecipient bool
	// SessionSigner, when set, is refused as a native recipient — the
	// gateway's shared controller. Bound at prepare/submit from
	// sessionSignerAddress.
	SessionSigner *common.Address
	// CodeAt looks up bytecode for native-recipient EOA checks (K4).
	// Required when native recipients are set and AllowContractRecipient
	// is false. Nil is fail-closed. Tests inject an empty-code stub;
	// production prepare/submit bind a pooled chain reader.
	CodeAt func(common.Address) ([]byte, error)
}

// Validate rejects a permission set the grant screen could not have produced.
func (p SessionPermissions) Validate() error {
	return p.validate(true)
}

func (p SessionPermissions) validate(withChain bool) error {
	erc20Class := len(p.AllowedActions) > 0
	nativeSend := len(p.NativeRecipients) > 0
	if !erc20Class && !nativeSend {
		return fmt.Errorf("a grant needs allowedActions and/or nativeRecipients")
	}

	if erc20Class {
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
			if _, err := parseCapAmount(cap.Amount); err != nil {
				return fmt.Errorf("spend cap %d: %w", i, err)
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
			if err := spendLimitSelectorsOK(*cap.Token, p.AllowedActions); err != nil {
				return err
			}
		}
	} else if p.SpendCap != nil || len(p.SpendCaps) > 0 {
		return fmt.Errorf("erc20SpendCap requires allowedActions")
	}

	if p.NativeSpendCap != nil {
		if _, err := parseCapAmount(p.NativeSpendCap.Amount); err != nil {
			return fmt.Errorf("nativeSpendCap: %w", err)
		}
	}

	if nativeSend {
		if p.NativeSpendCap == nil {
			return fmt.Errorf("nativeRecipients require a nativeSpendCap")
		}
		if len(p.NativeRecipients) > MaxNativeRecipients {
			return fmt.Errorf("at most %d native recipients", MaxNativeRecipients)
		}
		seenRecipients := make(map[common.Address]struct{}, len(p.NativeRecipients))
		for i, rec := range p.NativeRecipients {
			if rec == nil || *rec == (common.Address{}) {
				return fmt.Errorf("native recipient %d is the zero address", i)
			}
			if _, dup := seenRecipients[*rec]; dup {
				return fmt.Errorf("native recipient %s is listed twice", rec.Hex())
			}
			seenRecipients[*rec] = struct{}{}
			if reservedNativeRecipient(*rec) {
				return fmt.Errorf("native recipient %s is a known module, factory, or EntryPoint", rec.Hex())
			}
			if p.SessionSigner != nil && *rec == *p.SessionSigner {
				return fmt.Errorf("native recipient %s is the session signer", rec.Hex())
			}
			for _, action := range p.AllowedActions {
				if action.Target != nil && *action.Target == *rec {
					return fmt.Errorf("list contracts as allowed actions with selectors; native recipients are for empty-calldata sends")
				}
			}
			if withChain && !p.AllowContractRecipient {
				if p.CodeAt == nil {
					return fmt.Errorf("cannot verify native recipient %s is an EOA: session resolver is not installed (InstallSessionResolver); fail closed", rec.Hex())
				}
				code, err := p.CodeAt(*rec)
				if err != nil {
					return fmt.Errorf("looking up native recipient %s: %w", rec.Hex(), err)
				}
				if len(code) != 0 {
					return fmt.Errorf("native recipient is a contract; send via contractWrite, or set allowContractRecipient (any function on this address, ERC-20 uncapped)")
				}
			}
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
// HasSelectorAllowlist is true for every allowedActions entry. Native
// recipient rows set it false (K1/K4): that is the only way to authorize
// empty-calldata execute. A3 must then read the actual grant instead of
// refusing all MA v2 native sends. TestHooksForAlwaysScopesSelectors still
// covers ERC-20-only grants; TestHooksForNativeRecipientRowsAreUnscoped
// covers the native rows.
//
// HasERC20SpendLimit is set on every capped token (not only the singular
// alias). The module then meters transfer/approve on that target and reverts
// on any other selector — Validate has already refused those shapes.
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
		amount, err := parseCapAmount(cap.Amount)
		if err != nil {
			return nil, err
		}
		limitByToken[*cap.Token] = amount
	}
	inputs := make([]aa.AllowlistInput, 0, len(p.AllowedActions)+len(p.NativeRecipients))
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
	for _, rec := range p.NativeRecipients {
		if rec == nil {
			continue
		}
		inputs = append(inputs, aa.AllowlistInput{
			Target:               *rec,
			HasSelectorAllowlist: false,
		})
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
				if cap.Token != nil && *cap.Token == *p.SpendCap.Token && spendAmountsEqual(cap.Amount, p.SpendCap.Amount) {
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

// erc20TransferSelector / erc20ApproveSelector are the only functions
// AllowlistModule._isAllowedERC20Function accepts. A spend-limit row on any
// other selector reverts SelectorNotAllowed; calldata shorter than 68 bytes
// (deposit, withdraw) reverts InvalidCalldataLength.
var (
	erc20TransferSelector = [4]byte{0xa9, 0x05, 0x9c, 0xbb}
	erc20ApproveSelector  = [4]byte{0x09, 0x5e, 0xa7, 0xb3}
)

func spendLimitSelectorsOK(token common.Address, actions []model.AllowedAction) error {
	for _, action := range actions {
		if action.Target == nil || *action.Target != token {
			continue
		}
		for _, raw := range action.Selectors {
			sel, err := parseSelector(raw)
			if err != nil {
				return fmt.Errorf("cap token %s: %w", token.Hex(), err)
			}
			if sel != erc20TransferSelector && sel != erc20ApproveSelector {
				return fmt.Errorf("cannot cap %s: AllowlistModule only meters transfer/approve; %s would revert on-chain (deposit/withdraw and wrap paths cannot share a spend-limit row)", token.Hex(), raw)
			}
		}
	}
	return nil
}

func spendAmountsEqual(a, b string) bool {
	x, errX := parseCapAmount(a)
	y, errY := parseCapAmount(b)
	return errX == nil && errY == nil && x.Cmp(y) == 0
}

// parseCapAmount accepts OpenAPI ^[0-9]+$ only — no surrounding whitespace.
func parseCapAmount(s string) (*big.Int, error) {
	if s == "" {
		return nil, fmt.Errorf("spend cap amount %q is not a positive decimal integer", s)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("spend cap amount %q is not a positive decimal integer", s)
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() <= 0 {
		return nil, fmt.Errorf("spend cap amount %q is not a positive decimal integer", s)
	}
	return n, nil
}

// HooksFor builds the grant's hook entries for its allocated entity:
// the allowlist validation hook (targets, selectors, and per-token spend
// limits in one install payload), the allowlist execution hook that enforces
// those limits, and the time-range hook that expires the grant.
func (p SessionPermissions) HooksFor(entityID uint32) ([][]byte, error) {
	// Static checks only: Prepare/Submit already ran Validate() with chain
	// lookups. Repeating eth_getCode here doubled RPC on every grant.
	if err := p.validate(false); err != nil {
		return nil, err
	}
	// Fail closed if a future caller (Track B) packs without Validate():
	// HasSelectorAllowlist=false on an unproven address is the K4 High risk.
	// Nil-check only — no lookup — so the memoized engine path stays one RPC
	// per recipient.
	if len(p.NativeRecipients) > 0 && !p.AllowContractRecipient && p.CodeAt == nil {
		return nil, fmt.Errorf("cannot pack native recipients: CodeAt is unset; Validate must run first (InstallSessionResolver)")
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
	hooks := [][]byte{allowlistHook, aa.AllowlistExecHook(entityID)}
	if p.NativeSpendCap != nil {
		limit, err := parseCapAmount(p.NativeSpendCap.Amount)
		if err != nil {
			return nil, err
		}
		ntVal, err := aa.NativeTokenLimitValidationHook(entityID, limit)
		if err != nil {
			return nil, fmt.Errorf("building the native-token limit hook: %w", err)
		}
		hooks = append(hooks, ntVal, aa.NativeTokenLimitExecHook(entityID))
	}
	return append(hooks, timeRangeHook), nil
}

func reservedNativeRecipient(addr common.Address) bool {
	switch addr {
	case aa.AllowlistModuleAddress(),
		aa.NativeTokenLimitModuleAddress(),
		aa.TimeRangeModuleAddress(),
		aa.SingleSignerValidationModuleAddress(),
		aa.MAv2FactoryAddress(),
		common.HexToAddress(config.EntryPointV07AddressHex):
		return true
	}
	return false
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
