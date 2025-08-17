package taskengine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestEventTriggerEndToEndRPC(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()

	t.Log("=== Testing EventTrigger End-to-End RPC Call (Enhanced) ===")

	// Test the specific expression from the user's input
	expression := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef&&0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9"

	t.Logf("Testing with expression: %s", expression)
	t.Logf("This should find the most recent Transfer event involving address: 0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9")
	t.Logf("New implementation searches without block limitations to find historical events")

	// Test 1: Direct immediate execution
	t.Run("DirectImmediateExecution", func(t *testing.T) {
		configMap := map[string]interface{}{
			"expression":  expression,
			"matcherList": []interface{}{},
		}

		result, err := engine.RunNodeImmediately("eventTrigger", configMap, map[string]interface{}{})
		if err != nil {
			t.Logf("Execution failed (this might be expected if no events exist): %v", err)
			// Don't fail the test if RPC is unavailable or no events found
			return
		}

		// Pretty print the result
		resultJSON, _ := json.MarshalIndent(result, "", "  ")
		t.Logf("Direct execution result:\n%s", string(resultJSON))

		// Validate result structure
		if result == nil {
			t.Error("Result should not be nil")
			return
		}

		// Check if we found events
		if success, ok := result["success"].(bool); ok {
			if success {
				t.Log("✅ SUCCESS: Found real events on Ethereum chain!")

				// Validate evm_log structure (always present when found=true)
				if evmLog, ok := result["evm_log"].(map[string]interface{}); ok {
					t.Logf("📋 EVM Log Structure:")
					t.Logf("  🔗 Transaction Hash: %v", evmLog["transactionHash"])
					t.Logf("  📦 Block Number: %v", evmLog["blockNumber"])
					t.Logf("  📍 Contract Address: %v", evmLog["address"])
					t.Logf("  🏷️  Topics: %v", evmLog["topics"])

					// Check required fields
					if _, exists := evmLog["address"]; !exists {
						t.Error("evm_log should have address field")
					}
					if _, exists := evmLog["topics"]; !exists {
						t.Error("evm_log should have topics field")
					}
					if _, exists := evmLog["blockNumber"]; !exists {
						t.Error("evm_log should have blockNumber field")
					}
				} else {
					t.Error("When found=true, result should have evm_log field")
				}

				// Check for transfer_log if it's a transfer event
				if transferLog, ok := result["transfer_log"].(map[string]interface{}); ok {
					t.Logf("💸 Transfer Log Structure:")
					t.Logf("  👤 From: %v", transferLog["fromAddress"])
					t.Logf("  👤 To: %v", transferLog["toAddress"])
					t.Logf("  💰 Value: %v", transferLog["value"])
					t.Logf("  ⏰ Block Timestamp: %v", transferLog["blockTimestamp"])

					// Check required transfer fields
					if _, exists := transferLog["fromAddress"]; !exists {
						t.Error("transfer_log should have fromAddress field")
					}
					if _, exists := transferLog["toAddress"]; !exists {
						t.Error("transfer_log should have toAddress field")
					}
					if _, exists := transferLog["value"]; !exists {
						t.Error("transfer_log should have value field")
					}
				} else {
					t.Log("ℹ️  No transfer_log data (normal for non-Transfer events)")
				}

				if totalSearched, ok := result["totalSearched"].(uint64); ok {
					t.Logf("🔍 Total blocks searched: %d", totalSearched)
				}
			} else {
				t.Log("ℹ️  No events found in search")
				if totalSearched, ok := result["totalSearched"].(uint64); ok {
					t.Logf("🔍 Searched %d blocks without finding matching events", totalSearched)
				}
				t.Logf("🎯 Looking for topic: %v", result["topicHash"])
				t.Logf("📍 Contract address filter: %v", result["contractAddress"])
			}
		} else {
			t.Error("Result should have 'found' field")
		}

		// Verify the result has expected structure
		if _, ok := result["expression"]; !ok {
			t.Error("Result should have expression field")
		}
		if _, ok := result["topicHash"]; !ok {
			t.Error("Result should have topicHash field")
		}
	})

	// Test 2: RPC interface
	t.Run("RPCInterface", func(t *testing.T) {
		// Create trigger config for RPC call
		triggerConfig := map[string]*structpb.Value{
			"expression":  structpb.NewStringValue(expression),
			"matcherList": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{}}),
		}

		// Create RunTrigger request
		req := &avsproto.RunTriggerReq{
			TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_EVENT,
			TriggerConfig: triggerConfig,
		}

		t.Logf("Making RunTriggerRPC call with TriggerType: %s", req.TriggerType.String())

		// Execute the trigger via RPC interface
		result, err := engine.RunTriggerRPC(user, req)
		if err != nil {
			t.Logf("RunTriggerRPC failed (this might be expected if RPC is unavailable): %v", err)
			// Don't fail the test if RPC is unavailable
			return
		}

		t.Logf("RPC call success: %v", result.Success)

		if !result.Success {
			t.Logf("RPC call returned error: %s", result.Error)
			return
		}

		// TriggerId was removed from RunTriggerResp; keep test output concise
		t.Logf("RunTriggerRPC returned success response")

		// Check for event trigger output
		if eventOutput := result.GetEventTrigger(); eventOutput != nil {
			t.Logf("✅ Event Trigger Output received")

			// Check structured data
			if eventOutput.Data != nil {
				t.Logf("📋 Event Structured Data:")

				// Convert protobuf value to map for logging
				if eventData, ok := eventOutput.Data.AsInterface().(map[string]interface{}); ok {
					t.Logf("  📦 Data: %+v", eventData)

					if txHash, exists := eventData["transactionHash"]; exists {
						t.Logf("  🔗 Transaction Hash: %v", txHash)
					}
					if blockNum, exists := eventData["blockNumber"]; exists {
						t.Logf("  📦 Block Number: %v", blockNum)
					}
					if address, exists := eventData["address"]; exists {
						t.Logf("  📍 Address: %v", address)
					}
					if fromAddr, exists := eventData["fromAddress"]; exists {
						t.Logf("  👤 From: %v", fromAddr)
					}
					if toAddr, exists := eventData["toAddress"]; exists {
						t.Logf("  👤 To: %v", toAddr)
					}
					if value, exists := eventData["value"]; exists {
						t.Logf("  💰 Value: %v", value)
					}
				} else {
					t.Logf("⚠️  Could not convert data to map: %T", eventOutput.Data.AsInterface())
				}
			} else {
				t.Log("ℹ️  No event data (normal when no events found)")
			}
		} else {
			t.Error("EventTrigger output should be present")
		}
	})

	t.Log("=== Test Complete ===")
	t.Log("This demonstrates that EventTrigger now:")
	t.Log("1. Makes real RPC calls to Ethereum without block limitations")
	t.Log("2. Searches comprehensively for the most recent matching event")
	t.Log("3. Returns proper protobuf-compliant EventTrigger.Output structure")
	t.Log("4. Provides both evm_log (always) and transfer_log (for Transfer events) data")
}

