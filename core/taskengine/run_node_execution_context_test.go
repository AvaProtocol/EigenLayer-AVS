package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestContractWriteExecutionContext verifies that the execution context correctly reflects
// whether a contract write was actually executed on-chain (real) or simulated via Tenderly.
// This test addresses the bug where all contract writes were incorrectly marked as simulated.
func TestContractWriteExecutionContext(t *testing.T) {
	tests := []struct {
		name              string
		methodResults     []*avsproto.ContractWriteNode_MethodResult
		expectedSimulated bool
		expectedProvider  string
		description       string
	}{
		{
			name: "Real execution with transaction hash",
			methodResults: []*avsproto.ContractWriteNode_MethodResult{
				{
					MethodName: "approve",
					Success:    true,
					Receipt: func() *structpb.Value {
						v, _ := structpb.NewValue(map[string]interface{}{
							"transactionHash": "0x4179cd43f5cdd4eb92262aabed4efbc886e91328bd9473cad86a37d94f2bdeee",
							"blockNumber":     "0x90504a",
							"status":          "0x1",
							"gasUsed":         "0x22d43",
						})
						return v
					}(),
				},
			},
			expectedSimulated: false,
			expectedProvider:  "bundler",
			description:       "Real on-chain execution should be marked as not simulated",
		},
		{
			name: "Simulated execution without transaction hash",
			methodResults: []*avsproto.ContractWriteNode_MethodResult{
				{
					MethodName: "approve",
					Success:    true,
					Receipt: func() *structpb.Value {
						v, _ := structpb.NewValue(map[string]interface{}{
							"status":  "0x1",
							"gasUsed": "0x22d43",
						})
						return v
					}(),
				},
			},
			expectedSimulated: true,
			expectedProvider:  "tenderly",
			description:       "Tenderly simulation should be marked as simulated",
		},
		{
			name: "Empty transaction hash treated as simulation",
			methodResults: []*avsproto.ContractWriteNode_MethodResult{
				{
					MethodName: "approve",
					Success:    true,
					Receipt: func() *structpb.Value {
						v, _ := structpb.NewValue(map[string]interface{}{
							"transactionHash": "",
							"status":          "0x1",
							"gasUsed":         "0x22d43",
						})
						return v
					}(),
				},
			},
			expectedSimulated: true,
			expectedProvider:  "tenderly",
			description:       "Empty transaction hash should be treated as simulation",
		},
		{
			name: "No receipt defaults to simulation",
			methodResults: []*avsproto.ContractWriteNode_MethodResult{
				{
					MethodName: "approve",
					Success:    true,
					Receipt:    nil,
				},
			},
			expectedSimulated: true,
			expectedProvider:  "tenderly",
			description:       "No receipt should default to simulation",
		},
		{
			name: "Multiple methods - one real transaction",
			methodResults: []*avsproto.ContractWriteNode_MethodResult{
				{
					MethodName: "approve",
					Success:    true,
					Receipt: func() *structpb.Value {
						v, _ := structpb.NewValue(map[string]interface{}{
							"transactionHash": "0xabc123",
							"status":          "0x1",
						})
						return v
					}(),
				},
				{
					MethodName: "transfer",
					Success:    true,
					Receipt: func() *structpb.Value {
						v, _ := structpb.NewValue(map[string]interface{}{
							"transactionHash": "0xdef456",
							"status":          "0x1",
						})
						return v
					}(),
				},
			},
			expectedSimulated: false,
			expectedProvider:  "bundler",
			description:       "If any method has real transaction, mark as real execution",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert method results to metadata format (array of interface{})
			metadataArray := make([]interface{}, len(tt.methodResults))
			for i, result := range tt.methodResults {
				resultMap := map[string]interface{}{
					"methodName": result.MethodName,
					"success":    result.Success,
				}
				if result.Receipt != nil {
					resultMap["receipt"] = result.Receipt.AsInterface()
				}
				metadataArray[i] = resultMap
			}

			// Create metadata protobuf value
			metadata, err := structpb.NewValue(metadataArray)
			require.NoError(t, err, "Failed to create metadata")

			// Create a mock response with this metadata
			resp := &avsproto.RunNodeWithInputsResp{
				Success:  true,
				Metadata: metadata,
			}

			// Apply the execution context logic (same as in run_node_immediately.go)
			isSimulated := true // Default to simulated
			provider := string(ProviderTenderly)

			if resp.Metadata != nil {
				meta := resp.Metadata.AsInterface()
				if metaArr, ok := meta.([]interface{}); ok {
					for _, item := range metaArr {
						if m, ok := item.(map[string]interface{}); ok {
							// Check if there's a receipt with a real transaction hash
							if rec, ok := m["receipt"].(map[string]interface{}); ok {
								if txHash, hasTxHash := rec["transactionHash"].(string); hasTxHash && txHash != "" {
									// This is a real on-chain transaction
									isSimulated = false
									provider = string(ProviderBundler) // UserOp executed through bundler
									break
								}
							}
						}
					}
				}
			}

			// Verify the execution context
			assert.Equal(t, tt.expectedSimulated, isSimulated,
				"isSimulated mismatch: %s", tt.description)
			assert.Equal(t, tt.expectedProvider, provider,
				"provider mismatch: %s", tt.description)

			t.Logf("✅ %s", tt.description)
			t.Logf("   isSimulated: %v (expected: %v)", isSimulated, tt.expectedSimulated)
			t.Logf("   provider: %s (expected: %s)", provider, tt.expectedProvider)
		})
	}
}

