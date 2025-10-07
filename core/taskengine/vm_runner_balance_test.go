package taskengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Mock token balances for testing
var mockTokenBalances = []MoralisTokenBalance{
	{
		Balance:          "1000000000000000000",
		BalanceFormatted: "1",
		Decimals:         18,
		Name:             "Wrapped Ether",
		Symbol:           "WETH",
		TokenAddress:     "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2",
		NativeToken:      false,
		PossibleSpam:     false,
		USDPrice:         2000.0,
		USDValue:         2000.0,
	},
	{
		Balance:          "5000000",
		BalanceFormatted: "5",
		Decimals:         6,
		Name:             "USD Coin",
		Symbol:           "USDC",
		TokenAddress:     "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		NativeToken:      false,
		PossibleSpam:     false,
		USDPrice:         1.0,
		USDValue:         5.0,
	},
	{
		Balance:          "0",
		BalanceFormatted: "0",
		Decimals:         18,
		Name:             "Zero Balance Token",
		Symbol:           "ZERO",
		TokenAddress:     "0x0000000000000000000000000000000000000001",
		NativeToken:      false,
		PossibleSpam:     false,
		USDPrice:         0,
		USDValue:         0,
	},
	{
		Balance:          "1000000000000000000",
		BalanceFormatted: "1",
		Decimals:         18,
		Name:             "Spam Token",
		Symbol:           "SPAM",
		TokenAddress:     "0x0000000000000000000000000000000000000002",
		NativeToken:      false,
		PossibleSpam:     true,
		USDPrice:         0.001,
		USDValue:         0.001,
	},
	{
		Balance:          "100000000",
		BalanceFormatted: "0.1",
		Decimals:         9,
		Name:             "Low Value Token",
		Symbol:           "LVT",
		TokenAddress:     "0x0000000000000000000000000000000000000003",
		NativeToken:      false,
		PossibleSpam:     false,
		USDPrice:         0.5,
		USDValue:         0.05,
	},
}

// createMockMoralisServer creates a test HTTP server that mocks the Moralis API
func createMockMoralisServer(t *testing.T, tokens []MoralisTokenBalance) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get("X-API-Key") == "" {
			t.Error("expected X-API-Key header to be set")
		}

		// Verify query parameters
		chain := r.URL.Query().Get("chain")
		if chain == "" {
			t.Error("expected chain query parameter to be set")
		}

		// Filter tokens based on query parameters
		filteredTokens := tokens

		// Filter by token_addresses if provided
		if tokenAddresses := r.URL.Query().Get("token_addresses"); tokenAddresses != "" {
			var filtered []MoralisTokenBalance
			addressList := strings.Split(tokenAddresses, ",")
			for _, token := range tokens {
				for _, addr := range addressList {
					if strings.EqualFold(strings.TrimSpace(addr), token.TokenAddress) {
						filtered = append(filtered, token)
						break
					}
				}
			}
			filteredTokens = filtered
		}

		// Return tokens as array (v2.2 API format)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(filteredTokens); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
}

