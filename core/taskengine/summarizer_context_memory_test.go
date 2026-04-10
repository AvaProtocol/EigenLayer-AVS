package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
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

// TestBuildRequest_LoopNodeTokenMetadata verifies that a LOOP step with a contractWrite
// runner extracts token metadata from the nested runner config, resolving the contract
// address from execution metadata when the config uses a template variable.
func TestBuildRequest_LoopNodeTokenMetadata(t *testing.T) {
	usdcAddr := "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	daiAddr := "0x50c5725949a6f0c72e6c4a641f24049a917db0cb"

	oldService := GetTokenEnrichmentService()
	service := &TokenEnrichmentService{
		cache: map[string]*TokenMetadata{
			strings.ToLower(usdcAddr): {Symbol: "USDC", Decimals: 6, Name: "USD Coin"},
			strings.ToLower(daiAddr):  {Symbol: "DAI", Decimals: 18, Name: "Dai Stablecoin"},
		},
	}
	SetTokenEnrichmentService(service)
	defer SetTokenEnrichmentService(oldService)

	// Build loop output data: array of iteration results with metadata.contractAddress
	// This matches what wrapResultWithMetadata produces during execution.
	loopOutputData, err := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"transfer": map[string]interface{}{
				"from":  "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
				"to":    "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
				"value": "2000000",
			},
			"metadata": map[string]interface{}{
				"transactionHash": "0xe3c082af67cba0cc7ece21a4211567726834f9c8577e303e834b2196c528764e",
				"contractAddress": usdcAddr,
			},
		},
		map[string]interface{}{
			"transfer": map[string]interface{}{
				"from":  "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
				"to":    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
				"value": "3000000",
			},
			"metadata": map[string]interface{}{
				"transactionHash": "0x9e2c0483e8b2afcd4a50d1711ed9e5ebda4c4e314209f1d483d5cba83d50dfb1",
				"contractAddress": usdcAddr,
			},
		},
	})
	require.NoError(t, err)

	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Automatically Split Incoming USDC Payments",
			"chain":  "base",
			"runner": "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
			"tokens": []interface{}{usdcAddr, daiAddr},
		},
	}
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"trigger0": {Id: "trigger0", Name: "eventTrigger"},
		"split1":   {Id: "split1", Name: "split1"},
		"loop1": {
			Id:   "loop1",
			Name: "loop1",
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{
						InputVariable: "{{split1.data}}",
						IterVal:       "value",
						IterKey:       "index",
					},
					Runner: &avsproto.LoopNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "{{value.tokenAddress}}",
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
									{MethodName: "transfer", MethodParams: []string{"{{value.recipient}}", "{{value.amount}}"}},
								},
							},
						},
					},
				},
			},
		},
		"telegram1": {Id: "telegram1", Name: "telegram1"},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "trigger0", Name: "eventTrigger", Type: "eventTrigger", Success: true},
		{Id: "split1", Name: "split1", Type: "customCode", Success: true},
		{
			Id:      "loop1",
			Name:    "loop1",
			Type:    "loop",
			Success: true,
			// Loop step metadata is nil in practice — the contract address
			// is resolved from the loop output data (iteration metadata.contractAddress).
			Metadata: nil,
			OutputData: &avsproto.Execution_Step_Loop{
				Loop: &avsproto.LoopNode_Output{
					Data: loopOutputData,
				},
			},
		},
		{Id: "telegram1", Name: "telegram1", Type: "restApi", Success: true},
	}
	vm.mu.Unlock()

	summarizer := &ContextMemorySummarizer{baseURL: "http://localhost", authToken: "test"}
	req, err := summarizer.buildRequest(vm, "telegram1")
	require.NoError(t, err)

	// The loop step (index 2) should have per-step tokenMetadata populated with USDC
	loopStep := req.Steps[2]
	require.NotNil(t, loopStep.TokenMetadata, "loop step should have per-step tokenMetadata")
	assert.Equal(t, "USDC", loopStep.TokenMetadata.Symbol)
	assert.Equal(t, uint32(6), loopStep.TokenMetadata.Decimals)

	// The request-level tokenMetadata should include USDC (from per-step + settings.tokens)
	usdcMeta := req.TokenMetadata[strings.ToLower(usdcAddr)]
	require.NotNil(t, usdcMeta, "USDC should be in request-level tokenMetadata")
	assert.Equal(t, "USDC", usdcMeta.Symbol)
	assert.Equal(t, uint32(6), usdcMeta.Decimals)

	// DAI should also be present (from settings.tokens)
	daiMeta := req.TokenMetadata[strings.ToLower(daiAddr)]
	require.NotNil(t, daiMeta, "DAI should be in request-level tokenMetadata")
	assert.Equal(t, "DAI", daiMeta.Symbol)
	assert.Equal(t, uint32(18), daiMeta.Decimals)
}

// TestBuildRequest_LoopNodeTokenMetadata_NoMetadataFallback verifies that when a LOOP
// step has no execution metadata (e.g., simulation or failed run), the per-step
// tokenMetadata remains nil without panicking.
func TestBuildRequest_LoopNodeTokenMetadata_NoMetadataFallback(t *testing.T) {
	usdcAddr := "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	daiAddr := "0x50c5725949a6f0c72e6c4a641f24049a917db0cb"

	oldService := GetTokenEnrichmentService()
	service := &TokenEnrichmentService{
		cache: map[string]*TokenMetadata{
			strings.ToLower(usdcAddr): {Symbol: "USDC", Decimals: 6, Name: "USD Coin"},
			strings.ToLower(daiAddr):  {Symbol: "DAI", Decimals: 18, Name: "Dai Stablecoin"},
		},
	}
	SetTokenEnrichmentService(service)
	defer SetTokenEnrichmentService(oldService)

	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Automatically Split Incoming USDC Payments",
			"chain":  "base",
			"runner": "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
			"tokens": []interface{}{usdcAddr, daiAddr},
		},
	}
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"trigger0": {Id: "trigger0", Name: "eventTrigger"},
		"loop1": {
			Id:   "loop1",
			Name: "loop1",
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{
						InputVariable: "{{split1.data}}",
						IterVal:       "value",
						IterKey:       "index",
					},
					Runner: &avsproto.LoopNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "{{value.tokenAddress}}",
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
									{MethodName: "transfer", MethodParams: []string{"{{value.recipient}}", "{{value.amount}}"}},
								},
							},
						},
					},
				},
			},
		},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "trigger0", Name: "eventTrigger", Type: "eventTrigger", Success: true},
		{
			Id:       "loop1",
			Name:     "loop1",
			Type:     "loop",
			Success:  true,
			Metadata: nil, // No metadata — simulates a failed or simulation execution
		},
	}
	vm.mu.Unlock()

	summarizer := &ContextMemorySummarizer{baseURL: "http://localhost", authToken: "test"}
	req, err := summarizer.buildRequest(vm, "loop1")
	require.NoError(t, err)

	// Loop step should NOT have per-step tokenMetadata (no metadata to resolve from)
	loopStep := req.Steps[1]
	assert.Nil(t, loopStep.TokenMetadata, "loop step tokenMetadata should be nil when no metadata available")

	// Request-level tokenMetadata should still have tokens from settings.tokens
	assert.Len(t, req.TokenMetadata, 2, "settings.tokens should still populate request-level tokenMetadata")
	assert.NotNil(t, req.TokenMetadata[strings.ToLower(usdcAddr)])
	assert.NotNil(t, req.TokenMetadata[strings.ToLower(daiAddr)])
}
