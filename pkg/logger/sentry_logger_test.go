package logger

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/getsentry/sentry-go"
)

// testTransport captures Sentry events in memory for assertions.
type testTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *testTransport) Configure(options sentry.ClientOptions) {}
func (t *testTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}
func (t *testTransport) Flush(timeout time.Duration) bool { return true }
func (t *testTransport) Close()                           {}

func (t *testTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	copied := make([]*sentry.Event, len(t.events))
	copy(copied, t.events)
	return copied
}

// initTestSentry initialises Sentry with an in-memory transport and returns it.
func initTestSentry(t *testing.T) *testTransport {
	t.Helper()
	transport := &testTransport{}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://key@sentry.io/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("sentry.Init failed: %v", err)
	}
	return transport
}

// spyLogger records every method call for delegation verification.
type spyLogger struct {
	mu    sync.Mutex
	calls []spyCall
}

type spyCall struct {
	method string
	msg    string
	tags   []any
}

func (s *spyLogger) record(method, msg string, tags []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spyCall{method: method, msg: msg, tags: tags})
}

func (s *spyLogger) getCalls() []spyCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]spyCall, len(s.calls))
	copy(copied, s.calls)
	return copied
}

func (s *spyLogger) Debug(msg string, tags ...any) { s.record("Debug", msg, tags) }
func (s *spyLogger) Debugf(t string, args ...any)  { s.record("Debugf", fmt.Sprintf(t, args...), nil) }
func (s *spyLogger) Info(msg string, tags ...any)  { s.record("Info", msg, tags) }
func (s *spyLogger) Infof(t string, args ...any)   { s.record("Infof", fmt.Sprintf(t, args...), nil) }
func (s *spyLogger) Warn(msg string, tags ...any)  { s.record("Warn", msg, tags) }
func (s *spyLogger) Warnf(t string, args ...any)   { s.record("Warnf", fmt.Sprintf(t, args...), nil) }
func (s *spyLogger) Error(msg string, tags ...any) { s.record("Error", msg, tags) }
func (s *spyLogger) Errorf(t string, args ...any)  { s.record("Errorf", fmt.Sprintf(t, args...), nil) }
func (s *spyLogger) Fatal(msg string, tags ...any) { s.record("Fatal", msg, tags) }
func (s *spyLogger) Fatalf(t string, args ...any)  { s.record("Fatalf", fmt.Sprintf(t, args...), nil) }
func (s *spyLogger) With(tags ...any) sdklogging.Logger {
	s.record("With", "", tags)
	return s
}

// --- Tests ---

func TestSentryLogger_DelegatesAllMethods(t *testing.T) {
	spy := &spyLogger{}
	sl := NewSentryLogger(spy, "test-service")

	sl.Debug("debug msg", "k", "v")
	sl.Debugf("debug %s", "fmt")
	sl.Info("info msg", "k", "v")
	sl.Infof("info %s", "fmt")
	sl.Warn("warn msg", "k", "v")
	sl.Warnf("warn %s", "fmt")
	sl.Error("error msg", "k", "v")
	sl.Errorf("error %s", "fmt")

	calls := spy.getCalls()
	expected := []string{"Debug", "Debugf", "Info", "Infof", "Warn", "Warnf", "Error", "Errorf"}
	if len(calls) != len(expected) {
		t.Fatalf("expected %d calls, got %d", len(expected), len(calls))
	}
	for i, e := range expected {
		if calls[i].method != e {
			t.Errorf("call %d: expected method %q, got %q", i, e, calls[i].method)
		}
	}
}

func TestSentryLogger_ErrorCapturesSentryEvent(t *testing.T) {
	transport := initTestSentry(t)
	spy := &spyLogger{}
	sl := NewSentryLogger(spy, "aggregator")

	testErr := errors.New("connection refused")
	sl.Error("rpc failed", "url", "http://localhost:8545", "err", testErr)

	sentry.Flush(2 * time.Second)
	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 sentry event, got %d", len(events))
	}

	event := events[0]

	// Check service tag
	if event.Tags["service"] != "aggregator" {
		t.Errorf("expected service tag %q, got %q", "aggregator", event.Tags["service"])
	}

	// Check extras contain the key-value pairs
	if event.Extra["url"] != "http://localhost:8545" {
		t.Errorf("expected extra url %q, got %q", "http://localhost:8545", event.Extra["url"])
	}

	// Check the exception uses the error value
	if len(event.Exception) == 0 {
		t.Fatal("expected at least one exception in sentry event")
	}
	if event.Exception[0].Value != "connection refused" {
		t.Errorf("expected exception value %q, got %q", "connection refused", event.Exception[0].Value)
	}
}

func TestSentryLogger_ErrorfCapturesSentryEvent(t *testing.T) {
	transport := initTestSentry(t)
	spy := &spyLogger{}
	sl := NewSentryLogger(spy, "operator")

	sl.Errorf("request failed with status %d", 500)

	sentry.Flush(2 * time.Second)
	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 sentry event, got %d", len(events))
	}

	event := events[0]

	if event.Tags["service"] != "operator" {
		t.Errorf("expected service tag %q, got %q", "operator", event.Tags["service"])
	}

	// Check formatted message is in extras
	if event.Extra["formatted_message"] != "request failed with status 500" {
		t.Errorf("expected formatted_message extra, got %v", event.Extra["formatted_message"])
	}
}

func TestSentryLogger_ErrorWithoutErrorValue(t *testing.T) {
	transport := initTestSentry(t)
	spy := &spyLogger{}
	sl := NewSentryLogger(spy, "test")

	sl.Error("something went wrong", "code", 42)

	sentry.Flush(2 * time.Second)
	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 sentry event, got %d", len(events))
	}

	// When no error value is provided, the message should be wrapped as the error
	if len(events[0].Exception) == 0 {
		t.Fatal("expected at least one exception")
	}
	if events[0].Exception[0].Value != "something went wrong" {
		t.Errorf("expected exception value %q, got %q", "something went wrong", events[0].Exception[0].Value)
	}
}

func TestSentryLogger_ErrorfWithErrorArg(t *testing.T) {
	transport := initTestSentry(t)
	spy := &spyLogger{}
	sl := NewSentryLogger(spy, "test")

	testErr := errors.New("timeout")
	sl.Errorf("operation failed: %v", testErr)

	sentry.Flush(2 * time.Second)
	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 sentry event, got %d", len(events))
	}

	if len(events[0].Exception) == 0 {
		t.Fatal("expected at least one exception")
	}
	// Should use the actual error, not the formatted string
	if events[0].Exception[0].Value != "timeout" {
		t.Errorf("expected exception value %q, got %q", "timeout", events[0].Exception[0].Value)
	}
}

func TestSentryLogger_WithReturnsSentryLogger(t *testing.T) {
	spy := &spyLogger{}
	sl := NewSentryLogger(spy, "test-service")

	child := sl.With("request_id", "abc123")

	sentryChild, ok := child.(*SentryLogger)
	if !ok {
		t.Fatalf("With() returned %T, expected *SentryLogger", child)
	}
	if sentryChild.serviceName != "test-service" {
		t.Errorf("child serviceName = %q, want %q", sentryChild.serviceName, "test-service")
	}
}

func TestSentryLogger_ImplementsLoggerInterface(t *testing.T) {
	// Compile-time check that SentryLogger satisfies sdklogging.Logger
	var _ sdklogging.Logger = (*SentryLogger)(nil)
}
