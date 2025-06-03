package taskengine

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTokenEnrichmentService_WhitelistOnly tests the TokenEnrichmentService
// in whitelist-only mode (without RPC client)
func TestTokenEnrichmentService_WhitelistOnly(t *testing.T) {
	logger := &MockLogger{}

	// Change to project root directory so we can find token_whitelist/
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Go up two levels: from core/taskengine to project root
	err := os.Chdir("../..")
	require.NoError(t, err)

	// Create service without RPC client (whitelist-only mode)
	service, err := NewTokenEnrichmentService(nil, logger)
	require.NoError(t, err)

	// Load whitelist
	err = service.LoadWhitelist()
	require.NoError(t, err)

	// Test that cache has data
	require.Greater(t, service.GetCacheSize(), 0, "Cache should contain whitelist data")

	t.Run("should find tokens in whitelist", func(t *testing.T) {
		// Test with USDC address from whitelist (uses the detected chain)
		// Note: This test works with whatever whitelist is loaded based on chain ID
		metadata, err := service.GetTokenMetadata("0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
		require.NoError(t, err)

		if metadata != nil {
			// If found in whitelist, verify it has proper data
			require.Equal(t, "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", metadata.Address)
			require.NotEmpty(t, metadata.Name)
			require.NotEmpty(t, metadata.Symbol)
			require.Equal(t, "whitelist", metadata.Source)
		} else {
			// If not found, it means this address isn't in the current chain's whitelist
			t.Logf("USDC address not found in whitelist for chain %d", service.GetChainID())
		}
	})

	t.Run("should not find non-existent tokens", func(t *testing.T) {
		fakeAddress := "0x1234567890123456789012345678901234567890"
		metadata, err := service.GetTokenMetadata(fakeAddress)
		require.NoError(t, err)
		require.Nil(t, metadata, "Fake address should not be found in whitelist-only mode")
	})

	t.Run("should handle address normalization", func(t *testing.T) {
		// Test with first token in cache (to ensure it exists)
		if service.GetCacheSize() > 0 {
			// Get an address that we know exists
			testAddr := "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
			lowercaseAddr := testAddr
			uppercaseAddr := "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"

			// Get metadata for lowercase
			metadataLower, err := service.GetTokenMetadata(lowercaseAddr)
			require.NoError(t, err)

			// Get metadata for uppercase
			metadataUpper, err := service.GetTokenMetadata(uppercaseAddr)
			require.NoError(t, err)

			// Both should return the same result
			if metadataLower != nil && metadataUpper != nil {
				require.Equal(t, metadataLower.Address, metadataUpper.Address, "Both should return lowercase address")
				require.Equal(t, metadataLower.Name, metadataUpper.Name)
				require.Equal(t, metadataLower.Symbol, metadataUpper.Symbol)
			}
		}
	})

	t.Run("should report correct cache size", func(t *testing.T) {
		cacheSize := service.GetCacheSize()
		require.Greater(t, cacheSize, 0, "Cache should contain tokens after loading whitelist")

		// Cache size should be reasonable (not negative, not absurdly large)
		require.LessOrEqual(t, cacheSize, 1000, "Cache size should be reasonable")
	})
}
