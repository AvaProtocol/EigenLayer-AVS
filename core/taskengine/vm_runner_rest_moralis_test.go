package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
)

// TestRestRequestMoralisWalletBalances tests the Moralis API integration for wallet token balances
// This test demonstrates how to use the existing restApi node to get wallet balances
// instead of creating a new balance_node type
func TestRestRequestMoralisWalletBalances(t *testing.T) {
	// Test wallet address (Ethereum mainnet address with some tokens)
	testWalletAddress := "0xcB1C1FdE09f811B294172696404e88E658659905"

	// Create the REST API node for Moralis wallet balances
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    "https://deep-index.moralis.io/api/v2.2/wallets/" + testWalletAddress + "/tokens?chain=eth&limit=25&exclude_spam=true&exclude_unverified_contracts=true",
			Method: "GET",
			Headers: map[string]string{
				"Accept":     "application/json",
				"X-API-Key":  "{{apContext.configVars.moralis_api_key}}",
				"User-Agent": "AvaProtocol-EigenLayer-AVS/1.0",
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "moralis-wallet-balances",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "moralis-wallet-balances",
		},
	}

	// Get the test config which includes secrets from aggregator.yaml
	testConfig := testutil.GetTestConfig()
	if testConfig == nil {
		t.Skip("Test config not loaded, skipping integration test")
	}

	// Extract secrets from the loaded config
	globalSecrets := map[string]string{}
	if testConfig.MacroSecrets != nil {
		// Convert map[string]string to the format expected by VM
		for k, v := range testConfig.MacroSecrets {
			globalSecrets[k] = v
		}
	}

	// Verify moralis_api_key is available
	moralisKey, exists := globalSecrets["moralis_api_key"]
	if !exists || moralisKey == "" {
		t.Skip("Moralis API key not configured in test config, skipping integration test")
	}

	t.Logf("✅ Loaded moralis_api_key from aggregator.yaml config: %s", moralisKey[:20]+"...")

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "moralis-test",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), globalSecrets)

	require.NoError(t, err, "failed to create VM")

	// Create REST API processor
	processor := NewRestProrcessor(vm)

	// Execute the node
	stepID := "moralis-wallet-balances"
	result, err := processor.Execute(stepID, node)
	require.NoError(t, err, "Moralis API call should succeed")
	require.NotNil(t, result, "Result should not be nil")

	// Verify execution was successful
	require.True(t, result.Success, "Moralis API call should be successful")
	require.Empty(t, result.Error, "No error should be returned")

	// Verify output data structure
	require.NotNil(t, result.OutputData, "Output data should not be nil")
	restOutput := result.GetRestApi()
	require.NotNil(t, restOutput, "REST API output should not be nil")
	require.NotNil(t, restOutput.Data, "REST API data should not be nil")

	// Extract the actual data from protobuf response
	dataInterface := restOutput.Data.AsInterface()
	require.NotNil(t, dataInterface, "Data interface should not be nil")

	// The response is wrapped in a "data" field
	dataMap, ok := dataInterface.(map[string]interface{})
	require.True(t, ok, "Data should be a map")
	require.Contains(t, dataMap, "data", "Response should contain 'data' field")

	// Get the actual API response data
	apiResponse, ok := dataMap["data"].(map[string]interface{})
	require.True(t, ok, "API response should be a map")
	require.Contains(t, apiResponse, "result", "API response should contain 'result' field")

	// Get the token balances array
	tokenBalances, ok := apiResponse["result"].([]interface{})
	require.True(t, ok, "Result should be an array")

	// Log some basic information
	t.Logf("✅ Moralis API integration test passed!")
	t.Logf("✅ Retrieved %d token balances for wallet %s", len(tokenBalances), testWalletAddress)
	t.Logf("✅ Template variable {{apContext.configVars.moralis_api_key}} was properly resolved from aggregator.yaml")
	t.Logf("✅ Response contains valid Moralis API structure")

	// Log first few tokens for verification
	for i, tokenBalance := range tokenBalances {
		if i >= 3 { // Only log first 3 tokens
			break
		}
		tokenData, ok := tokenBalance.(map[string]interface{})
		if ok {
			t.Logf("Token %d: %s (%s) - Balance: %s",
				i+1,
				tokenData["name"],
				tokenData["symbol"],
				tokenData["balance_formatted"])
		}
	}
}