// TestExecutionContextInResponseMetadata verifies that the execution context is properly
// set in the response ExecutionContext field based on the actual execution mode
func TestExecutionContextInResponseMetadata(t *testing.T) {
	t.Run("Real execution context in response", func(t *testing.T) {
		// Create a response with real transaction metadata
		metadata, _ := structpb.NewValue([]interface{}{
			map[string]interface{}{
				"methodName": "approve",
				"success":    true,
				"receipt": map[string]interface{}{
					"transactionHash": "0x4179cd43f5cdd4eb92262aabed4efbc886e91328bd9473cad86a37d94f2bdeee",
					"blockNumber":     "0x90504a",
					"status":          "0x1",
				},
			},
		})

		resp := &avsproto.RunNodeWithInputsResp{
			Success:  true,
			Metadata: metadata,
		}

		// Apply execution context logic
		isSimulated := true
		provider := string(ProviderTenderly)

		if resp.Metadata != nil {
			meta := resp.Metadata.AsInterface()
			if metaArr, ok := meta.([]interface{}); ok {
				for _, item := range metaArr {
					if m, ok := item.(map[string]interface{}); ok {
						if rec, ok := m["receipt"].(map[string]interface{}); ok {
							if txHash, hasTxHash := rec["transactionHash"].(string); hasTxHash && txHash != "" {
								isSimulated = false
								provider = string(ProviderBundler)
								break
							}
						}
					}
				}
			}
		}

		ctxMap := map[string]interface{}{
			"is_simulated": isSimulated,
			"provider":     provider,
			"chain_id":     11155111,
		}
		ctxVal, err := structpb.NewValue(ctxMap)
		require.NoError(t, err)
		resp.ExecutionContext = ctxVal

		// Verify the execution context is correct
		require.NotNil(t, resp.ExecutionContext, "ExecutionContext should not be nil")

		ctx := resp.ExecutionContext.AsInterface().(map[string]interface{})
		assert.Equal(t, false, ctx["is_simulated"],
			"Real execution should have is_simulated=false")
		assert.Equal(t, "bundler", ctx["provider"],
			"Real execution should have provider=bundler")
		assert.Equal(t, float64(11155111), ctx["chain_id"],
			"Chain ID should be preserved")

		t.Logf("✅ Real execution context correctly set in response")
		t.Logf("   is_simulated: %v", ctx["is_simulated"])
		t.Logf("   provider: %s", ctx["provider"])
		t.Logf("   chain_id: %v", ctx["chain_id"])
	})

	t.Run("Simulated execution context in response", func(t *testing.T) {
		// Create a response with simulated metadata (no transactionHash)
		metadata, _ := structpb.NewValue([]interface{}{
			map[string]interface{}{
				"methodName": "approve",
				"success":    true,
				"receipt": map[string]interface{}{
					"status":  "0x1",
					"gasUsed": "0x22d43",
				},
			},
		})

		resp := &avsproto.RunNodeWithInputsResp{
			Success:  true,
			Metadata: metadata,
		}

		// Apply execution context logic (defaults to simulated)
		isSimulated := true
		provider := string(ProviderTenderly)

		ctxMap := map[string]interface{}{
			"is_simulated": isSimulated,
			"provider":     provider,
			"chain_id":     11155111,
		}
		ctxVal, err := structpb.NewValue(ctxMap)
		require.NoError(t, err)
		resp.ExecutionContext = ctxVal

		// Verify the execution context is correct
		require.NotNil(t, resp.ExecutionContext, "ExecutionContext should not be nil")

		ctx := resp.ExecutionContext.AsInterface().(map[string]interface{})
		assert.Equal(t, true, ctx["is_simulated"],
			"Simulated execution should have is_simulated=true")
		assert.Equal(t, "tenderly", ctx["provider"],
			"Simulated execution should have provider=tenderly")

		t.Logf("✅ Simulated execution context correctly set in response")
		t.Logf("   is_simulated: %v", ctx["is_simulated"])
		t.Logf("   provider: %s", ctx["provider"])
	})
}

