package aggregator

import (
	"time"

	"github.com/getsentry/sentry-go"
)

const (
	InternalError = "Internal Error"
)

// goSafe runs a function in a goroutine-like wrapper that recovers panics and reports to Sentry if available.
// Use this for background tasks so panics are not silently lost.
func goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Best-effort report to Sentry when it's initialized.
				// Avoid importing Sentry at top-level of all files; use a helper to flush on shutdown.
				sentryRecover(r)
				// Re-panic so default crash behavior (and supervisors) still see it in dev/testing.
				panic(r)
			}
		}()
		fn()
	}()
}

// sentryRecover attempts to report a recovered panic to Sentry if the SDK is linked.
// Kept in a separate function to keep imports localized here.
func sentryRecover(rec interface{}) {
	// Use build-time import to Sentry in this single file to avoid circular deps.
	// If Sentry isn't initialized, this is a no-op.
	// Avoid adding as required dep elsewhere.
	// nolint:staticcheck // best-effort reporting only
	sentry.CurrentHub().Recover(rec)
}

// sentryFlushSafely flushes Sentry with a timeout if Sentry is present; otherwise no-op.
func sentryFlushSafely(timeout time.Duration) {
	// If Sentry has not been initialized, this will just return false quickly.
	_ = sentry.Flush(timeout)
}
