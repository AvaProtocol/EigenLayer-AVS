package rest

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// Partner assertion verification for tenant-gated REST routes.
//
// A partner (tenant) such as Studio authenticates with a short-lived
// Ed25519-signed assertion in X-Partner-Assertion (private_key_jwt style),
// kept separate from Authorization: Bearer user JWTs.
//
// Which operations accept partner credentials is defined in permission.go
// (permissionMap + ensurePermission), not by calling these helpers from
// handlers directly. Partner-sufficient classes today:
//
//   - scope "read": token metadata (partner only) and preview wallet
//     list/create (partner with EOA sub, or user JWT)
//
// Simulate / runNode / runTrigger require a user JWT (LevelUser) — partner
// alone is not enough. Fund-moving and session policies never accept
// partner. See GATEWAY_CHANGE_REQUIRED_PARTNER_GATED_READS.md.
const (
	// partnerAssertionHeader carries the partner's Ed25519-signed JWT.
	partnerAssertionHeader = "X-Partner-Assertion"

	// scopeRead is the partner scope for non-fund reads and preview wallet resolve.
	scopeRead = "read"

	// maxPartnerAssertionTTL bounds how far in the future an assertion's
	// `exp` may sit. Assertions are meant to be minted per session/call and
	// be short-lived; a far-future expiry is rejected so a leaked assertion
	// cannot be replayed for long.
	maxPartnerAssertionTTL = time.Hour

	partnerStatusActive = "active"
)

// partnerPrincipal is the result of a verified partner assertion: which
// partner vouched (partnerID) and the end-user it vouched for (subject,
// possibly empty or a non-address identifier). Package-private — fields stay
// lowercase since it never leaves this package.
type partnerPrincipal struct {
	partnerID string
	subject   string
}

// refusePartnerAssertion explicitly rejects a request that presents
// X-Partner-Assertion on an endpoint partner delegation may never reach.
//
// Session-policy management is FUND AUTHORITY: a policy record carries the
// owner's signed grant of on-chain execution rights, and creating, reading,
// or revoking one is exactly the class of operation the package comment
// above excludes from delegation. The failure mode this guard exists for is
// silent non-consultation — a handler that authenticates the user JWT and
// never looks at the assertion header, leaving a partner integrating against
// the wrong endpoint to discover the boundary from behavior instead of from
// an error.
//
// The assertion is refused WITHOUT verification, valid or not: the boundary
// is categorical, verification costs signature checks, and a
// validity-dependent response would make this endpoint an oracle for
// assertion validity.
//
// Invoked via ensurePermission for LevelUserRefusePartner routes. Returns
// nil when no assertion is present — user-JWT requests pass through.
func refusePartnerAssertion(ctx echo.Context, capability string) error {
	if strings.TrimSpace(ctx.Request().Header.Get(partnerAssertionHeader)) == "" {
		return nil
	}
	return partnerError(http.StatusForbidden, "PARTNER_DELEGATION_UNSUPPORTED",
		"Partner delegation not supported here",
		fmt.Sprintf("%s is fund authority and cannot be exercised through a partner assertion. "+
			"Use the end-user's Bearer JWT (POST /api/v1/auth:exchange) for this endpoint.", capability))
}

