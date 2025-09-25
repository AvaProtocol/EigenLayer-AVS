package services

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/go-resty/resty/v2"
)

// MoralisService provides Web3 data using Moralis Web3 Data API
// Currently focused on native token price data but can be extended for other Moralis features such as:
// - NFT data and metadata
// - DeFi protocol data
// - Token transfers and transaction history
// - Wallet balances and portfolio data
// - Block and transaction data
// Implements singleton pattern for aggregator-wide shared instance
type MoralisService struct {
	apiKey     string
	httpClient *resty.Client
	logger     sdklogging.Logger

	// Cache for price data with TTL
	cache    map[string]*CachedPrice
	cacheMux sync.RWMutex
	cacheTTL time.Duration

	// Chain ID to native token mapping
	chainTokens map[int64]ChainToken
}

// ChainToken represents native token information for a blockchain
type ChainToken struct {
	Symbol       string `json:"symbol"`
	Decimals     int    `json:"decimals"`
	ContractAddr string `json:"contract_addr,omitempty"` // For wrapped tokens like WETH
}

// CachedPrice represents a cached price entry with timestamp
type CachedPrice struct {
	Price     *big.Float `json:"price"`
	Symbol    string     `json:"symbol"`
	Timestamp time.Time  `json:"timestamp"`
}

// MoralisTokenPriceResponse represents Moralis API response for token price
type MoralisTokenPriceResponse struct {
	TokenName     string `json:"tokenName"`
	TokenSymbol   string `json:"tokenSymbol"`
	TokenLogo     string `json:"tokenLogo"`
	TokenDecimals string `json:"tokenDecimals"`
	NativePrice   struct {
		Value    string `json:"value"`
		Decimals int    `json:"decimals"`
		Name     string `json:"name"`
		Symbol   string `json:"symbol"`
		Address  string `json:"address"`
	} `json:"nativePrice"`
	UsdPrice                float64 `json:"usdPrice"`
	UsdPriceFormatted       string  `json:"usdPriceFormatted"`
	ExchangeAddress         string  `json:"exchangeAddress"`
	ExchangeName            string  `json:"exchangeName"`
	TokenAddress            string  `json:"tokenAddress"`
	PriceLastChangedAtBlock string  `json:"priceLastChangedAtBlock"`
	BlockTimestamp          string  `json:"blockTimestamp"`
	PossibleSpam            bool    `json:"possibleSpam"`
	VerifiedContract        bool    `json:"verifiedContract"`
}

// Singleton instance
var (
	moralisServiceInstance *MoralisService
	moralisOnce            sync.Once
)

// GetMoralisService returns the singleton instance of MoralisService
func GetMoralisService(apiKey string, logger sdklogging.Logger) *MoralisService {
	moralisOnce.Do(func() {
		moralisServiceInstance = &MoralisService{
			apiKey:      apiKey,
			httpClient:  resty.New().SetTimeout(10 * time.Second),
			logger:      logger,
			cache:       make(map[string]*CachedPrice),
			cacheTTL:    5 * time.Minute, // Cache prices for 5 minutes
			chainTokens: getChainTokenMapping(),
		}

		// Set common headers for Moralis API
		moralisServiceInstance.httpClient.SetHeaders(map[string]string{
			"Accept":     "application/json",
			"X-API-Key":  apiKey,
			"User-Agent": "AvaProtocol-EigenLayer-AVS/1.0",
		})

		logger.Info("🌐 Moralis Service initialized",
			"cache_ttl_minutes", 5,
			"supported_chains", len(moralisServiceInstance.chainTokens))
	})

	return moralisServiceInstance
}

// =============================================================================
// CONFIGURATION AND INITIALIZATION
// =============================================================================

// getChainTokenMapping returns native token information for supported chains
// Only includes chains that the aggregator actually supports: Ethereum and Base
func getChainTokenMapping() map[int64]ChainToken {
	return map[int64]ChainToken{
		// Ethereum Mainnet and Testnet
		1: {
			Symbol:       "ETH",
			Decimals:     18,
			ContractAddr: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", // WETH for price lookup
		},
		11155111: { // Sepolia
			Symbol:       "ETH",
			Decimals:     18,
			ContractAddr: "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14", // WETH on Sepolia
		},

		// Base Mainnet and Testnet
		8453: {
			Symbol:       "ETH",
			Decimals:     18,
			ContractAddr: "0x4200000000000000000000000000000000000006", // WETH on Base
		},
		84532: { // Base Sepolia
			Symbol:       "ETH",
			Decimals:     18,
			ContractAddr: "0x4200000000000000000000000000000000000006", // WETH on Base Sepolia
		},
	}
}

