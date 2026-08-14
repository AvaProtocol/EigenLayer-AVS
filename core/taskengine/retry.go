package taskengine

import (
	"errors"
	"net"
	"regexp"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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

var http5xxRe = regexp.MustCompile(`\b5\d\d\b`)

// classifyRetryableError maps err to one of the retry classes, or "" if it is
// not a recognizably transient failure. Classification is best-effort and
// string-based: node runners surface upstream failures as wrapped errors rather
// than typed status codes, so this errs toward not retrying when unsure.
func classifyRetryableError(err error) string {
	if err == nil {
		return ""
	}

	// Structured network timeouts (and context deadlines) first — most reliable signal.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return retryClassTimeout
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return retryClassTimeout
	case strings.Contains(msg, "429") || strings.Contains(msg, "too many requests"):
		return retryClassHTTP429
	case http5xxRe.MatchString(msg) ||
		strings.Contains(msg, "internal server error") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "gateway timeout"):
		return retryClassHTTP5xx
	case strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "rpc"):
		return retryClassRPC
	}
	return ""
}

// isRetryableError reports whether err is a transient failure whose class is
// enabled by policy.
func isRetryableError(err error, policy *avsproto.RetryPolicy) bool {
	class := classifyRetryableError(err)
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

// executeWithRetry runs attempt up to policy.max_attempts times, sleeping with
// exponential backoff between attempts that fail with a retryable error. It
// returns the result of the final attempt. A nil policy, or max_attempts <= 1,
// means exactly one attempt — identical to the pre-retry behavior. Only the
// final attempt's step is returned, so the caller logs a single execution step.
//
// This is intended for idempotent read/off-chain nodes only; the caller is
// responsible for restricting it to the retry allowlist. On-chain write nodes
// must not be retried here (double-spend risk — see issue #676).
func executeWithRetry(
	policy *avsproto.RetryPolicy,
	sleep func(time.Duration),
	attempt func() (*avsproto.Execution_Step, error),
) (*avsproto.Execution_Step, error) {
	maxAttempts := 1
	if policy != nil && policy.GetMaxAttempts() > 1 {
		maxAttempts = int(policy.GetMaxAttempts())
	}

	backoff := time.Duration(policy.GetBackoffMs()) * time.Millisecond
	multiplier := policy.GetBackoffMultiplier()
	if multiplier <= 0 {
		multiplier = defaultBackoffMultiplier
	}
	maxBackoff := time.Duration(policy.GetMaxBackoffMs()) * time.Millisecond

	var step *avsproto.Execution_Step
	var err error
	for i := 0; i < maxAttempts; i++ {
		step, err = attempt()
		if err == nil {
			return step, nil
		}
		// Last attempt, or a failure we won't retry: return as-is.
		if i == maxAttempts-1 || !isRetryableError(err, policy) {
			return step, err
		}
		if backoff > 0 {
			wait := backoff
			if maxBackoff > 0 && wait > maxBackoff {
				wait = maxBackoff
			}
			sleep(wait)
			backoff = time.Duration(float64(backoff) * multiplier)
		}
	}
	return step, err
}
