package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// TokenMetadata represents ERC20 token information
type TokenMetadata struct {
	Address  string `json:"address"`
	Name     string `json:"name"`
	Symbol   string `json:"symbol"`
	Decimals uint32 `json:"decimals"`
	Source   string `json:"source"` // "whitelist" or "rpc"
}

// TokenEnrichmentService handles token metadata lookup and enrichment
type TokenEnrichmentService struct {
	cache     map[string]*TokenMetadata // In-memory cache: address -> metadata
	cacheMux  sync.RWMutex              // Protect cache access
	rpcClient *ethclient.Client
	chainID   uint64
	logger    sdklogging.Logger

	// ERC20 ABI for contract calls
	erc20ABI abi.ABI
}

// Standard ERC20 function signatures
const (
	erc20ABI_JSON = `[
		{
			"constant": true,
			"inputs": [],
			"name": "name",
			"outputs": [{"name": "", "type": "string"}],
			"type": "function"
		},
		{
			"constant": true,
			"inputs": [],
			"name": "symbol", 
			"outputs": [{"name": "", "type": "string"}],
			"type": "function"
		},
		{
			"constant": true,
			"inputs": [],
			"name": "decimals",
			"outputs": [{"name": "", "type": "uint8"}],
			"type": "function"
		}
	]`
)

// Chain ID constants
const (
	ChainIDEthereum uint64 = 1
	ChainIDSepolia  uint64 = 11155111
)

// NewTokenEnrichmentService creates a new token enrichment service
func NewTokenEnrichmentService(rpcClient *ethclient.Client, logger sdklogging.Logger) (*TokenEnrichmentService, error) {
	// Parse ERC20 ABI
	parsedABI, err := abi.JSON(strings.NewReader(erc20ABI_JSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ERC20 ABI: %w", err)
	}

	service := &TokenEnrichmentService{
		cache:     make(map[string]*TokenMetadata),
		rpcClient: rpcClient,
		logger:    logger,
		erc20ABI:  parsedABI,
	}

	// Get chain ID
	if rpcClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		chainID, err := rpcClient.ChainID(ctx)
		if err != nil {
			if logger != nil {
				logger.Warn("Failed to get chain ID from RPC", "error", err)
			}
			// Default to Ethereum mainnet if we can't detect
			service.chainID = ChainIDEthereum
		} else {
			service.chainID = chainID.Uint64()
		}
	} else {
		// Default to Ethereum mainnet if no RPC client
		service.chainID = ChainIDEthereum
	}

	// Load token whitelist for the detected chain
	err = service.LoadWhitelist()
	if err != nil {
		if logger != nil {
			logger.Warn("Failed to load token whitelist", "error", err, "chainID", service.chainID)
		}
		// Don't fail initialization - we can still work with RPC calls
	}

	if logger != nil {
		logger.Info("TokenEnrichmentService initialized",
			"chainID", service.chainID,
			"whitelistTokens", len(service.cache))
	}

	return service, nil
}

// LoadWhitelist loads token metadata from whitelist files
func (t *TokenEnrichmentService) LoadWhitelist() error {
	var filename string
	switch t.chainID {
	case ChainIDEthereum:
		filename = "ethereum.json"
	case ChainIDSepolia:
		filename = "sepolia.json"
	default:
		// For unknown chains, try ethereum.json as fallback
		filename = "ethereum.json"
	}

	// Look for whitelist file in token_whitelist directory
	whitelistPath := filepath.Join("token_whitelist", filename)

	// Check if file exists
	if _, err := os.Stat(whitelistPath); os.IsNotExist(err) {
		return fmt.Errorf("whitelist file not found: %s", whitelistPath)
	}

	// Read and parse the file
	data, err := os.ReadFile(whitelistPath)
	if err != nil {
		return fmt.Errorf("failed to read whitelist file %s: %w", whitelistPath, err)
	}

	var tokens []TokenMetadata
	err = json.Unmarshal(data, &tokens)
	if err != nil {
		return fmt.Errorf("failed to parse whitelist file %s: %w", whitelistPath, err)
	}

	// Load tokens into cache (normalize addresses to lowercase)
	t.cacheMux.Lock()
	for _, token := range tokens {
		normalizedAddr := strings.ToLower(token.Address)
		t.cache[normalizedAddr] = &TokenMetadata{
			Address:  normalizedAddr,
			Name:     token.Name,
			Symbol:   token.Symbol,
			Decimals: token.Decimals,
			Source:   "whitelist",
		}
	}
	t.cacheMux.Unlock()

	if t.logger != nil {
		t.logger.Info("Loaded token whitelist",
			"file", whitelistPath,
			"tokenCount", len(tokens),
			"chainID", t.chainID)
	}

	return nil
}

