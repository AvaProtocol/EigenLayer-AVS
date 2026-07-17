package rest

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

const testPartnerSubject = "0x1111111111111111111111111111111111111111"

// newPartnerServer builds a minimal Server whose registry contains a single
// "studio" partner keyed on the supplied Ed25519 public key.
func newPartnerServer(t *testing.T, pub ed25519.PublicKey, scopes []string, status string) *Server {
	t.Helper()
	return newPartnerServerAud(t, pub, scopes, status, "")
}

// newPartnerServerAud is newPartnerServer with a configured expected audience.
func newPartnerServerAud(t *testing.T, pub ed25519.PublicKey, scopes []string, status, audience string) *Server {
	t.Helper()
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return &Server{
		logger: logger,
		config: &config.Config{
			PartnerAssertionAudience: audience,
			Partners: []config.PartnerConfig{{
				ID:         "studio",
				PublicKeys: []string{base64.StdEncoding.EncodeToString(pub)},
				Scopes:     scopes,
				Status:     status,
			}},
		},
	}
}

// signAssertion mints an Ed25519-signed partner assertion with the given
// claims, overridable per-test.
func signAssertion(t *testing.T, priv ed25519.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}
	return signed
}

// ctxWithAssertion builds an Echo context carrying the partner assertion
// header (the user-JWT middleware never ran, so no AuthenticatedUser is set —
// exactly the partner-delegated case).
func ctxWithAssertion(assertion string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows:simulate", nil)
	if assertion != "" {
		req.Header.Set(partnerAssertionHeader, assertion)
	}
	return e.NewContext(req, httptest.NewRecorder())
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":   "studio",
		"sub":   testPartnerSubject,
		"scope": "simulate",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
	}
}

func TestRequireSimulateAuth_PartnerHappyPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)

	user, err := s.requireSimulateAuth(ctxWithAssertion(signAssertion(t, priv, validClaims())))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if got := user.Address.Hex(); got != testPartnerSubject {
		t.Fatalf("expected acting address %s, got %s", testPartnerSubject, got)
	}
}

func TestRequireSimulateAuth_NoCredential(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)

	_, err := s.requireSimulateAuth(ctxWithAssertion(""))
	assertHTTPStatus(t, err, http.StatusUnauthorized)
}

// ctxWithUser builds an Echo context as the JWT middleware would leave it —
// with an AuthenticatedUser attached (no partner header).
func ctxWithUser(subject string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows:simulate", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set("auth.user", &restmw.AuthenticatedUser{Subject: subject})
	return c
}

func TestRequireSimulateAuth_PrefersUserJWT(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)

	const subject = "0x2222222222222222222222222222222222222222"
	user, err := s.requireSimulateAuth(ctxWithUser(subject))
	if err != nil {
		t.Fatalf("valid user JWT should succeed, got: %v", err)
	}
	if user.Address.Hex() != subject {
		t.Fatalf("expected user %s, got %s", subject, user.Address.Hex())
	}

	// A present-but-empty-subject JWT must be rejected, NOT silently fall
	// through to the partner path.
	_, err = s.requireSimulateAuth(ctxWithUser(""))
	assertHTTPStatus(t, err, http.StatusUnauthorized)
}

func TestDecodeEd25519Keys_TolerantAndMultiEncoding(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	std := base64.StdEncoding.EncodeToString(pub)

	for _, enc := range []string{
		std,
		base64.RawStdEncoding.EncodeToString(pub),
		base64.URLEncoding.EncodeToString(pub),
		base64.RawURLEncoding.EncodeToString(pub),
		"ed25519:" + std,
	} {
		if _, err := decodeEd25519Key(enc); err != nil {
			t.Fatalf("expected %q to decode, got %v", enc, err)
		}
	}

	// A bad key is tolerated as long as at least one is usable (rotation).
	keys, err := decodeEd25519Keys([]string{"!!!not-base64!!!", std})
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected 1 usable key tolerating the bad one, got %d err=%v", len(keys), err)
	}

	// All-bad must fail (never silently accept any signature).
	if _, err := decodeEd25519Keys([]string{"nope", "alsobad"}); err == nil {
		t.Fatal("expected error when no keys are usable")
	}
}

