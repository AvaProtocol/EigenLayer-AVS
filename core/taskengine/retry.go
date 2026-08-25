package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Retry error classes. A RetryPolicy.retry_on entry must match one of these to
// enable retries for that class of failure.
const (
	retryClassTimeout = "timeout"
	retryClassHTTP429 = "http_429"
	retryClassHTTP5xx = "http_5xx"
	retryClassRPC     = "rpc_error"
)

// defaultRetryClasses is used when a RetryPolicy sets max_attempts but leaves
// retry_on empty: all transient classes are eligible.
var defaultRetryClasses = []string{retryClassTimeout, retryClassHTTP429, retryClassHTTP5xx, retryClassRPC}

// defaultBackoffMultiplier applies when a policy enables retries without setting one.
const defaultBackoffMultiplier = 2.0

// urlRe strips URLs from an error message before any status-code sniffing.
// Without this, an incidental number inside a URL is indistinguishable from a
// response status: "GET https://api.example.com/tokens?limit=500 returned 404"
// classified as http_5xx purely because of the query parameter.
var urlRe = regexp.MustCompile(`https?://\S+`)

// httpStatusRe matches a 3-digit status only in a status-like context ("HTTP
// 503", "status code: 502", "returned status 500"). Anchoring to a keyword is
// what keeps arbitrary 3-digit numbers in prose from being read as statuses.
var httpStatusRe = regexp.MustCompile(`(?:status code|statuscode|status|http)[^0-9]{0,12}(\d{3})\b`)

// eofRe word-bounds "eof" so it matches "unexpected EOF" but not a token that
// merely contains those letters.
var eofRe = regexp.MustCompile(`\beof\b`)

// grpcProseRe matches a *stringified* gRPC status (an error that was rendered
// to text somewhere upstream and so no longer satisfies status.FromError).
// Only the codes that are genuinely transient are listed.
var grpcProseRe = regexp.MustCompile(`rpc error: code = (unavailable|deadlineexceeded|resourceexhausted)`)

// classifyRetryableError maps err to one of the retry classes, or "" if it is
// not a recognizably transient failure.
//
// Order matters: structured signals (net.Error, context.DeadlineExceeded, gRPC
// status codes) are consulted before any string matching, because they are the
// only ones that cannot be spoofed by the shape of a message. String matching
// is the fallback for runners that wrap upstream failures as plain errors, and
// it deliberately errs toward *not* retrying when unsure — an unrecognized
// error returns "".
func classifyRetryableError(err error) string {
	if err == nil {
		return ""
	}

	// Structured network timeouts — most reliable signal.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return retryClassTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return retryClassTimeout
	}

	// Typed gRPC status. Substring-matching "rpc" here was the old behavior and
	// it classified every status.Errorf as retryable — including permanent
	// InvalidArgument / NotFound / PermissionDenied, which this codebase returns
	// widely. Read the code instead and allowlist the transient ones.
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable:
			return retryClassRPC
		case codes.DeadlineExceeded:
			return retryClassTimeout
		case codes.ResourceExhausted:
			// The canonical gRPC → HTTP mapping for a rate limit is 429, so a
			// user who turns http_429 off gets no gRPC rate-limit retries either.
			return retryClassHTTP429
		default:
			// Any other code is a definite answer from the server, not a blip.
			return ""
		}
	}

	msg := urlRe.ReplaceAllString(strings.ToLower(err.Error()), " ")

	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return retryClassTimeout
	}
	if grpcProseRe.MatchString(msg) {
		return retryClassRPC
	}
	if code := statusCodeFromMessage(msg); code > 0 {
		if class := classifyHTTPStatus(code); class != "" {
			return class
		}
	}
	if strings.Contains(msg, "too many requests") || strings.Contains(msg, "rate limit") {
		return retryClassHTTP429
	}
	if strings.Contains(msg, "internal server error") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "gateway timeout") {
		return retryClassHTTP5xx
	}
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "no such host") ||
		eofRe.MatchString(msg) {
		return retryClassRPC
	}
	return ""
}

// statusCodeFromMessage extracts an HTTP status code from a message that names
// one in a status-like context. Returns 0 when the message carries no status.
func statusCodeFromMessage(msg string) int {
	m := httpStatusRe.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	code := 0
	for _, ch := range m[1] {
		code = code*10 + int(ch-'0')
	}
	return code
}

// classifyHTTPStatus maps an HTTP status code to a retry class. Only 429 and
// 5xx are transient; every other status is the server's final answer.
func classifyHTTPStatus(code int) string {
	switch {
	case code == 429:
		return retryClassHTTP429
	case code >= 500 && code <= 599:
		return retryClassHTTP5xx
	}
	return ""
}

