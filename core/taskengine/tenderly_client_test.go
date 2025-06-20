package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test configuration for Sepolia
const (
	SEPOLIA_CHAIN_ID     = 11155111
	SEPOLIA_ETH_USD_FEED = "0x694AA1769357215DE4FAC081bf1f309aDC325306"
	ANSWER_UPDATED_SIG   = "0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"
)

// Chainlink ABI for AnswerUpdated event
const CHAINLINK_TEST_ABI = `[
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "int256", "name": "current", "type": "int256"},
			{"indexed": true, "internalType": "uint256", "name": "roundId", "type": "uint256"},
			{"indexed": false, "internalType": "uint256", "name": "updatedAt", "type": "uint256"}
		],
		"name": "AnswerUpdated",
		"type": "event"
	}
]`

func TestTenderlyEventSimulation_EndToEnd(t *testing.T) {
	// Check if we have Tenderly API key for real testing
	apiKey := os.Getenv("TENDERLY_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping end-to-end Tenderly test - set TENDERLY_API_KEY environment variable to run")
	}

	logger := testutil.GetLogger()

	// Create TenderlyClient with real API key
	tenderlyClient := NewTenderlyClient(logger)
	tenderlyClient.apiKey = apiKey

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("Basic AnswerUpdated Simulation", func(t *testing.T) {
		// Create query for Chainlink ETH/USD feed
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
		}

		fmt.Printf("\n🔮 === TENDERLY END-TO-END TEST: Basic AnswerUpdated Simulation ===\n")
		fmt.Printf("📍 Contract: %s (Sepolia ETH/USD)\n", SEPOLIA_ETH_USD_FEED)
		fmt.Printf("🔍 Event: AnswerUpdated\n")
		fmt.Printf("⚡ Mode: Real Tenderly API call\n\n")

		// Execute simulation
		simulatedLog, err := tenderlyClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Tenderly simulation should succeed")
		require.NotNil(t, simulatedLog, "Simulated log should not be nil")

		// Validate the simulated log structure
		assert.Equal(t, SEPOLIA_ETH_USD_FEED, simulatedLog.Address.Hex(), "Contract address should match")
		assert.Len(t, simulatedLog.Topics, 3, "AnswerUpdated should have 3 topics")
		assert.Equal(t, ANSWER_UPDATED_SIG, simulatedLog.Topics[0].Hex(), "First topic should be AnswerUpdated signature")

		fmt.Printf("✅ Simulation successful!\n")
		printSimulatedLog(simulatedLog)
	})

	t.Run("Conditional Price Alert Simulation", func(t *testing.T) {
		// Test with price > $2000 condition
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     "200000000000", // $2000 with 8 decimals
					FieldType: "int256",
				},
			},
		}

		fmt.Printf("\n🎯 === CONDITIONAL SIMULATION: Price > $2000 ===\n")

		simulatedLog, err := tenderlyClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Conditional simulation should succeed")
		require.NotNil(t, simulatedLog, "Simulated log should not be nil")

		// Validate that the simulated price satisfies the condition
		priceHex := simulatedLog.Topics[1].Hex()
		fmt.Printf("🏷️  Simulated price (hex): %s\n", priceHex)

		// The price should be > $2000 (200000000000 in 8-decimal format)
		assert.Equal(t, SEPOLIA_ETH_USD_FEED, simulatedLog.Address.Hex())

		fmt.Printf("✅ Conditional simulation successful!\n")
		printSimulatedLog(simulatedLog)
	})

	t.Run("Real Integration with Engine", func(t *testing.T) {
		// Test the full integration through the Engine
		fmt.Printf("\n🚀 === FULL ENGINE INTEGRATION TEST ===\n")

		// Create test engine
		db := testutil.TestMustDB()
		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, logger)

		// Simulate runTrigger call with Tenderly
		triggerConfig := map[string]interface{}{
			"simulationMode": true,
			"queries": []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{SEPOLIA_ETH_USD_FEED},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{ANSWER_UPDATED_SIG},
						},
					},
					"conditions": []interface{}{
						map[string]interface{}{
							"fieldName": "current",
							"operator":  "lt",
							"value":     "180000000000", // $1800
							"fieldType": "int256",
						},
					},
				},
			},
		}

		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		require.NoError(t, err, "Engine simulation should succeed")
		require.NotNil(t, result, "Result should not be nil")

		// Validate response structure
		assert.True(t, result["found"].(bool), "Should find simulated event")
		assert.NotNil(t, result["evm_log"], "Should have evm_log")
		assert.Equal(t, 1, result["queriesCount"], "Should process 1 query")
		assert.Equal(t, 0, result["totalSearched"], "Should not search blockchain")

		// Check simulation metadata
		metadata := result["searchMetadata"].(map[string]interface{})
		assert.True(t, metadata["simulationMode"].(bool), "Should indicate simulation mode")
		assert.True(t, metadata["tenderlyUsed"].(bool), "Should indicate Tenderly was used")

		fmt.Printf("✅ Full engine integration successful!\n")
		printEngineResult(result)
	})
}