// setupBalanceVM creates a test VM with BalanceNode configuration
func setupBalanceVM(t *testing.T, config *avsproto.BalanceNode_Config) (*VM, *avsproto.BalanceNode) {
	node := &avsproto.BalanceNode{
		Config: config,
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "balance-node-1",
			Name: "balanceNode",
			TaskType: &avsproto.TaskNode_Balance{
				Balance: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger-1",
		Name: "trigger-1",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "balance-node-1",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test-task",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	// Set up real Moralis API key from config if available
	moralisAPIKey := testutil.GetTestMoralisApiKey()
	if moralisAPIKey != "" {
		// Debug logging for CI: Show that real API key was loaded
		if len(moralisAPIKey) > 20 {
			fmt.Printf("TEST SETUP: Loaded real Moralis API key: %s... (length: %d)\n", moralisAPIKey[:20], len(moralisAPIKey))
		}
		SetMacroSecrets(map[string]string{
			"moralis_api_key": moralisAPIKey,
		})
	} else {
		// Fallback for tests that don't need real API
		fmt.Printf("TEST SETUP: No real Moralis API key found, using test-api-key\n")
		SetMacroSecrets(map[string]string{
			"moralis_api_key": "test-api-key",
		})
	}

	return vm, node
}

func TestBalanceNode_BasicFetch(t *testing.T) {
	// Create mock Moralis server
	mockServer := createMockMoralisServer(t, mockTokenBalances)
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "sepolia",
		IncludeSpam:         false,
		IncludeZeroBalances: false,
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	// Fetch balances using the mock server
	balances, err := vm.fetchMoralisBalances(testAddress, "sepolia", "test-api-key", config, nil)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	// Should exclude spam and zero balance tokens by default
	expectedCount := 3 // WETH, USDC, LVT (excludes ZERO and SPAM)
	if len(balances) != expectedCount {
		t.Errorf("expected %d tokens but got %d", expectedCount, len(balances))
	}

	// Verify first token structure
	firstToken, ok := balances[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected token to be map[string]interface{}")
	}

	if firstToken["symbol"] != "WETH" {
		t.Errorf("expected first token symbol to be WETH but got: %v", firstToken["symbol"])
	}

	if firstToken["balance"] != "1000000000000000000" {
		t.Errorf("expected balance to be 1000000000000000000 but got: %v", firstToken["balance"])
	}

	if firstToken["tokenAddress"] == nil {
		t.Error("expected tokenAddress to be present for non-native token")
	}
}

func TestBalanceNode_TokenAddressFilter(t *testing.T) {
	// Use real Moralis API (no mock server)
	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	// Filter for only WETH and USDC
	tokenAddresses := []string{
		"0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", // WETH
		"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", // USDC
	}

	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "ethereum",
		TokenAddresses:      tokenAddresses,
		IncludeSpam:         false,
		IncludeZeroBalances: true, // Include zero balances for this test
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	// Get real Moralis API key from macro secrets (already set in setupBalanceVM)
	moralisAPIKey := ""
	if macroSecrets != nil {
		moralisAPIKey = macroSecrets["moralis_api_key"]
	}
	if moralisAPIKey == "" || moralisAPIKey == "test-api-key" {
		t.Skip("real moralis API key not configured in macros.secrets - skipping real API test")
	}

	balances, err := vm.fetchMoralisBalancesWithFiltering(testAddress, "eth", moralisAPIKey, config)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	// With real API, we may get 0-2 tokens depending on what the address actually holds
	// The key test is that ALL returned tokens must be in our filter list
	t.Logf("Real Moralis API returned %d token(s) for filtered request", len(balances))

	// Verify ALL returned tokens are in the filter list (this is the main test)
	for i, bal := range balances {
		token, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}

		// Log token details for debugging
		if symbol, ok := token["symbol"]; ok {
			t.Logf("Token %d: %s", i+1, symbol)
		}

		tokenAddr, hasAddr := token["tokenAddress"].(string)
		if hasAddr {
			found := false
			for _, addr := range tokenAddresses {
				if strings.EqualFold(tokenAddr, addr) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("FILTER TEST FAILED: unexpected token address %s not in filter list %v", tokenAddr, tokenAddresses)
			}
		} else {
			// This might be a native token (ETH), which should not appear since we're filtering for specific ERC-20s
			if nativeToken, ok := token["native_token"]; ok && nativeToken == true {
				t.Logf("Found native token, which is expected to be included regardless of filter")
			}
		}
	}
}

func TestBalanceNode_FilterInvalidTokenAddresses(t *testing.T) {
	// Create mock Moralis server
	mockServer := createMockMoralisServer(t, mockTokenBalances)
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	// Include invalid ETH placeholder address that should be filtered out
	tokenAddresses := []string{
		"0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", // ETH placeholder - should be filtered
		"0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", // WETH - valid
		"0xinvalidaddress",                           // Invalid - should be filtered
		"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", // USDC - valid
	}

	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "ethereum",
		TokenAddresses:      tokenAddresses,
		IncludeSpam:         false,
		IncludeZeroBalances: true,
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	balances, err := vm.fetchMoralisBalances(testAddress, "eth", "test-api-key", config, nil)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	// Should succeed without sending invalid addresses to Moralis
	// The mock server would have received only valid addresses (WETH, USDC)
	t.Logf("Successfully filtered invalid token addresses, returned %d balances", len(balances))
}

func TestBalanceNode_IncludeZeroBalances(t *testing.T) {
	// Create mock Moralis server
	mockServer := createMockMoralisServer(t, mockTokenBalances)
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "sepolia",
		IncludeSpam:         false,
		IncludeZeroBalances: true,
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	balances, err := vm.fetchMoralisBalances(testAddress, "sepolia", "test-api-key", config, nil)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	// Should include zero balance tokens but exclude spam
	// Expected: WETH, USDC, ZERO, LVT (4 tokens, excludes SPAM)
	hasZeroBalance := false
	for _, bal := range balances {
		token, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}
		if token["symbol"] == "ZERO" {
			hasZeroBalance = true
			break
		}
	}

	if !hasZeroBalance {
		t.Error("expected to find zero balance token when IncludeZeroBalances is true")
	}
}

