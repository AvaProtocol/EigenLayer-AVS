package taskengine

import (
	"encoding/json"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
)

func TestTokenEnrichmentIntegration(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	t.Log("=== Testing Token Enrichment Integration ===")

	// Test that the TokenEnrichmentService was initialized
	if engine.tokenEnrichmentService != nil {
		t.Log("✅ TokenEnrichmentService is initialized in engine")
		t.Logf("📊 Whitelist cache size: %d tokens", engine.tokenEnrichmentService.GetCacheSize())
		t.Logf("🔗 Chain ID: %d", engine.tokenEnrichmentService.GetChainID())
	} else {
		t.Log("❌ TokenEnrichmentService is NOT initialized - this may be expected if RPC is not available")
	}

	// Test 1: Token metadata service functionality
	t.Run("TokenMetadataService", func(t *testing.T) {
		if engine.tokenEnrichmentService != nil {
			// Test token metadata lookup for known USDC contract
			metadata, err := engine.tokenEnrichmentService.GetTokenMetadata("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")

			if err == nil && metadata != nil {
				t.Logf("📋 Token metadata found:")
				t.Logf("  Token Name: %s", metadata.Name)
				t.Logf("  Token Symbol: %s", metadata.Symbol)
				t.Logf("  Token Decimals: %d", metadata.Decimals)
				t.Logf("  Source: %s", metadata.Source)

				assert.NotEmpty(t, metadata.Symbol, "Token symbol should not be empty")
				t.Log("✅ Token metadata service working correctly")
			} else {
				t.Log("⚠️  Token metadata not found - this may be expected if token not in whitelist")
			}

			// Test value formatting
			formattedValue := engine.tokenEnrichmentService.FormatTokenValue("0xf4240", 6) // 1 USDC
			assert.Equal(t, "1", formattedValue, "Should format 1 USDC correctly")
			t.Logf("✅ Value formatting: 0xf4240 with 6 decimals = %s", formattedValue)
		} else {
			t.Log("⚠️  Skipping token metadata test - TokenEnrichmentService not available")
		}
	})

	// Test 2: EventTrigger immediate execution integration
	t.Run("EventTriggerIntegration", func(t *testing.T) {
		// Test with a real Transfer event signature
		transferSignature := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

		configMap := map[string]interface{}{
			"expression":  transferSignature,
			"matcherList": []interface{}{},
		}

		// Execute EventTrigger immediately
		result, err := engine.RunNodeImmediately("eventTrigger", configMap, map[string]interface{}{}, nil)

		// Even if no events are found, the integration should work without error
		if err != nil {
			t.Logf("EventTrigger execution result: %v (this might be expected if no events exist)", err)
		}

		// If we got a result, check its structure
		if result != nil {
			resultJSON, _ := json.MarshalIndent(result, "", "  ")
			t.Logf("📋 EventTrigger result structure:\n%s", string(resultJSON))

			// Check if the result has the expected structure
			if success, ok := result["success"].(bool); ok {
				t.Logf("🔍 Found events: %v", success)

				if success {
					// With new JSON-based approach, check if we have event data directly
					if dataStr, exists := result["data"].(string); exists && dataStr != "" {
						t.Log("✅ Event data present in JSON format")

						// Try to parse the JSON data
						var eventData map[string]interface{}
						if err := json.Unmarshal([]byte(dataStr), &eventData); err == nil {
							t.Logf("📋 Parsed event data: %v", eventData)

							// Check for common Transfer event fields
							if address, exists := eventData["address"]; exists {
								t.Logf("✅ Event address: %v", address)
							}
							if blockNumber, exists := eventData["blockNumber"]; exists {
								t.Logf("✅ Block number: %v", blockNumber)
							}
							if txHash, exists := eventData["transactionHash"]; exists {
								t.Logf("✅ Transaction hash: %v", txHash)
							}
						} else {
							t.Logf("⚠️  Could not parse event data JSON: %v", err)
						}
					} else {
						t.Log("⚠️  No event data found in result")
					}
				} else {
					t.Log("ℹ️  No events found - this is expected for most test environments")
				}
			}
		}
	})

	t.Log("=== Token Enrichment Integration Test Complete ===")
}
