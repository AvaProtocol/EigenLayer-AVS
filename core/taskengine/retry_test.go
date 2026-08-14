package taskengine

import (
	"errors"
	"fmt"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// fakeNetErr is a net.Error whose Timeout() we control, to exercise the
// structured-timeout branch of classifyRetryableError independently of the
// error string.
type fakeNetErr struct{ timeout bool }

func (e fakeNetErr) Error() string   { return "dial tcp: something happened" }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return false }

func TestClassifyRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"structured net timeout", fakeNetErr{timeout: true}, retryClassTimeout},
		{"non-timeout net error is not classified by string", fakeNetErr{timeout: false}, ""},
		{"timeout keyword", errors.New("request timeout after 30s"), retryClassTimeout},
		{"context deadline", errors.New("context deadline exceeded"), retryClassTimeout},
		{"http 429", errors.New("server returned 429"), retryClassHTTP429},
		{"too many requests phrase", errors.New("Too Many Requests"), retryClassHTTP429},
		{"http 500", errors.New("unexpected status 500 from upstream"), retryClassHTTP5xx},
		{"http 503", errors.New("HTTP 503"), retryClassHTTP5xx},
		{"internal server error phrase", errors.New("Internal Server Error"), retryClassHTTP5xx},
		{"bad gateway phrase", errors.New("502 Bad Gateway"), retryClassHTTP5xx},
		{"connection refused", errors.New("dial: connection refused"), retryClassRPC},
		{"connection reset", errors.New("read: connection reset by peer"), retryClassRPC},
		{"no such host", errors.New("lookup foo: no such host"), retryClassRPC},
		{"eof", errors.New("unexpected EOF"), retryClassRPC},
		{"rpc keyword", errors.New("rpc call failed"), retryClassRPC},
		{"non-transient", errors.New("invalid ABI: field not found"), ""},
		{"4xx that is not 429 is not retryable", errors.New("400 bad request"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyRetryableError(tt.err); got != tt.want {
				t.Fatalf("classifyRetryableError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	timeoutErr := errors.New("i/o timeout")
	rpcErr := errors.New("connection refused")
	fatalErr := errors.New("nonsense")

	tests := []struct {
		name   string
		err    error
		policy *avsproto.RetryPolicy
		want   bool
	}{
		{"nil policy uses default classes", timeoutErr, nil, true},
		{"empty retry_on uses default classes", rpcErr, &avsproto.RetryPolicy{}, true},
		{"class explicitly enabled", timeoutErr, &avsproto.RetryPolicy{RetryOn: []string{retryClassTimeout}}, true},
		{"class not in list", rpcErr, &avsproto.RetryPolicy{RetryOn: []string{retryClassTimeout}}, false},
		{"unclassifiable error never retryable", fatalErr, &avsproto.RetryPolicy{}, false},
		{"unclassifiable error even with all classes", fatalErr, &avsproto.RetryPolicy{RetryOn: defaultRetryClasses}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableError(tt.err, tt.policy); got != tt.want {
				t.Fatalf("isRetryableError(%v, %v) = %v, want %v", tt.err, tt.policy, got, tt.want)
			}
		})
	}
}

// stepWithID builds a minimal execution step tagged with an id so tests can
// assert which attempt's step is returned.
func stepWithID(id string) *avsproto.Execution_Step {
	return &avsproto.Execution_Step{Id: id}
}

func TestExecuteWithRetry_SingleAttemptWhenNotConfigured(t *testing.T) {
	cases := []struct {
		name   string
		policy *avsproto.RetryPolicy
	}{
		{"nil policy", nil},
		{"max_attempts 0 treated as 1", &avsproto.RetryPolicy{}},
		{"max_attempts 1", &avsproto.RetryPolicy{MaxAttempts: 1, RetryOn: defaultRetryClasses}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls, sleeps int
			retryErr := errors.New("i/o timeout") // retryable class, but policy forbids more than one attempt
			step, err := executeWithRetry(tc.policy, func(time.Duration) { sleeps++ }, func() (*avsproto.Execution_Step, error) {
				calls++
				return stepWithID("s"), retryErr
			})
			if calls != 1 {
				t.Fatalf("expected exactly 1 attempt, got %d", calls)
			}
			if sleeps != 0 {
				t.Fatalf("expected no sleeps, got %d", sleeps)
			}
			if err != retryErr || step.GetId() != "s" {
				t.Fatalf("expected the single attempt's step and error, got step=%v err=%v", step, err)
			}
		})
	}
}

func TestExecuteWithRetry_SuccessFirstTryDoesNotRetry(t *testing.T) {
	var calls, sleeps int
	policy := &avsproto.RetryPolicy{MaxAttempts: 3, BackoffMs: 10}
	step, err := executeWithRetry(policy, func(time.Duration) { sleeps++ }, func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("ok"), nil
	})
	if calls != 1 || sleeps != 0 {
		t.Fatalf("expected 1 call and 0 sleeps, got calls=%d sleeps=%d", calls, sleeps)
	}
	if err != nil || step.GetId() != "ok" {
		t.Fatalf("expected success step, got step=%v err=%v", step, err)
	}
}