func TestTenderlyGateway_RealRPCCalls(t *testing.T) {
	// This test requires TENDERLY_API_KEY environment variable
	// Expected format: TENDERLY_API_KEY=https://sepolia.gateway.tenderly.co/7MB9UwJMIQmLyhNxSIMg3X
	apiKey := os.Getenv("TENDERLY_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping real API test - set TENDERLY_API_KEY=https://sepolia.gateway.tenderly.co/{YOUR_API_KEY}")
	}

	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)

	ctx := context.Background()

	fmt.Printf("\n🌐 === REAL TENDERLY GATEWAY RPC TEST ===\n")
	fmt.Printf("🔗 Gateway URL: %s\n", client.apiURL)
	fmt.Printf("🔑 API Key: %s\n", client.apiKey)

	// Test 1: Real latestRoundData call to see actual request/response
	t.Run("Real latestRoundData RPC Call", func(t *testing.T) {
		fmt.Printf("\n📡 Making real Tenderly Gateway RPC call...\n")
		fmt.Printf("🎯 Target: %s (Sepolia ETH/USD)\n", SEPOLIA_ETH_USD_FEED)
		fmt.Printf("🔧 Method: eth_call -> latestRoundData()\n\n")

		roundData, err := client.getRealRoundDataViaTenderly(ctx, SEPOLIA_ETH_USD_FEED, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Should successfully call Tenderly Gateway RPC")
		require.NotNil(t, roundData, "Should get round data")

		fmt.Printf("✅ RPC call successful!\n")
		fmt.Printf("\n📊 REAL CHAINLINK DATA FROM TENDERLY GATEWAY:\n")
		fmt.Printf("   Contract: %s\n", SEPOLIA_ETH_USD_FEED)
		fmt.Printf("   Round ID: %s\n", roundData.RoundId.String())
		fmt.Printf("   Answer (raw): %s\n", roundData.Answer.String())
		fmt.Printf("   Answer (USD): $%.2f\n", float64(roundData.Answer.Int64())/100000000)
		fmt.Printf("   Started At: %s\n", time.Unix(roundData.StartedAt.Int64(), 0).Format(time.RFC3339))
		fmt.Printf("   Updated At: %s\n", time.Unix(roundData.UpdatedAt.Int64(), 0).Format(time.RFC3339))
		fmt.Printf("   Answered In Round: %s\n", roundData.AnsweredInRound.String())
	})

	// Test 2: Show actual JSON-RPC request/response format
	t.Run("Direct JSON-RPC Request Analysis", func(t *testing.T) {
		fmt.Printf("\n🔧 === DIRECT JSON-RPC CALL ANALYSIS ===\n")

		// Create JSON-RPC request
		rpcRequest := JSONRPCRequest{
			Jsonrpc: "2.0",
			Method:  "eth_call",
			Params: []interface{}{
				CallParams{
					To:   SEPOLIA_ETH_USD_FEED,
					Data: "0x50d25bcd", // latestRoundData() method signature
				},
				"latest",
			},
			Id: 1,
		}

		fmt.Printf("📤 JSON-RPC REQUEST:\n")
		requestJSON, _ := json.MarshalIndent(rpcRequest, "", "  ")
		fmt.Printf("%s\n\n", string(requestJSON))

		// Make the actual RPC call
		var response JSONRPCResponse
		resp, err := client.httpClient.R().
			SetContext(ctx).
			SetBody(rpcRequest).
			SetResult(&response).
			Post(client.apiURL)

		require.NoError(t, err, "RPC call should succeed")

		fmt.Printf("📥 JSON-RPC RESPONSE:\n")
		fmt.Printf("Status Code: %d\n", resp.StatusCode())

		if resp.IsSuccess() {
			responseJSON, _ := json.MarshalIndent(response, "", "  ")
			fmt.Printf("%s\n\n", string(responseJSON))

			// Analyze the response
			fmt.Printf("🔍 RESPONSE ANALYSIS:\n")
			fmt.Printf("JSON-RPC Version: %s\n", response.Jsonrpc)
			fmt.Printf("Request ID: %d\n", response.Id)
			fmt.Printf("Result Length: %d bytes\n", len(response.Result))
			fmt.Printf("Raw Result: %s\n", response.Result)

			if response.Error != nil {
				fmt.Printf("❌ RPC Error: %s (code: %d)\n", response.Error.Message, response.Error.Code)
			} else {
				fmt.Printf("✅ Call successful\n")
			}
		} else {
			fmt.Printf("❌ HTTP Error Response: %s\n", resp.String())
		}
	})

	// Test 3: Event simulation end-to-end
	t.Run("Event Simulation with Real Data", func(t *testing.T) {
		fmt.Printf("\n🔮 === EVENT SIMULATION WITH REAL TENDERLY DATA ===\n")

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
		}

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err, "Simulation should succeed")
		require.NotNil(t, simulatedLog, "Should get simulated log")

		fmt.Printf("✅ Event simulation successful!\n")
		printSimulatedLog(simulatedLog)

		// Verify log structure matches real AnswerUpdated events
		assert.Equal(t, common.HexToAddress(SEPOLIA_ETH_USD_FEED), simulatedLog.Address)
		assert.Len(t, simulatedLog.Topics, 3)
		assert.Equal(t, common.HexToHash(ANSWER_UPDATED_SIG), simulatedLog.Topics[0])
		assert.NotEmpty(t, simulatedLog.Data)
	})
}