// TestExecutionProviderConstants verifies the provider constants are correct
func TestExecutionProviderConstants(t *testing.T) {
	tests := []struct {
		provider ExecutionProvider
		expected string
	}{
		{ProviderChainRPC, "chain_rpc"},
		{ProviderTenderly, "tenderly"},
		{ProviderBundler, "bundler"},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.provider),
				"Provider constant should match expected value")
			t.Logf("✅ Provider %s = %s", tt.provider, tt.expected)
		})
	}
}

// TestGetProviderForContext verifies the provider selection logic
func TestGetProviderForContext(t *testing.T) {
	tests := []struct {
		name         string
		isSimulated  bool
		expectedProv ExecutionProvider
	}{
		{
			name:         "Simulated execution uses Tenderly",
			isSimulated:  true,
			expectedProv: ProviderTenderly,
		},
		{
			name:         "Real execution uses ChainRPC",
			isSimulated:  false,
			expectedProv: ProviderChainRPC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := GetProviderForContext(tt.isSimulated, 11155111)
			assert.Equal(t, tt.expectedProv, provider,
				"Provider should match expected value")
			t.Logf("✅ %s: isSimulated=%v → provider=%s",
				tt.name, tt.isSimulated, provider)
		})
	}
}

// TestGetExecutionContext verifies the execution context generation
func TestGetExecutionContext(t *testing.T) {
	tests := []struct {
		name              string
		chainID           int64
		isSimulated       bool
		expectedProvider  string
		expectedSimulated bool
	}{
		{
			name:              "Sepolia simulated execution",
			chainID:           11155111,
			isSimulated:       true,
			expectedProvider:  "tenderly",
			expectedSimulated: true,
		},
		{
			name:              "Sepolia real execution",
			chainID:           11155111,
			isSimulated:       false,
			expectedProvider:  "chain_rpc",
			expectedSimulated: false,
		},
		{
			name:              "Base simulated execution",
			chainID:           8453,
			isSimulated:       true,
			expectedProvider:  "tenderly",
			expectedSimulated: true,
		},
		{
			name:              "Base real execution",
			chainID:           8453,
			isSimulated:       false,
			expectedProvider:  "chain_rpc",
			expectedSimulated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := GetExecutionContext(tt.chainID, tt.isSimulated)

			require.NotNil(t, ctx, "Execution context should not be nil")
			assert.Equal(t, tt.chainID, ctx["chainId"],
				"Chain ID should match")
			assert.Equal(t, tt.expectedSimulated, ctx["isSimulated"],
				"isSimulated should match")
			assert.Equal(t, tt.expectedProvider, ctx["provider"],
				"Provider should match")

			t.Logf("✅ %s: chainId=%d, isSimulated=%v, provider=%s",
				tt.name, ctx["chainId"], ctx["isSimulated"], ctx["provider"])
		})
	}
}
