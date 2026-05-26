package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// CORS allowlist for the REST API. Per the implementation plan:
//
//   - https://app.avaprotocol.org    — production Next.js app
//   - the staging Vercel preview     — staging app
//
// No other consumers today; partners hitting the REST API directly use
// the JWT Bearer auth flow and are not subject to CORS (CORS is browser-
// enforced, not server-enforced).
//
// Extending the allowlist later: just append origins here and ship a
// new release; no config plumbing.
var defaultAllowedOrigins = []string{
	"https://app.avaprotocol.org",
	// Staging Vercel preview — TBD; fill in actual hostname when known.
	// Until then the staging URL slot is left empty and CORS rejects.
}

// CORS returns the Echo CORS middleware configured for the REST API's
// hard-coded origin allowlist. Allows credentials so cookies/JWTs work
// from the Next.js app under the same origin policy.
func CORS(extraOrigins ...string) echo.MiddlewareFunc {
	origins := append([]string{}, defaultAllowedOrigins...)
	origins = append(origins, extraOrigins...)
	return echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: origins,
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
			RequestIDHeader,
		},
		ExposeHeaders: []string{
			RequestIDHeader,
			"X-RateLimit-Limit",
			"X-RateLimit-Remaining",
			"X-RateLimit-Reset",
			"Retry-After",
		},
		AllowCredentials: true,
		MaxAge:           600, // 10 min preflight cache
	})
}
