package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)

	s := &Server{config: &config.Config{JwtSecret: testJwtSecret}}
	return rec, s.AuthExchange(ctx)
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
