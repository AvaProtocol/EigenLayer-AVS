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

// LogBundlerError logs a bundler/UserOp failure at the severity appropriate
// for its cause: Warn for on-chain reverts (see IsUserOpRevert) so they do not
// page Sentry, Error for real infra/AA failures that operators must see.
//
// Callers pass the error both for classification (the first argument) and,
// conventionally, as a tag value so the logged record includes the full error.
func LogBundlerError(lgr logger.Logger, err error, msg string, tags ...any) {
	if lgr == nil {
		return
	}
	if IsUserOpRevert(err) {
		lgr.Warn(msg, tags...)
		return
	}
	lgr.Error(msg, tags...)
}
