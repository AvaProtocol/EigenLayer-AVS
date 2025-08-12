package aggregator

import (
	"fmt"
	"log"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/getsentry/sentry-go"
)

const (
	InternalError = "Internal Error"
)

// repanicOnPanic controls whether goSafe should re-panic after reporting to Sentry.
// Default is false (no re-panic) and can be enabled in development for fail-fast.
var repanicOnPanic bool

// SetRepanicOnPanic toggles whether recovered panics should be re-thrown.
func SetRepanicOnPanic(v bool) { repanicOnPanic = v }

// packageLogger allows package-level helpers to log using the project's structured logger when available.
var packageLogger sdklogging.Logger

// SetPackageLogger injects the structured logger for use in package-level helpers.
func SetPackageLogger(l sdklogging.Logger) { packageLogger = l }

// goSafe runs a function in a goroutine-like wrapper that recovers panics and reports to Sentry if available.
// Use this for background tasks so panics are not silently lost.
func goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Best-effort report to Sentry when it's initialized.
				// Avoid importing Sentry at top-level of all files; use a helper to flush on shutdown.
				sentryRecover(r)
				if repanicOnPanic {
					// Re-panic so default crash behavior (and supervisors) still see it in dev/testing.
					panic(r)
				}
				// In production, log and continue to keep the process alive.
				if packageLogger != nil {
					packageLogger.Error("Recovered panic in goSafe", "panic", fmt.Sprintf("%v", r))
				} else {
					log.Printf("Recovered panic in goSafe: %v", r)
				}
			}
		}()
		fn()
	}()
}

// sentryRecover attempts to report a recovered panic to Sentry if the SDK is linked.
// Kept in a separate function to keep imports localized here.
func sentryRecover(rec interface{}) {
	// Convert recovered panic to error and capture via Sentry
	var err error
	switch v := rec.(type) {
	case error:
		err = v
	default:
		err = fmt.Errorf("%v", v)
	}
	sentry.CaptureException(err)
}

// sentryFlushSafely flushes Sentry with a timeout if Sentry is present; otherwise no-op.
func sentryFlushSafely(timeout time.Duration) {
	// If Sentry has not been initialized, this will just return false quickly.
	_ = sentry.Flush(timeout)
}