// retryClassFor classifies the outcome of one attempt.
//
// A returned error is not the only way a node fails: the REST runner reports an
// HTTP 4xx/5xx by returning (step, nil) with step.Success == false, so looking
// only at err would make http_429 / http_5xx retries dead code for the node
// they matter most for. When the step carries a structured status code, that
// number is used directly — no prose sniffing.
func retryClassFor(step *avsproto.Execution_Step, err error) string {
	if err != nil {
		return classifyRetryableError(err)
	}
	if step == nil || step.GetSuccess() {
		return ""
	}
	if code := httpStatusFromStep(step); code > 0 {
		return classifyHTTPStatus(code)
	}
	if stepErr := step.GetError(); stepErr != "" {
		return classifyRetryableError(errors.New(stepErr))
	}
	return ""
}

// httpStatusFromStep reads the HTTP status the REST runner records in its
// standard response envelope ({status, statusText, url, headers, data}).
// Returns 0 for any step that carries no such envelope.
func httpStatusFromStep(step *avsproto.Execution_Step) int {
	restOut := step.GetRestApi()
	if restOut == nil {
		return 0
	}
	structVal, ok := restOut.GetData().GetKind().(*structpb.Value_StructValue)
	if !ok || structVal.StructValue == nil {
		return 0
	}
	statusVal, ok := structVal.StructValue.GetFields()["status"]
	if !ok {
		return 0
	}
	num, ok := statusVal.GetKind().(*structpb.Value_NumberValue)
	if !ok {
		return 0
	}
	return int(num.NumberValue)
}

// retryClassEnabled reports whether policy opts into retrying class.
func retryClassEnabled(class string, policy *avsproto.RetryPolicy) bool {
	if class == "" {
		return false
	}
	enabled := policy.GetRetryOn()
	if len(enabled) == 0 {
		enabled = defaultRetryClasses
	}
	for _, c := range enabled {
		if c == class {
			return true
		}
	}
	return false
}

// retryBudget bounds the total time one execution may spend sleeping between
// retries, across every node it runs.
//
// Per-node validation (limits.go) bounds a single node's worst case, but a Loop
// runs its retryable runner once per element — up to MaxLoopIterations times —
// so the per-node bound alone still multiplies. This is the execution-wide
// backstop: once the budget is spent, later nodes stop retrying and fail on
// their first attempt rather than extending the execution further.
type retryBudget struct {
	mu   sync.Mutex
	left time.Duration
}

func newRetryBudget(total time.Duration) *retryBudget {
	return &retryBudget{left: total}
}

// reserve deducts d from the budget, reporting false (and deducting nothing)
// when the remaining budget cannot cover it.
func (b *retryBudget) reserve(d time.Duration) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if d > b.left {
		return false
	}
	b.left -= d
	return true
}

// remaining reports the unspent budget. Test/observability helper.
func (b *retryBudget) remaining() time.Duration {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.left
}

// sleepFunc waits for d, returning ctx.Err() if the context is cancelled first.
type sleepFunc func(ctx context.Context, d time.Duration) error

