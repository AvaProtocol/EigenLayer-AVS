//go:build integration
// +build integration

package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// TestAuthExchangeRoundTrip drives the production EIP-191 auth flow:
// (1) sign the canonical template with a fresh keypair,
// (2) POST it to /auth:exchange,
// (3) confirm the returned JWT works against a protected endpoint.
//
// This is the path the SDK uses on first connect — breaking it would
// block every new user.
func TestAuthExchangeRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// Fresh, deterministic keypair so the test doesn't leak the static
	// TestUser1 key into production telemetry.
	pk, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(pk.PublicKey)

	chainID := h.cfg.SmartWallet.ChainID
	now := time.Now().UTC()
	message := fmt.Sprintf(authTemplate,
		chainID,
		"vTest",
		now.Format("2006-01-02T15:04:05.000Z"),
		now.Add(2*time.Hour).Format("2006-01-02T15:04:05.000Z"),
		owner.Hex())

	hash := accounts.TextHash([]byte(message))
	sig, err := crypto.Sign(hash, pk)
	require.NoError(t, err)
	// Geth signs with v in {0,1}; the REST handler accepts either
	// {0,1} or {27,28}. Pass {0,1} as-is so the test exercises the
	// no-normalization branch.
	signature := hexutil.Encode(sig)

	resp, body := h.doRequest(http.MethodPost, "/api/v1/auth:exchange", generated.AuthExchangeRequest{
		Message:      message,
		Signature:    generated.Hex(signature),
		OwnerAddress: generated.EthereumAddress(owner.Hex()),
	}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	out := decodeJSON[generated.AuthExchangeResponse](t, body)
	require.NotEmpty(t, out.Token, "JWT should be present")
	require.NotNil(t, out.Subject)
	assert.Equal(t, owner.Hex(), string(*out.Subject))
	assert.WithinDuration(t, now.Add(2*time.Hour), out.ExpiresAt, time.Minute,
		"expiry should match the signed message's Expire At")

	// Confirm the returned JWT actually unlocks a protected endpoint —
	// the workflows count handler is the cheapest such proof.
	resp2, body2 := h.doRequest(http.MethodGet, "/api/v1/workflows:count", nil, map[string]string{
		"Authorization": "Bearer " + out.Token,
	})
	require.Equal(t, http.StatusOK, resp2.StatusCode, "auth round-trip count: body=%s", string(body2))
}

// TestAuthExchangeRejectsBadSignature checks the negative path —
// a mangled signature should produce 401 / problem+json, not 500.
func TestAuthExchangeRejectsBadSignature(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	pk, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(pk.PublicKey)
	chainID := h.cfg.SmartWallet.ChainID
	now := time.Now().UTC()
	message := fmt.Sprintf(authTemplate,
		chainID, "vTest",
		now.Format("2006-01-02T15:04:05.000Z"),
		now.Add(2*time.Hour).Format("2006-01-02T15:04:05.000Z"),
		owner.Hex())

	resp, body := h.doRequest(http.MethodPost, "/api/v1/auth:exchange", generated.AuthExchangeRequest{
		Message:      message,
		Signature:    "0xdeadbeef", // too short — handler rejects up front
		OwnerAddress: generated.EthereumAddress(owner.Hex()),
	}, nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "body=%s", string(body))
}

// TestAuthExchangeRejectsExpiredMessage proves the handler honors the
// Expire At line in the signed template — a signature signed in the
// past should be rejected even when the cryptography checks out.
func TestAuthExchangeRejectsExpiredMessage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	pk, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(pk.PublicKey)
	chainID := h.cfg.SmartWallet.ChainID
	past := time.Now().Add(-2 * time.Hour).UTC()
	message := fmt.Sprintf(authTemplate,
		chainID, "vTest",
		past.Format("2006-01-02T15:04:05.000Z"),
		past.Add(1*time.Hour).Format("2006-01-02T15:04:05.000Z"),
		owner.Hex())

	hash := accounts.TextHash([]byte(message))
	sig, err := crypto.Sign(hash, pk)
	require.NoError(t, err)
	signature := hexutil.Encode(sig)

	resp, _ := h.doRequest(http.MethodPost, "/api/v1/auth:exchange", generated.AuthExchangeRequest{
		Message:      message,
		Signature:    generated.Hex(signature),
		OwnerAddress: generated.EthereumAddress(owner.Hex()),
	}, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// authTemplate is the canonical EIP-191 message format the aggregator
// signs/verifies. Kept verbatim here (not imported) because the
// aggregator package's copy is unexported and we want this test to
// double as documentation of the wire shape SDKs must reproduce.
const authTemplate = `Please sign the below text for ownership verification.

URI: https://app.avaprotocol.org
Chain ID: %d
Version: %s
Issued At: %s
Expire At: %s
Wallet: %s`
