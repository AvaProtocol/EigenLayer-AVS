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
func (v *VM) runBalance(stepID string, taskNode *avsproto.TaskNode) (*avsproto.Execution_Step, error) {
	// Extract the balance node from taskNode
	node := taskNode.GetBalance()
	if node == nil {
		err := fmt.Errorf("taskNode does not contain a BalanceNode")
		executionLog := CreateNodeExecutionStep(stepID, taskNode, v)
		finalizeStep(executionLog, false, err, err.Error(), "")
		return executionLog, err
	}
	// Create execution step
	executionLogStep := CreateNodeExecutionStep(stepID, taskNode, v)

	var logBuilder strings.Builder
	logBuilder.WriteString(formatNodeExecutionLogHeader(executionLogStep))

	var err error
	defer func() {
		finalizeStep(executionLogStep, err == nil, err, "", logBuilder.String())
	}()

	config := node.Config
	if err = validateNodeConfig(config, "BalanceNode"); err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return executionLogStep, err
	}

	// Resolve template variables in config
	address := v.preprocessTextWithVariableMapping(config.Address)
	chain := v.preprocessTextWithVariableMapping(config.Chain)

	// Resolve template variables in tokenAddresses array
	resolvedTokenAddresses := make([]string, len(config.TokenAddresses))
	for i, tokenAddr := range config.TokenAddresses {
		resolved := v.preprocessTextWithVariableMapping(tokenAddr)

		// If the resolved value is a JSON object (e.g., {"id":"0x...","symbol":"WETH"}),
		// extract the address from the 'id' or 'address' field
		if strings.HasPrefix(resolved, "{") && strings.HasSuffix(resolved, "}") {
			var tokenObj map[string]interface{}
			if err := json.Unmarshal([]byte(resolved), &tokenObj); err == nil {
				// Try 'id' field first (common in subgraph responses)
				if idVal, ok := tokenObj["id"].(string); ok && idVal != "" {
					resolved = idVal
				} else if addrVal, ok := tokenObj["address"].(string); ok && addrVal != "" {
					// Try 'address' field as fallback
					resolved = addrVal
				}
				// If neither 'id' nor 'address' exists, keep the original JSON string
				// The validation below will catch it as an invalid address
			} else {
				// JSON unmarshaling failed - log warning and keep original value
				// The address validation below will catch invalid addresses
				if v.logger != nil {
					v.logger.Warn("Failed to parse token address as JSON object, treating as literal string",
						"tokenAddress", tokenAddr,
						"resolvedValue", resolved,
						"error", err)
				}
			}
		}

		resolvedTokenAddresses[i] = resolved
	}
	// Update config with resolved addresses for downstream use
	config.TokenAddresses = resolvedTokenAddresses

	if len(resolvedTokenAddresses) > 0 {
		logBuilder.WriteString(fmt.Sprintf("Fetching balances for address: %s on chain: %s (filtering %d tokens)\n", address, chain, len(resolvedTokenAddresses)))
	} else {
		logBuilder.WriteString(fmt.Sprintf("Fetching balances for address: %s on chain: %s\n", address, chain))
	}

	// Validate inputs
	if address == "" {
		err = fmt.Errorf("address is required")
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		return executionLogStep, err
	}
	if !common.IsHexAddress(address) {
		err = fmt.Errorf("invalid ethereum address format: %s", address)
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		return executionLogStep, err
	}
	// Validate minUsdValueCents: must be 0 (no filter) or >= 1 (1 cent minimum)
	if config.MinUsdValueCents < 0 {
		err = fmt.Errorf("minUsdValue must be non-negative, got %d cents", config.MinUsdValueCents)
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		return executionLogStep, err
	}
	if chain == "" {
		err = fmt.Errorf("chain is required")
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		return executionLogStep, err
	}

	// Normalize chain to Moralis format
	moralisChain, err := normalizeChainID(chain)
	if err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		return executionLogStep, err
	}

	// Get Moralis API key from macro secrets (global package variable set by SetMacroSecrets)
	moralisAPIKey := ""
	if macroSecrets != nil {
		moralisAPIKey = macroSecrets["moralis_api_key"]
	}
	if moralisAPIKey == "" {
		err = fmt.Errorf("moralis API key is not configured in macros.secrets")
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
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
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
		return executionLogStep, err
	}

	logBuilder.WriteString(fmt.Sprintf("Successfully fetched %d token balances\n", len(balances)))

	// Convert to protobuf Value
	balancesValue, err := structpb.NewValue(balances)
	if err != nil {
		err = fmt.Errorf("failed to convert balances to protobuf value: %w", err)
		logBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))
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

	// Finalize execution step (success)
	finalizeStep(executionLogStep, true, nil, "", logBuilder.String())

	return executionLogStep, nil
}

// testMoralisAPIBaseURL allows tests to override the Moralis API base URL
var testMoralisAPIBaseURL string

