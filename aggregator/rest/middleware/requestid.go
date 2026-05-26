// Package middleware contains the Echo middleware used by the REST API.
//
// Each middleware is registered in aggregator/rest/server.go's Mount
// function in a fixed order:
//
//	requestid  — assign / propagate X-Request-Id
//	sentry     — tag the Sentry scope with route + status + request id
//	cors       — allowlist app.avaprotocol.org + the staging Vercel domain
//	problem    — convert handler errors to RFC 7807 problem+json
//	jwt        — verify Authorization: Bearer; put user in context
//	ratelimit  — per-subject token bucket; 429 on exceed
//
// jwt is registered AFTER cors so preflight OPTIONS requests don't need
// a token; AFTER problem so auth errors get the consistent error shape;
// BEFORE ratelimit so rate buckets are keyed on a verified subject.
package middleware

import (
	"github.com/labstack/echo/v4"
	"github.com/oklog/ulid/v2"
	"math/rand"
	"sync"
	"time"
)

const RequestIDHeader = "X-Request-Id"

// RequestID assigns a ULID to every request that doesn't already carry one
// and writes it back as `X-Request-Id`. Used by problem+json error
// responses (instance field), structured logging, and Sentry tagging so
// every entry can be correlated to a single request.
func RequestID() echo.MiddlewareFunc {
	var (
		mu      sync.Mutex
		entropy = ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0)
	)
	gen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := c.Request().Header.Get(RequestIDHeader)
			if id == "" {
				id = gen()
			}
			c.Set(RequestIDHeader, id)
			c.Response().Header().Set(RequestIDHeader, id)
			return next(c)
		}
	}
}

// RequestIDFromContext returns the request id stored by the RequestID
// middleware, or the empty string if none was assigned.
func RequestIDFromContext(c echo.Context) string {
	if id, ok := c.Get(RequestIDHeader).(string); ok {
		return id
	}
	return ""
}