func TestVerifyPartnerAssertion_Rejections(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)

	cases := []struct {
		name       string
		server     func(t *testing.T) *Server
		assertion  func(t *testing.T) string
		wantStatus int
	}{
		{
			name: "wrong signing key",
			server: func(t *testing.T) *Server {
				return newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)
			},
			// Signed by a key the registry doesn't know.
			assertion:  func(t *testing.T) string { return signAssertion(t, otherPriv, validClaims()) },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unknown issuer",
			server: func(t *testing.T) *Server {
				return newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)
			},
			assertion: func(t *testing.T) string {
				c := validClaims()
				c["iss"] = "acme"
				return signAssertion(t, priv, c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "suspended partner",
			server:     func(t *testing.T) *Server { return newPartnerServer(t, pub, []string{scopeSimulate}, "suspended") },
			assertion:  func(t *testing.T) string { return signAssertion(t, priv, validClaims()) },
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scope not granted to partner",
			server:     func(t *testing.T) *Server { return newPartnerServer(t, pub, []string{"other"}, partnerStatusActive) },
			assertion:  func(t *testing.T) string { return signAssertion(t, priv, validClaims()) },
			wantStatus: http.StatusForbidden,
		},
		{
			name: "scope not declared in token",
			server: func(t *testing.T) *Server {
				return newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)
			},
			assertion: func(t *testing.T) string {
				c := validClaims()
				c["scope"] = "other"
				return signAssertion(t, priv, c)
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "expired assertion",
			server: func(t *testing.T) *Server {
				return newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)
			},
			assertion: func(t *testing.T) string {
				c := validClaims()
				c["exp"] = time.Now().Add(-time.Minute).Unix()
				return signAssertion(t, priv, c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "ttl too long",
			server: func(t *testing.T) *Server {
				return newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)
			},
			assertion: func(t *testing.T) string {
				c := validClaims()
				c["exp"] = time.Now().Add(maxPartnerAssertionTTL + time.Hour).Unix()
				return signAssertion(t, priv, c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "missing expiry",
			server: func(t *testing.T) *Server {
				return newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)
			},
			assertion: func(t *testing.T) string {
				c := validClaims()
				delete(c, "exp")
				return signAssertion(t, priv, c)
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.server(t)
			_, err := s.verifyPartnerAssertion(ctxWithAssertion(tc.assertion(t)), scopeSimulate)
			assertHTTPStatus(t, err, tc.wantStatus)
		})
	}
}

func TestVerifyPartnerAssertion_Audience(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)

	t.Run("matching audience accepted", func(t *testing.T) {
		s := newPartnerServerAud(t, pub, []string{scopeSimulate}, partnerStatusActive, "avs-gateway-staging")
		c := validClaims()
		c["aud"] = "avs-gateway-staging"
		principal, err := s.verifyPartnerAssertion(ctxWithAssertion(signAssertion(t, priv, c)), scopeSimulate)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if principal == nil || principal.partnerID != "studio" {
			t.Fatalf("expected studio principal, got %+v", principal)
		}
	})

	t.Run("missing/mismatched audience rejected", func(t *testing.T) {
		s := newPartnerServerAud(t, pub, []string{scopeSimulate}, partnerStatusActive, "avs-gateway-staging")
		// Assertion targets prod, gateway expects staging.
		c := validClaims()
		c["aud"] = "avs-gateway-prod"
		_, err := s.verifyPartnerAssertion(ctxWithAssertion(signAssertion(t, priv, c)), scopeSimulate)
		assertHTTPStatus(t, err, http.StatusUnauthorized)
	})
}

// requireWalletDeriveAuth must reject a partner assertion whose `sub` is not a
// concrete EOA — a smart wallet can't be derived without an owner.
func TestRequireWalletDeriveAuth_OpaqueSubjectRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)

	c := validClaims()
	c["sub"] = "studio-user-42" // not an address
	_, err := s.requireWalletDeriveAuth(ctxWithAssertion(signAssertion(t, priv, c)))
	assertHTTPStatus(t, err, http.StatusBadRequest)

	// With a real EOA sub it resolves to that owner.
	user, err := s.requireWalletDeriveAuth(ctxWithAssertion(signAssertion(t, priv, validClaims())))
	if err != nil {
		t.Fatalf("expected success with address sub, got: %v", err)
	}
	if user.Address.Hex() != testPartnerSubject {
		t.Fatalf("expected owner %s, got %s", testPartnerSubject, user.Address.Hex())
	}
}

func TestVerifyPartnerAssertion_AbsentHeaderFallsThrough(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeSimulate}, partnerStatusActive)

	principal, err := s.verifyPartnerAssertion(ctxWithAssertion(""), scopeSimulate)
	if err != nil {
		t.Fatalf("absent header must not error, got: %v", err)
	}
	if principal != nil {
		t.Fatalf("absent header must yield nil principal, got: %+v", principal)
	}
}

func assertHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with status %d, got nil", want)
	}
	httpErr, ok := err.(*restmw.HTTPError)
	if !ok {
		t.Fatalf("expected *restmw.HTTPError, got %T: %v", err, err)
	}
	if httpErr.Status != want {
		t.Fatalf("expected status %d, got %d (%s)", want, httpErr.Status, httpErr.Code)
	}
}
