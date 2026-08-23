package taskengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeNetErr is a net.Error whose Timeout() we control, to exercise the
// structured-timeout branch of classifyRetryableError independently of the
// error string.
type fakeNetErr struct{ timeout bool }

func (e fakeNetErr) Error() string   { return "dial tcp: something happened" }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return false }

// recordingSleep is a sleepFunc that records what it was asked to wait for and
// returns immediately, so retry tests cost no wall time.
func recordingSleep(waits *[]time.Duration) sleepFunc {
	return func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
}

func TestClassifyRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"structured net timeout", fakeNetErr{timeout: true}, retryClassTimeout},
		{"non-timeout net error is not classified by string", fakeNetErr{timeout: false}, ""},
		{"context deadline sentinel", context.DeadlineExceeded, retryClassTimeout},
		{"timeout keyword", errors.New("request timeout after 30s"), retryClassTimeout},
		{"context deadline text", errors.New("context deadline exceeded"), retryClassTimeout},
		{"http 429", errors.New("server returned status 429"), retryClassHTTP429},
		{"too many requests phrase", errors.New("Too Many Requests"), retryClassHTTP429},
		{"http 500", errors.New("unexpected status 500 from upstream"), retryClassHTTP5xx},
		{"http 503", errors.New("HTTP 503"), retryClassHTTP5xx},
		{"graphql non-200 status", errors.New("graphql: server returned a non-200 status code: 502"), retryClassHTTP5xx},
		{"internal server error phrase", errors.New("Internal Server Error"), retryClassHTTP5xx},
		{"bad gateway phrase", errors.New("Bad Gateway"), retryClassHTTP5xx},
		{"connection refused", errors.New("dial: connection refused"), retryClassRPC},
		{"connection reset", errors.New("read: connection reset by peer"), retryClassRPC},
		{"no such host", errors.New("lookup foo: no such host"), retryClassRPC},
		{"eof", errors.New("unexpected EOF"), retryClassRPC},
		{"stringified transient grpc status", errors.New("rpc error: code = Unavailable desc = connection closed"), retryClassRPC},
		{"non-transient", errors.New("invalid ABI: field not found"), ""},
		{"4xx that is not 429 is not retryable", errors.New("status 400 bad request"), ""},

		// Regressions: a bare substring match read these as transient.
		{
			"the word rpc alone is not a retry signal",
			errors.New("rpc call failed: unsupported method"),
			"",
		},
		{
			"3-digit number in a URL is not a status code",
			errors.New("GET https://api.example.com/v2/tokens?limit=500 returned status 404 not found"),
			"",
		},
		{
			"429 inside a URL is not a rate limit",
			errors.New("GET https://api.example.com/pools/429 returned status 404"),
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyRetryableError(tt.err); got != tt.want {
				t.Fatalf("classifyRetryableError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestClassifyGRPCStatus pins the typed-status path: permanent codes must not
// burn the retry budget just because status.Errorf renders as "rpc error: ...".
func TestClassifyGRPCStatus(t *testing.T) {
	tests := []struct {
		code codes.Code
		want string
	}{
		{codes.InvalidArgument, ""},
		{codes.NotFound, ""},
		{codes.PermissionDenied, ""},
		{codes.Unauthenticated, ""},
		{codes.FailedPrecondition, ""},
		{codes.Internal, ""},
		{codes.Unavailable, retryClassRPC},
		{codes.DeadlineExceeded, retryClassTimeout},
		{codes.ResourceExhausted, retryClassHTTP429},
	}
	for _, tt := range tests {
		t.Run(tt.code.String(), func(t *testing.T) {
			err := status.Errorf(tt.code, "task id is malformed")
			if got := classifyRetryableError(err); got != tt.want {
				t.Fatalf("classifyRetryableError(%v) = %q, want %q", err, got, tt.want)
			}
		})
	}
}

func TestRetryClassEnabled(t *testing.T) {
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
			// Same decision executeWithRetry makes: classify the attempt, then
			// ask whether the policy opted into that class.
			got := retryClassEnabled(retryClassFor(nil, tt.err), tt.policy)
			if got != tt.want {
				t.Fatalf("retryClassEnabled(retryClassFor(nil, %v), %v) = %v, want %v", tt.err, tt.policy, got, tt.want)
			}
		})
	}
}

