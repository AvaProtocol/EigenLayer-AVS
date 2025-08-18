package taskengine

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// cleanupPartialExecutionState cleans up any partial state from a failed method execution
func (r *ContractWriteProcessor) cleanupPartialExecutionState(methodName string) {
	if r.vm == nil {
		return
	}

	// Clear any temporary variables that might have been set during execution
	r.vm.mu.Lock()
	defer r.vm.mu.Unlock()

	// Remove any method-specific temporary variables
	tempVarPrefix := fmt.Sprintf("temp_%s_", methodName)
	for key := range r.vm.vars {
		if strings.HasPrefix(key, tempVarPrefix) {
			delete(r.vm.vars, key)
		}
	}

	if r.vm.logger != nil {
		r.vm.logger.Debug("Cleaned up partial execution state after panic",
			"method", methodName,
			"cleanup_time", time.Now().Format(time.RFC3339))
	}
}

// safeExecuteWithTimeout executes a function with timeout and panic recovery
func safeExecuteWithTimeout(
	fn func() error,
	timeout time.Duration,
	operation string,
) error {
	done := make(chan error, 1)

	go func() {
		defer func() {
			if rcv := recover(); rcv != nil {
				// Report panic to Sentry with full context
				sentry.WithScope(func(scope *sentry.Scope) {
					scope.SetTag("component", "safe_execution")
					scope.SetTag("operation", operation)
					scope.SetContext("panic_details", map[string]interface{}{
						"recovered_value": fmt.Sprintf("%v", rcv),
						"stack_trace":     string(debug.Stack()),
						"timestamp":       time.Now().Unix(),
					})
					sentry.CaptureException(fmt.Errorf("panic in %s: %v", operation, rcv))
				})

				done <- fmt.Errorf("panic in %s: %v", operation, rcv)
			}
		}()

		done <- fn()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		// Report timeout to Sentry
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("component", "safe_execution")
			scope.SetTag("operation", operation)
			scope.SetContext("timeout_details", map[string]interface{}{
				"timeout_duration": timeout.String(),
				"operation":        operation,
			})
			sentry.CaptureMessage(fmt.Sprintf("Operation timeout: %s", operation))
		})

		return fmt.Errorf("operation %s timed out after %v", operation, timeout)
	}
}

// enhancedPanicRecovery provides standardized panic recovery with Sentry reporting
func enhancedPanicRecovery(component, operation string, extraContext map[string]interface{}) {
	if rcv := recover(); rcv != nil {
		// Create comprehensive Sentry report
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("component", component)
			scope.SetTag("operation", operation)
			scope.SetTag("panic_type", fmt.Sprintf("%T", rcv))

			context := map[string]interface{}{
				"recovered_value": fmt.Sprintf("%v", rcv),
				"stack_trace":     string(debug.Stack()),
				"timestamp":       time.Now().Unix(),
				"go_version":      runtime.Version(),
			}

			// Add any extra context provided
			for key, value := range extraContext {
				context[key] = value
			}

			scope.SetContext("panic_details", context)
			sentry.CaptureException(fmt.Errorf("panic in %s.%s: %v", component, operation, rcv))
		})

		// Re-panic to maintain original behavior while ensuring Sentry capture
		panic(rcv)
	}
}

// safeChannelSend performs a non-blocking channel send with timeout and Sentry reporting
func safeChannelSend[T any](ch chan T, value T, timeout time.Duration, context string) error {
	select {
	case ch <- value:
		return nil
	case <-time.After(timeout):
		// Report channel send timeout to Sentry
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("component", "channel_operations")
			scope.SetContext("channel_send", map[string]interface{}{
				"context":    context,
				"timeout":    timeout.String(),
				"value_type": fmt.Sprintf("%T", value),
			})
			sentry.CaptureMessage(fmt.Sprintf("Channel send timeout: %s", context))
		})
		return fmt.Errorf("channel send timeout after %v in %s", timeout, context)
	default:
		// Channel is full or closed
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("component", "channel_operations")
			scope.SetContext("channel_send", map[string]interface{}{
				"context":    context,
				"reason":     "channel_full_or_closed",
				"value_type": fmt.Sprintf("%T", value),
			})
			sentry.CaptureMessage(fmt.Sprintf("Channel send failed - channel full or closed: %s", context))
		})
		return fmt.Errorf("channel send failed - channel full or closed in %s", context)
	}
}

// nilSafetyGuard provides comprehensive nil checking with Sentry reporting
func nilSafetyGuard(value interface{}, name, operation string) error {
	if value == nil {
		err := fmt.Errorf("nil value detected: %s in %s", name, operation)

		// Report nil safety violation to Sentry
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("component", "nil_safety")
			scope.SetTag("operation", operation)
			scope.SetContext("nil_check", map[string]interface{}{
				"variable_name": name,
				"operation":     operation,
				"stack_trace":   string(debug.Stack()),
			})
			sentry.CaptureException(err)
		})

		return err
	}
	return nil
}

// circuitBreakerState tracks the state of circuit breakers for external services
type circuitBreakerState struct {
	failures    int
	lastFailure time.Time
	isOpen      bool
}

var (
	rpcCircuitBreaker = &circuitBreakerState{}
	maxFailures       = 5
	resetTimeout      = 5 * time.Minute
)

// rpcCallWithCircuitBreaker wraps RPC calls with circuit breaker pattern
func rpcCallWithCircuitBreaker(fn func() error, service string) error {
	// Check if circuit breaker is open
	if rpcCircuitBreaker.isOpen {
		if time.Since(rpcCircuitBreaker.lastFailure) > resetTimeout {
			// Reset circuit breaker
			rpcCircuitBreaker.isOpen = false
			rpcCircuitBreaker.failures = 0
		} else {
			err := fmt.Errorf("circuit breaker open for %s service", service)
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("component", "circuit_breaker")
				scope.SetTag("service", service)
				scope.SetContext("circuit_breaker", map[string]interface{}{
					"state":        "open",
					"failures":     rpcCircuitBreaker.failures,
					"last_failure": rpcCircuitBreaker.lastFailure.Unix(),
				})
				sentry.CaptureMessage(fmt.Sprintf("Circuit breaker open: %s", service))
			})
			return err
		}
	}

	// Execute the RPC call
	err := fn()

	if err != nil {
		rpcCircuitBreaker.failures++
		rpcCircuitBreaker.lastFailure = time.Now()

		if rpcCircuitBreaker.failures >= maxFailures {
			rpcCircuitBreaker.isOpen = true

			// Report circuit breaker opening to Sentry
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("component", "circuit_breaker")
				scope.SetTag("service", service)
				scope.SetContext("circuit_breaker", map[string]interface{}{
					"state":    "opening",
					"failures": rpcCircuitBreaker.failures,
					"error":    err.Error(),
				})
				sentry.CaptureException(fmt.Errorf("circuit breaker opening for %s: %w", service, err))
			})
		}

		return err
	}

	// Reset failure count on success
	rpcCircuitBreaker.failures = 0
	return nil
}