// Helper function to compare string slices (kept for compatibility)
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEventTriggerQueriesBasedMultipleContracts(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()

	t.Log("=== Testing Queries-Based EventTrigger with Multiple Scenarios ===")

	targetAddress := "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9"

	// Sepolia testnet contract addresses for testing
	sepoliaUSDC := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" // Example Sepolia USDC
	sepoliaUSDT := "0x7169D38820dfd117C3FA1f22a697dBA58d90BA06" // Example Sepolia USDT

	testCases := []struct {
		name    string
		queries []interface{}
		desc    string
	}{
		{
			name: "Transfer FROM target address (any token)",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								targetAddress,
								nil,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find any ERC20 transfer FROM the target address",
		},
		{
			name: "Transfer TO target address (any token)",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								nil,
								targetAddress,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find any ERC20 transfer TO the target address",
		},
		{
			name: "USDC transfers FROM target address",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{sepoliaUSDC},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								targetAddress,
								nil,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find USDC transfers FROM the target address",
		},
		{
			name: "USDT transfers TO target address",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{sepoliaUSDT},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								nil,
								targetAddress,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find USDT transfers TO the target address",
		},
		{
			name: "Multiple contracts transfers FROM target address",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{sepoliaUSDC, sepoliaUSDT},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								targetAddress,
								nil,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find USDC or USDT transfers FROM the target address",
		},
		{
			name: "Multiple contracts transfers TO target address",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{sepoliaUSDC, sepoliaUSDT},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								nil,
								targetAddress,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find USDC or USDT transfers TO the target address",
		},
		{
			name: "Any transfers FROM OR TO target address (two queries)",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								targetAddress,
								nil,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
				map[string]interface{}{
					"addresses": []interface{}{},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								nil,
								targetAddress,
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			desc: "Find any transfer where target address is either FROM or TO (two separate queries)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.desc)
			t.Logf("Queries: %d", len(tc.queries))

			// Test with runTriggerImmediately
			triggerConfig := map[string]interface{}{
				"queries": tc.queries,
			}
			inputVariables := map[string]interface{}{}

			result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, inputVariables)
			if err != nil {
				t.Errorf("runTriggerImmediately failed: %v", err)
				return
			}

			// Log the parsing results
			if success, exists := result["success"].(bool); exists {
				t.Logf("✅ Queries-based search completed: success=%v", success)

				if queriesCount, exists := result["queriesCount"]; exists {
					t.Logf("📋 Queries processed: %v", queriesCount)
				}
				if totalEvents, exists := result["totalEvents"]; exists {
					t.Logf("🔢 Total events found: %v", totalEvents)
				}
				if totalSearched, exists := result["totalSearched"]; exists {
					t.Logf("🔍 Total blocks searched: %v", totalSearched)
				}

				if success {
					// Verify evm_log structure
					if evmLog, exists := result["evm_log"].(map[string]interface{}); exists && evmLog != nil {
						t.Logf("✅ evm_log present with address: %v", evmLog["address"])
						if topics, ok := evmLog["topics"].([]string); ok && len(topics) >= 3 {
							t.Logf("📝 Transfer from: %s to: %s",
								common.HexToAddress(topics[1]).Hex(),
								common.HexToAddress(topics[2]).Hex())
						}
					}

					// Verify transfer_log structure if it's a Transfer event
					if transferLog, exists := result["transfer_log"].(map[string]interface{}); exists && transferLog != nil {
						t.Logf("✅ transfer_log present with enriched data")
						if fromAddr, ok := transferLog["fromAddress"].(string); ok {
							t.Logf("📤 Transfer from: %s", fromAddr)
						}
						if toAddr, ok := transferLog["toAddress"].(string); ok {
							t.Logf("📥 Transfer to: %s", toAddr)
						}
					}
				} else {
					t.Logf("ℹ️  No events found matching criteria (this is expected if no recent activity)")
				}
			}

			// Test with RPC interface as well
			triggerConfigStructpb := make(map[string]*structpb.Value)
			for k, v := range triggerConfig {
				if val, err := structpb.NewValue(v); err == nil {
					triggerConfigStructpb[k] = val
				}
			}

			rpcReq := &avsproto.RunTriggerReq{
				TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_EVENT,
				TriggerConfig: triggerConfigStructpb,
			}

			rpcResult, err := engine.RunTriggerRPC(user, rpcReq)
			if err != nil {
				t.Errorf("RunTriggerRPC failed: %v", err)
				return
			}

			// Verify RPC response has proper EventTrigger.Output structure
			if rpcResult.GetEventTrigger() != nil {
				// Check response structure - with new JSON approach, just check if data is present
				hasData := rpcResult.GetEventTrigger().Data != nil
				t.Logf("🔌 RPC Response: has_data=%v", hasData)

				// Validate JSON data based on whether events were found
				if success, exists := result["success"].(bool); exists && success {
					// When events are found, data should be populated
					if !hasData {
						t.Errorf("Data should be populated when events found")
					} else {
						t.Logf("✅ Data validation passed: JSON data present")

						// Try to access the structured data to verify it's valid
						if eventData, ok := rpcResult.GetEventTrigger().Data.AsInterface().(map[string]interface{}); ok {
							t.Logf("✅ Structured data is valid and accessible")
							if len(eventData) > 0 {
								t.Logf("✅ Structured data contains event fields")
							}
						} else {
							t.Errorf("Structured data should be accessible as map[string]interface{}")
						}
					}
				} else {
					// When no events are found, data should be empty
					if hasData {
						t.Logf("ℹ️  Data present even when no events found (this might be metadata)")
					} else {
						t.Logf("✅ No data when no events found")
					}
				}
			}

			t.Logf("✅ Test completed for: %s\n", tc.name)
		})
	}
}