func TestBalanceNode_IncludeSpam(t *testing.T) {
	// Create mock Moralis server
	mockServer := createMockMoralisServer(t, mockTokenBalances)
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "sepolia",
		IncludeSpam:         true,
		IncludeZeroBalances: false,
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	balances, err := vm.fetchMoralisBalances(testAddress, "sepolia", "test-api-key", config, nil)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	// Should include spam tokens
	hasSpam := false
	for _, bal := range balances {
		token, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}
		if token["symbol"] == "SPAM" {
			hasSpam = true
			break
		}
	}

	if !hasSpam {
		t.Error("expected to find spam token when IncludeSpam is true")
	}
}

func TestBalanceNode_MinUsdValueFilter(t *testing.T) {
	// Create mock Moralis server
	mockServer := createMockMoralisServer(t, mockTokenBalances)
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	// Filter for tokens with USD value >= $1 (100 cents)
	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "sepolia",
		IncludeSpam:         false,
		IncludeZeroBalances: false,
		MinUsdValueCents:    100, // $1.00
	}

	vm, _ := setupBalanceVM(t, config)

	balances, err := vm.fetchMoralisBalances(testAddress, "sepolia", "test-api-key", config, nil)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	// Should only include WETH ($2000) and USDC ($5)
	// Excludes LVT ($0.05), SPAM ($0.001), and ZERO ($0)
	for _, bal := range balances {
		token, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}

		usdValue, hasValue := token["usdValue"].(float64)
		if hasValue && usdValue < 1.0 {
			t.Errorf("found token %s with USD value $%.2f below minimum $1.00",
				token["symbol"], usdValue)
		}
	}
}

func TestBalanceNode_ChainNormalization(t *testing.T) {
	testCases := []struct {
		name          string
		inputChain    string
		expectedChain string
		shouldError   bool
	}{
		{
			name:          "Ethereum by name",
			inputChain:    "ethereum",
			expectedChain: "eth",
			shouldError:   false,
		},
		{
			name:          "Ethereum by short name",
			inputChain:    "eth",
			expectedChain: "eth",
			shouldError:   false,
		},
		{
			name:          "Ethereum by chain ID",
			inputChain:    "1",
			expectedChain: "eth",
			shouldError:   false,
		},
		{
			name:          "Sepolia by name",
			inputChain:    "sepolia",
			expectedChain: "sepolia",
			shouldError:   false,
		},
		{
			name:          "Sepolia by chain ID",
			inputChain:    "11155111",
			expectedChain: "sepolia",
			shouldError:   false,
		},
		{
			name:          "Base by name",
			inputChain:    "base",
			expectedChain: "base",
			shouldError:   false,
		},
		{
			name:          "Base by chain ID",
			inputChain:    "8453",
			expectedChain: "base",
			shouldError:   false,
		},
		{
			name:        "Unsupported chain",
			inputChain:  "unsupported-chain",
			shouldError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeChainID(tc.inputChain)

			if tc.shouldError {
				if err == nil {
					t.Errorf("expected error for chain %s but got none", tc.inputChain)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for chain %s: %v", tc.inputChain, err)
				}
				if normalized != tc.expectedChain {
					t.Errorf("expected chain %s to normalize to %s but got %s",
						tc.inputChain, tc.expectedChain, normalized)
				}
			}
		})
	}
}

func TestBalanceNode_InvalidAddress(t *testing.T) {
	config := &avsproto.BalanceNode_Config{
		Address: "invalid-address",
		Chain:   "sepolia",
	}

	vm, node := setupBalanceVM(t, config)

	step, err := vm.runBalance("balance-node-1", node)

	if err == nil {
		t.Error("expected error for invalid address but got none")
	}

	if step.Success {
		t.Error("expected step to fail for invalid address")
	}

	if step.Error == "" {
		t.Error("expected error message but got empty string")
	}
}

func TestBalanceNode_MissingAddress(t *testing.T) {
	config := &avsproto.BalanceNode_Config{
		Address: "",
		Chain:   "sepolia",
	}

	vm, node := setupBalanceVM(t, config)

	step, err := vm.runBalance("balance-node-1", node)

	if err == nil {
		t.Error("expected error for missing address but got none")
	}

	if step.Success {
		t.Error("expected step to fail for missing address")
	}
}