// contextSleep is the production sleepFunc: an interruptible sleep, so a
// cancelled request aborts a pending backoff instead of holding the execution
// open for the full delay.
func contextSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// executeWithRetry runs attempt up to policy.max_attempts times, sleeping with
// exponential backoff between attempts that fail with a retryable error. It
// returns the result of the final attempt. A nil policy, or max_attempts <= 1,
// means exactly one attempt — identical to the pre-retry behavior.
//
// Every bound here is also enforced at task creation (validateRetryPolicy), so
// a stored task cannot carry a policy that exceeds them; the clamps below are
// defense in depth for tasks created before validation existed and for internal
// callers that bypass CreateTask.
//
// This is intended for idempotent read/off-chain nodes only; the caller is
// responsible for restricting it to the retry allowlist. On-chain write nodes
// must not be retried here (double-spend risk — see issue #676).
func executeWithRetry(
	ctx context.Context,
	policy *avsproto.RetryPolicy,
	budget *retryBudget,
	sleep sleepFunc,
	attempt func() (*avsproto.Execution_Step, error),
) (*avsproto.Execution_Step, error) {
	maxAttempts := 1
	if policy != nil && policy.GetMaxAttempts() > 1 {
		maxAttempts = int(policy.GetMaxAttempts())
		if maxAttempts > MaxRetryAttempts {
			maxAttempts = MaxRetryAttempts
		}
	}

	// max_backoff_ms is optional, but the ceiling is not: `backoff` itself is
	// capped (not just the local wait), so repeated multiplication cannot run
	// away. Uncapped it overflowed to a negative Duration at ~34 attempts and
	// every later "sleep" silently became a no-op.
	maxBackoff := time.Duration(policy.GetMaxBackoffMs()) * time.Millisecond
	if maxBackoff <= 0 || maxBackoff > MaxRetryBackoff {
		maxBackoff = MaxRetryBackoff
	}

	// backoff_ms has no documented default, and 0 meant "retry with no delay at
	// all" — so the most natural policy a user writes, {max_attempts: 5}, turned
	// one 429 into five immediate hits. Default it whenever retries are on.
	backoff := time.Duration(policy.GetBackoffMs()) * time.Millisecond
	if backoff <= 0 {
		backoff = DefaultRetryBackoff
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	multiplier := policy.GetBackoffMultiplier()
	if multiplier <= 0 {
		multiplier = defaultBackoffMultiplier
	}

	startedAt := time.Now()
	var step *avsproto.Execution_Step
	var err error
	attempts := 0

	for i := 0; i < maxAttempts; i++ {
		step, err = attempt()
		attempts++

		if i == maxAttempts-1 {
			break
		}
		if !retryClassEnabled(retryClassFor(step, err), policy) {
			break
		}
		// A cancelled iteration (loop collector timeout) must not reserve
		// budget or start another attempt the caller is no longer waiting for.
		if ctx.Err() != nil {
			break
		}
		// Reserve before sleep so a sibling node in the same execution cannot
		// also claim this delay. A ctx cancel mid-backoff still keeps the
		// deduction: the execution is ending and the budget dies with it.
		if !budget.reserve(backoff) {
			break
		}
		if sleepErr := sleep(ctx, backoff); sleepErr != nil {
			// Cancelled or deadline-exceeded: return the last attempt's result
			// instead of starting work the caller is no longer waiting for.
			break
		}
		backoff = nextBackoff(backoff, multiplier, maxBackoff)
	}

	if attempts > 1 {
		annotateRetriedStep(step, attempts, time.Since(startedAt))
	}
	return step, err
}

// nextBackoff grows cur by multiplier, saturating at max. Computed in float64
// and bounds-checked before the conversion so an overflowing product clamps
// instead of wrapping to a negative Duration.
func nextBackoff(cur time.Duration, multiplier float64, max time.Duration) time.Duration {
	next := float64(cur) * multiplier
	if math.IsNaN(next) || math.IsInf(next, 0) || next >= float64(max) {
		return max
	}
	// A positive product below 1ns truncates to Duration 0. Treating that as
	// overflow jumped the next wait to max (1ms × 1e-10 → 0 → 30s). Keep a
	// 1ns floor so growth stays monotonic and tiny multipliers cannot skip
	// to the cap. Non-positive products (negative multiplier, already
	// rejected at create) also stay at 1ns rather than jumping to max.
	if next < 1 {
		return 1
	}
	return time.Duration(next)
}

// annotateRetriedStep records that a step took more than one attempt.
//
// Only the final attempt's step is returned, so without this a node that
// retried four times over 30s would be indistinguishable from one fast success:
// start_at is rewound to the first attempt so end_at - start_at is the true
// elapsed time, and the attempt count goes in the step log.
func annotateRetriedStep(step *avsproto.Execution_Step, attempts int, elapsed time.Duration) {
	if step == nil {
		return
	}
	endAt := step.GetEndAt()
	if endAt == 0 {
		endAt = time.Now().UnixMilli()
		step.EndAt = endAt
	}
	step.StartAt = endAt - elapsed.Milliseconds()

	note := fmt.Sprintf("Retry: %d attempts over %s (per-node retry_policy)", attempts, elapsed.Round(time.Millisecond))
	if step.GetLog() == "" {
		step.Log = note
	} else {
		step.Log = step.GetLog() + "\n" + note
	}
}

// nodeSupportsRetry reports whether a node's runner is on the retry allowlist:
// idempotent read/off-chain work that is safe to re-invoke.
//
// A Loop counts when — and only when — its own runner is one of those; a Loop
// wrapping ContractWrite or EthTransfer is not retryable for the same
// double-spend reason the top-level write nodes are not (#676).
func nodeSupportsRetry(node *avsproto.TaskNode) bool {
	if node == nil {
		return false
	}
	if node.GetRestApi() != nil || node.GetGraphqlQuery() != nil ||
		node.GetContractRead() != nil || node.GetBalance() != nil {
		return true
	}
	if loop := node.GetLoop(); loop != nil {
		return loop.GetRestApi() != nil || loop.GetGraphqlDataQuery() != nil || loop.GetContractRead() != nil
	}
	return false
}

// propagateRetryPolicyToIteration copies a Loop node's retry policy onto the
// synthetic per-iteration node, for read runners only.
//
// A REST GET fanned out across a Loop is exactly where 429s show up, so the
// loop path is the feature's strongest use case; before this the policy was
// accepted on a Loop and silently did nothing there. The write runners are
// skipped so the #676 exclusion holds inside loops too, and the execution-wide
// retryBudget bounds the fan-out that per-node validation cannot see.
func propagateRetryPolicyToIteration(parent *avsproto.TaskNode, nested *avsproto.TaskNode) {
	if parent == nil || nested == nil {
		return
	}
	policy := parent.GetRetryPolicy()
	if policy == nil || !nodeSupportsRetry(nested) {
		return
	}
	nested.RetryPolicy = policy
}