// verifyPartnerAssertion validates the X-Partner-Assertion header against the
// configured partner registry. It returns:
//
//   - (nil, nil) when the header is absent — the caller falls through to
//     other auth (so this never blocks a normal user-JWT request).
//   - (nil, err) when a header is present but invalid (bad signature,
//     unknown/suspended partner, missing/insufficient scope, expired).
//   - (principal, nil) on success.
func (s *Server) verifyPartnerAssertion(ctx echo.Context, requiredScope string) (*partnerPrincipal, error) {
	raw := strings.TrimSpace(ctx.Request().Header.Get(partnerAssertionHeader))
	if raw == "" {
		return nil, nil
	}

	// Read the issuer without verifying so we can select the partner's keys.
	// No alg enforcement here — ParseUnverified validates nothing; the EdDSA
	// requirement is enforced on the real verify (jwt.Parse) below.
	preview := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(raw, preview); err != nil {
		return nil, partnerError(http.StatusUnauthorized, "PARTNER_ASSERTION_MALFORMED",
			"Malformed partner assertion", err.Error())
	}
	issuer, _ := preview["iss"].(string)
	partner := s.lookupPartner(issuer)
	if partner == nil {
		return nil, partnerError(http.StatusUnauthorized, "PARTNER_UNKNOWN",
			"Unknown partner", fmt.Sprintf("No registered partner for issuer %q.", issuer))
	}
	if !strings.EqualFold(strings.TrimSpace(partner.Status), partnerStatusActive) {
		return nil, partnerError(http.StatusForbidden, "PARTNER_SUSPENDED",
			"Partner is not active", fmt.Sprintf("Partner %q is not active.", issuer))
	}

	pubKeys, err := decodeEd25519Keys(partner.PublicKeys)
	if err != nil {
		// Misconfiguration on our side, not the caller's fault.
		return nil, partnerError(http.StatusInternalServerError, "PARTNER_KEY_MISCONFIGURED",
			"Partner key misconfigured", fmt.Sprintf("Partner %q has no usable public key: %v", issuer, err))
	}

	// Verify the signature against each registered key (rotation support).
	var verified *jwt.Token
	for _, pk := range pubKeys {
		tok, verr := jwt.Parse(raw, func(*jwt.Token) (any, error) { return pk, nil },
			jwt.WithValidMethods([]string{"EdDSA"}))
		if verr == nil && tok.Valid {
			verified = tok
			break
		}
	}
	if verified == nil {
		return nil, partnerError(http.StatusUnauthorized, "PARTNER_ASSERTION_INVALID",
			"Invalid or expired partner assertion",
			"The assertion signature did not verify against any registered key, or it has expired.")
	}

	claims, _ := verified.Claims.(jwt.MapClaims)

	// Assertions must be short-lived: `exp` is required and capped.
	expTime, err := claims.GetExpirationTime()
	if err != nil || expTime == nil {
		return nil, partnerError(http.StatusUnauthorized, "PARTNER_ASSERTION_NO_EXP",
			"Partner assertion missing expiry", "Partner assertions must carry a short-lived `exp` claim.")
	}
	if time.Until(expTime.Time) > maxPartnerAssertionTTL {
		return nil, partnerError(http.StatusUnauthorized, "PARTNER_ASSERTION_TTL",
			"Partner assertion lifetime too long",
			fmt.Sprintf("`exp` may be at most %s in the future.", maxPartnerAssertionTTL))
	}

	// Replay binding: when an expected audience is configured, the assertion
	// must target this gateway/environment so a captured token can't be
	// replayed elsewhere.
	if want := strings.TrimSpace(s.config.PartnerAssertionAudience); want != "" {
		aud, _ := claims.GetAudience()
		if !slices.Contains(aud, want) {
			return nil, partnerError(http.StatusUnauthorized, "PARTNER_ASSERTION_AUDIENCE",
				"Partner assertion audience mismatch",
				fmt.Sprintf("Assertion `aud` must include %q for this gateway.", want))
		}
	}

	// Scope must be granted by the registry AND declared by the token.
	if !slices.Contains(partner.Scopes, requiredScope) {
		return nil, partnerError(http.StatusForbidden, "PARTNER_SCOPE_DENIED",
			"Partner scope denied", fmt.Sprintf("Partner %q is not granted scope %q.", issuer, requiredScope))
	}
	if !slices.Contains(claimScopes(claims), requiredScope) {
		return nil, partnerError(http.StatusForbidden, "PARTNER_TOKEN_SCOPE",
			"Assertion scope insufficient", fmt.Sprintf("Assertion does not declare scope %q.", requiredScope))
	}

	subject, _ := claims["sub"].(string)
	return &partnerPrincipal{partnerID: issuer, subject: strings.TrimSpace(subject)}, nil
}

// lookupPartner returns the registered partner whose ID matches id, or nil.
func (s *Server) lookupPartner(id string) *config.PartnerConfig {
	if s.config == nil || id == "" {
		return nil
	}
	for i := range s.config.Partners {
		if s.config.Partners[i].ID == id {
			return &s.config.Partners[i]
		}
	}
	return nil
}

// ed25519Base64Encodings are the base64 variants accepted for a partner
// public key, so keys produced by common tooling (openssl/age/ssh-keygen emit
// padded or raw, std or url-safe) decode without a silent mismatch.
var ed25519Base64Encodings = []*base64.Encoding{
	base64.StdEncoding,
	base64.RawStdEncoding,
	base64.URLEncoding,
	base64.RawURLEncoding,
}

// decodeEd25519Key decodes one base64 (optionally "ed25519:"-prefixed) public
// key, trying each accepted encoding.
func decodeEd25519Key(s string) (ed25519.PublicKey, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "ed25519:")
	for _, enc := range ed25519Base64Encodings {
		if b, err := enc.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
			return ed25519.PublicKey(b), nil
		}
	}
	return nil, fmt.Errorf("not a valid base64-encoded %d-byte Ed25519 public key", ed25519.PublicKeySize)
}

// decodeEd25519Keys decodes a partner's public keys, tolerating individual bad
// entries (so a fat-fingered key during rotation doesn't break a partner whose
// other key is valid) but failing when NONE are usable — a partner with no
// usable key must never silently accept any signature.
func decodeEd25519Keys(encoded []string) ([]ed25519.PublicKey, error) {
	out := make([]ed25519.PublicKey, 0, len(encoded))
	var errs []string
	for _, e := range encoded {
		if strings.TrimPrefix(strings.TrimSpace(e), "ed25519:") == "" {
			continue
		}
		pk, err := decodeEd25519Key(e)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		out = append(out, pk)
	}
	if len(out) == 0 {
		if len(errs) > 0 {
			return nil, fmt.Errorf("no usable public keys: %s", strings.Join(errs, "; "))
		}
		return nil, fmt.Errorf("no public keys configured")
	}
	return out, nil
}

// claimScopes extracts the `scope` claim as a slice, accepting either an
// OAuth-style space-delimited string or a JSON array of strings.
func claimScopes(claims jwt.MapClaims) []string {
	switch v := claims["scope"].(type) {
	case string:
		return strings.Fields(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func partnerError(status int, code, title, detail string) error {
	return &restmw.HTTPError{
		Status: status,
		Code:   code,
		Title:  title,
		Detail: detail,
	}
}