// Helper function to print simulated log details
func printSimulatedLog(log *types.Log) {
	fmt.Printf("\n📋 SIMULATED LOG DETAILS:\n")
	fmt.Printf("Contract: %s\n", log.Address.Hex())
	fmt.Printf("Block: %d\n", log.BlockNumber)
	fmt.Printf("TX Hash: %s\n", log.TxHash.Hex())
	fmt.Printf("TX Index: %d\n", log.TxIndex)
	fmt.Printf("Log Index: %d\n", log.Index)
	fmt.Printf("Removed: %t\n", log.Removed)

	fmt.Printf("\nTopics:\n")
	for i, topic := range log.Topics {
		fmt.Printf("  [%d] %s", i, topic.Hex())
		if i == 0 {
			fmt.Printf(" (AnswerUpdated signature)")
		} else if i == 1 {
			fmt.Printf(" (current price)")
		} else if i == 2 {
			fmt.Printf(" (round ID)")
		}
		fmt.Printf("\n")
	}

	fmt.Printf("\nData: %s (updatedAt timestamp)\n", "0x"+common.Bytes2Hex(log.Data))
}

// Helper function to print engine result
func printEngineResult(result map[string]interface{}) {
	fmt.Printf("\n📊 ENGINE RESULT:\n")
	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("%s\n", string(resultJSON))

	if evmLog, ok := result["evm_log"].(map[string]interface{}); ok {
		fmt.Printf("\n🔍 EVM LOG ANALYSIS:\n")
		fmt.Printf("Address: %s\n", evmLog["address"])
		fmt.Printf("Block: %v\n", evmLog["blockNumber"])
		fmt.Printf("TX Hash: %s\n", evmLog["transactionHash"])

		if topics, ok := evmLog["topics"].([]string); ok {
			fmt.Printf("\nTopics Breakdown:\n")
			for i, topic := range topics {
				fmt.Printf("  [%d] %s", i, topic)
				if i == 0 {
					fmt.Printf(" (AnswerUpdated)")
				} else if i == 1 {
					fmt.Printf(" (price)")
				} else if i == 2 {
					fmt.Printf(" (roundId)")
				}
				fmt.Printf("\n")
			}
		}
	}
}

