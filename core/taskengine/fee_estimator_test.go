package taskengine

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
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

	// Test with different RPC endpoints to verify chain ID detection
	testCases := []struct {
		name            string
		rpcURL          string
		expectedChainID int64
	}{
		{
			name:            "Ethereum Mainnet",
			rpcURL:          "https://eth.llamarpc.com",
			expectedChainID: 1,
		},
		{
			name:            "Base Mainnet",
			rpcURL:          "https://mainnet.base.org",
			expectedChainID: 8453,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create eth client
			ethClient, err := ethclient.Dial(tc.rpcURL)
			require.NoError(t, err, "Failed to connect to RPC")
			defer ethClient.Close()

			// Create mock smart wallet config
			smartWalletConfig := &config.SmartWalletConfig{
				FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
			}

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

			assert.Equal(t, tc.expectedChainID, detectedChainID, "Detected chain ID should match expected")
			assert.Equal(t, tc.expectedChainID, feeEstimator.chainID, "Chain ID should be cached")

			// Test that subsequent calls return cached value
			cachedChainID, err := feeEstimator.getChainID(ctx)
			require.NoError(t, err, "Failed to get cached chain ID")
			assert.Equal(t, tc.expectedChainID, cachedChainID, "Cached chain ID should match")

			// Test utility methods use correct chain ID
			testAmount := big.NewInt(1000000000000000000) // 1 ETH in wei
			feeAmount, err := feeEstimator.convertToFeeAmount(testAmount)
			require.NoError(t, err, "Failed to convert fee amount")

			assert.Equal(t, "ETH", feeAmount.NativeTokenSymbol, "Symbol should be ETH")
			assert.Equal(t, testAmount.String(), feeAmount.NativeTokenAmount, "Native token amount should match input")

			// Verify price was fetched for correct chain ID (different chains have different mock prices)
			if tc.expectedChainID == 11155111 || tc.expectedChainID == 84532 {
				// Testnet chains should have 2500 price
				assert.Contains(t, feeAmount.UsdAmount, "2500", "USD amount should reflect testnet price")
			} else {
				// Mainnet chains should have 3000 price
				assert.Contains(t, feeAmount.UsdAmount, "3000", "USD amount should reflect mainnet price")
			}
		})
	}
}

func TestFeeEstimator_MoralisServiceIntegration(t *testing.T) {
	// Test with actual Moralis service if API key is available
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err, "Failed to create logger")

	// Connect to Ethereum mainnet
	ethClient, err := ethclient.Dial("https://eth.llamarpc.com")
	require.NoError(t, err, "Failed to connect to Ethereum RPC")
	defer ethClient.Close()

	smartWalletConfig := &config.SmartWalletConfig{
		FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

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
	assert.Equal(t, int64(1), chainID, "Should detect Ethereum mainnet")

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