// TestRetryClassFor_FailedStepWithoutError covers the REST runner's shape: an
// HTTP 4xx/5xx is reported as (step, nil) with success=false, so a classifier
// that only looked at err would make http_5xx retries dead code.
func TestRetryClassFor_FailedStepWithoutError(t *testing.T) {
	restStep := func(statusCode int, success bool) *avsproto.Execution_Step {
		data, err := structpb.NewValue(map[string]interface{}{
			"status":     statusCode,
			"statusText": "",
			"url":        "https://api.example.com/v2/tokens?limit=500",
			"data":       "",
		})
		if err != nil {
			t.Fatalf("structpb.NewValue: %v", err)
		}
		return &avsproto.Execution_Step{
			Id:      "rest1",
			Success: success,
			Error:   fmt.Sprintf("HTTP %d", statusCode),
			OutputData: &avsproto.Execution_Step_RestApi{
				RestApi: &avsproto.RestAPINode_Output{Data: data},
			},
		}
	}

	tests := []struct {
		name string
		step *avsproto.Execution_Step
		err  error
		want string
	}{
		{"successful step", restStep(200, true), nil, ""},
		{"502 from the response envelope", restStep(502, false), nil, retryClassHTTP5xx},
		{"429 from the response envelope", restStep(429, false), nil, retryClassHTTP429},
		{"404 is not retryable despite the limit=500 url", restStep(404, false), nil, ""},
		{"a returned error still wins", restStep(200, true), errors.New("i/o timeout"), retryClassTimeout},
		{"failed step with only a message", &avsproto.Execution_Step{Success: false, Error: "connection refused"}, nil, retryClassRPC},
		{"nil step, nil error", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryClassFor(tt.step, tt.err); got != tt.want {
				t.Fatalf("retryClassFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

// stepWithID builds a minimal execution step tagged with an id so tests can
// assert which attempt's step is returned.
func stepWithID(id string) *avsproto.Execution_Step {
	return &avsproto.Execution_Step{Id: id, Success: false}
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
			var calls int
			var waits []time.Duration
			retryErr := errors.New("i/o timeout") // retryable class, but policy forbids more than one attempt
			step, err := executeWithRetry(context.Background(), tc.policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
				calls++
				return stepWithID("s"), retryErr
			})
			if calls != 1 {
				t.Fatalf("expected exactly 1 attempt, got %d", calls)
			}
			if len(waits) != 0 {
				t.Fatalf("expected no sleeps, got %v", waits)
			}
			if err != retryErr || step.GetId() != "s" {
				t.Fatalf("expected the single attempt's step and error, got step=%v err=%v", step, err)
			}
		})
	}
}

func TestExecuteWithRetry_SuccessFirstTryDoesNotRetry(t *testing.T) {
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 3, BackoffMs: 10}
	step, err := executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		return &avsproto.Execution_Step{Id: "ok", Success: true}, nil
	})
	if calls != 1 || len(waits) != 0 {
		t.Fatalf("expected 1 call and 0 sleeps, got calls=%d waits=%v", calls, waits)
	}
	if err != nil || step.GetId() != "ok" {
		t.Fatalf("expected success step, got step=%v err=%v", step, err)
	}
	if step.GetLog() != "" {
		t.Fatalf("a single-attempt step must not be annotated as retried, got log %q", step.GetLog())
	}
}

func TestExecuteWithRetry_RetriesThenSucceeds(t *testing.T) {
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 3, BackoffMs: 10, RetryOn: []string{retryClassTimeout}}

	step, err := executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		if calls < 3 {
			return stepWithID(fmt.Sprintf("fail-%d", calls)), errors.New("i/o timeout")
		}
		return &avsproto.Execution_Step{Id: "success", Success: true}, nil
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
	// Observability: a step that took 3 attempts must not read as one fast success.
	if !strings.Contains(step.GetLog(), "Retry: 3 attempts") {
		t.Fatalf("expected the step log to record the retries, got %q", step.GetLog())
	}
}

// TestExecuteWithRetry_DefaultsBackoffWhenUnset covers the most natural policy a
// user writes. With no default, backoff started at 0, the sleep was skipped
// entirely, and one 429 became five immediate hits.
func TestExecuteWithRetry_DefaultsBackoffWhenUnset(t *testing.T) {
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 4}

	_, _ = executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		return stepWithID("x"), errors.New("i/o timeout")
	})

	want := []time.Duration{DefaultRetryBackoff, 2 * DefaultRetryBackoff, 4 * DefaultRetryBackoff}
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
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 5, BackoffMs: 10}
	fatal := errors.New("invalid ABI: field not found")
	step, err := executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("boom"), fatal
	})
	if calls != 1 || len(waits) != 0 {
		t.Fatalf("expected 1 call and 0 sleeps for a non-retryable error, got calls=%d waits=%v", calls, waits)
	}
	if err != fatal || step.GetId() != "boom" {
		t.Fatalf("expected the failing step and error, got step=%v err=%v", step, err)
	}
}

func TestExecuteWithRetry_ExhaustsAndReturnsLastFailure(t *testing.T) {
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 2, BackoffMs: 5, RetryOn: []string{retryClassTimeout}}

	step, err := executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
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
	_, _ = executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
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