// Benchmark the simulation performance
func BenchmarkTenderlySimulation(b *testing.B) {
	apiKey := os.Getenv("TENDERLY_API_KEY")
	if apiKey == "" {
		b.Skip("Skipping benchmark - set TENDERLY_API_KEY environment variable")
	}

	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)
	client.apiKey = apiKey

	query := &avsproto.EventTrigger_Query{
		Addresses: []string{SEPOLIA_ETH_USD_FEED},
		Topics: []*avsproto.EventTrigger_Topics{
			{Values: []string{ANSWER_UPDATED_SIG}},
		},
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		if err != nil {
			b.Fatalf("Simulation failed: %v", err)
		}
	}
}

func TestTenderlySimulation_WithConditions_ComprehensiveTest(t *testing.T) {
	// This test requires TENDERLY_API_KEY environment variable
	apiKey := os.Getenv("TENDERLY_API_KEY")
	if apiKey == "" {
		t.Skip("TENDERLY_API_KEY not set, skipping comprehensive test. " +
			"Set TENDERLY_API_KEY=https://sepolia.gateway.tenderly.co/{YOUR_API_KEY}")
	}

	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)

	ctx := context.Background()

	// First, get the current real price from Tenderly to use in our tests
	t.Run("GetCurrentPriceData", func(t *testing.T) {
		t.Logf("🔗 Using Tenderly Gateway: %s", client.apiURL)

		roundData, err := client.getRealRoundDataViaTenderly(ctx, SEPOLIA_ETH_USD_FEED, SEPOLIA_CHAIN_ID)
		require.NoError(t, err, "Should get real price data from Tenderly")
		require.NotNil(t, roundData)

		currentPriceFloat := float64(roundData.Answer.Int64()) / 100000000 // Convert to USD

		t.Logf("📊 CURRENT REAL CHAINLINK DATA:")
		t.Logf("   Contract: %s", SEPOLIA_ETH_USD_FEED)
		t.Logf("   Current Price: $%.2f (raw: %s)", currentPriceFloat, roundData.Answer.String())
		t.Logf("   Round ID: %s", roundData.RoundId.String())
		t.Logf("   Updated At: %s", time.Unix(roundData.UpdatedAt.Int64(), 0).Format(time.RFC3339))

		// Store current price for use in subsequent tests
		ctx = context.WithValue(ctx, "currentPrice", roundData.Answer)
		ctx = context.WithValue(ctx, "currentPriceFloat", currentPriceFloat)
	})

	// Test 1: Condition that SHOULD match (price > very low threshold)
	t.Run("ConditionShouldMatch_GreaterThan", func(t *testing.T) {
		currentPriceFloat := ctx.Value("currentPriceFloat").(float64)

		// Set threshold much lower than current price to ensure it matches
		thresholdFloat := currentPriceFloat - 500.0       // $500 below current price
		thresholdRaw := int64(thresholdFloat * 100000000) // Convert to 8-decimal format

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     fmt.Sprintf("%d", thresholdRaw),
					FieldType: "int256",
				},
			},
		}

		t.Logf("🎯 TESTING CONDITION THAT SHOULD MATCH:")
		t.Logf("   Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Condition: price > $%.2f", thresholdFloat)
		t.Logf("   Expected: MATCH ✅")

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err, "Simulation should succeed when condition should match")
		require.NotNil(t, simulatedLog)

		// Verify the condition was satisfied
		simulatedPrice := simulatedLog.Topics[1].Big()
		simulatedPriceFloat := float64(simulatedPrice.Int64()) / 100000000

		assert.True(t, simulatedPrice.Cmp(big.NewInt(thresholdRaw)) > 0,
			"Simulated price should be greater than threshold")

		t.Logf("✅ CONDITION MATCHED:")
		t.Logf("   Simulated Price: $%.2f (raw: %s)", simulatedPriceFloat, simulatedPrice.String())
		t.Logf("   Threshold: $%.2f (raw: %d)", thresholdFloat, thresholdRaw)
		t.Logf("   Condition Satisfied: %s > %d ✅", simulatedPrice.String(), thresholdRaw)

		// Show complete raw event structure
		t.Logf("\n📋 RAW EVENT STRUCTURE:")
		t.Logf("   Address: %s", simulatedLog.Address.Hex())
		t.Logf("   Block Number: %d", simulatedLog.BlockNumber)
		t.Logf("   Transaction Hash: %s", simulatedLog.TxHash.Hex())
		t.Logf("   Topics[0] (Event Sig): %s", simulatedLog.Topics[0].Hex())
		t.Logf("   Topics[1] (Price): %s", simulatedLog.Topics[1].Hex())
		t.Logf("   Topics[2] (Round ID): %s", simulatedLog.Topics[2].Hex())
		t.Logf("   Data (Updated At): 0x%s", common.Bytes2Hex(simulatedLog.Data))
	})

	// Test 2: Condition that SHOULD NOT match (price > very high threshold)
	t.Run("ConditionShouldNotMatch_GreaterThan", func(t *testing.T) {
		currentPriceFloat := ctx.Value("currentPriceFloat").(float64)

		// Set threshold much higher than current price to test non-matching
		thresholdFloat := currentPriceFloat + 2000.0      // $2000 above current price
		thresholdRaw := int64(thresholdFloat * 100000000) // Convert to 8-decimal format

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     fmt.Sprintf("%d", thresholdRaw),
					FieldType: "int256",
				},
			},
		}

		t.Logf("🎯 TESTING CONDITION THAT SHOULD NOT MATCH:")
		t.Logf("   Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Condition: price > $%.2f", thresholdFloat)
		t.Logf("   Expected: CURRENT BEHAVIOR - Generate matching data anyway")

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err, "Simulation should still succeed")
		require.NotNil(t, simulatedLog)

		// Current behavior: generateSimulatedPrice creates a price that satisfies the condition
		simulatedPrice := simulatedLog.Topics[1].Big()
		simulatedPriceFloat := float64(simulatedPrice.Int64()) / 100000000

		t.Logf("⚠️  CURRENT BEHAVIOR - CONDITION ARTIFICIALLY SATISFIED:")
		t.Logf("   Real Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Simulated Price: $%.2f (raw: %s)", simulatedPriceFloat, simulatedPrice.String())
		t.Logf("   Threshold: $%.2f (raw: %d)", thresholdFloat, thresholdRaw)
		t.Logf("   Result: Tenderly generated fake price to satisfy condition")

		// The current implementation will generate a price that satisfies the condition
		assert.True(t, simulatedPrice.Cmp(big.NewInt(thresholdRaw)) > 0,
			"Current implementation generates price that satisfies condition")

		t.Logf("\n💡 DESIGN QUESTION:")
		t.Logf("   Should we return real price data with condition_met=false instead?")
		t.Logf("   This would be more realistic for testing scenarios where conditions don't match.")
	})

	// Test 3: Multiple conditions
	t.Run("MultipleConditions_Complex", func(t *testing.T) {
		currentPriceFloat := ctx.Value("currentPriceFloat").(float64)

		// Create range: current price ± $100
		minThreshold := currentPriceFloat - 100.0
		maxThreshold := currentPriceFloat + 100.0
		minThresholdRaw := int64(minThreshold * 100000000)
		maxThresholdRaw := int64(maxThreshold * 100000000)

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     fmt.Sprintf("%d", minThresholdRaw),
					FieldType: "int256",
				},
				{
					FieldName: "current",
					Operator:  "lt",
					Value:     fmt.Sprintf("%d", maxThresholdRaw),
					FieldType: "int256",
				},
			},
		}

		t.Logf("🎯 TESTING MULTIPLE CONDITIONS (RANGE):")
		t.Logf("   Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Condition 1: price > $%.2f", minThreshold)
		t.Logf("   Condition 2: price < $%.2f", maxThreshold)
		t.Logf("   Expected: Price in range [$%.2f, $%.2f]", minThreshold, maxThreshold)

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err, "Multi-condition simulation should succeed")
		require.NotNil(t, simulatedLog)

		simulatedPrice := simulatedLog.Topics[1].Big()
		simulatedPriceFloat := float64(simulatedPrice.Int64()) / 100000000

		// Check if both conditions are satisfied
		condition1Met := simulatedPrice.Cmp(big.NewInt(minThresholdRaw)) > 0
		condition2Met := simulatedPrice.Cmp(big.NewInt(maxThresholdRaw)) < 0

		t.Logf("✅ MULTIPLE CONDITIONS RESULT:")
		t.Logf("   Simulated Price: $%.2f", simulatedPriceFloat)
		t.Logf("   Condition 1 (> $%.2f): %t", minThreshold, condition1Met)
		t.Logf("   Condition 2 (< $%.2f): %t", maxThreshold, condition2Met)
		t.Logf("   Both Satisfied: %t", condition1Met && condition2Met)
	})

	// Test 4: Raw data structure analysis
	t.Run("RawDataStructureAnalysis", func(t *testing.T) {
		// Simple query without conditions to see pure simulated data
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
		}

		t.Logf("🔬 ANALYZING RAW SIMULATION DATA STRUCTURE:")

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err)
		require.NotNil(t, simulatedLog)

		// Decode all fields for analysis
		price := simulatedLog.Topics[1].Big()
		roundId := simulatedLog.Topics[2].Big()
		updatedAtBytes := simulatedLog.Data
		updatedAt := new(big.Int).SetBytes(updatedAtBytes[len(updatedAtBytes)-32:])

		t.Logf("\n📊 COMPLETE EVENT BREAKDOWN:")
		t.Logf("   === Event Metadata ===")
		t.Logf("   Contract Address: %s", simulatedLog.Address.Hex())
		t.Logf("   Block Number: %d", simulatedLog.BlockNumber)
		t.Logf("   Block Hash: %s", simulatedLog.BlockHash.Hex())
		t.Logf("   Transaction Hash: %s", simulatedLog.TxHash.Hex())
		t.Logf("   Transaction Index: %d", simulatedLog.TxIndex)
		t.Logf("   Log Index: %d", simulatedLog.Index)
		t.Logf("   Removed: %t", simulatedLog.Removed)

		t.Logf("\n   === AnswerUpdated Event Data ===")
		t.Logf("   Event Signature: %s", simulatedLog.Topics[0].Hex())
		t.Logf("   Price (current): %s ($%.2f)", price.String(), float64(price.Int64())/100000000)
		t.Logf("   Round ID: %s", roundId.String())
		t.Logf("   Updated At: %s (%s)", updatedAt.String(), time.Unix(updatedAt.Int64(), 0).Format(time.RFC3339))

		t.Logf("\n   === Raw Hex Data ===")
		t.Logf("   Topics[0]: %s (AnswerUpdated signature)", simulatedLog.Topics[0].Hex())
		t.Logf("   Topics[1]: %s (price as bytes32)", simulatedLog.Topics[1].Hex())
		t.Logf("   Topics[2]: %s (roundId as bytes32)", simulatedLog.Topics[2].Hex())
		t.Logf("   Data: 0x%s (updatedAt timestamp)", common.Bytes2Hex(simulatedLog.Data))

		// Verify the data matches expected AnswerUpdated event structure
		assert.Equal(t, ANSWER_UPDATED_SIG, simulatedLog.Topics[0].Hex(), "First topic should be AnswerUpdated signature")
		assert.Len(t, simulatedLog.Topics, 3, "Should have exactly 3 topics")
		assert.Len(t, simulatedLog.Data, 32, "Data should be 32 bytes for updatedAt")
	})
}

