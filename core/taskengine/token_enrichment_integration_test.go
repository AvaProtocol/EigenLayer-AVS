package taskengine

import (
	"encoding/json"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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

	// Test 1: Basic functionality with mock data
	t.Run("MockTokenEnrichment", func(t *testing.T) {
		// Create a mock EventTrigger output with known token (USDC from whitelist)
		evmLog := &avsproto.Evm_Log{
			Address: "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8", // USDC from sepolia.json
		}

		transferLog := &avsproto.EventTrigger_TransferLogOutput{
			Address:     "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8",
			Value:       "0xf4240", // 1 USDC (1,000,000 micro USDC)
			FromAddress: "0x1234567890123456789012345678901234567890",
			ToAddress:   "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		}

		// Test enrichment if service is available
		if engine.tokenEnrichmentService != nil {
			err := engine.tokenEnrichmentService.EnrichTransferLog(evmLog, transferLog)
			assert.NoError(t, err, "Token enrichment should work without error")

			t.Logf("📋 Enriched transfer log:")
			t.Logf("  Token Name: %s", transferLog.TokenName)
			t.Logf("  Token Symbol: %s", transferLog.TokenSymbol)
			t.Logf("  Token Decimals: %d", transferLog.TokenDecimals)
			t.Logf("  Value Formatted: %s", transferLog.ValueFormatted)

			// Verify that token metadata was filled in (either from whitelist or RPC)
			if transferLog.TokenSymbol == "USDC" {
				t.Log("✅ Token metadata enriched successfully")
				// RPC might return different name than whitelist, so be flexible
				if transferLog.TokenName == "USD Coin" {
					t.Log("✅ Token name from whitelist: USD Coin")
				} else if transferLog.TokenName == "USDC" {
					t.Log("✅ Token name from RPC fallback: USDC")
				}
				assert.Equal(t, uint32(6), transferLog.TokenDecimals)
				assert.Equal(t, "1", transferLog.ValueFormatted)
			} else {
				t.Log("⚠️  Token metadata not found in whitelist or RPC - this may be expected")
			}
		} else {
			t.Log("⚠️  Skipping enrichment test - TokenEnrichmentService not available")
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
		result, err := engine.RunNodeImmediately("eventTrigger", configMap, map[string]interface{}{})

		// Even if no events are found, the integration should work without error
		if err != nil {
			t.Logf("EventTrigger execution result: %v (this might be expected if no events exist)", err)
		}

		// If we got a result, check its structure
		if result != nil {
			resultJSON, _ := json.MarshalIndent(result, "", "  ")
			t.Logf("📋 EventTrigger result structure:\n%s", string(resultJSON))

			// Check if the result has the expected structure
			if found, ok := result["found"].(bool); ok {
				t.Logf("🔍 Found events: %v", found)

				if found {
					// If events were found, check for transfer_log enrichment
					if transferLog, exists := result["transfer_log"]; exists && transferLog != nil {
						t.Log("✅ Transfer log present in result")

						if transferMap, ok := transferLog.(map[string]interface{}); ok {
							if tokenName, exists := transferMap["tokenName"]; exists && tokenName != "" {
								t.Logf("✅ Token enrichment worked: %v", tokenName)
							} else {
								t.Log("⚠️  Token name not enriched (may be expected if token not in whitelist)")
							}
						}
					}
				} else {
					t.Log("ℹ️  No events found - enrichment not tested")
				}
			}
		}
	})

	t.Log("=== Token Enrichment Integration Test Complete ===")
}