// fetchMoralisBalancesWithFiltering retrieves token balances with two-phase approach:
// Phase 1: Get all tokens (to include native token like ETH)
// Phase 2: If client specified tokenAddresses and some are missing, query them specifically
//
// SMART DEFAULT BEHAVIOR (see protobuf/avs.proto BalanceNode.Config.include_zero_balances):
// When tokenAddresses is provided, the system automatically enables zero balance inclusion
// to ensure all explicitly requested tokens are returned, even if the wallet has never held them.
//
// This function implements the smart default by creating an effectiveConfig with
// include_zero_balances=true when token_addresses is provided.
//
// PROTOBUF LIMITATION HANDLING:
// Since protobuf bool fields cannot distinguish between "user explicitly set false" and
// "default false", this function treats both cases the same when tokenAddresses is provided:
// - tokenAddresses provided + includeZeroBalances=false → Auto-enable (smart default)
// - tokenAddresses empty + includeZeroBalances=false → Respect false (standard behavior)
//
// For the rare case where users want to filter zero balances for specific tokens,
// set includeZeroBalances=true explicitly and filter on the client side.
func (vm *VM) fetchMoralisBalancesWithFiltering(
	address, chain, apiKey string,
	config *avsproto.BalanceNode_Config,
) ([]interface{}, error) {
	// Apply smart default: auto-enable includeZeroBalances when tokenAddresses is provided
	// This ensures users always get the tokens they explicitly request, even with zero balance
	effectiveConfig := config
	if len(config.TokenAddresses) > 0 && !config.IncludeZeroBalances {
		// User specified token addresses but includeZeroBalances is false (default or explicit)
		// Apply smart default: create a modified config with includeZeroBalances = true
		effectiveConfig = &avsproto.BalanceNode_Config{
			Address:             config.Address,
			Chain:               config.Chain,
			TokenAddresses:      config.TokenAddresses,
			IncludeSpam:         config.IncludeSpam,
			IncludeZeroBalances: true, // Smart default: include zero balances for explicitly requested tokens
			MinUsdValueCents:    config.MinUsdValueCents,
		}

		if vm.logger != nil {
			vm.logger.Debug("BalanceNode: Smart default applied - auto-enabling includeZeroBalances",
				"reason", "tokenAddresses provided",
				"tokenCount", len(config.TokenAddresses))
		}
	}

	// Phase 1: Always fetch all tokens first (no token_addresses filter)
	// This ensures we get native tokens (ETH, BNB, etc.)
	allBalances, err := vm.fetchMoralisBalances(address, chain, apiKey, effectiveConfig, nil)
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
		missingBalances, err := vm.fetchMoralisBalances(address, chain, apiKey, effectiveConfig, missingAddrs)
		if err != nil {
			// Don't fail entirely - just log warning and continue with what we have
			fmt.Printf("WARNING: Failed to fetch missing token addresses: %v\n", err)
		} else {
			result = append(result, missingBalances...)

			// Update foundAddrs map with what we got from Phase 2
			for _, bal := range missingBalances {
				token, ok := bal.(map[string]interface{})
				if !ok {
					continue
				}
				if tokenAddr, hasAddr := token["tokenAddress"].(string); hasAddr {
					foundAddrs[strings.ToLower(tokenAddr)] = true
				}
			}
		}
	}

	// Phase 3: For any STILL missing requested addresses, synthesize zero balance entries
	// This handles the case where Moralis doesn't return tokens the wallet has never held
	// When user explicitly requests specific token addresses, we should return them even with zero balance
	for lowercaseAddr, checksummedAddr := range requestedAddrs {
		if !foundAddrs[lowercaseAddr] {
			// Token was requested but not found even after Phase 2 - synthesize it with zero balance
			synthesizedToken := map[string]interface{}{
				"symbol":           "UNKNOWN", // We don't have token metadata
				"name":             "Unknown Token",
				"balance":          "0",
				"balanceFormatted": "0",
				"decimals":         18, // Default to 18 decimals (standard ERC20)
				"tokenAddress":     checksummedAddr,
			}
			result = append(result, synthesizedToken)
		}
	}

	return result, nil
}

// fetchMoralisBalances calls the Moralis API to get wallet token balances
// If tokenAddresses is nil, fetches all tokens. If specified, fetches only those tokens.
// When tokenAddresses is specified (Phase 2), zero balances are always included
func (vm *VM) fetchMoralisBalances(
	address, chain, apiKey string,
	config *avsproto.BalanceNode_Config,
	tokenAddresses []string, // NEW: optional specific addresses to fetch
) ([]interface{}, error) {
	// Phase 2 check: When we're fetching specific missing tokens (Phase 2 of two-phase approach),
	// always include zero balances because user explicitly requested these specific tokens
	isPhase2Request := len(tokenAddresses) > 0
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

		// Zero balance filtering logic (3-tier priority):
		// 1. If this is Phase 2 (fetching specific missing tokens), ALWAYS include zero balances
		//    because user explicitly requested these specific token addresses
		// 2. Otherwise, respect user's includeZeroBalances setting
		// Note: The "smart default" logic (auto-enable for tokenAddresses) is handled at the
		// fetchMoralisBalancesWithFiltering level, not here
		shouldIncludeZeroBalance := isPhase2Request || config.IncludeZeroBalances
		if !shouldIncludeZeroBalance && token.Balance == "0" {
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
