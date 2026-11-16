//go:build integration

package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/avsclient"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/require"
)

// The SDK local runner owns this opt-in fixture's lifetime. HTTP, partner/SIWE
// auth, JWT verification, routing, Badger and inventory logic are real. Address
// derivation uses Engine's deterministic test mode; nonce reads are deterministic.
// No worker, Alchemy request, account deployment, grant or UserOp is involved.
func TestSDKWalletGatewayFixture(t *testing.T) {
	statePath := os.Getenv("SDK_WALLET_GATEWAY_STATE_FILE")
	if statePath == "" {
		t.Skip("started explicitly by ava-sdk-js test:wallet-inventory:local")
	}
	publicKey, partnerKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)
	sepolia := &config.SmartWalletConfig{ChainID: 11155111, EthRpcUrl: "mock://sdk-wallet-inventory"}
	base := &config.SmartWalletConfig{ChainID: 8453, EthRpcUrl: "mock://sdk-wallet-inventory"}
	cfg := &config.Config{
		SmartWallet: sepolia, IsGateway: true, JwtSecret: secret,
		WalletInventory: &config.WalletInventoryConfig{MaxActiveWalletsPerOwner: 3},
		Chains: []*config.ChainConfig{
			{ChainID: sepolia.ChainID, Name: "sepolia", SmartWallet: sepolia},
			{ChainID: base.ChainID, Name: "base", SmartWallet: base},
		},
		RestRateLimitPerSecond: 200, RestRateLimitBurst: 1000,
		PartnerAssertionAudience: "sdk-wallet-e2e-local",
		Partners: []config.PartnerConfig{{
			ID: "sdk-wallet-e2e", PublicKeys: []string{base64.StdEncoding.EncodeToString(publicKey)}, Status: "active",
			Scopes: []string{"read", "auth:exchange"},
		}},
	}
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	logger := testutil.GetLogger()
	engine := taskengine.New(db, cfg, nil, logger)
	taskengine.RegisterChainStateReader(uint64(sepolia.ChainID), &sdkWalletNonceReader{sequence: 2})
	taskengine.RegisterChainStateReader(uint64(base.ChainID), &sdkWalletNonceReader{sequence: 7})
	t.Cleanup(taskengine.ClearChainStateReaderRegistry)
	e := echo.New()
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod: true, LogURI: true, LogStatus: true, HandleError: true,
		LogValuesFunc: func(_ echo.Context, value middleware.RequestLoggerValues) error {
			t.Logf("SDK_HTTP status=%d method=%s path=%s", value.Status, value.Method, value.URI)
			return nil
		},
	}))
	rest.NewServer(engine, logger, cfg, avsclient.Surface{}).Mount(e)
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	state, err := json.Marshal(map[string]interface{}{
		"baseUrl":           server.URL + "/api/v1",
		"partnerPrivateKey": base64.StdEncoding.EncodeToString(partnerKey.Seed()),
		"partnerIssuer":     "sdk-wallet-e2e", "partnerAudience": cfg.PartnerAssertionAudience,
	})
	require.NoError(t, err)
	// The key is ephemeral and scoped to this loopback fixture. Never log it.
	require.NoError(t, os.WriteFile(statePath, state, 0600))
	t.Log("SDK_GATEWAY_READY: isolated local HTTP gateway; deterministic chain state")
	timer := time.NewTimer(8 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(statePath + ".stop"); err == nil {
				return
			}
		case <-timer.C:
			t.Fatal("SDK gateway fixture timed out waiting for its runner to stop it")
		}
	}
}

type sdkWalletNonceReader struct {
	taskengine.ChainStateReader
	sequence int64
}

func (r *sdkWalletNonceReader) GetEntryPointNonce(_ context.Context, _ common.Address, key *big.Int) (*big.Int, error) {
	return new(big.Int).Add(new(big.Int).Lsh(new(big.Int).Set(key), 64), big.NewInt(r.sequence)), nil
}
