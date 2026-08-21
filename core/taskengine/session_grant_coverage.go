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

// SessionPolicyNativeNotAllowedCode marks a refused native-value operation.
// Exported so the producers and the REST layer share one symbol: this package
// formats the message, and aggregator/rest keys the problem+json `code` off it.
//
// preset.IsClientUserOpFailure matches the same text as a bare string literal
// and cannot use this constant — taskengine imports preset (the send path), so
// preset importing taskengine back would be a cycle. Renaming this constant
// therefore has to be paired with editing that literal in
// pkg/erc4337/preset/bundler_error.go by hand; the compiler will not catch it.
const SessionPolicyNativeNotAllowedCode = "SESSION_POLICY_NATIVE_NOT_ALLOWED"

// FormatSessionPolicyNativeNotAllowed builds the refusal for an inner call
// that moves native ETH under a REST session grant.
//
// This is not a coverage miss that re-granting can fix, which is why it is a
// separate code from SESSION_POLICY_TARGET_NOT_ALLOWED. A native transfer is
// execute(to, value, 0x) — empty inner calldata — and every REST grant
// installs the allowlist with hasSelectorAllowlist=true (see
// SessionPermissions.HooksFor). Alchemy's AllowlistModule then rejects the
// call outright:
//
//	if (hasSelectorAllowlist) {
//	    if (data.length < 4) revert NoSelectorSpecified();
//
// So listing the recipient with selector 0x00000000 does NOT help: the module
// reverts on the length check before it ever consults the selector set. The
// zero-address wildcard would skip that branch, but SessionPermissions.Validate
// refuses a zero target. There is no grant shape the REST surface can emit
// that authorizes this, so the message must not tell the caller to re-grant.
//
// Without this refusal the operation reaches the bundler and comes back as
// opaque AA23, which reads as "your grant is wrong" and sends people to
// re-grant in a loop that cannot converge.
func FormatSessionPolicyNativeNotAllowed(recipient common.Address, policyID string) string {
	msg := SessionPolicyNativeNotAllowedCode +
		": session grants cannot authorize native ETH transfers (to " + recipient.Hex() + ")" +
		" — the account's allowlist hook requires a 4-byte selector and a native transfer carries none," +
		" so no allowlist entry can cover it; move ERC-20 value instead, or send the ETH" +
		" with the owner key outside the session"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}

// FormatSessionPolicyTargetNotAllowed builds a stable, client-parseable error.
// Prefix SESSION_POLICY_TARGET_NOT_ALLOWED is the machine code for Studio maps.
// Remediation is capability-agnostic: this preflight applies to any MA v2
// contract write, not only Uniswap.
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
			label, m.Target.Hex(), normalizeSelector(m.Selector)))
	}
	msg := "SESSION_POLICY_TARGET_NOT_ALLOWED: session grant does not allow: " +
		strings.Join(parts, "; ") +
		" — re-grant the session policy so the allowlist includes these target(s) and selector(s)"
	if policyID != "" {
		msg += " (policy " + policyID + ")"
	}
	return msg
}

// normalizeSelector matches MissingGrantCalls: trim, lowercase, ensure 0x prefix.
func normalizeSelector(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "0x00000000"
	}
	if !strings.HasPrefix(s, "0x") {
		return "0x" + s
	}
	return s
}
