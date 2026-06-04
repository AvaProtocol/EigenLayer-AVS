package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// RateLimitConfig defines the bucket parameters. RatePerSecond is the
// steady-state allowed requests per second per subject (keyed on the
// JWT `sub`); Burst is the max bucket size (allowing short bursts above
// the steady rate).
type RateLimitConfig struct {
	RatePerSecond float64
	Burst         int
}

// DefaultRateLimit is the v1 default — generous enough for normal use,
// tight enough that runaway clients are caught. Adjust by config later.
var DefaultRateLimit = RateLimitConfig{
	RatePerSecond: 10,
	Burst:         50,
}

// limiter is a single bucket. Last is the timestamp of the most recent
// allowed request; Tokens is the current bucket level.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (l *limiter) allow(cfg RateLimitConfig, now time.Time) (allowed bool, remaining int, resetAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.last.IsZero() {
		l.tokens = float64(cfg.Burst)
	} else {
		elapsed := now.Sub(l.last).Seconds()
		l.tokens = min(float64(cfg.Burst), l.tokens+elapsed*cfg.RatePerSecond)
	}
	l.last = now

	if l.tokens >= 1 {
		l.tokens--
		remaining = int(l.tokens)
		// Reset is when the bucket would next fully refill.
		secsToFull := (float64(cfg.Burst) - l.tokens) / cfg.RatePerSecond
		resetAt = now.Add(time.Duration(secsToFull * float64(time.Second)))
		return true, remaining, resetAt
	}
	// Reset is when the bucket gains its next token.
	secsToOne := (1 - l.tokens) / cfg.RatePerSecond
	return false, 0, now.Add(time.Duration(secsToOne * float64(time.Second)))
}

// Backend abstracts the bucket store. The default in-process map
// implementation is fine for a single aggregator; a Redis-backed
// implementation can swap in later without touching middleware callers
// by satisfying this interface.
type Backend interface {
	Get(key string) *limiter
}

type inMemoryBackend struct {
	mu       sync.Mutex
	limiters map[string]*limiter
}

func NewInMemoryBackend() Backend {
	return &inMemoryBackend{limiters: map[string]*limiter{}}
}

func (b *inMemoryBackend) Get(key string) *limiter {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.limiters[key]
	if !ok {
		l = &limiter{}
		b.limiters[key] = l
	}
	return l
}

// RateLimit returns the middleware. Keyed on the authenticated user's
// subject; anonymous requests (no JWT) share a single bucket so a
// flood of unauthenticated calls can't drown the aggregator.
func RateLimit(cfg RateLimitConfig, backend Backend) echo.MiddlewareFunc {
	if backend == nil {
		backend = NewInMemoryBackend()
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := "anonymous"
			if u := UserFromContext(c); u != nil && u.Subject != "" {
				key = u.Subject
			}
			l := backend.Get(key)
			ok, remaining, resetAt := l.allow(cfg, time.Now())

			c.Response().Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.Burst))
			c.Response().Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			c.Response().Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))

			if !ok {
				retryAfter := int(time.Until(resetAt).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				c.Response().Header().Set("Retry-After", strconv.Itoa(retryAfter))
				return &HTTPError{
					Status: http.StatusTooManyRequests,
					Code:   "RATE_LIMITED",
					Title:  "Rate limit exceeded",
					Detail: fmt.Sprintf("Try again in %ds.", retryAfter),
				}
			}
			return next(c)
		}
	}
}