// TestNextBackoff_SaturatesInsteadOfOverflowing pins the growth function. When
// only a local copy of the wait was capped, `backoff` kept multiplying, wrapped
// to a negative Duration at ~34 attempts, and every later sleep silently became
// a no-op — turning the tail of the retry budget into a hot loop.
func TestNextBackoff_SaturatesInsteadOfOverflowing(t *testing.T) {
	backoff := time.Second
	for i := 0; i < 200; i++ {
		backoff = nextBackoff(backoff, 2.0, MaxRetryBackoff)
		if backoff <= 0 {
			t.Fatalf("backoff went non-positive (%v) after %d growth steps", backoff, i+1)
		}
		if backoff > MaxRetryBackoff {
			t.Fatalf("backoff %v exceeded the %v cap after %d growth steps", backoff, MaxRetryBackoff, i+1)
		}
	}
	if backoff != MaxRetryBackoff {
		t.Fatalf("expected backoff to saturate at %v, got %v", MaxRetryBackoff, backoff)
	}
}

// TestExecuteWithRetry_ClampsAboveCeilingPolicy is defense in depth: creation
// rejects such a policy, but a task stored before validation existed must still
// be bounded at execution time.
func TestExecuteWithRetry_ClampsAboveCeilingPolicy(t *testing.T) {
	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 100, BackoffMs: 600000}

	_, _ = executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("x"), errors.New("i/o timeout")
	})

	if calls != MaxRetryAttempts {
		t.Fatalf("expected attempts to be clamped to %d, got %d", MaxRetryAttempts, calls)
	}
	for i, w := range waits {
		if w > MaxRetryBackoff {
			t.Fatalf("sleep[%d] = %v exceeds the %v cap (all: %v)", i, w, MaxRetryBackoff, waits)
		}
	}
}

// TestExecuteWithRetry_StopsWhenContextCancelled: the async queue path has no
// deadline, but a request-scoped execution must be able to abandon a backoff.
func TestExecuteWithRetry_StopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	policy := &avsproto.RetryPolicy{MaxAttempts: 5, BackoffMs: 10}
	_, err := executeWithRetry(ctx, policy, nil, contextSleep, func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("x"), errors.New("i/o timeout")
	})

	if calls != 1 {
		t.Fatalf("expected a cancelled context to stop after the first attempt, got %d attempts", calls)
	}
	if err == nil {
		t.Fatal("expected the last attempt's error to be returned")
	}
}

// TestExecuteWithRetry_HonorsExecutionBudget covers the fan-out case per-node
// validation cannot see: a Loop runs the same retryable runner once per element.
func TestExecuteWithRetry_HonorsExecutionBudget(t *testing.T) {
	budget := newRetryBudget(25 * time.Millisecond)
	policy := &avsproto.RetryPolicy{MaxAttempts: 5, BackoffMs: 10, BackoffMultiplier: 1}

	var calls int
	var waits []time.Duration
	_, _ = executeWithRetry(context.Background(), policy, budget, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("x"), errors.New("i/o timeout")
	})

	// 25ms of budget covers two 10ms backoffs, so the third is refused and the
	// node stops after 3 attempts rather than its configured 5.
	if calls != 3 {
		t.Fatalf("expected the budget to cut retries off after 3 attempts, got %d (waits %v)", calls, waits)
	}
	if budget.remaining() != 5*time.Millisecond {
		t.Fatalf("expected 5ms of budget left, got %v", budget.remaining())
	}

	// A later node in the same execution inherits the drained budget.
	calls = 0
	_, _ = executeWithRetry(context.Background(), policy, budget, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		return stepWithID("y"), errors.New("i/o timeout")
	})
	if calls != 1 {
		t.Fatalf("expected a drained budget to leave one attempt, got %d", calls)
	}
}

// TestExecuteWithRetry_RetriesFailedRESTStepWithoutError pins the REST runner
// shape through the loop that actually retries: HTTP ≥400 is (step, nil) with
// success=false, so a classifier that only looked at err would never sleep.
func TestExecuteWithRetry_RetriesFailedRESTStepWithoutError(t *testing.T) {
	restFail := func(statusCode int) *avsproto.Execution_Step {
		data, err := structpb.NewValue(map[string]interface{}{
			"status":     statusCode,
			"statusText": "",
			"url":        "https://api.example.com/v2/tokens?limit=500",
			"data":       "",
		})
		if err != nil {
			t.Fatalf("structpb.NewValue: %v", err)
		}
		return &avsproto.Execution_Step{
			Id:      "rest1",
			Success: false,
			Error:   fmt.Sprintf("HTTP %d", statusCode),
			OutputData: &avsproto.Execution_Step_RestApi{
				RestApi: &avsproto.RestAPINode_Output{Data: data},
			},
		}
	}

	var calls int
	var waits []time.Duration
	policy := &avsproto.RetryPolicy{MaxAttempts: 3, BackoffMs: 10, RetryOn: []string{retryClassHTTP5xx}}
	step, err := executeWithRetry(context.Background(), policy, nil, recordingSleep(&waits), func() (*avsproto.Execution_Step, error) {
		calls++
		return restFail(502), nil
	})
	if err != nil {
		t.Fatalf("REST reports HTTP failures as (step, nil), got err %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts for a 502 envelope, got %d", calls)
	}
	if len(waits) != 2 {
		t.Fatalf("expected 2 sleeps, got %v", waits)
	}
	if step.GetId() != "rest1" {
		t.Fatalf("expected the last REST step, got %v", step)
	}
}
