package rest

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
)

const testAuthTemplate = `Please sign the below text for ownership verification.

URI: https://app.avaprotocol.org
Chain ID: 11155111
Version: vTest
Issued At: %s
Expire At: %s
Wallet: %s`

const authTimeLayout = "2006-01-02T15:04:05.000Z"

var testJwtSecret = []byte("test-jwt-secret")

// exchangeSignedMessage signs the canonical auth message with a fresh key,
// expiring at expireAt, and posts it to AuthExchange.
func exchangeSignedMessage(t *testing.T, expireAt time.Time) (*httptest.ResponseRecorder, error) {
	return exchangeSignedMessageWithPartner(t, expireAt, nil)
}

func exchangeSignedMessageWithPartner(t *testing.T, expireAt time.Time, configure func(*Server, *http.Request, ed25519.PrivateKey, jwt.MapClaims)) (*httptest.ResponseRecorder, error) {
	t.Helper()
	pk, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	owner := crypto.PubkeyToAddress(pk.PublicKey)
	message := fmt.Sprintf(testAuthTemplate,
		time.Now().UTC().Format(authTimeLayout),
		expireAt.UTC().Format(authTimeLayout),
		owner.Hex())
	sig, err := crypto.Sign(accounts.TextHash([]byte(message)), pk)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	body, err := json.Marshal(generated.AuthExchangeRequest{
		Message:      message,
		Signature:    generated.Hex(hexutil.Encode(sig)),
		OwnerAddress: generated.EthereumAddress(owner.Hex()),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth:exchange", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newPartnerServerAud(t, pub, []string{scopeAuthExchange}, partnerStatusActive, "test-gateway")
	s.config.JwtSecret = testJwtSecret
	claims := validClaims()
	claims["sub"] = owner.Hex()
	claims["scope"] = scopeAuthExchange
	claims["aud"] = "test-gateway"
	req.Header.Set(partnerAssertionHeader, signAssertion(t, priv, claims))
	if configure != nil {
		configure(s, req, priv, claims)
	}
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)

	return rec, s.AuthExchange(ctx)
}

func TestAuthExchangeRequiresPartnerAndWalletProof(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*Server, *http.Request, ed25519.PrivateKey, jwt.MapClaims)
		code      string
	}{
		{"missing configured audience", func(s *Server, _ *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			s.config.PartnerAssertionAudience = ""
		}, "PARTNER_AUTH_AUDIENCE_REQUIRED"},
		{"blank configured audience", func(s *Server, _ *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			s.config.PartnerAssertionAudience = " \t "
		}, "PARTNER_AUTH_AUDIENCE_REQUIRED"},
		{"missing assertion", func(_ *Server, r *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			r.Header.Del(partnerAssertionHeader)
		}, "PARTNER_REQUIRED"},
		{"user JWT alone", func(_ *Server, r *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			r.Header.Del(partnerAssertionHeader)
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": testPartnerSubject, "exp": time.Now().Add(time.Hour).Unix()})
			signed, err := token.SignedString(testJwtSecret)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set(echo.HeaderAuthorization, "Bearer "+signed)
		}, "PARTNER_REQUIRED"},
		{"plain partner ID", func(_ *Server, r *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			r.Header.Set(partnerAssertionHeader, "studio")
		}, "PARTNER_ASSERTION_MALFORMED"},
		{"wrong key", func(_ *Server, r *http.Request, _ ed25519.PrivateKey, c jwt.MapClaims) {
			_, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_ASSERTION_INVALID"},
		{"suspended partner", func(s *Server, _ *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			s.config.Partners[0].Status = "suspended"
		}, "PARTNER_SUSPENDED"},
		{"registry read scope only", func(s *Server, _ *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			s.config.Partners[0].Scopes = []string{scopeRead}
		}, "PARTNER_SCOPE_DENIED"},
		{"assertion read scope only", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["scope"] = scopeRead
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_TOKEN_SCOPE"},
		{"wrong audience", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["aud"] = "other-gateway"
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_ASSERTION_AUDIENCE"},
		{"expired assertion", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["exp"] = time.Now().Add(-time.Minute).Unix()
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_ASSERTION_INVALID"},
		{"missing subject", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			delete(c, "sub")
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_SUBJECT_MISMATCH"},
		{"opaque subject", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["sub"] = "studio-user:123"
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_SUBJECT_MISMATCH"},
		{"malformed address subject", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["sub"] = "0x" + strings.Repeat("g", 40)
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_SUBJECT_MISMATCH"},
		{"zero address subject", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["sub"] = (common.Address{}).Hex()
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_SUBJECT_MISMATCH"},
		{"non-string subject", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["sub"] = 123
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_SUBJECT_MISMATCH"},
		{"another wallet", func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
			c["sub"] = testPartnerSubject
			r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
		}, "PARTNER_SUBJECT_MISMATCH"},
		{"partner does not replace wallet signature", func(_ *Server, r *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
			var body generated.AuthExchangeRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			body.Signature = generated.Hex("0x" + strings.Repeat("00", 65))
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r.Body = io.NopCloser(strings.NewReader(string(encoded)))
		}, "AUTH_INVALID_SIGNATURE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exchangeSignedMessageWithPartner(t, time.Now().Add(time.Hour), tc.configure)
			if err == nil {
				t.Fatal("unauthorized exchange minted a token")
			}
			httpErr, ok := err.(*restmw.HTTPError)
			if !ok || httpErr.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
	for _, arrayScope := range []bool{false, true} {
		t.Run(fmt.Sprintf("valid partner and wallet (array scope %t)", arrayScope), func(t *testing.T) {
			rec, err := exchangeSignedMessageWithPartner(t, time.Now().Add(time.Hour), func(_ *Server, r *http.Request, key ed25519.PrivateKey, c jwt.MapClaims) {
				if arrayScope {
					c["scope"] = []string{scopeAuthExchange}
					r.Header.Set(partnerAssertionHeader, signAssertion(t, key, c))
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			var response generated.AuthExchangeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			token, err := jwt.Parse(response.Token, func(*jwt.Token) (any, error) { return testJwtSecret, nil })
			if err != nil {
				t.Fatal(err)
			}
			claims := token.Claims.(jwt.MapClaims)
			if claims["partner_id"] != "studio" || claims["sub"] != string(*response.Subject) {
				t.Fatalf("unexpected attribution: %v", claims)
			}
			assertWalletJWTConsumerCompatibility(t, response)
		})
	}
}

func assertWalletJWTConsumerCompatibility(t *testing.T, response generated.AuthExchangeResponse) {
	t.Helper()
	owner := common.HexToAddress(string(*response.Subject))
	if valid, err := auth.VerifyJwtKeyForUser(testJwtSecret, response.Token, owner); err != nil || !valid {
		t.Fatalf("wallet JWT verifier rejected partner-attributed token: %v", err)
	}
	if valid, _ := auth.VerifyJwtKeyForUser(testJwtSecret, response.Token, common.Address{}); valid {
		t.Fatal("partner attribution must not authorize a different wallet")
	}
	// CLI API claims use typed registered claims; unknown attribution fields
	// must not change the subject or introduce an administrative role.
	cliClaims := &auth.APIClaim{RegisteredClaims: &jwt.RegisteredClaims{}}
	if _, err := jwt.ParseWithClaims(response.Token, cliClaims, func(*jwt.Token) (any, error) {
		return testJwtSecret, nil
	}); err != nil || cliClaims.Subject != owner.Hex() || len(cliClaims.Roles) != 0 {
		t.Fatalf("CLI claim compatibility failed: claims=%+v err=%v", cliClaims, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/authenticated", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+response.Token)
	ctx := echo.New().NewContext(req, httptest.NewRecorder())
	called := false
	handler := restmw.JWT(restmw.JWTConfig{SigningKey: testJwtSecret})(func(c echo.Context) error {
		called = true
		user := restmw.UserFromContext(c)
		if user == nil || user.Subject != owner.Hex() || user.ChainID != 11155111 || user.Role != "" {
			t.Fatalf("REST middleware lost wallet identity or elevated its role: %+v", user)
		}
		return nil
	})
	if err := handler(ctx); err != nil || !called {
		t.Fatalf("REST middleware rejected partner-attributed token: %v", err)
	}
}

func TestAuthExchangeAcceptsLowercasePartnerSubject(t *testing.T) {
	rec, err := exchangeSignedMessageWithPartner(t, time.Now().Add(time.Hour), func(_ *Server, r *http.Request, key ed25519.PrivateKey, claims jwt.MapClaims) {
		claims["sub"] = strings.ToLower(claims["sub"].(string))
		r.Header.Set(partnerAssertionHeader, signAssertion(t, key, claims))
	})
	if err != nil || rec.Code != http.StatusOK {
		t.Fatalf("same address in lowercase must remain valid: status=%d err=%v", rec.Code, err)
	}
}

type authExchangeRecordingLogger struct {
	sdklogging.Logger
	messages []string
	tags     [][]any
}

func (l *authExchangeRecordingLogger) Info(message string, tags ...any) {
	l.messages = append(l.messages, message)
	l.tags = append(l.tags, tags)
}

func TestAuthExchangeLogsMissingPartnerWithoutCredentials(t *testing.T) {
	var recorded *authExchangeRecordingLogger
	_, err := exchangeSignedMessageWithPartner(t, time.Now().Add(time.Hour), func(s *Server, r *http.Request, _ ed25519.PrivateKey, _ jwt.MapClaims) {
		recorded = &authExchangeRecordingLogger{Logger: s.logger}
		s.logger = recorded
		r.Header.Del(partnerAssertionHeader)
		r.Header.Set(echo.HeaderAuthorization, "Bearer private-wallet-token")
	})
	if err == nil {
		t.Fatal("missing partner must still refuse exchange")
	}
	if len(recorded.messages) != 1 || recorded.messages[0] != "wallet JWT exchange refused" {
		t.Fatalf("expected one observable refusal, got %v", recorded.messages)
	}
	fields := map[string]any{}
	for i := 0; i+1 < len(recorded.tags[0]); i += 2 {
		key, ok := recorded.tags[0][i].(string)
		if !ok || (key != "operation" && key != "code" && key != "status") {
			t.Fatalf("refusal log must contain only operation/status/code, got %v", recorded.tags[0])
		}
		fields[key] = recorded.tags[0][i+1]
	}
	if fields["operation"] != OpAuthExchange || fields["code"] != "PARTNER_REQUIRED" || fields["status"] != http.StatusUnauthorized {
		t.Fatalf("refusal log is missing cutover diagnostics: %v", fields)
	}
}

// A wallet sign-in may last at most 48 hours (studio#1888). The token
// expires at the signed message's Expire At, so a message that names a later
// one is refused: one signature never unlocks more than that window.
func TestAuthExchangeCapsTheSignInLifetime(t *testing.T) {
	now := time.Now()

	accepted := []struct {
		name     string
		expireAt time.Time
	}{
		{"an hour", now.Add(time.Hour)},
		{"48 hours", now.Add(authTokenExpirationDuration)},
		{"48 hours by a client clock 4 minutes ahead", now.Add(authTokenExpirationDuration + 4*time.Minute)},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			rec, err := exchangeSignedMessage(t, tc.expireAt)
			if err != nil {
				t.Fatalf("expected a token, got %v", err)
			}
			var out generated.AuthExchangeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			claims := &jwt.RegisteredClaims{}
			if _, err := jwt.ParseWithClaims(out.Token, claims, func(*jwt.Token) (interface{}, error) {
				return testJwtSecret, nil
			}); err != nil {
				t.Fatalf("parse token: %v", err)
			}
			if got, want := claims.ExpiresAt.Time, tc.expireAt.Truncate(time.Second); !got.Equal(want) {
				t.Fatalf("token expires at %v, want the message's %v", got, want)
			}
		})
	}

	refused := []struct {
		name     string
		expireAt time.Time
	}{
		{"48 hours by a client clock 6 minutes ahead", now.Add(authTokenExpirationDuration + 6*time.Minute)},
		{"49 hours", now.Add(authTokenExpirationDuration + time.Hour)},
		{"a year", now.Add(365 * 24 * time.Hour)},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			_, err := exchangeSignedMessage(t, tc.expireAt)
			assertHTTPStatus(t, err, http.StatusBadRequest)
			if code := err.(*restmw.HTTPError).Code; code != "AUTH_EXPIRY_TOO_FAR" {
				t.Fatalf("expected AUTH_EXPIRY_TOO_FAR, got %s", code)
			}
		})
	}
}
