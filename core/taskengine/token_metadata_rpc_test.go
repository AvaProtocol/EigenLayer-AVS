package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTokenMetadataRPC(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create a test user
	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	t.Log("=== Testing GetTokenMetadata RPC Endpoint ===")

	// Test 1: Valid USDC address (should be in Sepolia whitelist)
	t.Run("ValidUSDCToken", func(t *testing.T) {
		req := &avsproto.GetTokenMetadataReq{
			Address: "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8", // USDC from sepolia.json
		}

		resp, err := engine.GetTokenMetadata(user, req)
		require.NoError(t, err, "GetTokenMetadata should not return error")
		require.NotNil(t, resp, "Response should not be nil")

		t.Logf("📋 Token Metadata Response:")
		t.Logf("  Found: %v", resp.Found)
		t.Logf("  Source: %s", resp.Source)
		if resp.Token != nil {
			t.Logf("  Address: %s", resp.Token.Address)
			t.Logf("  Name: %s", resp.Token.Name)
			t.Logf("  Symbol: %s", resp.Token.Symbol)
			t.Logf("  Decimals: %d", resp.Token.Decimals)
		}

		// Verify the response
		assert.True(t, resp.Found, "Token should be found")
		assert.NotNil(t, resp.Token, "Token data should not be nil")
		if resp.Token != nil {
			assert.Equal(t, "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8", resp.Token.Address)
			assert.Equal(t, "USDC", resp.Token.Symbol) // Should be either "USDC" or "USD Coin"
			assert.Equal(t, uint32(6), resp.Token.Decimals)
			assert.Contains(t, []string{"whitelist", "rpc", "cache"}, resp.Source)
		}
	})

	// Test 2: Invalid address format
	t.Run("InvalidAddressFormat", func(t *testing.T) {
		req := &avsproto.GetTokenMetadataReq{
			Address: "invalid-address",
		}

		resp, err := engine.GetTokenMetadata(user, req)
		assert.Error(t, err, "Should return error for invalid address")
		assert.NotNil(t, resp, "Response should not be nil even on error")
		assert.False(t, resp.Found, "Token should not be found for invalid address")
	})

	// Test 3: Empty address
	t.Run("EmptyAddress", func(t *testing.T) {
		req := &avsproto.GetTokenMetadataReq{
			Address: "",
		}

		resp, err := engine.GetTokenMetadata(user, req)
		assert.Error(t, err, "Should return error for empty address")
		assert.NotNil(t, resp, "Response should not be nil even on error")
		assert.False(t, resp.Found, "Token should not be found for empty address")
	})

	// Test 4: Valid hex address but non-ERC20 contract (or non-existent)
	t.Run("NonERC20Contract", func(t *testing.T) {
		req := &avsproto.GetTokenMetadataReq{
			Address: "0x1111111111111111111111111111111111111111", // Valid hex but likely non-existent
		}

		resp, err := engine.GetTokenMetadata(user, req)
		// Should not error out, just return not found
		assert.NoError(t, err, "Should not error for valid hex address")
		assert.NotNil(t, resp, "Response should not be nil")
		// Token might not be found if it's not a valid ERC20 contract
		t.Logf("📋 Non-ERC20 Response: Found=%v, Source=%s", resp.Found, resp.Source)
	})

	// Test 5: Test with engine that has no TokenEnrichmentService
	t.Run("NoTokenEnrichmentService", func(t *testing.T) {
		// Create engine with no RPC client to simulate missing service
		engineNoService := &Engine{
			db:                     db,
			logger:                 testutil.GetLogger(),
			tokenEnrichmentService: nil, // Explicitly nil
		}

		req := &avsproto.GetTokenMetadataReq{
			Address: "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8",
		}

		resp, err := engineNoService.GetTokenMetadata(user, req)
		assert.Error(t, err, "Should return error when service is not available")
		assert.NotNil(t, resp, "Response should not be nil even on error")
		assert.False(t, resp.Found, "Token should not be found when service unavailable")
	})

	t.Log("=== GetTokenMetadata RPC Test Complete ===")
}

func TestGetTokenMetadataIntegration(t *testing.T) {
	// This is a more comprehensive integration test that tests the entire flow
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create a test user
	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	t.Log("=== Integration Test: Multiple Token Lookups ===")

	// Test multiple tokens from the whitelist
	testTokens := []struct {
		name    string
		address string
		symbol  string
	}{
		{"USDC", "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8", "USDC"},
		{"WETH", "0x7b79995e5f793a07bc00c21412e50ecae098e7f9", "WETH"},
		{"DAI", "0x3e622317f8c93f7328350cf0b56d9ed4c620c5d6", "DAI"},
	}

	for _, testToken := range testTokens {
		t.Run(testToken.name, func(t *testing.T) {
			req := &avsproto.GetTokenMetadataReq{
				Address: testToken.address,
			}

			resp, err := engine.GetTokenMetadata(user, req)
			assert.NoError(t, err, "Should not return error for %s", testToken.name)
			assert.NotNil(t, resp, "Response should not be nil for %s", testToken.name)

			if resp.Found && resp.Token != nil {
				t.Logf("✅ %s: %s (%d decimals) - Source: %s",
					resp.Token.Symbol, resp.Token.Name, resp.Token.Decimals, resp.Source)

				// Verify basic properties
				assert.NotEmpty(t, resp.Token.Symbol, "Symbol should not be empty")
				assert.NotEmpty(t, resp.Token.Name, "Name should not be empty")
				assert.True(t, resp.Token.Decimals <= 18, "Decimals should be reasonable")
			} else {
				t.Logf("⚠️  %s not found - this may be expected if not available via RPC", testToken.name)
			}
		})
	}

	t.Log("=== Integration Test Complete ===")
}