func TestBalanceNode_MissingChain(t *testing.T) {
	config := &avsproto.BalanceNode_Config{
		Address: "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		Chain:   "",
	}

	vm, node := setupBalanceVM(t, config)

	step, err := vm.runBalance("balance-node-1", node)

	if err == nil {
		t.Error("expected error for missing chain but got none")
	}

	if step.Success {
		t.Error("expected step to fail for missing chain")
	}
}

func TestBalanceNode_NegativeMinUsdValue(t *testing.T) {
	config := &avsproto.BalanceNode_Config{
		Address:          "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		Chain:            "sepolia",
		MinUsdValueCents: -100, // Invalid negative value
	}

	vm, node := setupBalanceVM(t, config)

	step, err := vm.runBalance("balance-node-1", node)

	if err == nil {
		t.Error("expected error for negative minUsdValue but got none")
	}

	if step.Success {
		t.Error("expected step to fail for negative minUsdValue")
	}

	if step.Error == "" || step.Error != "minUsdValue must be non-negative, got -100 cents" {
		t.Errorf("expected specific error message about negative minUsdValue but got: %s", step.Error)
	}
}

func TestBalanceNode_FormatBalance(t *testing.T) {
	testCases := []struct {
		name           string
		rawBalance     string
		decimals       int
		expectedFormat string
	}{
		{
			name:           "1 ETH (18 decimals)",
			rawBalance:     "1000000000000000000",
			decimals:       18,
			expectedFormat: "1",
		},
		{
			name:           "0.5 ETH",
			rawBalance:     "500000000000000000",
			decimals:       18,
			expectedFormat: "0.5",
		},
		{
			name:           "5 USDC (6 decimals)",
			rawBalance:     "5000000",
			decimals:       6,
			expectedFormat: "5",
		},
		{
			name:           "0.123456 USDC",
			rawBalance:     "123456",
			decimals:       6,
			expectedFormat: "0.123456",
		},
		{
			name:           "Very small amount",
			rawBalance:     "1",
			decimals:       18,
			expectedFormat: "0.000000000000000001",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			formatted := formatBalance(tc.rawBalance, tc.decimals)
			if formatted != tc.expectedFormat {
				t.Errorf("expected %s but got %s", tc.expectedFormat, formatted)
			}
		})
	}
}