// GetTokenMetadata retrieves token metadata, checking cache first, then RPC
func (t *TokenEnrichmentService) GetTokenMetadata(contractAddress string) (*TokenMetadata, error) {
	normalizedAddr := strings.ToLower(contractAddress)

	// Check cache first (this includes whitelist data)
	t.cacheMux.RLock()
	if cached, exists := t.cache[normalizedAddr]; exists {
		t.cacheMux.RUnlock()
		return cached, nil
	}
	t.cacheMux.RUnlock()

	// Not in cache/whitelist, try RPC calls if available
	if t.rpcClient == nil {
		// No RPC client available and token not in whitelist
		// Return nil to indicate not found in whitelist-only mode
		return nil, nil
	}

	metadata, err := t.fetchTokenMetadataFromRPC(contractAddress)
	if err != nil {
		return nil, err
	}

	// Cache the result for future use
	t.cacheMux.Lock()
	t.cache[normalizedAddr] = metadata
	t.cacheMux.Unlock()

	if t.logger != nil {
		t.logger.Info("Fetched token metadata from RPC and cached",
			"address", contractAddress,
			"name", metadata.Name,
			"symbol", metadata.Symbol,
			"decimals", metadata.Decimals)
	}

	return metadata, nil
}

// fetchTokenMetadataFromRPC fetches token metadata directly from the blockchain
func (t *TokenEnrichmentService) fetchTokenMetadataFromRPC(contractAddress string) (*TokenMetadata, error) {
	addr := common.HexToAddress(contractAddress)

	// Create a bound contract instance
	contract := bind.NewBoundContract(addr, t.erc20ABI, t.rpcClient, t.rpcClient, t.rpcClient)

	metadata := &TokenMetadata{
		Address: strings.ToLower(contractAddress),
		Source:  "rpc",
	}

	// Call name() method
	var nameResult []interface{}
	err := contract.Call(&bind.CallOpts{}, &nameResult, "name")
	if err != nil {
		if t.logger != nil {
			t.logger.Warn("Failed to get token name", "address", contractAddress, "error", err)
		}
		metadata.Name = "Unknown Token"
	} else if len(nameResult) > 0 {
		if name, ok := nameResult[0].(string); ok {
			metadata.Name = name
		}
	}

	// Call symbol() method
	var symbolResult []interface{}
	err = contract.Call(&bind.CallOpts{}, &symbolResult, "symbol")
	if err != nil {
		if t.logger != nil {
			t.logger.Warn("Failed to get token symbol", "address", contractAddress, "error", err)
		}
		metadata.Symbol = "UNKNOWN"
	} else if len(symbolResult) > 0 {
		if symbol, ok := symbolResult[0].(string); ok {
			metadata.Symbol = symbol
		}
	}

	// Call decimals() method
	var decimalsResult []interface{}
	err = contract.Call(&bind.CallOpts{}, &decimalsResult, "decimals")
	if err != nil {
		if t.logger != nil {
			t.logger.Warn("Failed to get token decimals", "address", contractAddress, "error", err)
		}
		metadata.Decimals = 18 // Default to 18 decimals
	} else if len(decimalsResult) > 0 {
		if decimals, ok := decimalsResult[0].(uint8); ok {
			metadata.Decimals = uint32(decimals)
		}
	}

	return metadata, nil
}