// =============================================================================
// PRICE DATA METHODS
// =============================================================================

// GetNativeTokenPriceUSD implements PriceService interface
func (ms *MoralisService) GetNativeTokenPriceUSD(chainID int64) (*big.Float, error) {
	chainToken, exists := ms.chainTokens[chainID]
	if !exists {
		ms.logger.Warn("Unsupported chain ID for price lookup", "chain_id", chainID)
		// Return ETH price as fallback for unsupported chains
		return ms.getETHPrice()
	}

	// Check cache first
	cacheKey := fmt.Sprintf("chain_%d", chainID)
	if cachedPrice := ms.getCachedPrice(cacheKey); cachedPrice != nil {
		ms.logger.Debug("Using cached price",
			"chain_id", chainID,
			"symbol", cachedPrice.Symbol,
			"price_usd", cachedPrice.Price,
			"age_seconds", int(time.Since(cachedPrice.Timestamp).Seconds()))
		return cachedPrice.Price, nil
	}

	// Fetch fresh price from Moralis API
	price, err := ms.fetchTokenPrice(chainID, chainToken)
	if err != nil {
		ms.logger.Error("Failed to fetch token price from Moralis",
			"chain_id", chainID,
			"symbol", chainToken.Symbol,
			"error", err)

		// Return fallback price based on token type
		return ms.getFallbackPrice(chainToken.Symbol), nil
	}

	// Cache the result
	ms.setCachedPrice(cacheKey, price, chainToken.Symbol)

	ms.logger.Debug("Fetched fresh token price",
		"chain_id", chainID,
		"symbol", chainToken.Symbol,
		"price_usd", price)

	return price, nil
}

// GetNativeTokenSymbol implements PriceService interface
func (ms *MoralisService) GetNativeTokenSymbol(chainID int64) string {
	if chainToken, exists := ms.chainTokens[chainID]; exists {
		return chainToken.Symbol
	}
	return "ETH" // Default fallback
}

// GetPriceDataAge returns the age of cached price data in seconds
func (ms *MoralisService) GetPriceDataAge(chainID int64) int64 {
	cacheKey := fmt.Sprintf("chain_%d", chainID)
	ms.cacheMux.RLock()
	defer ms.cacheMux.RUnlock()

	if cachedPrice, exists := ms.cache[cacheKey]; exists {
		return int64(time.Since(cachedPrice.Timestamp).Seconds())
	}
	return -1 // No cached data
}

// fetchTokenPrice fetches token price from Moralis Web3 Data API
func (ms *MoralisService) fetchTokenPrice(chainID int64, chainToken ChainToken) (*big.Float, error) {
	if ms.apiKey == "" {
		// Try to return cached price as fallback
		cacheKey := fmt.Sprintf("chain_%d", chainID)
		ms.cacheMux.RLock()
		cachedPrice, exists := ms.cache[cacheKey]
		ms.cacheMux.RUnlock()
		if exists {
			return cachedPrice.Price, nil
		}
		return nil, fmt.Errorf("moralis API key not configured and no cached price available")
	}

	// Convert chainID to Moralis chain format
	moralisChain := ms.chainIDToMoralisChain(chainID)
	if moralisChain == "" {
		return nil, fmt.Errorf("unsupported chain ID: %d", chainID)
	}

	url := fmt.Sprintf("https://deep-index.moralis.io/api/v2.2/erc20/%s/price", chainToken.ContractAddr)

	resp, err := ms.httpClient.R().
		SetQueryParams(map[string]string{
			"chain": moralisChain,
		}).
		SetResult(&MoralisTokenPriceResponse{}).
		Get(url)

	if err != nil {
		return nil, fmt.Errorf("moralis API request failed: %w", err)
	}

	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("moralis API returned status %d: %s", resp.StatusCode(), resp.String())
	}

	result := resp.Result().(*MoralisTokenPriceResponse)

	// Validate response
	if result.UsdPrice <= 0 {
		return nil, fmt.Errorf("invalid price data from Moralis: %f", result.UsdPrice)
	}

	return big.NewFloat(result.UsdPrice), nil
}

