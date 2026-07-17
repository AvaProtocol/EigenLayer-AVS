package rest

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// Partner-delegated auth for the no-fund "simulate" family.
//
// A partner (tenant) such as Studio authenticates its own end users in its
// own system and then vouches for them to the AVS for operations that move
// no funds — workflows:simulate, nodes:run, triggers:run. Because simulate
// skips wallet-ownership (engine.SimulateTask) and the authKey chain is
// cosmetic, the partner does NOT need a per-user wallet signature for these.
//
// The partner proves its identity with a short-lived Ed25519-signed
// assertion (private_key_jwt style) sent in the X-Partner-Assertion header,
// kept deliberately separate from the user `Authorization: Bearer` path so
// the existing user-JWT flow is untouched. The assertion's claims are the
// stable contract that a future RFC 8693 token-exchange endpoint will reuse:
//
//	{ "iss": "<partner_id>",        // selects the registered partner + keys
//	  "sub": "<end-user address>",   // attribution; may be empty/opaque
//	  "scope": "simulate",           // required scope for the call
//	  "exp": <unix>, "iat": <unix> } // short-lived
//
// Fund-moving operations (createTask/execute) are NEVER authorized by a
// partner assertion — those require real on-chain fund authority (the
// controller-signed path today, Uniswap Calibur later). See
// PLAN_PARTNER_PAYMENTS.md.
const (
	// partnerAssertionHeader carries the partner's Ed25519-signed JWT.
	partnerAssertionHeader = "X-Partner-Assertion"

	// scopeSimulate is the only delegation scope honored today.
	scopeSimulate = "simulate"

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

// requireSimulateAuth authorizes a simulate-family request via EITHER an
// end-user JWT (the existing path) OR a partner assertion (the delegated
// path). It returns the *model.User the engine expects.
//
//   - User JWT present  → behaves exactly like requireUser.
//   - Else partner assertion present and valid → a User keyed on the
//     assertion's `sub` (zero address when `sub` is empty/non-address;
//     simulate tolerates this since it skips ownership).
//   - Else → 401.
func (s *Server) requireSimulateAuth(ctx echo.Context) (*model.User, error) {
	// Any presented end-user JWT goes through the user path. requireUser
	// rejects a missing/invalid subject — we must NOT silently fall through to
	// the partner path for a structurally-valid-but-empty-subject token.
	if authed := restmw.UserFromContext(ctx); authed != nil {
		return s.requireUser(ctx)
	}

	principal, err := s.verifyPartnerAssertion(ctx, scopeSimulate)
	if err != nil {
		return nil, err
	}
	if principal != nil {
		user := &model.User{}
		// `sub` is attribution only. Use it as the acting address when it's
		// a real EOA; otherwise leave the zero address — simulate runs
		// against any address and never checks ownership.
		if common.IsHexAddress(principal.subject) {
			user.Address = common.HexToAddress(principal.subject)
		}
		s.logger.Info("partner-delegated simulate call",
			"partner_id", principal.partnerID,
			"subject", principal.subject,
		)
		return user, nil
	}

	// Neither credential present.
	return nil, &restmw.HTTPError{
		Status: http.StatusUnauthorized,
		Code:   "AUTH_REQUIRED",
		Title:  "Authentication required",
		Detail: "This endpoint requires a Bearer JWT (POST /api/v1/auth:exchange) or a partner assertion (X-Partner-Assertion).",
	}
}

// requireWalletDeriveAuth authorizes the no-fund wallet derivation/list step
// (runner resolution during a preview) via the same either/or path as
// requireSimulateAuth, but additionally requires a concrete owner EOA: a smart
// wallet is derived deterministically from its owner, so an empty/opaque
// subject cannot be served. Partner assertions used here must carry a real
// `sub` address. This unblocks Studio's `$SMART_WALLET$` placeholder
// resolution (getWallets) for partner-delegated previews; it stays no-fund and
// needs no ownership check — derivation only reads (owner, salt, factory).
func (s *Server) requireWalletDeriveAuth(ctx echo.Context) (*model.User, error) {
	user, err := s.requireSimulateAuth(ctx)
	if err != nil {
		return nil, err
	}
	if user.Address == (common.Address{}) {
		return nil, &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "PARTNER_SUBJECT_REQUIRED",
			Title:  "Owner address required",
			Detail: "Wallet derivation requires the assertion `sub` to be the end-user's 0x EOA address.",
		}
	}
	return user, nil
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