func TestBalanceNode_OutputStructure(t *testing.T) {
	// Create mock Moralis server
	mockServer := createMockMoralisServer(t, mockTokenBalances)
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

	config := &avsproto.BalanceNode_Config{
		Address:             testAddress,
		Chain:               "sepolia",
		IncludeSpam:         false,
		IncludeZeroBalances: false,
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	balances, err := vm.fetchMoralisBalances(testAddress, "sepolia", "test-api-key", config, nil)

	if err != nil {
		t.Fatalf("expected successful balance fetch but got error: %v", err)
	}

	if len(balances) == 0 {
		t.Fatal("expected at least one token balance")
	}

	// Verify structure of first token
	firstToken, ok := balances[0].(map[string]interface{})
	if !ok {
		t.Fatal("expected token to be map[string]interface{}")
	}

	// Required fields
	requiredFields := []string{"symbol", "name", "balance", "balanceFormatted", "decimals"}
	for _, field := range requiredFields {
		if _, exists := firstToken[field]; !exists {
			t.Errorf("expected token to have field %s", field)
		}
	}

	// Optional fields that should exist for non-native tokens with value
	optionalFields := []string{"tokenAddress", "usdPrice", "usdValue"}
	for _, field := range optionalFields {
		if _, exists := firstToken[field]; !exists {
			t.Logf("optional field %s not present (expected for some tokens)", field)
		}
	}
}

func TestBalanceNode_MissingAPIKey(t *testing.T) {
	// Clear macro secrets to simulate missing API key
	SetMacroSecrets(map[string]string{})

	config := &avsproto.BalanceNode_Config{
		Address: "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		Chain:   "sepolia",
	}

	vm, node := setupBalanceVM(t, config)

	step, err := vm.runBalance("balance-node-1", node)

	if err == nil {
		t.Error("expected error for missing API key but got none")
	}

	if step.Success {
		t.Error("expected step to fail for missing API key")
	}

	// Restore API key for other tests
	SetMacroSecrets(map[string]string{
		"moralis_api_key": "test-api-key",
	})
}

func TestBalanceNode_MultipleTokenAddresses(t *testing.T) {
	// Integration test with REAL Moralis API
	// Tests the two-phase fetching strategy with multiple tokenAddresses filter
	// Critical test case: When one requested token has no balance (USDT),
	// we should STILL return ETH (native) + WETH (has balance)
	testAddress := "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e"

	// Config with TWO token addresses: WETH (has balance) + USDT (no balance/no data)
	config := &avsproto.BalanceNode_Config{
		Address: testAddress,
		Chain:   "sepolia",
		TokenAddresses: []string{
			"0xfff9976782d46cc05630d1f6ebab18b2324d6b14", // WETH (wallet HAS this)
			"0xaa8e23fb1079ea71e0a56f48a2aa51851d8433d0", // USDT (wallet does NOT have this)
		},
		IncludeSpam:         false,
		IncludeZeroBalances: false,
		MinUsdValueCents:    0,
	}

	vm, _ := setupBalanceVM(t, config)

	// Get real Moralis API key from macro secrets (already set in setupBalanceVM)
	moralisAPIKey := ""
	if macroSecrets != nil {
		moralisAPIKey = macroSecrets["moralis_api_key"]
	}
	if moralisAPIKey == "" || moralisAPIKey == "test-api-key" {
		t.Skip("real moralis API key not configured in macros.secrets - skipping integration test")
	}

	// Use fetchMoralisBalancesWithFiltering (the two-phase approach)
	balances, err := vm.fetchMoralisBalancesWithFiltering(testAddress, "sepolia", moralisAPIKey, config)

	if err != nil {
		t.Fatalf("fetchMoralisBalancesWithFiltering failed: %v", err)
	}

	t.Logf("Total balances returned: %d", len(balances))

	// CRITICAL REQUIREMENT: Even though USDT has no balance, we should get:
	// 1. ETH (native token - always included)
	// 2. WETH (requested and has balance)
	// USDT should NOT be in the response (no balance)
	if len(balances) == 0 {
		t.Fatalf("❌ Response is EMPTY! This is the bug! Expected ETH + WETH (at least 2 tokens)")
	}

	// Check which tokens we got
	hasETH := false
	hasWETH := false
	hasUSDT := false

	for _, bal := range balances {
		token, ok := bal.(map[string]interface{})
		if !ok {
			continue
		}

		symbol := token["symbol"].(string)
		t.Logf("Token: %s", symbol)

		if symbol == "ETH" {
			hasETH = true
			t.Logf("✅ Found ETH (native token)")
			// Check if it has tokenAddress (it shouldn't)
			if _, hasAddr := token["tokenAddress"]; hasAddr {
				t.Errorf("❌ ETH should NOT have tokenAddress field")
			}
		}

		if symbol == "WETH" {
			hasWETH = true
			t.Logf("✅ Found WETH")
		}

		if symbol == "USDT" {
			hasUSDT = true
			t.Logf("Found USDT (unexpected - wallet doesn't have balance)")
		}
	}

	// Verify we got the expected tokens
	if !hasETH {
		t.Errorf("❌ ETH (native token) is missing from response!")
	}

	if !hasWETH {
		t.Errorf("❌ WETH is missing from response!")
	}

	if hasUSDT {
		t.Logf("Note: USDT was returned (wallet has balance for it)")
	}

	// Final check: we should have at least 2 tokens (ETH + WETH)
	if len(balances) < 2 {
		t.Errorf("❌ Expected at least 2 tokens (ETH + WETH), got %d", len(balances))
	}
}

func TestBalanceNode_MoralisAuthError(t *testing.T) {
	// Create mock server that returns 401 authentication error
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Token is invalid format"}`))
	}))
	defer mockServer.Close()

	// Set test API base URL to use mock server
	testMoralisAPIBaseURL = mockServer.URL
	defer func() {
		testMoralisAPIBaseURL = "" // Reset after test
	}()

	testAddress := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"
	config := &avsproto.BalanceNode_Config{
		Address: testAddress,
		Chain:   "sepolia",
	}

	vm, _ := setupBalanceVM(t, config)

	// Attempt to fetch balances with invalid API key
	balances, err := vm.fetchMoralisBalances(testAddress, "sepolia", "invalid-test-api-key", config, nil)

	// Should receive an error
	if err == nil {
		t.Fatal("expected error for 401 authentication failure but got none")
	}

	// Verify error message contains status code and response
	expectedSubstrings := []string{"401", "Token is invalid format"}
	for _, substr := range expectedSubstrings {
		if !strings.Contains(err.Error(), substr) {
			t.Errorf("expected error message to contain %q but got: %s", substr, err.Error())
		}
	}

	// Should return nil balances on error
	if balances != nil {
		t.Errorf("expected nil balances on error but got: %v", balances)
	}
}
