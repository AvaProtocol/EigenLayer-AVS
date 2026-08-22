package preset

import (
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// userOpRevertMarker identifies errors returned by SendUserOp when the UserOp
// was included on-chain but the target contract call reverted. The marker
// string is emitted from waitForUserOpConfirmation via fmt.Errorf.
const userOpRevertMarker = "success=false in UserOperationEvent"

// IsUserOpRevert reports whether err represents an on-chain revert of the user
// target contract (UserOp was mined but UserOperationEvent.success == false),
// as distinct from infra/AA failures such as bundler unreachable, AA21 prefund,
// AA23 reverted, AA25 invalid nonce, or paymaster revert.
//
// On-chain reverts are expected user-workflow outcomes and should not escalate
// to Sentry error alerts. Infra/AA failures should.
func IsUserOpRevert(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), userOpRevertMarker)
}

// IsClientUserOpFailure reports failures that are expected outcomes of client
// state or product configuration — missing/wrong session grant, FeeLedger
// webhook deny, self-funded prefund empty, Gas Manager simulation of an
// invalid UserOp — rather than gateway infrastructure faults.
//
// Studio/SDK testing against prod (and users with incomplete grants) produce
// these at high volume. Logging them at Error fans Sentry (EIGENLAYER-AVS-2E
// and siblings) without an operator action. Warn keeps them in logs for
// debugging; Error is reserved for true infra (bundler down, RPC, unexpected
// panic paths).
//
// Classification is substring-based because these errors are already
// fmt.Errorf-wrapped at several layers (send_v07, RequestSponsorshipV07,
// LogBundlerError callers). Prefer adding a new marker constant here when
// introducing a new client-facing failure class.
//
// Deliberately NOT matched (must stay Error → Sentry):
//   - SESSION_POLICY_LOOKUP_FAILED — wraps Badger GetByPrefix / corrupt records
//     as well as multi-grant; multi-grant has its own marker below
//   - bare "gas manager declined to sponsor" without a known denial reason —
//     RequestSponsorshipV07 also wraps dial/timeout/malformed RPC that way
//   - bare "AA23" without a sponsorship-denial context — packing bugs and wrong
//     account type also AA23 and are operator-actionable
func IsClientUserOpFailure(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "SESSION_POLICY_TARGET_NOT_ALLOWED"):
		// Typed preflight: batch target/selector outside the active grant.
		return true
	case strings.Contains(s, "SESSION_POLICY_EXPIRED"):
		// Typed preflight: the grant's TimeRangeModule window closed. The
		// owner renews it by granting again; nothing here is operable.
		return true
	case strings.Contains(s, "SESSION_POLICY_NATIVE_NOT_ALLOWED"):
		// Typed preflight: native ETH under a selector-scoped grant. Refused
		// before the bundler, so this never reflects gateway health — and it
		// is high-volume by nature (any ETH transfer node on an MA v2 chain
		// produces one), which is exactly the shape that fans Sentry.
		return true
	case strings.Contains(s, "no session authorization for smart wallet"):
		return true
	case strings.Contains(s, "more than one usable session policy"):
		// Client-clearable by revoking extras (or re-grant after supersede).
		return true
	case strings.Contains(s, "cannot pay gas"):
		// Self-funded prefund: zero native + no policy (or deposit).
		return true
	case strings.Contains(s, "Request was denied by webhook"):
		// FeeLedger / policy / secret gate. Often config, still not "bundler down".
		return true
	// Gas Manager: only known *denial* payloads from Alchemy, not transport
	// failures that share the "gas manager declined to sponsor:" wrap in send_v07.
	case strings.Contains(s, "gas manager declined to sponsor") &&
		(strings.Contains(s, "Request was denied by webhook") ||
			strings.Contains(s, "AA23") ||
			strings.Contains(s, "validation reverted") ||
			strings.Contains(s, "execution reverted")):
		return true
	case strings.Contains(s, "SESSION_GRANT_INSTALL_FAILED"):
		// Deferred install/replace batch failed validation or simulation —
		// new grant did not land and prior entities were not torn down.
		return true
	default:
		return false
	}
}

// LogBundlerError logs a bundler/UserOp failure at the severity appropriate
// for its cause: Warn for expected client outcomes (on-chain user reverts,
// missing grants, Gas Manager deny of an invalid UserOp) so they do not page
// Sentry; Error for real infra failures that operators must see.
//
// Callers pass the error both for classification (the first argument) and,
// conventionally, as a tag value so the logged record includes the full error.
func LogBundlerError(lgr logger.Logger, err error, msg string, tags ...any) {
	if lgr == nil {
		return
	}
	if IsUserOpRevert(err) || IsClientUserOpFailure(err) {
		lgr.Warn(msg, tags...)
		return
	}
	lgr.Error(msg, tags...)
}
