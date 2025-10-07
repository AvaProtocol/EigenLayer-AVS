package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

// MoralisTokenBalance represents a single token balance in the Moralis API response
type MoralisTokenBalance struct {
	Balance                    string   `json:"balance"`
	BalanceFormatted           string   `json:"balance_formatted"`
	Decimals                   int      `json:"decimals"`
	Logo                       string   `json:"logo"`
	Name                       string   `json:"name"`
	NativeToken                bool     `json:"native_token"`
	PercentageRelativeToSupply *float64 `json:"percentage_relative_to_total_supply"`
	PortfolioPercentage        float64  `json:"portfolio_percentage"`
	PossibleSpam               bool     `json:"possible_spam"`
	SecurityScore              int      `json:"security_score"`
	Symbol                     string   `json:"symbol"`
	Thumbnail                  string   `json:"thumbnail"`
	TokenAddress               string   `json:"token_address"`
	TotalSupply                *string  `json:"total_supply"`
	TotalSupplyFormatted       *string  `json:"total_supply_formatted"`
	USDPrice                   float64  `json:"usd_price"`
	USDPrice24hrPercentChange  float64  `json:"usd_price_24hr_percent_change"`
	USDPrice24hrUSDChange      float64  `json:"usd_price_24hr_usd_change"`
	USDValue                   float64  `json:"usd_value"`
	USDValue24hrUSDChange      float64  `json:"usd_value_24hr_usd_change"`
	VerifiedContract           bool     `json:"verified_contract"`
}

// MoralisBalanceResponse represents the full Moralis API response
type MoralisBalanceResponse struct {
	BlockNumber int                   `json:"block_number"`
	Cursor      *string               `json:"cursor"`
	Page        int                   `json:"page"`
	PageSize    int                   `json:"page_size"`
	Result      []MoralisTokenBalance `json:"result"`
}

// SimplifiedTokenBalance represents the simplified token balance output
type SimplifiedTokenBalance struct {
	Symbol           string   `json:"symbol"`
	Name             string   `json:"name"`
	Balance          string   `json:"balance"`
	BalanceFormatted string   `json:"balanceFormatted"`
	Decimals         int      `json:"decimals"`
	TokenAddress     *string  `json:"tokenAddress,omitempty"` // Only for non-native tokens
	USDPrice         *float64 `json:"usdPrice,omitempty"`
	USDValue         *float64 `json:"usdValue,omitempty"`
}

// Chain name to Moralis chain identifier mapping
var chainIDMap = map[string]string{
	// Ethereum
	"ethereum": "eth",
	"eth":      "eth",
	"1":        "eth",
	"0x1":      "eth",

	// Sepolia (Ethereum testnet)
	"sepolia":  "sepolia",
	"11155111": "sepolia",
	"0xaa36a7": "sepolia",

	// Base
	"base":   "base",
	"8453":   "base",
	"0x2105": "base",

	// Base Sepolia (Base testnet)
	"base-sepolia": "base-sepolia",
	"base sepolia": "base-sepolia",
	"basesepolia":  "base-sepolia",
	"84532":        "base-sepolia",
	"0x14a34":      "base-sepolia",

	// BSC
	"bsc":     "bsc",
	"binance": "bsc",
	"56":      "bsc",
	"0x38":    "bsc",
}

// normalizeChainID converts various chain identifier formats to Moralis chain name
func normalizeChainID(chain string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(chain))
	if moralisChain, ok := chainIDMap[normalized]; ok {
		return moralisChain, nil
	}
	return "", fmt.Errorf("unsupported chain: %s", chain)
}

// formatBalance converts raw balance to formatted balance using decimals
func formatBalance(rawBalance string, decimals int) string {
	// Parse the raw balance as a big integer
	balance := new(big.Int)
	balance, ok := balance.SetString(rawBalance, 10)
	if !ok {
		return ""
	}

	// Calculate divisor (10^decimals)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)

	// Calculate quotient and remainder
	quotient := new(big.Int).Div(balance, divisor)
	remainder := new(big.Int).Mod(balance, divisor)

	// Format remainder with leading zeros if needed
	remainderStr := remainder.String()
	if len(remainderStr) < decimals {
		remainderStr = strings.Repeat("0", decimals-len(remainderStr)) + remainderStr
	}

	// Remove trailing zeros from remainder
	remainderStr = strings.TrimRight(remainderStr, "0")

	// Format result
	if remainderStr == "" {
		return quotient.String()
	}
	return quotient.String() + "." + remainderStr
}

