package taskengine

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// PlannedCall is one inner call the MA v2 account will make under session
// hooks (execute / executeBatch target + 4-byte selector). Used to preflight
// session grants before the bundler returns opaque AA23.
type PlannedCall struct {
	Target   common.Address
	Selector string // 0x-prefixed 4-byte hex, lowercase preferred
	// Label is optional (method name) for error messages only.
	Label string
}

// SelectorFromCalldata returns the 4-byte selector of calldata, or
// "0x00000000" when data is empty (native value transfer with no payload).
func SelectorFromCalldata(data []byte) string {
	if len(data) < 4 {
		return "0x00000000"
	}
	return "0x" + common.Bytes2Hex(data[:4])
}

// MissingGrantCalls returns planned calls not covered by the grant's
// allowedActions. Matching is case-insensitive on target and selector.
//
// An empty allowed list means "no preflight data" — the caller should skip
// coverage (legacy/tests) rather than treat everything as missing.
func MissingGrantCalls(allowed []model.AllowedAction, planned []PlannedCall) []PlannedCall {
	if len(allowed) == 0 || len(planned) == 0 {
		return nil
	}
	// target(lower) → set of selectors(lower)
	byTarget := make(map[string]map[string]struct{}, len(allowed))
	for _, a := range allowed {
		if a.Target == nil {
			continue
		}
		key := strings.ToLower(a.Target.Hex())
		selSet := byTarget[key]
		if selSet == nil {
			selSet = make(map[string]struct{})
			byTarget[key] = selSet
		}
		for _, s := range a.Selectors {
			selSet[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
		}
	}

	var missing []PlannedCall
	for _, c := range planned {
		sel := strings.ToLower(strings.TrimSpace(c.Selector))
		if sel == "" {
			sel = "0x00000000"
		}
		if !strings.HasPrefix(sel, "0x") {
			sel = "0x" + sel
		}
		tkey := strings.ToLower(c.Target.Hex())
		selSet, ok := byTarget[tkey]
		if !ok {
			missing = append(missing, c)
			continue
		}
		if _, ok := selSet[sel]; !ok {
			missing = append(missing, c)
		}
	}
	return missing
}

// FormatSessionPolicyTargetNotAllowed builds a stable, client-parseable error.
// Prefix SESSION_POLICY_TARGET_NOT_ALLOWED is the machine code for Studio maps.
func FormatSessionPolicyTargetNotAllowed(missing []PlannedCall, policyID string) string {
	if len(missing) == 0 {
		return "SESSION_POLICY_TARGET_NOT_ALLOWED: session grant does not cover the planned calls"
	}
	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		label := m.Label
		if label == "" {
			label = "call"
		}
		parts = append(parts, fmt.Sprintf("%s target=%s selector=%s",
			label, m.Target.Hex(), strings.ToLower(m.Selector)))
	}
	msg := "SESSION_POLICY_TARGET_NOT_ALLOWED: session grant does not allow: " +
		strings.Join(parts, "; ") +
		" — re-grant Uniswap (or the capability) with approve for this token and the router"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}