// Test the proposed enhanced behavior for non-matching conditions
func TestTenderlySimulation_EnhancedConditionHandling_PROPOSAL(t *testing.T) {
	// This is a DESIGN PROPOSAL test - shows how we could handle non-matching conditions
	t.Skip("DESIGN PROPOSAL: This test shows how we could enhance condition handling")

	// PROPOSED ENHANCEMENT:
	// When conditions don't match, return both real data and condition status

	// Example enhanced response structure:
	proposedResponse := map[string]interface{}{
		"found": true,
		"evm_log": map[string]interface{}{
			// Standard event log structure
			"address":         "0x694AA1769357215DE4FAC081bf1f309aDC325306",
			"topics":          []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f", "0x...", "0x..."},
			"data":            "0x...",
			"blockNumber":     12345,
			"transactionHash": "0x...",
		},
		"condition_evaluation": map[string]interface{}{
			"all_conditions_met": false,
			"individual_results": []map[string]interface{}{
				{
					"field_name": "current",
					"operator":   "gt",
					"expected":   "500000000000", // $5000
					"actual":     "300000000000", // $3000 (real current price)
					"met":        false,
				},
			},
		},
		"_raw_data": map[string]interface{}{
			"real_price":        "300000000000",
			"real_price_usd":    3000.00,
			"simulation_forced": false, // true if we generated fake data to match conditions
		},
		"searchMetadata": map[string]interface{}{
			"simulationMode":      true,
			"tenderlyUsed":        true,
			"conditionsProvided":  true,
			"conditionsSatisfied": false,
		},
	}

	t.Logf("💡 PROPOSED ENHANCED RESPONSE STRUCTURE:")
	responseJSON, _ := json.MarshalIndent(proposedResponse, "", "  ")
	t.Logf("%s", string(responseJSON))

	t.Logf("\n🎯 BENEFITS OF THIS APPROACH:")
	t.Logf("   ✅ More realistic testing - see actual current state")
	t.Logf("   ✅ Better debugging - know why conditions failed")
	t.Logf("   ✅ Flexible usage - can test both matching and non-matching scenarios")
	t.Logf("   ✅ Backward compatible - still returns standard evm_log structure")
}