// chainIDToMoralisChain converts chain ID to Moralis chain identifier
// Only supports Ethereum and Base chains that the aggregator works with
func (ms *MoralisService) chainIDToMoralisChain(chainID int64) string {
	switch chainID {
	case 1:
		return "eth"
	case 11155111:
		return "sepolia"
	case 8453:
		return "base"
	case 84532:
		return "base-sepolia"
	default:
		return ""
	}
}

// getCachedPrice retrieves cached price if still valid
func (ms *MoralisService) getCachedPrice(cacheKey string) *CachedPrice {
	ms.cacheMux.RLock()
	defer ms.cacheMux.RUnlock()

	if cachedPrice, exists := ms.cache[cacheKey]; exists {
		if time.Since(cachedPrice.Timestamp) < ms.cacheTTL {
			return cachedPrice
		}
		// Cache expired, will be cleaned up later
	}
	return nil
}

// setCachedPrice stores price in cache
func (ms *MoralisService) setCachedPrice(cacheKey string, price *big.Float, symbol string) {
	ms.cacheMux.Lock()
	defer ms.cacheMux.Unlock()

	ms.cache[cacheKey] = &CachedPrice{
		Price:     price,
		Symbol:    symbol,
		Timestamp: time.Now(),
	}
}

// getETHPrice returns ETH price as fallback
func (ms *MoralisService) getETHPrice() (*big.Float, error) {
	return ms.GetNativeTokenPriceUSD(1) // Ethereum mainnet
}

// getFallbackPrice returns hardcoded fallback prices for supported tokens
// Only includes ETH since aggregator only supports Ethereum and Base chains
func (ms *MoralisService) getFallbackPrice(symbol string) *big.Float {
	// Only ETH is supported across all chains (Ethereum and Base)
	if symbol == "ETH" {
		return big.NewFloat(2500.0)
	}

	return big.NewFloat(2500.0) // Default to ETH price
}

// CleanupCache removes expired cache entries (called periodically)
func (ms *MoralisService) CleanupCache() {
	ms.cacheMux.Lock()
	defer ms.cacheMux.Unlock()

	now := time.Now()
	for key, cachedPrice := range ms.cache {
		if now.Sub(cachedPrice.Timestamp) > ms.cacheTTL {
			delete(ms.cache, key)
		}
	}
}

// GetSupportedChains returns list of supported chain IDs
func (ms *MoralisService) GetSupportedChains() []int64 {
	chains := make([]int64, 0, len(ms.chainTokens))
	for chainID := range ms.chainTokens {
		chains = append(chains, chainID)
	}
	return chains
}

// =============================================================================
// UTILITY AND HEALTH METHODS
// =============================================================================

// Health check method for monitoring
func (ms *MoralisService) HealthCheck(ctx context.Context) error {
	// Try to fetch ETH price as a health check
	_, err := ms.GetNativeTokenPriceUSD(1) // Ethereum mainnet
	if err != nil {
		return fmt.Errorf("moralis service health check failed: %w", err)
	}
	return nil
}

// =============================================================================
// FUTURE FEATURES (PLACEHOLDER)
// =============================================================================
// The following methods can be added as needed:
//
// NFT DATA METHODS:
// - GetNFTsByWallet(walletAddress, chainID) - Get NFTs owned by a wallet
// - GetNFTMetadata(contractAddr, tokenId, chainID) - Get NFT metadata
// - GetNFTTransfers(contractAddr, chainID) - Get NFT transfer history
//
// TOKEN DATA METHODS:
// - GetTokenBalances(walletAddress, chainID) - Get all token balances
// - GetTokenTransfers(walletAddress, chainID) - Get token transfer history
// - GetTokenMetadata(contractAddr, chainID) - Get token metadata
//
// DEFI DATA METHODS:
// - GetDeFiPositions(walletAddress, chainID) - Get DeFi positions
// - GetLiquidityPools(chainID) - Get liquidity pool data
//
// TRANSACTION DATA METHODS:
// - GetTransactionsByWallet(walletAddress, chainID) - Get transaction history
// - GetBlockData(blockNumber, chainID) - Get block information
