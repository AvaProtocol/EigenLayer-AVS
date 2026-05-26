//go:build integration
// +build integration

// Package api_test exercises the REST surface end-to-end against an
// in-process aggregator (engine + Echo router) — no external services,
// no real RPC, no operator stream. The tests give the SDK rewrite a
// regression safety net: any change to wire shape or auth flow breaks
// these tests immediately.
//
// Build tag `integration` keeps them out of `go test ./...` because
// the harness still needs the engine's full init path. Run with
// `make test/integration`.
package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest"
	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// testHarness owns one aggregator's worth of state. Each test spins
// up a fresh harness so they can run in parallel without sharing the
// engine or the BadgerDB instance.
type testHarness struct {
	t      *testing.T
	server *httptest.Server
	engine *taskengine.Engine
	cfg    *config.Config
	db     storage.Storage
	jwt    string
}

// newHarness boots an in-memory aggregator and mounts the REST router
// on an httptest server. The returned harness ships with a JWT minted
// for the standard test user — handlers that require auth use it via
// authedRequest below.
func newHarness(t *testing.T) *testHarness {
	t.Helper()

	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	cfg := testutil.GetAggregatorConfig()
	// Pin a deterministic JWT secret so the harness can mint tokens the
	// REST middleware accepts without depending on whatever the YAML
	// happens to carry.
	cfg.JwtSecret = []byte("integration-test-jwt-secret-do-not-use-in-prod")

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	engine := taskengine.New(db, cfg, nil, logger)
	require.NoError(t, engine.MustStart(), "engine.MustStart")
	t.Cleanup(engine.Stop)

	e := echo.New()
	// Tests run against an in-process engine — no operators registered,
	// no smart-wallet RPC, no withdraw service. Handlers that need
	// those deps return a structured 501; the lifecycle/auth tests
	// don't touch them.
	srv := rest.NewServer(engine, logger, cfg, rest.ServerDeps{})
	srv.Mount(e)

	httpServer := httptest.NewServer(e)
	t.Cleanup(httpServer.Close)

	user := testutil.TestUser1()
	token := mintJWT(t, cfg.JwtSecret, user.Address.Hex(), cfg.SmartWallet.ChainID)

	// Seed the user's smart wallet so engine.ValidWalletOwner accepts
	// the workflow's smartWalletAddress. Production callers reach this
	// state via POST /wallets; tests skip the RPC-driven derivation
	// and stuff the record straight into BadgerDB.
	require.NoError(t, taskengine.StoreWallet(db, user.Address, &model.SmartWallet{
		Owner:   &user.Address,
		Address: user.SmartAccountAddress,
		Factory: walletFactoryPointer(cfg),
		Salt:    big.NewInt(0),
	}))

	return &testHarness{
		t:      t,
		server: httpServer,
		engine: engine,
		cfg:    cfg,
		db:     db,
		jwt:    token,
	}
}

// walletFactoryPointer returns a pointer to the configured factory
// address, or nil when the YAML didn't set one. StoreWallet accepts
// nil so the record persists even in single-chain test configs.
func walletFactoryPointer(cfg *config.Config) *common.Address {
	if cfg == nil || cfg.SmartWallet == nil {
		return nil
	}
	addr := cfg.SmartWallet.FactoryAddress
	return &addr
}

// mintJWT shortcuts the EIP-191 signature flow for tests — the REST
// middleware verifies HS256 against cfg.JwtSecret regardless of how
// the token got minted. Production callers exchange a personal_sign
// signature via POST /auth:exchange.
func mintJWT(t *testing.T, secret []byte, subject string, chainID int64) string {
	t.Helper()
	claims := &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Hour)),
		Issuer:    auth.Issuer,
		Subject:   subject,
		Audience:  jwt.ClaimStrings{intToStr(chainID)},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	require.NoError(t, err)
	return signed
}

func intToStr(v int64) string {
	// Avoid pulling strconv into every test file.
	return jwtAudienceFromInt(v)
}

// jwtAudienceFromInt mirrors the chainIDStr formatting the REST auth
// handler uses when minting tokens via /auth:exchange.
func jwtAudienceFromInt(v int64) string {
	return strings.TrimSpace(stringFromInt(v))
}

func stringFromInt(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// doRequest issues an HTTP request against the harness and returns the
// raw status code + body. JSON responses are decoded by callers via
// decodeJSON below.
func (h *testHarness) doRequest(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	h.t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(h.t, err, "marshal request body")
		reqBody = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, h.server.URL+path, reqBody)
	require.NoError(h.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := h.server.Client().Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	return resp, raw
}

// authedRequest is doRequest with the harness's JWT pre-attached as
// the Bearer token. Use for every handler that calls requireUser.
func (h *testHarness) authedRequest(method, path string, body any) (*http.Response, []byte) {
	return h.doRequest(method, path, body, map[string]string{
		"Authorization": "Bearer " + h.jwt,
	})
}

// decodeJSON is the canonical decoder used by every test — fails the
// test on bad JSON rather than returning an error to keep callers
// terse.
func decodeJSON[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(raw, &out), "decode JSON: %s", string(raw))
	return out
}