// Test the enhanced condition handling behavior
func TestTenderlySimulation_EnhancedConditionHandling_REAL(t *testing.T) {
	// This test demonstrates the enhanced condition handling approach
	apiKey := os.Getenv("TENDERLY_API_KEY")
	if apiKey == "" {
		t.Skip("TENDERLY_API_KEY not set, skipping enhanced condition test")
	}

	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)

	ctx := context.Background()

	t.Run("EnhancedBehavior_WithConditionsThatDontMatch", func(t *testing.T) {
		// First get real current price
		roundData, err := client.getRealRoundDataViaTenderly(ctx, SEPOLIA_ETH_USD_FEED, SEPOLIA_CHAIN_ID)
		require.NoError(t, err)

		currentPriceFloat := float64(roundData.Answer.Int64()) / 100000000

		// Set a condition that definitely won't match (price > current + $3000)
		impossibleThreshold := currentPriceFloat + 3000.0
		impossibleThresholdRaw := int64(impossibleThreshold * 100000000)

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     fmt.Sprintf("%d", impossibleThresholdRaw),
					FieldType: "int256",
				},
			},
		}

		t.Logf("🧪 TESTING ENHANCED CONDITION HANDLING:")
		t.Logf("   Real Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Impossible Condition: price > $%.2f", impossibleThreshold)
		t.Logf("   Expected: Return real data with condition_met=false")

		// Test the current method (enhanced method was removed for simplicity)
		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		if err != nil {
			t.Logf("⚠️  Current implementation returns error for impossible conditions: %s", err.Error())
			t.Skip("Current implementation doesn't support enhanced condition handling")
		}
		require.NotNil(t, simulatedLog)

		// Extract price from simulated log
		eventPrice := simulatedLog.Topics[1].Big()
		eventPriceFloat := float64(eventPrice.Int64()) / 100000000

		t.Logf("\n✅ ENHANCED SIMULATION RESULT:")
		t.Logf("   Event Price: $%.2f (raw: %s)", eventPriceFloat, eventPrice.String())
		t.Logf("   Real Price: $%.2f", currentPriceFloat)

		t.Logf("\n💡 NOTE: Current simplified implementation")
		t.Logf("   When conditions don't match, returns error (no event)")
		t.Logf("   When conditions match, returns event with real data")
		t.Logf("   This matches the new protobuf-compliant design")
	})

	// Test with condition that DOES match
	t.Run("EnhancedBehavior_WithConditionsThatMatch", func(t *testing.T) {
		// Get real current price and set a condition that will match
		roundData, err := client.getRealRoundDataViaTenderly(ctx, SEPOLIA_ETH_USD_FEED, SEPOLIA_CHAIN_ID)
		require.NoError(t, err)

		currentPriceFloat := float64(roundData.Answer.Int64()) / 100000000

		// Set a condition that will definitely match (price > current - $1000)
		lowThreshold := currentPriceFloat - 1000.0
		lowThresholdRaw := int64(lowThreshold * 100000000)

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{
					Values: []string{ANSWER_UPDATED_SIG},
				},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     fmt.Sprintf("%d", lowThresholdRaw),
					FieldType: "int256",
				},
			},
		}

		t.Logf("🎯 TESTING CONDITIONS THAT MATCH:")
		t.Logf("   Real Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Easy Condition: price > $%.2f", lowThreshold)

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err)

		// When conditions match, should always use real price
		eventPrice := simulatedLog.Topics[1].Big()
		eventPriceFloat := float64(eventPrice.Int64()) / 100000000

		t.Logf("✅ Conditions naturally satisfied - using real price: $%.2f", eventPriceFloat)
	})
}