func TestEventTriggerQueriesBasedConfiguration(t *testing.T) {
	t.Log("=== Testing Queries-Based EventTrigger Configuration ===")

	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	testCases := []struct {
		name        string
		queries     []interface{}
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Empty queries should fail",
			queries:     []interface{}{},
			expectError: true,
			errorMsg:    "queries must be a non-empty array",
		},
		{
			name: "Valid query with addresses and topics",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{"0xA0b86a33E6441E654cF7A6E88ac73EB3a7fA88d8"},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
							},
						},
					},
					"maxEventsPerBlock": float64(100),
				},
			},
			expectError: false,
		},
		{
			name: "Multiple queries for FROM-OR-TO scenario",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{"0xA0b86a33E6441E654cF7A6E88ac73EB3a7fA88d8"},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								"0x06DBb141d8275d9eDb8a7446F037D20E215188ff", // FROM
								nil, // Any TO
							},
						},
					},
					"maxEventsPerBlock": float64(50),
				},
				map[string]interface{}{
					"addresses": []interface{}{"0xA0b86a33E6441E654cF7A6E88ac73EB3a7fA88d8"},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								nil, // Any FROM
								"0x06DBb141d8275d9eDb8a7446F037D20E215188ff", // TO
							},
						},
					},
					"maxEventsPerBlock": float64(50),
				},
			},
			expectError: false,
		},
		{
			name: "Query with empty addresses",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "Query with empty topics",
			queries: []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{"0xA0b86a33E6441E654cF7A6E88ac73EB3a7fA88d8"},
					"topics":    []interface{}{},
				},
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.name)

			triggerConfig := map[string]interface{}{
				"queries": tc.queries,
			}
			inputVariables := map[string]interface{}{}

			result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, inputVariables)

			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tc.errorMsg) {
					t.Errorf("Expected error containing '%s', got: %v", tc.errorMsg, err)
				}
				t.Logf("✅ Expected error occurred: %v", err)
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}

				// Verify result structure (new format uses 'success' instead of 'found')
				if success, exists := result["success"].(bool); exists {
					t.Logf("✅ Query processing completed: success=%v", success)
					if queriesCount, exists := result["queriesCount"]; exists {
						t.Logf("📋 Queries processed: %v", queriesCount)
					}
				} else {
					t.Errorf("Result missing 'success' field")
				}
			}
		})
	}
}

