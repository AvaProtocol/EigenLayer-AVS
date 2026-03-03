package logger

import (
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// SentryLogger wraps an sdklogging.Logger and forwards Error/Errorf/Fatal/Fatalf
// calls to Sentry in addition to the underlying logger. If Sentry is not
// initialized, the CaptureException calls are safe no-ops.
type SentryLogger struct {
	inner       sdklogging.Logger
	serviceName string
}

// NewSentryLogger creates a SentryLogger that delegates all logging to inner
// and additionally reports Error/Fatal level messages to Sentry.
func NewSentryLogger(inner sdklogging.Logger, serviceName string) *SentryLogger {
	return &SentryLogger{
		inner:       inner,
		serviceName: serviceName,
	}
}

// --- Delegated pass-through methods ---

func (l *SentryLogger) Debug(msg string, tags ...any)       { l.inner.Debug(msg, tags...) }
func (l *SentryLogger) Debugf(template string, args ...any) { l.inner.Debugf(template, args...) }
func (l *SentryLogger) Info(msg string, tags ...any)        { l.inner.Info(msg, tags...) }
func (l *SentryLogger) Infof(template string, args ...any)  { l.inner.Infof(template, args...) }
func (l *SentryLogger) Warn(msg string, tags ...any)        { l.inner.Warn(msg, tags...) }
func (l *SentryLogger) Warnf(template string, args ...any)  { l.inner.Warnf(template, args...) }

// --- Error/Fatal methods: delegate + capture to Sentry ---

func (l *SentryLogger) Error(msg string, tags ...any) {
	l.inner.Error(msg, tags...)
	l.captureToSentry(msg, tags)
}

func (l *SentryLogger) Errorf(template string, args ...any) {
	l.inner.Errorf(template, args...)
	l.captureFormattedToSentry(template, args)
}

func (l *SentryLogger) Fatal(msg string, tags ...any) {
	l.captureToSentry(msg, tags)
	sentry.Flush(2 * time.Second)
	l.inner.Fatal(msg, tags...)
}

func (l *SentryLogger) Fatalf(template string, args ...any) {
	l.captureFormattedToSentry(template, args)
	sentry.Flush(2 * time.Second)
	l.inner.Fatalf(template, args...)
}

// With returns a new SentryLogger wrapping the delegated With() result,
// preserving the SentryLogger chain.
func (l *SentryLogger) With(tags ...any) sdklogging.Logger {
	return &SentryLogger{
		inner:       l.inner.With(tags...),
		serviceName: l.serviceName,
	}
}

// captureToSentry sends a structured error event to Sentry.
// It extracts key-value pairs from tags as Sentry extras, and uses the first
// error value found in tags as the Sentry exception. If no error is found,
// the message string is wrapped as the error.
func (l *SentryLogger) captureToSentry(msg string, tags []any) {
	var sentryErr error
	extras := make(map[string]any)

	for i := 0; i+1 < len(tags); i += 2 {
		key := fmt.Sprintf("%v", tags[i])
		value := tags[i+1]

		if err, ok := value.(error); ok && sentryErr == nil {
			sentryErr = err
		}
		extras[key] = value
	}

	// Handle odd trailing tag
	if len(tags)%2 != 0 {
		extras["unpaired_tag"] = tags[len(tags)-1]
	}

	if sentryErr == nil {
		sentryErr = fmt.Errorf("%s", msg)
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("service", l.serviceName)
		scope.SetExtra("log_message", msg)
		scope.SetExtras(extras)
		sentry.CaptureException(sentryErr)
	})
}

// captureFormattedToSentry sends a formatted error message to Sentry.
// It checks if any arg is an error to use as the exception; otherwise wraps
// the formatted string.
func (l *SentryLogger) captureFormattedToSentry(template string, args []any) {
	var sentryErr error
	for _, arg := range args {
		if err, ok := arg.(error); ok {
			sentryErr = err
			break
		}
	}

	formatted := fmt.Sprintf(template, args...)
	if sentryErr == nil {
		sentryErr = fmt.Errorf("%s", formatted)
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("service", l.serviceName)
		scope.SetExtra("formatted_message", formatted)
		sentry.CaptureException(sentryErr)
	})
}