func TestExecuteWithRetry_RetriesThenSucceeds(t *testing.T) {
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 3, BackoffMs: 10, RetryOn: []string{retryClassTimeout}}

	step, err := executeWithRetry(policy, func(d time.Duration) { waits = append(waits, d) }, func() (*avsproto.Execution_Step, error) {
		calls++
		if calls < 3 {
			return stepWithID(fmt.Sprintf("fail-%d", calls)), errors.New("i/o timeout")
		}
		return stepWithID("success"), nil
	})

	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
	if err != nil || step.GetId() != "success" {
		t.Fatalf("expected final success step, got step=%v err=%v", step, err)
	}
	// Backoff doubles by the default multiplier (2.0) between attempts.
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	if len(waits) != len(want) {
		t.Fatalf("expected %d sleeps, got %d (%v)", len(want), len(waits), waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("sleep[%d] = %v, want %v (all: %v)", i, waits[i], want[i], waits)
		}
	}
}

func TestExecuteWithRetry_NonRetryableFailsImmediately(t *testing.T) {
	var calls, sleeps int
	policy := &avsproto.RetryPolicy{MaxAttempts: 5, BackoffMs: 10}
	fatal := errors.New("invalid ABI: field not found")
	step, err := executeWithRetry(policy, func(time.Duration) { sleeps++ }, func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("boom"), fatal
	})
	if calls != 1 || sleeps != 0 {
		t.Fatalf("expected 1 call and 0 sleeps for a non-retryable error, got calls=%d sleeps=%d", calls, sleeps)
	}
	if err != fatal || step.GetId() != "boom" {
		t.Fatalf("expected the failing step and error, got step=%v err=%v", step, err)
	}
}

func TestExecuteWithRetry_ExhaustsAndReturnsLastFailure(t *testing.T) {
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 2, BackoffMs: 5, RetryOn: []string{retryClassTimeout}}

	step, err := executeWithRetry(policy, func(d time.Duration) { waits = append(waits, d) }, func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID(fmt.Sprintf("attempt-%d", calls)), errors.New("i/o timeout")
	})

	if calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
	if len(waits) != 1 || waits[0] != 5*time.Millisecond {
		t.Fatalf("expected one 5ms sleep between the two attempts, got %v", waits)
	}
	if err == nil || step.GetId() != "attempt-2" {
		t.Fatalf("expected the last attempt's step and its error, got step=%v err=%v", step, err)
	}
}

func TestExecuteWithRetry_MaxBackoffCaps(t *testing.T) {
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{
		MaxAttempts:       4,
		BackoffMs:         100,
		BackoffMultiplier: 10,
		MaxBackoffMs:      150,
		RetryOn:           []string{retryClassTimeout},
	}
	_, _ = executeWithRetry(policy, func(d time.Duration) { waits = append(waits, d) }, func() (*avsproto.Execution_Step, error) {
		return stepWithID("x"), errors.New("i/o timeout")
	})

	// 3 sleeps for 4 attempts. Raw backoffs would be 100, 1000, 10000; the cap
	// clamps every wait to at most 150ms.
	want := []time.Duration{100 * time.Millisecond, 150 * time.Millisecond, 150 * time.Millisecond}
	if len(waits) != len(want) {
		t.Fatalf("expected %d sleeps, got %d (%v)", len(want), len(waits), waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("sleep[%d] = %v, want %v (all: %v)", i, waits[i], want[i], waits)
		}
	}
}
