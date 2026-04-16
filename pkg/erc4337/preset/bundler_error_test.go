package preset

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

func TestIsUserOpRevert(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("dial tcp: connection refused"), false},
		{"AA21 prefund", errors.New("AA21 didn't pay prefund"), false},
		{"AA25 nonce", errors.New("AA25 invalid account nonce"), false},
		{"direct marker", errors.New("UserOp execution failed (success=false in UserOperationEvent) - tx: 0xabc"), true},
		{"wrapped marker", fmt.Errorf("UserOp execution failed: %w", errors.New("UserOp execution failed (success=false in UserOperationEvent) - tx: 0xabc")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUserOpRevert(tc.err); got != tc.want {
				t.Errorf("IsUserOpRevert(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// bundlerErrorSpyLogger captures which severity method was invoked for LogBundlerError.
type bundlerErrorSpyLogger struct {
	mu    sync.Mutex
	calls []string // method names in order
}

func (s *bundlerErrorSpyLogger) record(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method)
}

func (s *bundlerErrorSpyLogger) Debug(string, ...any)          {}
func (s *bundlerErrorSpyLogger) Debugf(string, ...any)         {}
func (s *bundlerErrorSpyLogger) Info(string, ...any)           {}
func (s *bundlerErrorSpyLogger) Infof(string, ...any)          {}
func (s *bundlerErrorSpyLogger) Warn(string, ...any)           { s.record("Warn") }
func (s *bundlerErrorSpyLogger) Warnf(string, ...any)          { s.record("Warn") }
func (s *bundlerErrorSpyLogger) Error(string, ...any)          { s.record("Error") }
func (s *bundlerErrorSpyLogger) Errorf(string, ...any)         { s.record("Error") }
func (s *bundlerErrorSpyLogger) Fatal(string, ...any)          {}
func (s *bundlerErrorSpyLogger) Fatalf(string, ...any)         {}
func (s *bundlerErrorSpyLogger) With(...any) sdklogging.Logger { return s }

func TestLogBundlerError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"on-chain revert → Warn", errors.New("UserOp execution failed (success=false in UserOperationEvent) - tx: 0xabc"), "Warn"},
		{"AA21 infra → Error", errors.New("AA21 didn't pay prefund"), "Error"},
		{"bundler down → Error", errors.New("dial tcp: connection refused"), "Error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &bundlerErrorSpyLogger{}
			LogBundlerError(spy, tc.err, "bundler failed", "err", tc.err)
			if len(spy.calls) != 1 || spy.calls[0] != tc.want {
				t.Errorf("expected single %s call, got %v", tc.want, spy.calls)
			}
		})
	}
}

func TestLogBundlerError_NilLogger(t *testing.T) {
	// Must not panic.
	LogBundlerError(nil, errors.New("anything"), "msg")
}