// TestRestRequestMoralisSpecificTokens tests getting balances for specific tokens using correct format
func TestRestRequestMoralisSpecificTokens(t *testing.T) {
	// Test wallet address
	testWalletAddress := "0xcB1C1FdE09f811B294172696404e88E658659905"

	// Specific token addresses (USDC and WETH on Ethereum mainnet)
	// Moralis expects comma-separated addresses, not JSON array
	specificTokens := "0xA0b86a33E6441b8c4C8C0C4C0C4C0C4C0C4C0C4C,0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"

	// Create the REST API node for specific token balances
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    "https://deep-index.moralis.io/api/v2.2/wallets/" + testWalletAddress + "/tokens?chain=eth&token_addresses=" + specificTokens,
			Method: "GET",
			Headers: map[string]string{
				"Accept":     "application/json",
				"X-API-Key":  "{{apContext.configVars.moralis_api_key}}",
				"User-Agent": "AvaProtocol-EigenLayer-AVS/1.0",
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "moralis-specific-tokens",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "moralis-specific-tokens",
		},
	}

	// Get the test config which includes secrets from aggregator.yaml
	testConfig := testutil.GetTestConfig()
	if testConfig == nil {
		t.Skip("Test config not loaded, skipping integration test")
	}

	// Extract secrets from the loaded config
	globalSecrets := map[string]string{}
	if testConfig.MacroSecrets != nil {
		// Convert map[string]string to the format expected by VM
		for k, v := range testConfig.MacroSecrets {
			globalSecrets[k] = v
		}
	}

	// Verify moralis_api_key is available
	moralisKey, exists := globalSecrets["moralis_api_key"]
	if !exists || moralisKey == "" {
		t.Skip("Moralis API key not configured in test config, skipping integration test")
	}

	t.Logf("✅ Loaded moralis_api_key from aggregator.yaml config: %s", moralisKey[:20]+"...")

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "moralis-specific-test",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), globalSecrets)

	require.NoError(t, err, "failed to create VM")

	// Create REST API processor
	processor := NewRestProrcessor(vm)

	// Execute the node
	stepID := "moralis-specific-tokens"
	result, err := processor.Execute(stepID, node)
	require.NoError(t, err, "Moralis API call should succeed")
	require.NotNil(t, result, "Result should not be nil")

	// Verify execution was successful
	if !result.Success {
		t.Logf("❌ API call failed with error: %s", result.Error)
		t.Logf("❌ Response data: %v", result.OutputData)
	}
	require.True(t, result.Success, "Moralis API call should be successful")
	require.Empty(t, result.Error, "No error should be returned")

	// Extract the actual data from protobuf response
	dataInterface := result.GetRestApi().Data.AsInterface()
	require.NotNil(t, dataInterface, "Data interface should not be nil")

	// The response is wrapped in a "data" field
	dataMap, ok := dataInterface.(map[string]interface{})
	require.True(t, ok, "Data should be a map")
	require.Contains(t, dataMap, "data", "Response should contain 'data' field")

	// Get the actual API response data
	apiResponse, ok := dataMap["data"].(map[string]interface{})
	require.True(t, ok, "API response should be a map")
	require.Contains(t, apiResponse, "result", "API response should contain 'result' field")

	// Get the token balances array
	tokenBalances, ok := apiResponse["result"].([]interface{})
	require.True(t, ok, "Result should be an array")

	// Log some basic information
	t.Logf("✅ Moralis specific tokens test passed!")
	t.Logf("✅ Retrieved %d specific token balances for wallet %s", len(tokenBalances), testWalletAddress)
	t.Logf("✅ Used correct comma-separated format for token_addresses parameter")

	// Log tokens for verification
	for i, tokenBalance := range tokenBalances {
		tokenData, ok := tokenBalance.(map[string]interface{})
		if ok {
			t.Logf("Specific Token %d: %s (%s) - Balance: %s, Address: %s",
				i+1,
				tokenData["name"],
				tokenData["symbol"],
				tokenData["balance_formatted"],
				tokenData["token_address"])
		}
	}
}