// runBalance is the VM method wrapper for balance node execution
func (v *VM) runBalance(stepID string, nodeValue *avsproto.BalanceNode) (*avsproto.Execution_Step, error) {
	// Create execution step
	executionLogStep := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_BALANCE, v)

	var logBuilder strings.Builder
	logBuilder.WriteString(fmt.Sprintf("Executing Balance Node ID: %s at %s\n", stepID, time.Now()))

	config := nodeValue.Config
	if config == nil {
		err := fmt.Errorf("balance node config is nil")
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		return executionLogStep, err
	}

	// Resolve template variables in config
	address := v.preprocessTextWithVariableMapping(config.Address)
	chain := v.preprocessTextWithVariableMapping(config.Chain)

	logBuilder.WriteString(fmt.Sprintf("Fetching balances for address: %s on chain: %s\n", address, chain))

	// Validate inputs
	if address == "" {
		err := fmt.Errorf("address is required")
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}
	if !common.IsHexAddress(address) {
		err := fmt.Errorf("invalid ethereum address format: %s", address)
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}
	// Validate minUsdValueCents: must be 0 (no filter) or >= 1 (1 cent minimum)
	if config.MinUsdValueCents < 0 {
		err := fmt.Errorf("minUsdValue must be non-negative, got %d cents", config.MinUsdValueCents)
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}
	if chain == "" {
		err := fmt.Errorf("chain is required")
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	// Normalize chain to Moralis format
	moralisChain, err := normalizeChainID(chain)
	if err != nil {
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	// Get Moralis API key from macro secrets (global package variable set by SetMacroSecrets)
	moralisAPIKey := ""
	if macroSecrets != nil {
		moralisAPIKey = macroSecrets["moralis_api_key"]
	}
	if moralisAPIKey == "" {
		err := fmt.Errorf("moralis API key is not configured in macros.secrets")
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	// Debug logging for CI/testing (show first 20 chars of API key to verify it's loaded)
	if len(moralisAPIKey) > 20 {
		fmt.Printf("DEBUG: Moralis API key loaded: %s... (length: %d)\n", moralisAPIKey[:20], len(moralisAPIKey))
	} else {
		fmt.Printf("DEBUG: Moralis API key loaded (length: %d)\n", len(moralisAPIKey))
	}

	// Additional debug: check if it looks like a JWT
	if strings.HasPrefix(moralisAPIKey, "eyJ") {
		fmt.Printf("DEBUG: Moralis API key format: JWT (starts with eyJ)\n")
	} else if moralisAPIKey == "test-api-key" {
		fmt.Printf("DEBUG: Moralis API key format: test/mock key\n")
	} else {
		fmt.Printf("DEBUG: Moralis API key format: unknown/other\n")
	}

	// Fetch balances from Moralis
	// Note: If client specifies tokenAddresses, we need a two-phase approach:
	// 1. Get all tokens first (to include native token)
	// 2. If any requested addresses are missing, query them specifically
	balances, err := v.fetchMoralisBalancesWithFiltering(address, moralisChain, moralisAPIKey, config)
	if err != nil {
		err = fmt.Errorf("failed to fetch balances: %w", err)
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	logBuilder.WriteString(fmt.Sprintf("✅ Successfully fetched %d token balances\n", len(balances)))

	// Convert to protobuf Value
	balancesValue, err := structpb.NewValue(balances)
	if err != nil {
		err = fmt.Errorf("failed to convert balances to protobuf value: %w", err)
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	// Create protobuf output
	outputData := &avsproto.BalanceNode_Output{
		Data: balancesValue,
	}

	executionLogStep.OutputData = &avsproto.Execution_Step_Balance{
		Balance: outputData,
	}

	// Set output variable for following nodes
	processor := &CommonProcessor{vm: v}
	setNodeOutputData(processor, stepID, balances)

	// Finalize execution step
	finalizeExecutionStep(executionLogStep, true, "", logBuilder.String())

	return executionLogStep, nil
}

// testMoralisAPIBaseURL allows tests to override the Moralis API base URL
var testMoralisAPIBaseURL string

// fetchMoralisBalancesWithFiltering retrieves token balances with two-phase approach:
// Phase 1: Get all tokens (to include native token like ETH)
// Phase 2: If client specified tokenAddresses and some are missing, query them specifically
func (vm *VM) fetchMoralisBalancesWithFiltering(
	address, chain, apiKey string,
	config *avsproto.BalanceNode_Config,
) ([]interface{}, error) {
	// Phase 1: Always fetch all tokens first (no token_addresses filter)
	// This ensures we get native tokens (ETH, BNB, etc.)
	allBalances, err := vm.fetchMoralisBalances(address, chain, apiKey, config, nil)
	if err != nil {
		return nil, err
	}
	// Phase 1 complete

	// If no tokenAddresses filter specified, return all balances
	if len(config.TokenAddresses) == 0 {
		return allBalances, nil
	}

	// Client specified tokenAddresses - need to filter and potentially fetch missing ones
	// Normalize client's requested addresses to lowercase for comparison
	requestedAddrs := make(map[string]string) // lowercase -> checksummed
	for _, addr := range config.TokenAddresses {
		if addr != "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" && common.IsHexAddress(addr) {
			checksummed := common.HexToAddress(addr).Hex()
			requestedAddrs[strings.ToLower(checksummed)] = checksummed
		}
	}

	// Build result set: tokens that match client's filter
	result := make([]interface{}, 0)
	foundAddrs := make(map[string]bool) // Track which addresses we found

	// Scan all balances and include matches (including native token)
	for _, bal := range allBalances {
		token, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}

		// Check if this is a native token (native tokens don't have the tokenAddress field)
		// The fetchMoralisBalances function only adds tokenAddress for non-native tokens
		tokenAddr, hasTokenAddr := token["tokenAddress"].(string)

		if !hasTokenAddr {
			// Native token (no tokenAddress field) - always include it
			result = append(result, bal)
			foundAddrs["native"] = true
			continue
		}

		// Check if token address matches client's filter
		if _, requested := requestedAddrs[strings.ToLower(tokenAddr)]; requested {
			result = append(result, bal)
			foundAddrs[strings.ToLower(tokenAddr)] = true
		}
	}

	// Phase 2: Check if any requested addresses are missing
	missingAddrs := make([]string, 0)
	for lowercaseAddr, checksummedAddr := range requestedAddrs {
		if !foundAddrs[lowercaseAddr] {
			missingAddrs = append(missingAddrs, checksummedAddr)
		}
	}

	// If we have missing addresses, fetch them specifically
	if len(missingAddrs) > 0 {
		missingBalances, err := vm.fetchMoralisBalances(address, chain, apiKey, config, missingAddrs)
		if err != nil {
			// Don't fail entirely - just log warning and continue with what we have
			fmt.Printf("WARNING: Failed to fetch missing token addresses: %v\n", err)
		} else {
			result = append(result, missingBalances...)
		}
	}

	return result, nil
}

// fetchMoralisBalances calls the Moralis API to get wallet token balances
// If tokenAddresses is nil, fetches all tokens. If specified, fetches only those tokens.
func (vm *VM) fetchMoralisBalances(
	address, chain, apiKey string,
	config *avsproto.BalanceNode_Config,
	tokenAddresses []string, // NEW: optional specific addresses to fetch
) ([]interface{}, error) {
	// Build Moralis API URL
	// NOTE: Use /tokens endpoint (not /erc20) to support exclude_native parameter
	// The /erc20 endpoint doesn't support native tokens even with exclude_native=false
	baseURL := "https://deep-index.moralis.io/api/v2.2"
	if testMoralisAPIBaseURL != "" {
		baseURL = testMoralisAPIBaseURL
	}
	url := fmt.Sprintf("%s/wallets/%s/tokens", baseURL, address)

	// Create HTTP client with timeout to prevent indefinite blocking
	client := resty.New().SetTimeout(30 * time.Second)
	request := client.R().
		SetHeader("X-API-Key", apiKey).
		SetQueryParam("chain", chain)

	// Add exclude_spam parameter (exclude by default unless includeSpam is true)
	if !config.IncludeSpam {
		request.SetQueryParam("exclude_spam", "true")
	}

	// CRITICAL: Always include native tokens (ETH, BNB, etc.)
	// Moralis excludes native tokens by default, so we must explicitly request them
	request.SetQueryParam("exclude_native", "false")

	// Add token_addresses parameter if specified (used for Phase 2 missing token fetching)
	if len(tokenAddresses) > 0 {
		tokenAddressesStr := strings.Join(tokenAddresses, ",")
		request.SetQueryParam("token_addresses", tokenAddressesStr)
	}

	// Execute request
	resp, err := request.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to call Moralis API: %w", err)
	}

	// Check response status
	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("moralis API returned status %d: %s", resp.StatusCode(), resp.String())
	}

	// Parse Moralis response - try direct array first (v2.2 API returns array directly)
	var tokens []MoralisTokenBalance
	if err := json.Unmarshal(resp.Body(), &tokens); err != nil {
		// If direct array parsing fails, try wrapper object format (older API version)
		var moralisResp MoralisBalanceResponse
		if err2 := json.Unmarshal(resp.Body(), &moralisResp); err2 != nil {
			return nil, fmt.Errorf("failed to parse Moralis response as array or wrapper: %w", err)
		}
		tokens = moralisResp.Result
	}

	// Convert to simplified format
	simplified := make([]interface{}, 0, len(tokens))
	for _, token := range tokens {
		// Skip spam tokens if not included
		if token.PossibleSpam && !config.IncludeSpam {
			continue
		}

		// Skip zero balances if not included
		if !config.IncludeZeroBalances && token.Balance == "0" {
			continue
		}

		// Skip tokens below minimum USD value (convert cents to dollars)
		if config.MinUsdValueCents > 0 {
			minUsdValueDollars := float64(config.MinUsdValueCents) / 100.0
			if token.USDValue < minUsdValueDollars {
				continue
			}
		}

		// Format balance if Moralis didn't provide it
		balanceFormatted := token.BalanceFormatted
		if balanceFormatted == "" && token.Balance != "" {
			balanceFormatted = formatBalance(token.Balance, token.Decimals)
		}

		// Create simplified token balance
		simpleToken := SimplifiedTokenBalance{
			Symbol:           token.Symbol,
			Name:             token.Name,
			Balance:          token.Balance,
			BalanceFormatted: balanceFormatted,
			Decimals:         token.Decimals,
		}

		// Only include tokenAddress for non-native tokens
		if !token.NativeToken {
			simpleToken.TokenAddress = &token.TokenAddress
		}

		// Include USD price if available
		if token.USDPrice > 0 {
			simpleToken.USDPrice = &token.USDPrice
		}

		// Include USD value if available
		if token.USDValue > 0 {
			simpleToken.USDValue = &token.USDValue
		}

		// Convert to map for JSON compatibility
		tokenMap := map[string]interface{}{
			"symbol":           simpleToken.Symbol,
			"name":             simpleToken.Name,
			"balance":          simpleToken.Balance,
			"balanceFormatted": simpleToken.BalanceFormatted,
			"decimals":         simpleToken.Decimals,
		}

		if simpleToken.TokenAddress != nil {
			tokenMap["tokenAddress"] = *simpleToken.TokenAddress
		}

		if simpleToken.USDPrice != nil {
			tokenMap["usdPrice"] = *simpleToken.USDPrice
		}

		if simpleToken.USDValue != nil {
			tokenMap["usdValue"] = *simpleToken.USDValue
		}

		simplified = append(simplified, tokenMap)
	}

	return simplified, nil
}