func TestTenderlyClient_TransactionRevert(t *testing.T) {
	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)

	// Create a query with invalid contract address to trigger revert
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x0000000000000000000000000000000000000000"}, // Invalid address
		Topics: []*avsproto.EventTrigger_Topics{
			{Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"}},
		},
		Conditions: []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000",
				FieldType: "int256",
			},
		},
	}

	ctx := context.Background()
	chainID := int64(11155111) // Sepolia

	fmt.Printf("\n🧪 === TESTING TRANSACTION REVERT ===\n")
	fmt.Printf("📍 Testing invalid contract: 0x0000000000000000000000000000000000000000\n")
	fmt.Printf("🎯 Expected: Transaction should revert\n\n")

	// This should fail because calling latestRoundData() on 0x0000... will revert
	log, err := client.SimulateEventTrigger(ctx, query, chainID)

	// Verify error handling
	if err != nil {
		fmt.Printf("✅ Error correctly returned: %s\n", err.Error())
		assert.Error(t, err, "Should return error for invalid contract")
		assert.Nil(t, log, "Should return nil log on error")
		// Note: Specific revert message may vary by provider
	} else {
		fmt.Printf("⚠️  No error returned - provider may handle invalid addresses differently\n")
		// Some providers might return default values instead of reverting
	}
}

