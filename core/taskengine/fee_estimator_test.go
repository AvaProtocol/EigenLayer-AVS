package taskengine

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPriceService implements PriceService interface for testing
type mockPriceService struct{}

func (m *mockPriceService) GetNativeTokenPriceUSD(chainID int64) (*big.Float, error) {
	// Return different prices based on chain ID to verify it's being passed correctly
	switch chainID {
	case 1: // Ethereum mainnet
		return big.NewFloat(3000.0), nil
	case 11155111: // Sepolia
		return big.NewFloat(2500.0), nil
	case 8453: // Base mainnet
		return big.NewFloat(3000.0), nil
	case 84532: // Base Sepolia
		return big.NewFloat(2500.0), nil
	default:
		return big.NewFloat(2000.0), nil // Fallback
	}
}

func (m *mockPriceService) GetNativeTokenSymbol(chainID int64) string {
	return "ETH" // All supported chains use ETH
}

func TestFeeEstimator_ChainIDDetection(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err, "Failed to create logger")

	smartWalletConfig := testutil.GetTestSmartWalletConfig()

	// Create eth client from test config RPC
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	// Create fee estimator with mock price service
	feeEstimator := NewFeeEstimator(
		logger,
		ethClient,
		nil, // No tenderly client needed for this test
		smartWalletConfig,
		&mockPriceService{},
	)

	// Verify chain ID starts as 0 (not detected yet)
	assert.Equal(t, int64(0), feeEstimator.chainID, "Chain ID should be 0 initially")

	// Test chain ID detection
	ctx := context.Background()
	detectedChainID, err := feeEstimator.getChainID(ctx)
	require.NoError(t, err, "Failed to detect chain ID")
	assert.True(t, detectedChainID > 0, "Detected chain ID should be positive")
	assert.Equal(t, detectedChainID, feeEstimator.chainID, "Chain ID should be cached")

	// Test that subsequent calls return cached value
	cachedChainID, err := feeEstimator.getChainID(ctx)
	require.NoError(t, err, "Failed to get cached chain ID")
	assert.Equal(t, detectedChainID, cachedChainID, "Cached chain ID should match")

	// Test utility methods use correct chain ID
	testAmount := big.NewInt(1000000000000000000) // 1 ETH in wei
	feeAmount, err := feeEstimator.convertToFeeAmount(testAmount)
	require.NoError(t, err, "Failed to convert fee amount")

	assert.Equal(t, "ETH", feeAmount.NativeTokenSymbol, "Symbol should be ETH")
	assert.Equal(t, testAmount.String(), feeAmount.NativeTokenAmount, "Native token amount should match input")

	// Verify USD amount is non-empty (the mock returns a price for any chain)
	assert.NotEmpty(t, feeAmount.UsdAmount, "USD amount should be set")
}

func TestFeeEstimator_MoralisServiceIntegration(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err, "Failed to create logger")

	smartWalletConfig := testutil.GetTestSmartWalletConfig()

	// Connect using test config RPC
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	// Create Moralis service (will use fallback pricing if no API key)
	moralisService := services.GetMoralisService("", logger)

	// Create fee estimator
	feeEstimator := NewFeeEstimator(
		logger,
		ethClient,
		nil,
		smartWalletConfig,
		moralisService,
	)

	ctx := context.Background()

	// Test chain ID detection
	chainID, err := feeEstimator.getChainID(ctx)
	require.NoError(t, err, "Failed to detect chain ID")
	assert.True(t, chainID > 0, "Should detect a valid chain ID")

	// Test Moralis service integration for price data
	price, err := moralisService.GetNativeTokenPriceUSD(chainID)
	require.NoError(t, err, "Failed to get ETH price")
	assert.True(t, price.Cmp(big.NewFloat(0)) > 0, "Price should be positive")

	symbol := moralisService.GetNativeTokenSymbol(chainID)
	assert.Equal(t, "ETH", symbol, "Symbol should be ETH")

	// Test supported chains
	supportedChains := moralisService.GetSupportedChains()
	assert.Contains(t, supportedChains, int64(1), "Should support Ethereum mainnet")
	assert.Contains(t, supportedChains, int64(8453), "Should support Base mainnet")
	assert.Len(t, supportedChains, 4, "Should support exactly 4 chains (ETH + Base mainnet/testnet)")
}
