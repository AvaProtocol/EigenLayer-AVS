package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// User context keys. Handlers extract the authenticated user via
// UserFromContext(c).
const (
	contextKeyUser = "auth.user"
)

// AuthenticatedUser is what handlers see after the JWT middleware accepts
// a request. Mirrors the existing aggregator.User shape but lives in
// this package to avoid an import cycle from middleware back to the
// aggregator package.
type AuthenticatedUser struct {
	// Subject is the 0x-prefixed EOA the JWT is bound to. Always set.
	Subject string
	// Role is "user" (default) or "admin" (from CLI-issued JWTs).
	Role string
	// ChainID is the smart-wallet chain ID parsed from the JWT's first
	// `aud` claim. 0 when the claim is absent or unparseable. Handlers
	// use it as the default chain context when neither the request body
	// nor inputVariables.settings carries one.
	ChainID int64
}

// JWTConfig configures the JWT middleware. SigningKey is the same secret
// the aggregator uses to mint tokens via create-api-key + auth:exchange.
type JWTConfig struct {
	SigningKey []byte
}

// JWT verifies the Authorization: Bearer header on every request and
// attaches the resolved AuthenticatedUser to the Echo context. Returns
// 401 (via the problem+json error handler) on any failure: missing
// header, malformed token, bad signature, expired claims.
//
// The implementation here is the auth glue point — it will be wired to
// the existing JWT secret + claims structure when the auth handler
// (POST /auth:exchange) and the engine rename land. For the scaffold,
// it parses and verifies but treats every request as anonymous if no
// token is sent (handlers themselves enforce auth via the OpenAPI
// security requirements that the generator does NOT auto-enforce — we
// gate at the handler layer instead).
func JWT(cfg JWTConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get(echo.HeaderAuthorization)
			if header == "" {
				return next(c) // anonymous — handlers requiring auth will reject
			}

			const prefix = "Bearer "
			if !strings.HasPrefix(header, prefix) {
				return &HTTPError{
					Status: http.StatusUnauthorized,
					Code:   "AUTH_MALFORMED",
					Title:  "Malformed Authorization header",
					Detail: "Authorization header must be `Bearer <jwt>`.",
				}
			}
			rawToken := strings.TrimPrefix(header, prefix)

			parsed, err := jwt.Parse(rawToken, func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return cfg.SigningKey, nil
			})
			if err != nil || !parsed.Valid {
				return &HTTPError{
					Status: http.StatusUnauthorized,
					Code:   "AUTH_INVALID_TOKEN",
					Title:  "Invalid or expired token",
					Detail: "Reissue via POST /api/v1/auth:exchange or your CLI-issued token.",
				}
			}

			claims, _ := parsed.Claims.(jwt.MapClaims)
			user := &AuthenticatedUser{}
			if sub, ok := claims["sub"].(string); ok {
				user.Subject = sub
			}
			if role, ok := claims["role"].(string); ok {
				user.Role = role
			}
			user.ChainID = audienceChainID(claims)
			c.Set(contextKeyUser, user)
			return next(c)
		}
	}
}

// UserFromContext returns the authenticated user attached by the JWT
// middleware, or nil if the request is anonymous. Handlers that require
// auth must check for nil and return a 401 via HTTPError.
func UserFromContext(c echo.Context) *AuthenticatedUser {
	if u, ok := c.Get(contextKeyUser).(*AuthenticatedUser); ok {
		return u
	}
	return nil
}

// audienceChainID extracts the first `aud` entry from the JWT claims
// and parses it as a numeric chain ID. Returns 0 when the claim is
// missing, empty, or not a number string. The token mint paths
// (create-api-key + auth:exchange) always set aud to a single-element
// list containing the smart-wallet chain ID, so taking the first entry
// is correct for every token this gateway issues.
func audienceChainID(claims jwt.MapClaims) int64 {
	raw, ok := claims["aud"]
	if !ok {
		return 0
	}
	var first string
	switch v := raw.(type) {
	case string:
		first = v
	case []interface{}:
		if len(v) == 0 {
			return 0
		}
		s, ok := v[0].(string)
		if !ok {
			return 0
		}
		first = s
	default:
		return 0
	}
	parsed, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