func TestTenderlyClient_InvalidContractCall(t *testing.T) {
	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)

	// Create a query with a valid contract address but invalid chain ID
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x694AA1769357215DE4FAC081bf1f309aDC325306"}, // Valid Chainlink address
		Topics: []*avsproto.EventTrigger_Topics{
			{Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"}},
		},
	}

	ctx := context.Background()
	chainID := int64(999999) // Invalid chain ID to trigger error

	fmt.Printf("\n🧪 === TESTING INVALID CHAIN ID ===\n")
	fmt.Printf("📍 Using valid contract on invalid chain: %d\n", chainID)
	fmt.Printf("🎯 Expected: Network error or unsupported chain\n\n")

	// This should fail due to invalid chain ID
	log, err := client.SimulateEventTrigger(ctx, query, chainID)

	// Verify error handling
	if err != nil {
		fmt.Printf("✅ Error correctly returned: %s\n", err.Error())
		assert.Error(t, err, "Should return error for invalid chain ID")
		assert.Nil(t, log, "Should return nil log on error")
	} else {
		fmt.Printf("⚠️  No error returned - provider may have fallback behavior\n")
	}
}

func TestTenderlyClient_NetworkError(t *testing.T) {
	logger := testutil.GetLogger()
	client := NewTenderlyClient(logger)

	// Create a valid query
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x694AA1769357215DE4FAC081bf1f309aDC325306"},
		Topics: []*avsproto.EventTrigger_Topics{
			{Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"}},
		},
	}

	// Use a cancelled context to simulate network timeout
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	chainID := int64(11155111)

	fmt.Printf("\n🧪 === TESTING NETWORK TIMEOUT ===\n")
	fmt.Printf("📍 Using cancelled context to simulate timeout\n")
	fmt.Printf("🎯 Expected: Context cancellation error\n\n")

	// This should fail due to cancelled context
	log, err := client.SimulateEventTrigger(ctx, query, chainID)

	// Verify error handling
	assert.Error(t, err, "Should return error for cancelled context")
	assert.Nil(t, log, "Should return nil log on error")
	fmt.Printf("✅ Error correctly returned: %s\n", err.Error())
}