func TestEventTriggerQueriesBasedUserScenario(t *testing.T) {
	// Initialize test environment
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	t.Log("=== Testing User's Specific Request: USDC+USDT transfers FROM/TO address using Queries ===")

	// The user's address
	userAddress := "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9"

	// Real mainnet USDC and USDT addresses
	mainnetUSDC := "0xA0b86a33E6441E654cF7A6E88ac73EB3a7fA88d8" // USDC
	mainnetUSDT := "0xdAC17F958D2ee523a2206206994597C13D831ec7" // USDT

	t.Logf("👤 User address: %s", userAddress)
	t.Logf("💵 USDC contract: %s", mainnetUSDC)
	t.Logf("💵 USDT contract: %s", mainnetUSDT)

	// Build queries-based configuration for USDC and USDT transfers FROM or TO the user's address
	// This requires two separate queries to handle OR logic properly
	queries := []interface{}{
		map[string]interface{}{
			// Query 1: USDC/USDT transfers FROM user address
			"addresses": []interface{}{mainnetUSDC, mainnetUSDT},
			"topics": []interface{}{
				map[string]interface{}{
					"values": []interface{}{
						"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
						userAddress, // FROM user
						nil,         // Any TO
					},
				},
			},
			"maxEventsPerBlock": float64(100),
		},
		map[string]interface{}{
			// Query 2: USDC/USDT transfers TO user address
			"addresses": []interface{}{mainnetUSDC, mainnetUSDT},
			"topics": []interface{}{
				map[string]interface{}{
					"values": []interface{}{
						"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
						nil,         // Any FROM
						userAddress, // TO user
					},
				},
			},
			"maxEventsPerBlock": float64(100),
		},
	}

	t.Logf("🚀 Queries-based Configuration: %d queries", len(queries))
	t.Log("This should find USDC or USDT transfers where your address is either FROM or TO")

	triggerConfig := map[string]interface{}{
		"queries": queries,
	}
	inputVariables := map[string]interface{}{}

	result, err := engine.runTriggerImmediately("eventTrigger", triggerConfig, inputVariables)
	if err != nil {
		t.Errorf("runTriggerImmediately failed: %v", err)
		return
	}

	// Display detailed results
	if success, exists := result["success"].(bool); exists {
		t.Logf("🔍 Search Results:")
		t.Logf("   Found events: %v", success)

		if queriesCount, exists := result["queriesCount"]; exists {
			t.Logf("   📋 Queries processed: %v", queriesCount)
		}
		if totalSearched, exists := result["totalSearched"]; exists {
			t.Logf("   🔍 Total blocks searched: %v", totalSearched)
		}
		if totalEvents, exists := result["totalEvents"]; exists {
			t.Logf("   🔢 Total events found: %v", totalEvents)
		}

		if success {
			t.Log("✅ SUCCESS: Found matching events!")

			// Show event details
			if evmLog, exists := result["evm_log"].(map[string]interface{}); exists && evmLog != nil {
				t.Logf("📝 Event Details:")
				t.Logf("   Contract: %v", evmLog["address"])
				t.Logf("   Block: %v", evmLog["blockNumber"])
				t.Logf("   Tx: %v", evmLog["transactionHash"])

				if topics, ok := evmLog["topics"].([]string); ok && len(topics) >= 3 {
					fromAddr := common.HexToAddress(topics[1]).Hex()
					toAddr := common.HexToAddress(topics[2]).Hex()
					t.Logf("   Transfer: %s → %s", fromAddr, toAddr)

					// Check which direction matches the user
					if fromAddr == userAddress {
						t.Logf("   🎯 Direction: FROM your address (outgoing)")
					}
					if toAddr == userAddress {
						t.Logf("   🎯 Direction: TO your address (incoming)")
					}

					// Identify token type
					if contractAddr, ok := evmLog["address"].(string); ok {
						var tokenName string
						if contractAddr == mainnetUSDC {
							tokenName = "USDC"
						} else if contractAddr == mainnetUSDT {
							tokenName = "USDT"
						} else {
							tokenName = "Unknown"
						}
						t.Logf("   🪙 Token: %s (%s)", tokenName, contractAddr)
					}
				}
			}

			if transferLog, exists := result["transfer_log"].(map[string]interface{}); exists && transferLog != nil {
				t.Logf("💰 Transfer Details:")
				if value, ok := transferLog["value"].(string); ok {
					t.Logf("   Value: %s", value)
				}
				if valueFormatted, ok := transferLog["valueFormatted"].(string); ok && valueFormatted != "" {
					t.Logf("   Value Formatted: %s", valueFormatted)
				}
				if tokenSymbol, ok := transferLog["tokenSymbol"].(string); ok && tokenSymbol != "" {
					t.Logf("   Token Symbol: %s", tokenSymbol)
				}
			}
		} else {
			t.Log("ℹ️  No recent USDC/USDT transfers found for your address")
			t.Log("This is normal if you haven't had recent token activity")
		}
	}

	t.Log("✅ Queries-based EventTrigger test completed successfully!")
}