// EnrichTransferLog enriches a transfer log with token metadata and formatted value
func (t *TokenEnrichmentService) EnrichTransferLog(evmLog *avsproto.Evm_Log, transferLog *avsproto.EventTrigger_TransferLogOutput) error {
	if evmLog == nil || transferLog == nil {
		return fmt.Errorf("evmLog and transferLog cannot be nil")
	}

	// Get token metadata
	metadata, err := t.GetTokenMetadata(evmLog.Address)
	if err != nil {
		if t.logger != nil {
			t.logger.Warn("Failed to get token metadata for enrichment",
				"address", evmLog.Address,
				"error", err)
		}
		// Continue with partial enrichment - don't fail the entire operation
		return nil
	}

	// Enrich the transfer log with token metadata
	transferLog.TokenName = metadata.Name
	transferLog.TokenSymbol = metadata.Symbol
	transferLog.TokenDecimals = metadata.Decimals

	// Format the value using token decimals
	if transferLog.Value != "" {
		formattedValue := t.FormatTokenValue(transferLog.Value, metadata.Decimals)
		transferLog.ValueFormatted = formattedValue
	}

	return nil
}

// FormatTokenValue formats a hex value string using the specified decimal places
func (t *TokenEnrichmentService) FormatTokenValue(rawValue string, decimals uint32) string {
	// Remove 0x prefix if present
	if strings.HasPrefix(rawValue, "0x") {
		rawValue = rawValue[2:]
	}

	// Convert hex string to big.Int
	value := new(big.Int)
	if _, ok := value.SetString(rawValue, 16); !ok {
		if t.logger != nil {
			t.logger.Warn("Failed to parse token value", "rawValue", rawValue)
		}
		return "0"
	}

	// If decimals is 0, return as-is
	if decimals == 0 {
		return value.String()
	}

	// Calculate the divisor (10^decimals)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)

	// Divide value by divisor to get the main part
	quotient := new(big.Int).Div(value, divisor)
	remainder := new(big.Int).Mod(value, divisor)

	// If no fractional part, return just the quotient
	if remainder.Cmp(big.NewInt(0)) == 0 {
		return quotient.String()
	}

	// Format with decimal places
	// Pad remainder with leading zeros if necessary
	remainderStr := remainder.String()
	for len(remainderStr) < int(decimals) {
		remainderStr = "0" + remainderStr
	}

	// Remove trailing zeros from the fractional part
	remainderStr = strings.TrimRight(remainderStr, "0")

	// If no fractional part remains after trimming, return just the quotient
	if remainderStr == "" {
		return quotient.String()
	}

	return fmt.Sprintf("%s.%s", quotient.String(), remainderStr)
}

// IsERC20Contract performs a basic check to see if the contract implements ERC20
func (t *TokenEnrichmentService) IsERC20Contract(contractAddress string) bool {
	// Check cache first
	normalizedAddr := strings.ToLower(contractAddress)
	t.cacheMux.RLock()
	_, exists := t.cache[normalizedAddr]
	t.cacheMux.RUnlock()

	if exists {
		return true // If it's in our cache, we know it's ERC20
	}

	// If not in cache and no RPC client, assume it might be ERC20
	if t.rpcClient == nil {
		return true
	}

	// Try a simple symbol() call to check if it responds like ERC20
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr := common.HexToAddress(contractAddress)

	// Use low-level call to avoid errors stopping execution
	symbolData, err := t.erc20ABI.Pack("symbol")
	if err != nil {
		return false
	}

	msg := ethereum.CallMsg{
		To:   &addr,
		Data: symbolData,
	}

	_, err = t.rpcClient.CallContract(ctx, msg, nil)
	return err == nil // If the call succeeds, likely ERC20
}

// GetChainID returns the current chain ID
func (t *TokenEnrichmentService) GetChainID() uint64 {
	return t.chainID
}

// GetCacheSize returns the number of tokens in cache
func (t *TokenEnrichmentService) GetCacheSize() int {
	t.cacheMux.RLock()
	defer t.cacheMux.RUnlock()
	return len(t.cache)
}
