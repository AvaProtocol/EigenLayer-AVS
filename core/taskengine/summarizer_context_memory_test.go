package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildRequest_SettingsTokens verifies that settings.tokens addresses
// are resolved via TokenEnrichmentService and included in the request-level tokenMetadata.
func TestBuildRequest_SettingsTokens(t *testing.T) {
	usdcAddr := "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
	wethAddr := "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"

	// Set up a TokenEnrichmentService with pre-populated cache (no RPC needed)
	oldService := GetTokenEnrichmentService()
	service := &TokenEnrichmentService{
		cache: map[string]*TokenMetadata{
			strings.ToLower(usdcAddr): {Symbol: "USDC", Decimals: 6, Name: "USD Coin"},
			strings.ToLower(wethAddr): {Symbol: "WETH", Decimals: 18, Name: "Wrapped Ether"},
		},
	}
	SetTokenEnrichmentService(service)
	defer SetTokenEnrichmentService(oldService)

	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Transfer Alert",
			"chain":  "Ethereum",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
			"tokens": []interface{}{usdcAddr, wethAddr},
		},
	}
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node0": {Id: "node0", Name: "trigger"},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "node0", Name: "trigger", Type: "eventTrigger", Success: true},
	}
	vm.mu.Unlock()

	summarizer := &ContextMemorySummarizer{baseURL: "http://localhost", authToken: "test"}
	req, err := summarizer.buildRequest(vm, "trigger")
	require.NoError(t, err)

	// Both tokens should appear in the request-level tokenMetadata
	assert.Len(t, req.TokenMetadata, 2, "expected 2 token entries in tokenMetadata")

	usdcMeta := req.TokenMetadata[strings.ToLower(usdcAddr)]
	require.NotNil(t, usdcMeta, "USDC metadata should be present")
	assert.Equal(t, "USDC", usdcMeta.Symbol)
	assert.Equal(t, uint32(6), usdcMeta.Decimals)

	wethMeta := req.TokenMetadata[strings.ToLower(wethAddr)]
	require.NotNil(t, wethMeta, "WETH metadata should be present")
	assert.Equal(t, "WETH", wethMeta.Symbol)
	assert.Equal(t, uint32(18), wethMeta.Decimals)
}

// TestBuildRequest_SettingsTokensNilService verifies settings.tokens is safely
// skipped when TokenEnrichmentService is not available.
func TestBuildRequest_SettingsTokensNilService(t *testing.T) {
	oldService := GetTokenEnrichmentService()
	SetTokenEnrichmentService(nil)
	defer SetTokenEnrichmentService(oldService)

	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Transfer Alert",
			"chain":  "Ethereum",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
			"tokens": []interface{}{"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"},
		},
	}
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node0": {Id: "node0", Name: "trigger"},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "node0", Name: "trigger", Type: "eventTrigger", Success: true},
	}
	vm.mu.Unlock()

	summarizer := &ContextMemorySummarizer{baseURL: "http://localhost", authToken: "test"}
	req, err := summarizer.buildRequest(vm, "trigger")
	require.NoError(t, err)

	// No tokens should be resolved when service is nil
	assert.Empty(t, req.TokenMetadata, "tokenMetadata should be empty when service is nil")
}

// TestBuildRequest_SettingsTokensDeduplication verifies that tokens already resolved
// from step-level metadata (CONTRACT_WRITE) are not overwritten by settings.tokens.
func TestBuildRequest_SettingsTokensDeduplication(t *testing.T) {
	usdcAddr := "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"

	// The enrichment service returns "USDC" with 6 decimals.
	// The step-level metadata (section 1) will populate this first.
	// settings.tokens (section 3) should NOT overwrite it.
	oldService := GetTokenEnrichmentService()
	service := &TokenEnrichmentService{
		cache: map[string]*TokenMetadata{
			strings.ToLower(usdcAddr): {Symbol: "USDC", Decimals: 6, Name: "USD Coin"},
		},
	}
	SetTokenEnrichmentService(service)
	defer SetTokenEnrichmentService(oldService)

	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Transfer Alert",
			"chain":  "Ethereum",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"tokens": []interface{}{usdcAddr},
		},
	}

	// Set up a CONTRACT_WRITE node targeting USDC with an ERC20 "transfer" method.
	// This causes section 1 of buildRequest to populate tokenMetadataMap first.
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"trigger0": {Id: "trigger0", Name: "trigger"},
		"node1": {
			Id:   "node1",
			Name: "transferUSDC",
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: usdcAddr,
						MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
							{MethodName: "transfer"},
						},
					},
				},
			},
		},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "trigger0", Name: "trigger", Type: "eventTrigger", Success: true},
		{Id: "node1", Name: "transferUSDC", Type: "contractWrite", Success: true},
	}
	vm.mu.Unlock()

	summarizer := &ContextMemorySummarizer{baseURL: "http://localhost", authToken: "test"}
	req, err := summarizer.buildRequest(vm, "trigger")
	require.NoError(t, err)

	// Should have exactly 1 entry — step-level resolved it, settings.tokens should skip it.
	assert.Len(t, req.TokenMetadata, 1, "should have exactly 1 token, no duplicates")
	usdcMeta := req.TokenMetadata[strings.ToLower(usdcAddr)]
	require.NotNil(t, usdcMeta, "USDC metadata should be present")
	assert.Equal(t, "USDC", usdcMeta.Symbol)
	assert.Equal(t, uint32(6), usdcMeta.Decimals)
}
