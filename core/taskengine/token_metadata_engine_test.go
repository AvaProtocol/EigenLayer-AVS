package taskengine

import (
	"os"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// TestEngine_GetTokenMetadata tests the full engine integration for GetTokenMetadata gRPC endpoint
// This is an integration test that validates the complete flow from gRPC request to response
func TestEngine_GetTokenMetadata(t *testing.T) {
	// Change to project root directory so we can find token_whitelist/
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Go up two levels: from core/taskengine to project root
	err := os.Chdir("../..")
	require.NoError(t, err)

	// Create a mock config with a valid RPC URL
	cfg := &config.Config{
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "https://eth.llamarpc.com", // Valid RPC URL for testing
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	// Create engine with nil storage and queue (we only need token service for this test)
	logger := &MockLogger{}
	engine := New(nil, cfg, nil, logger)

	// Verify token enrichment service was initialized
	require.NotNil(t, engine.tokenEnrichmentService, "Token enrichment service should be initialized")

	// Verify whitelist was loaded
	require.Greater(t, engine.tokenEnrichmentService.GetCacheSize(), 0, "Whitelist should be loaded")

	// Create a mock user for gRPC requests
	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	t.Run("should return whitelist data for known tokens", func(t *testing.T) {
		// Test with a known token address (this test is flexible for different chains)
		req := &avsproto.GetTokenMetadataReq{
			Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", // USDC on mainnet
		}

		resp, err := engine.GetTokenMetadata(user, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.True(t, resp.Found, "Should find token (either in whitelist or via RPC)")

		// Check that we get proper response structure
		require.NotNil(t, resp.Token)
		require.NotEmpty(t, resp.Source)
		require.Contains(t, []string{"whitelist", "rpc"}, resp.Source)

		// Validate token structure
		require.NotEmpty(t, resp.Token.Id)
		require.NotEmpty(t, resp.Token.Name)
		require.NotEmpty(t, resp.Token.Symbol)
		require.GreaterOrEqual(t, resp.Token.Decimals, uint32(0))

		t.Logf("Token found: Name=%s, Symbol=%s, Decimals=%d, Source=%s",
			resp.Token.Name, resp.Token.Symbol, resp.Token.Decimals, resp.Source)
	})

	t.Run("should handle RPC fallback for unknown tokens", func(t *testing.T) {
		// Test with a non-existent but valid address format
		req := &avsproto.GetTokenMetadataReq{
			Address: "0x1234567890123456789012345678901234567890",
		}

		resp, err := engine.GetTokenMetadata(user, req)
		require.NoError(t, err)
		require.NotNil(t, resp)

		// RPC typically returns placeholder data for non-existent contracts
		if resp.Found {
			require.Equal(t, "rpc", resp.Source, "Non-whitelist addresses should use RPC source")
			require.NotNil(t, resp.Token)

			// RPC calls to non-existent contracts typically return default values
			if resp.Token.Name == "Unknown Token" {
				require.Equal(t, "UNKNOWN", resp.Token.Symbol)
			}
		}

		t.Logf("Non-existent address response: Found=%v, Source=%s", resp.Found, resp.Source)
	})

	t.Run("should handle case insensitive addresses", func(t *testing.T) {
		// Test with both lowercase and uppercase versions of the same address
		lowercaseReq := &avsproto.GetTokenMetadataReq{
			Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		}
		uppercaseReq := &avsproto.GetTokenMetadataReq{
			Address: "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
		}

		respLower, err := engine.GetTokenMetadata(user, lowercaseReq)
		require.NoError(t, err)

		respUpper, err := engine.GetTokenMetadata(user, uppercaseReq)
		require.NoError(t, err)

		// Both should return the same results
		require.Equal(t, respLower.Found, respUpper.Found)
		require.Equal(t, respLower.Source, respUpper.Source)

		if respLower.Found && respUpper.Found {
			require.Equal(t, respLower.Token.Name, respUpper.Token.Name)
			require.Equal(t, respLower.Token.Symbol, respUpper.Token.Symbol)
			require.Equal(t, respLower.Token.Decimals, respUpper.Token.Decimals)
			// Id should always be returned in lowercase
			require.Equal(t, strings.ToLower(respLower.Token.Id), respLower.Token.Id)
			require.Equal(t, strings.ToLower(respUpper.Token.Id), respUpper.Token.Id)
		}
	})

	t.Run("should handle invalid addresses", func(t *testing.T) {
		invalidAddresses := []string{
			"",        // Empty
			"invalid", // Not hex
			"0x123",   // Too short
			"0x12345678901234567890123456789012345678901", // Too long
		}

		for _, addr := range invalidAddresses {
			req := &avsproto.GetTokenMetadataReq{Address: addr}

			_, err := engine.GetTokenMetadata(user, req)
			if addr == "" {
				// Empty address should return an error
				require.Error(t, err, "Empty address should return error")
			} else {
				// Other invalid formats should also return errors
				require.Error(t, err, "Invalid address format should return error: %s", addr)
			}
		}
	})

	t.Run("should initialize token service correctly", func(t *testing.T) {
		// Verify the engine was set up correctly
		require.NotNil(t, engine.tokenEnrichmentService)

		// Should have loaded some tokens from whitelist
		cacheSize := engine.tokenEnrichmentService.GetCacheSize()
		require.Greater(t, cacheSize, 0, "Should have loaded whitelist tokens")

		// Chain ID should be set
		chainID := engine.tokenEnrichmentService.GetChainID()
		require.Greater(t, chainID, uint64(0), "Chain ID should be set")

		t.Logf("Token service initialized: %d tokens loaded for chain %d", cacheSize, chainID)
	})
}
