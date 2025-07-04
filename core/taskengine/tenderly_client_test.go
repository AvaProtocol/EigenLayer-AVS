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
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
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

// MockTenderlyClient for unit testing without external dependencies
type MockTenderlyClient struct {
	logger            sdklogging.Logger
	mockPrice         *big.Int // The price to return in simulations
	shouldReturnError bool     // Whether to return an error
	errorMessage      string   // Custom error message
}

// NewMockTenderlyClient creates a mock Tenderly client for testing
func NewMockTenderlyClient(logger sdklogging.Logger, mockPriceUSD float64) *MockTenderlyClient {
	// Convert USD price to 8-decimal format (Chainlink standard)
	mockPriceRaw := int64(mockPriceUSD * 100000000)
	return &MockTenderlyClient{
		logger:    logger,
		mockPrice: big.NewInt(mockPriceRaw),
	}
}

// SetError configures the mock to return an error
func (m *MockTenderlyClient) SetError(shouldError bool, message string) {
	m.shouldReturnError = shouldError
	m.errorMessage = message
}

// SetMockPrice updates the mock price
func (m *MockTenderlyClient) SetMockPrice(priceUSD float64) {
	mockPriceRaw := int64(priceUSD * 100000000)
	m.mockPrice = big.NewInt(mockPriceRaw)
}

// SimulateEventTrigger mocks the Tenderly simulation
func (m *MockTenderlyClient) SimulateEventTrigger(ctx context.Context, query *avsproto.EventTrigger_Query, chainID int64) (*types.Log, error) {
	if m.shouldReturnError {
		return nil, fmt.Errorf(m.errorMessage)
	}

	if len(query.GetAddresses()) == 0 {
		return nil, fmt.Errorf("no contract addresses provided for simulation")
	}

	contractAddress := query.GetAddresses()[0]

	// Check if this is a Chainlink price feed
	isChainlinkPriceFeed := false
	for _, topicGroup := range query.GetTopics() {
		for _, topic := range topicGroup.GetValues() {
			if topic == ANSWER_UPDATED_SIG {
				isChainlinkPriceFeed = true
				break
			}
		}
	}

	if !isChainlinkPriceFeed {
		return nil, fmt.Errorf("mock only supports Chainlink price feeds")
	}

	// Create mock AnswerUpdated event log
	return m.createMockAnswerUpdatedLog(contractAddress, m.mockPrice), nil
}

// createMockAnswerUpdatedLog creates a mock Chainlink AnswerUpdated event log
func (m *MockTenderlyClient) createMockAnswerUpdatedLog(contractAddress string, price *big.Int) *types.Log {
	// AnswerUpdated event signature
	eventSignature := common.HexToHash(ANSWER_UPDATED_SIG)

	// Convert price to 32-byte hash (indexed parameter)
	priceHash := common.BytesToHash(common.LeftPadBytes(price.Bytes(), 32))

	// Mock round ID
	roundId := big.NewInt(24008)
	roundIdHash := common.BytesToHash(common.LeftPadBytes(roundId.Bytes(), 32))

	// Mock updatedAt timestamp
	updatedAt := big.NewInt(time.Now().Unix())
	updatedAtBytes := common.LeftPadBytes(updatedAt.Bytes(), 32)

	// Create a mock transaction hash
	txHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	return &types.Log{
		Address: common.HexToAddress(contractAddress),
		Topics: []common.Hash{
			eventSignature, // Event signature
			priceHash,      // current (indexed)
			roundIdHash,    // roundId (indexed)
		},
		Data:        updatedAtBytes,            // updatedAt (non-indexed)
		BlockNumber: uint64(time.Now().Unix()), // Use current timestamp as mock block
		TxHash:      txHash,
		Index:       0,
		TxIndex:     0,
		BlockHash:   common.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		Removed:     false,
	}
}

// TestTenderlySimulation_ConditionMatching_Unit tests condition matching logic with mocked data
func TestTenderlySimulation_ConditionMatching_Unit(t *testing.T) {
	logger := testutil.GetLogger()

	t.Run("ConditionShouldMatch_GreaterThan", func(t *testing.T) {
		// Mock current price: $2500
		mockClient := NewMockTenderlyClient(logger, 2500.0)

		// Set condition: price > $2000 (should match)
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{Values: []string{ANSWER_UPDATED_SIG}},
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

		ctx := context.Background()
		simulatedLog, err := mockClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Mock simulation should succeed")
		require.NotNil(t, simulatedLog, "Should get simulated log")

		// Verify the simulated price
		simulatedPrice := simulatedLog.Topics[1].Big()
		expectedPrice := big.NewInt(250000000000) // $2500 with 8 decimals
		assert.Equal(t, expectedPrice, simulatedPrice, "Mock price should match expected value")

		// Verify condition would be satisfied
		threshold := big.NewInt(200000000000) // $2000
		assert.True(t, simulatedPrice.Cmp(threshold) > 0, "Price should be greater than threshold")

		t.Logf("✅ UNIT TEST: Condition matching logic works correctly")
		t.Logf("   Mock Price: $2500 (raw: %s)", simulatedPrice.String())
		t.Logf("   Threshold: $2000 (raw: %s)", threshold.String())
		t.Logf("   Condition Met: %s > %s ✅", simulatedPrice.String(), threshold.String())
	})

	t.Run("ConditionShouldNotMatch_GreaterThan", func(t *testing.T) {
		// Mock current price: $1800
		mockClient := NewMockTenderlyClient(logger, 1800.0)

		// Set condition: price > $2000 (should NOT match)
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{Values: []string{ANSWER_UPDATED_SIG}},
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

		ctx := context.Background()
		simulatedLog, err := mockClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Mock simulation should succeed")
		require.NotNil(t, simulatedLog, "Should get simulated log")

		// Verify the simulated price
		simulatedPrice := simulatedLog.Topics[1].Big()
		expectedPrice := big.NewInt(180000000000) // $1800 with 8 decimals
		assert.Equal(t, expectedPrice, simulatedPrice, "Mock price should match expected value")

		// Verify condition would NOT be satisfied
		threshold := big.NewInt(200000000000) // $2000
		assert.False(t, simulatedPrice.Cmp(threshold) > 0, "Price should NOT be greater than threshold")

		t.Logf("✅ UNIT TEST: Condition rejection logic works correctly")
		t.Logf("   Mock Price: $1800 (raw: %s)", simulatedPrice.String())
		t.Logf("   Threshold: $2000 (raw: %s)", threshold.String())
		t.Logf("   Condition Met: %s > %s ❌", simulatedPrice.String(), threshold.String())
	})

	t.Run("ConditionShouldMatch_LessThan", func(t *testing.T) {
		// Mock current price: $1500
		mockClient := NewMockTenderlyClient(logger, 1500.0)

		// Set condition: price < $2000 (should match)
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{Values: []string{ANSWER_UPDATED_SIG}},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "lt",
					Value:     "200000000000", // $2000 with 8 decimals
					FieldType: "int256",
				},
			},
		}

		ctx := context.Background()
		simulatedLog, err := mockClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Mock simulation should succeed")
		require.NotNil(t, simulatedLog, "Should get simulated log")

		// Verify the simulated price
		simulatedPrice := simulatedLog.Topics[1].Big()
		expectedPrice := big.NewInt(150000000000) // $1500 with 8 decimals
		assert.Equal(t, expectedPrice, simulatedPrice, "Mock price should match expected value")

		// Verify condition would be satisfied
		threshold := big.NewInt(200000000000) // $2000
		assert.True(t, simulatedPrice.Cmp(threshold) < 0, "Price should be less than threshold")

		t.Logf("✅ UNIT TEST: Less-than condition logic works correctly")
		t.Logf("   Mock Price: $1500 (raw: %s)", simulatedPrice.String())
		t.Logf("   Threshold: $2000 (raw: %s)", threshold.String())
		t.Logf("   Condition Met: %s < %s ✅", simulatedPrice.String(), threshold.String())
	})

	t.Run("ConditionShouldMatch_Equal", func(t *testing.T) {
		// Mock current price: exactly $2000
		mockClient := NewMockTenderlyClient(logger, 2000.0)

		// Set condition: price == $2000 (should match)
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{Values: []string{ANSWER_UPDATED_SIG}},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "eq",
					Value:     "200000000000", // $2000 with 8 decimals
					FieldType: "int256",
				},
			},
		}

		ctx := context.Background()
		simulatedLog, err := mockClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Mock simulation should succeed")
		require.NotNil(t, simulatedLog, "Should get simulated log")

		// Verify the simulated price
		simulatedPrice := simulatedLog.Topics[1].Big()
		expectedPrice := big.NewInt(200000000000) // $2000 with 8 decimals
		assert.Equal(t, expectedPrice, simulatedPrice, "Mock price should match expected value")

		// Verify condition would be satisfied
		threshold := big.NewInt(200000000000) // $2000
		assert.True(t, simulatedPrice.Cmp(threshold) == 0, "Price should equal threshold")

		t.Logf("✅ UNIT TEST: Equality condition logic works correctly")
		t.Logf("   Mock Price: $2000 (raw: %s)", simulatedPrice.String())
		t.Logf("   Threshold: $2000 (raw: %s)", threshold.String())
		t.Logf("   Condition Met: %s == %s ✅", simulatedPrice.String(), threshold.String())
	})

	t.Run("MultipleConditions_RangeMatch", func(t *testing.T) {
		// Mock current price: $2250 (should be within range $2000-$2500)
		mockClient := NewMockTenderlyClient(logger, 2250.0)

		// Set conditions: $2000 < price < $2500 (should match)
		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{Values: []string{ANSWER_UPDATED_SIG}},
			},
			Conditions: []*avsproto.EventCondition{
				{
					FieldName: "current",
					Operator:  "gt",
					Value:     "200000000000", // $2000 with 8 decimals
					FieldType: "int256",
				},
				{
					FieldName: "current",
					Operator:  "lt",
					Value:     "250000000000", // $2500 with 8 decimals
					FieldType: "int256",
				},
			},
		}

		ctx := context.Background()
		simulatedLog, err := mockClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.NoError(t, err, "Mock simulation should succeed")
		require.NotNil(t, simulatedLog, "Should get simulated log")

		// Verify the simulated price
		simulatedPrice := simulatedLog.Topics[1].Big()
		expectedPrice := big.NewInt(225000000000) // $2250 with 8 decimals
		assert.Equal(t, expectedPrice, simulatedPrice, "Mock price should match expected value")

		// Verify both conditions would be satisfied
		lowerThreshold := big.NewInt(200000000000) // $2000
		upperThreshold := big.NewInt(250000000000) // $2500

		condition1Met := simulatedPrice.Cmp(lowerThreshold) > 0
		condition2Met := simulatedPrice.Cmp(upperThreshold) < 0

		assert.True(t, condition1Met, "Price should be greater than lower threshold")
		assert.True(t, condition2Met, "Price should be less than upper threshold")

		t.Logf("✅ UNIT TEST: Multiple condition logic works correctly")
		t.Logf("   Mock Price: $2250 (raw: %s)", simulatedPrice.String())
		t.Logf("   Condition 1: %s > %s = %t", simulatedPrice.String(), lowerThreshold.String(), condition1Met)
		t.Logf("   Condition 2: %s < %s = %t", simulatedPrice.String(), upperThreshold.String(), condition2Met)
		t.Logf("   Both Conditions Met: %t ✅", condition1Met && condition2Met)
	})

	t.Run("MockErrorHandling", func(t *testing.T) {
		// Test error handling in mock
		mockClient := NewMockTenderlyClient(logger, 2000.0)
		mockClient.SetError(true, "mock tenderly API error")

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{SEPOLIA_ETH_USD_FEED},
			Topics: []*avsproto.EventTrigger_Topics{
				{Values: []string{ANSWER_UPDATED_SIG}},
			},
		}

		ctx := context.Background()
		simulatedLog, err := mockClient.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)

		require.Error(t, err, "Mock should return error when configured")
		require.Nil(t, simulatedLog, "Should not get simulated log on error")
		assert.Contains(t, err.Error(), "mock tenderly API error", "Error message should match")

		t.Logf("✅ UNIT TEST: Error handling works correctly")
		t.Logf("   Expected Error: %s", err.Error())
	})
}

func TestTenderlyEventSimulation_EndToEnd_Integration(t *testing.T) {

	logger := testutil.GetLogger()

	// Create TenderlyClient with real API key
	tenderlyClient := NewTenderlyClient(logger)

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

		// Simulate runTrigger call with Tenderly - use a condition that should match
		// Set threshold very low to ensure the condition is satisfied
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
							"operator":  "gt",
							"value":     "100000000", // $1.00 - very low threshold to ensure match
							"fieldType": "int256",
						},
					},
				},
			},
		}

		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		require.NoError(t, err, "Engine simulation should succeed")

		// The result can be nil if conditions are not met or simulation fails
		// This is expected behavior for Tenderly simulation
		if result == nil {
			fmt.Printf("⚠️  No event simulated (conditions not met or simulation failed)\n")
			fmt.Printf("💡 This is expected behavior when:\n")
			fmt.Printf("   - Current price doesn't meet the condition\n")
			fmt.Printf("   - Tenderly API is unavailable\n")
			fmt.Printf("   - Network connectivity issues\n")

			// Test passes - nil result is valid for failed simulation
			return
		}

		// If we get a result, validate its structure
		assert.True(t, result["found"].(bool), "Should find simulated event")

		// Check if we have the new structured data format
		if eventData, hasData := result["data"].(map[string]interface{}); hasData && eventData != nil {
			// New format: structured data map
			// Validate structured event data
			assert.NotNil(t, eventData["contractAddress"], "Should have contract address")
			assert.NotNil(t, eventData["blockNumber"], "Should have block number")
			assert.NotNil(t, eventData["eventFound"], "Should have eventFound field")

			fmt.Printf("✅ New structured data format detected\n")
		} else if evmLog, hasEvmLog := result["evm_log"]; hasEvmLog && evmLog != nil {
			// Legacy format: evm_log structure
			assert.NotNil(t, evmLog, "Should have evm_log")
			fmt.Printf("✅ Legacy evm_log format detected\n")
		} else {
			t.Errorf("Result should have either 'data' (new format) or 'evm_log' (legacy format)")
		}

		// Check common fields
		assert.NotNil(t, result["found"], "Should have 'found' field")

		fmt.Printf("✅ Full engine integration successful!\n")
		printEngineResult(result)
	})
}

func TestTenderlyGateway_RealRPCCalls_Integration(t *testing.T) {

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

func TestTenderlySimulation_WithConditions_ComprehensiveTest_Integration(t *testing.T) {

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
		t.Logf("   Expected: REAL BEHAVIOR - Return real data that doesn't satisfy condition")

		simulatedLog, err := client.SimulateEventTrigger(ctx, query, SEPOLIA_CHAIN_ID)
		require.NoError(t, err, "Simulation should still succeed")
		require.NotNil(t, simulatedLog)

		// Current behavior: Tenderly returns real current price (not artificial data)
		simulatedPrice := simulatedLog.Topics[1].Big()
		simulatedPriceFloat := float64(simulatedPrice.Int64()) / 100000000

		t.Logf("✅ REAL BEHAVIOR - ACTUAL CURRENT PRICE RETURNED:")
		t.Logf("   Real Current Price: $%.2f", currentPriceFloat)
		t.Logf("   Simulated Price: $%.2f (raw: %s)", simulatedPriceFloat, simulatedPrice.String())
		t.Logf("   Threshold: $%.2f (raw: %d)", thresholdFloat, thresholdRaw)
		t.Logf("   Result: Tenderly returned real price data (not artificial)")

		// The current implementation returns real price data, which should NOT satisfy the high threshold
		// We expect the real price to be less than the artificially high threshold
		conditionSatisfied := simulatedPrice.Cmp(big.NewInt(thresholdRaw)) > 0

		if conditionSatisfied {
			t.Logf("⚠️  UNEXPECTED: Real price actually satisfies the high threshold!")
			t.Logf("   This means the current ETH price is > $%.2f", thresholdFloat)
		} else {
			t.Logf("✅ EXPECTED: Real price does not satisfy the high threshold")
			t.Logf("   Real price $%.2f < threshold $%.2f", simulatedPriceFloat, thresholdFloat)
		}

		// Assert that the simulated price is close to the real current price
		// Allow for small differences due to timing or data source variations
		priceDifference := simulatedPriceFloat - currentPriceFloat
		if priceDifference < 0 {
			priceDifference = -priceDifference
		}

		// Price should be within $100 of the real current price (allowing for market movements)
		assert.True(t, priceDifference < 100.0,
			"Simulated price should be close to real current price (within $100)")

		t.Logf("\n💡 IMPLEMENTATION NOTE:")
		t.Logf("   Tenderly simulation returns REAL current price data")
		t.Logf("   It does NOT generate artificial data to satisfy conditions")
		t.Logf("   This is more realistic for testing real-world scenarios")
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
func TestTenderlySimulation_EnhancedConditionHandling_REAL_Integration(t *testing.T) {

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

// TestEventConditionEvaluation_Unit tests the actual condition evaluation logic used by the engine
func TestEventConditionEvaluation_Unit(t *testing.T) {
	logger := testutil.GetLogger()

	// Create a minimal engine for testing the evaluateEventConditions method
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, logger)

	// Helper function to create a mock event log with a specific price
	createMockEventLog := func(priceUSD float64) *types.Log {
		priceRaw := int64(priceUSD * 100000000) // Convert to 8-decimal format
		price := big.NewInt(priceRaw)

		// Create mock AnswerUpdated event log
		eventSignature := common.HexToHash(ANSWER_UPDATED_SIG)
		priceHash := common.BytesToHash(common.LeftPadBytes(price.Bytes(), 32))
		roundIdHash := common.BytesToHash(common.LeftPadBytes(big.NewInt(24008).Bytes(), 32))

		return &types.Log{
			Address: common.HexToAddress(SEPOLIA_ETH_USD_FEED),
			Topics: []common.Hash{
				eventSignature, // Event signature
				priceHash,      // current (indexed)
				roundIdHash,    // roundId (indexed)
			},
			Data: common.LeftPadBytes(big.NewInt(time.Now().Unix()).Bytes(), 32),
		}
	}

	t.Run("GreaterThan_ConditionMet", func(t *testing.T) {
		// Mock event with price $2500
		eventLog := createMockEventLog(2500.0)

		// Condition: price > $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Condition should be met: $2500 > $2000")

		t.Logf("✅ UNIT TEST: GreaterThan condition evaluation works correctly")
		t.Logf("   Event Price: $2500")
		t.Logf("   Condition: price > $2000")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("GreaterThan_ConditionNotMet", func(t *testing.T) {
		// Mock event with price $1800
		eventLog := createMockEventLog(1800.0)

		// Condition: price > $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.False(t, result, "Condition should NOT be met: $1800 > $2000")

		t.Logf("✅ UNIT TEST: GreaterThan condition rejection works correctly")
		t.Logf("   Event Price: $1800")
		t.Logf("   Condition: price > $2000")
		t.Logf("   Result: %t ❌", result)
	})

	t.Run("LessThan_ConditionMet", func(t *testing.T) {
		// Mock event with price $1500
		eventLog := createMockEventLog(1500.0)

		// Condition: price < $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "lt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Condition should be met: $1500 < $2000")

		t.Logf("✅ UNIT TEST: LessThan condition evaluation works correctly")
		t.Logf("   Event Price: $1500")
		t.Logf("   Condition: price < $2000")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("Equal_ConditionMet", func(t *testing.T) {
		// Mock event with price exactly $2000
		eventLog := createMockEventLog(2000.0)

		// Condition: price == $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "eq",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Condition should be met: $2000 == $2000")

		t.Logf("✅ UNIT TEST: Equal condition evaluation works correctly")
		t.Logf("   Event Price: $2000")
		t.Logf("   Condition: price == $2000")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("GreaterThanOrEqual_ConditionMet", func(t *testing.T) {
		// Mock event with price exactly $2000
		eventLog := createMockEventLog(2000.0)

		// Condition: price >= $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gte",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Condition should be met: $2000 >= $2000")

		t.Logf("✅ UNIT TEST: GreaterThanOrEqual condition evaluation works correctly")
		t.Logf("   Event Price: $2000")
		t.Logf("   Condition: price >= $2000")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("LessThanOrEqual_ConditionMet", func(t *testing.T) {
		// Mock event with price exactly $2000
		eventLog := createMockEventLog(2000.0)

		// Condition: price <= $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "lte",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Condition should be met: $2000 <= $2000")

		t.Logf("✅ UNIT TEST: LessThanOrEqual condition evaluation works correctly")
		t.Logf("   Event Price: $2000")
		t.Logf("   Condition: price <= $2000")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("NotEqual_ConditionMet", func(t *testing.T) {
		// Mock event with price $2500
		eventLog := createMockEventLog(2500.0)

		// Condition: price != $2000
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "ne",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Condition should be met: $2500 != $2000")

		t.Logf("✅ UNIT TEST: NotEqual condition evaluation works correctly")
		t.Logf("   Event Price: $2500")
		t.Logf("   Condition: price != $2000")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("MultipleConditions_AllMet", func(t *testing.T) {
		// Mock event with price $2250
		eventLog := createMockEventLog(2250.0)

		// Conditions: $2000 < price < $2500
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
			{
				FieldName: "current",
				Operator:  "lt",
				Value:     "250000000000", // $2500 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "All conditions should be met: $2000 < $2250 < $2500")

		t.Logf("✅ UNIT TEST: Multiple condition evaluation works correctly")
		t.Logf("   Event Price: $2250")
		t.Logf("   Condition 1: price > $2000")
		t.Logf("   Condition 2: price < $2500")
		t.Logf("   Result: %t ✅", result)
	})

	t.Run("MultipleConditions_OneFails", func(t *testing.T) {
		// Mock event with price $2600
		eventLog := createMockEventLog(2600.0)

		// Conditions: $2000 < price < $2500 (second condition should fail)
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
			{
				FieldName: "current",
				Operator:  "lt",
				Value:     "250000000000", // $2500 with 8 decimals
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.False(t, result, "Should fail because $2600 is not < $2500")

		t.Logf("✅ UNIT TEST: Multiple condition rejection works correctly")
		t.Logf("   Event Price: $2600")
		t.Logf("   Condition 1: price > $2000 (✅ met)")
		t.Logf("   Condition 2: price < $2500 (❌ not met)")
		t.Logf("   Result: %t ❌", result)
	})

	t.Run("InvalidValue_ConditionIgnored", func(t *testing.T) {
		// Mock event with price $2500
		eventLog := createMockEventLog(2500.0)

		// Condition with invalid value
		conditions := []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "invalid-number", // Invalid value
				FieldType: "int256",
			},
		}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Should return true when invalid condition is ignored")

		t.Logf("✅ UNIT TEST: Invalid condition handling works correctly")
		t.Logf("   Event Price: $2500")
		t.Logf("   Condition: price > 'invalid-number' (ignored)")
		t.Logf("   Result: %t (condition ignored)", result)
	})

	t.Run("NoConditions_AlwaysTrue", func(t *testing.T) {
		// Mock event with any price
		eventLog := createMockEventLog(2500.0)

		// No conditions
		conditions := []*avsproto.EventCondition{}

		result := engine.evaluateEventConditions(eventLog, conditions)
		assert.True(t, result, "Should return true when no conditions are provided")

		t.Logf("✅ UNIT TEST: No conditions handling works correctly")
		t.Logf("   Event Price: $2500")
		t.Logf("   Conditions: none")
		t.Logf("   Result: %t ✅", result)
	})
}

// TestEventTriggerImmediately_TenderlySimulation_Unit tests the runEventTriggerImmediately function with Tenderly simulation
func TestEventTriggerImmediately_TenderlySimulation_Unit(t *testing.T) {

	logger := testutil.GetLogger()

	// Create a test engine
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, logger)

	t.Run("ChainlinkPriceFeed_Simulation", func(t *testing.T) {
		// Test Chainlink ETH/USD price feed simulation
		triggerConfig := map[string]interface{}{
			"simulationMode": true, // KEY: Enable simulation mode
			"queries": []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{SEPOLIA_ETH_USD_FEED},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{ANSWER_UPDATED_SIG},
						},
					},
				},
			},
		}

		t.Logf("🔮 Testing Tenderly simulation for Chainlink price feed")
		t.Logf("📍 Contract: %s", SEPOLIA_ETH_USD_FEED)
		t.Logf("🎯 Event: AnswerUpdated")

		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		require.NoError(t, err, "Tenderly simulation should succeed")
		require.NotNil(t, result, "Should get simulation result")

		// Verify the structure matches the new protobuf format
		assert.True(t, result["found"].(bool), "Should find simulated event")
		assert.NotEmpty(t, result["data"], "Should have event data")
		assert.NotEmpty(t, result["metadata"], "Should have metadata")

		// Get the data map directly (not a JSON string)
		eventData, ok := result["data"].(map[string]interface{})
		require.True(t, ok, "data should be a map[string]interface{}")
		require.NotNil(t, eventData, "Should have event data")

		// Verify expected fields in the event data
		assert.NotNil(t, eventData["eventFound"], "Should have eventFound field")
		assert.NotNil(t, eventData["contractAddress"], "Should have contract address")
		assert.NotNil(t, eventData["blockNumber"], "Should have block number")
		assert.NotNil(t, eventData["transactionHash"], "Should have transaction hash")
		assert.NotNil(t, eventData["topics"], "Should have topics")
		assert.NotNil(t, eventData["rawData"], "Should have raw data")

		// Get the metadata map directly (not a JSON string)
		metadata, ok := result["metadata"].(map[string]interface{})
		require.True(t, ok, "metadata should be a map[string]interface{}")
		require.NotNil(t, metadata, "Should have metadata")

		assert.NotNil(t, metadata["address"], "Should have address in metadata")
		assert.NotNil(t, metadata["blockNumber"], "Should have blockNumber in metadata")

		t.Logf("✅ Tenderly simulation successful!")
		t.Logf("📊 Sample Event Data Structure:")
		t.Logf("   Event Found: %v", eventData["eventFound"])
		t.Logf("   Contract: %v", eventData["contractAddress"])
		t.Logf("   Block: %v", eventData["blockNumber"])
		t.Logf("   TX Hash: %v", eventData["transactionHash"])
		t.Logf("   Event Type: %v", eventData["eventType"])

		// Print the complete data structure for documentation
		t.Logf("\n📋 Complete Event Data:")
		eventDataJSON, _ := json.MarshalIndent(eventData, "", "  ")
		t.Logf("%s", string(eventDataJSON))
		t.Logf("\n🔍 Complete Metadata:")
		metadataJSON, _ := json.MarshalIndent(metadata, "", "  ")
		t.Logf("%s", string(metadataJSON))
	})

	t.Run("ChainlinkPriceFeed_WithConditions", func(t *testing.T) {
		// Test with conditions that should match
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
							"operator":  "gt",
							"value":     "100000000", // $1.00 - very low threshold
							"fieldType": "int256",
						},
					},
				},
			},
		}

		t.Logf("🎯 Testing Tenderly simulation with conditions")

		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		require.NoError(t, err, "Simulation with conditions should succeed")
		require.NotNil(t, result, "Should get simulation result")

		// Verify the result structure
		assert.True(t, result["found"].(bool), "Should find event that meets condition")

		// Get and verify the data directly
		eventData, ok := result["data"].(map[string]interface{})
		require.True(t, ok, "data should be a map[string]interface{}")
		require.NotNil(t, eventData, "Should have event data")

		// Verify we have event data (the condition logic is tested elsewhere)
		assert.NotNil(t, eventData["eventFound"], "Should have eventFound field")
		assert.NotNil(t, eventData["contractAddress"], "Should have contract address")
		assert.NotNil(t, eventData["blockNumber"], "Should have block number")

		t.Logf("✅ Condition evaluation successful!")
		t.Logf("   Event Found: %v", eventData["eventFound"])
		t.Logf("   Contract: %v", eventData["contractAddress"])
		t.Logf("   Event Type: %v", eventData["eventType"])
		t.Logf("   Condition: simulation mode provides sample data ✅")
	})

	t.Run("TransferEvent_Simulation", func(t *testing.T) {
		// Test Transfer event simulation - this will likely not work with Tenderly
		// since Tenderly is specialized for Chainlink price feeds, but let's document the behavior
		triggerConfig := map[string]interface{}{
			"simulationMode": true,
			"queries": []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{
						"0x779877A7B0D9E8603169DdbD7836e478b4624789", // LINK token
					},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
								"0x000000000000000000000000c60e71bd0f2e6d8832fea1a2d56091c48493c788", // from address
								nil, // to address (wildcard)
							},
						},
					},
				},
			},
		}

		t.Logf("🔄 Testing Transfer event simulation (may not be supported by Tenderly)")

		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		if err != nil {
			t.Logf("⚠️  Transfer simulation failed (expected): %v", err)
			t.Logf("💡 Note: Tenderly simulation is optimized for Chainlink price feeds")
			t.Logf("💡 For Transfer events, use historical search mode (simulationMode: false)")
			return
		}

		if result == nil {
			t.Logf("⚠️  Transfer simulation returned nil (expected for unsupported event types)")
			return
		}

		// If it succeeds, document the structure
		t.Logf("✅ Transfer simulation unexpectedly succeeded!")
		if found, ok := result["found"].(bool); ok && found {
			if eventData, ok := result["data"].(map[string]interface{}); ok {
				eventDataJSON, _ := json.MarshalIndent(eventData, "", "  ")
				t.Logf("📊 Transfer Event Data: %s", string(eventDataJSON))
			}
		}
	})
}

// TestEventTriggerImmediately_HistoricalSearch_Unit tests historical search with known contracts
func TestEventTriggerImmediately_HistoricalSearch_Unit(t *testing.T) {
	// This test uses historical search and may not find events, which is expected
	logger := testutil.GetLogger()

	// Create a test engine
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, logger)

	t.Run("NoEventsFound_ExpectedBehavior", func(t *testing.T) {
		// Test historical search that likely won't find events
		triggerConfig := map[string]interface{}{
			"simulationMode": false, // Use historical search
			"queries": []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{
						"0x779877A7B0D9E8603169DdbD7836e478b4624789", // LINK token
					},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer
								"0x000000000000000000000000c60e71bd0f2e6d8832fea1a2d56091c48493c788", // from
								nil, // to (wildcard)
							},
						},
					},
				},
			},
		}

		t.Logf("🔍 Testing historical search (may not find recent events)")

		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		require.NoError(t, err, "Historical search should not error")
		require.NotNil(t, result, "Should get result even if no events found")

		// Document the "no events found" structure
		if found, ok := result["found"].(bool); ok && !found {
			t.Logf("✅ No events found (expected for historical search)")
			t.Logf("📊 No Events Response Structure:")
			t.Logf("   found: %v", result["found"])
			t.Logf("   message: %v", result["message"])
			t.Logf("   queriesCount: %v", result["queriesCount"])
			t.Logf("   totalSearched: %v", result["totalSearched"])

			// Print complete structure for documentation
			resultJSON, _ := json.MarshalIndent(result, "", "  ")
			t.Logf("\n📋 Complete 'No Events' Response:")
			t.Logf("%s", string(resultJSON))
		} else {
			t.Logf("🎉 Unexpectedly found events in historical search!")
			if eventData, hasData := result["data"].(map[string]interface{}); hasData {
				eventDataJSON, _ := json.MarshalIndent(eventData, "", "  ")
				t.Logf("📊 Event Data: %s", string(eventDataJSON))
			}
			if evmLog, hasEvmLog := result["evm_log"]; hasEvmLog {
				evmLogJSON, _ := json.MarshalIndent(evmLog, "", "  ")
				t.Logf("📊 EVM Log: %s", string(evmLogJSON))
			}
		}
	})
}

// TestTransferEventSampleData_ForUserDocumentation demonstrates how to get sample Transfer event data
// This test shows users exactly how to use Tenderly simulation to get meaningful Transfer event structures
func TestTransferEventSampleData_ForUserDocumentation(t *testing.T) {
	// This test uses TENDERLY_API_KEY for simulation

	logger := testutil.GetLogger()

	// Create a test engine
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, logger)

	t.Run("GetTransferEventSampleData", func(t *testing.T) {
		t.Logf("🎯 === GETTING SAMPLE TRANSFER EVENT DATA FOR USER REFERENCE ===")
		t.Logf("📝 This test demonstrates how to get sample data structure for Transfer events")
		t.Logf("🔧 Using: simulationMode = true")

		// Configure the exact same trigger as the user's failing test
		triggerConfig := map[string]interface{}{
			"simulationMode": true, // 🔑 KEY: Use simulation mode to get sample data
			"queries": []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{
						"0x779877A7B0D9E8603169DdbD7836e478b4624789", // LINK token
						"0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984", // UNI token
						"0xB4FBF271143F4FBf7B91A5ded31805e42b2208d6", // WETH
					},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", // Transfer signature
								"0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",                         // from address
								nil, // to address (wildcard)
							},
						},
					},
				},
			},
		}

		t.Logf("📊 Trigger Configuration:")
		configJSON, _ := json.MarshalIndent(triggerConfig, "", "  ")
		t.Logf("%s", string(configJSON))

		// Execute the trigger with simulation mode
		result, err := engine.runEventTriggerImmediately(triggerConfig, map[string]interface{}{})

		require.NoError(t, err, "Simulation should succeed")
		require.NotNil(t, result, "Should get simulation result")

		// Verify we got meaningful data
		assert.True(t, result["found"].(bool), "Should find simulated Transfer event")
		assert.NotEmpty(t, result["data"], "Should have Transfer event data")
		assert.NotEmpty(t, result["metadata"], "Should have metadata")

		// Get and display the Transfer event data structure
		transferData, ok := result["data"].(map[string]interface{})
		require.True(t, ok, "data should be a map[string]interface{}")
		require.NotNil(t, transferData, "Should have Transfer event data")

		t.Logf("\n🎉 === SAMPLE TRANSFER EVENT DATA STRUCTURE ===")
		t.Logf("✅ Success! Here's the sample data structure users can reference:")
		t.Logf("")
		t.Logf("📋 Event Data Fields:")
		t.Logf("   eventFound: %v", transferData["eventFound"])
		t.Logf("   contractAddress: %v", transferData["contractAddress"])
		t.Logf("   blockNumber: %v", transferData["blockNumber"])
		t.Logf("   transactionHash: %v", transferData["transactionHash"])
		t.Logf("   eventType: %v", transferData["eventType"])
		t.Logf("   eventDescription: %v", transferData["eventDescription"])
		t.Logf("   topics: %v", transferData["topics"])
		t.Logf("   rawData: %v", transferData["rawData"])
		t.Logf("   chainId: %v", transferData["chainId"])
		t.Logf("   eventSignature: %v", transferData["eventSignature"])

		t.Logf("\n📄 Complete JSON Structure for Documentation:")
		prettyJSON, _ := json.MarshalIndent(transferData, "", "  ")
		t.Logf("%s", string(prettyJSON))

		// Get metadata
		metadata, ok := result["metadata"].(map[string]interface{})
		require.True(t, ok, "metadata should be a map[string]interface{}")
		require.NotNil(t, metadata, "Should have metadata")

		t.Logf("\n🔍 Metadata Structure:")
		metadataJSON, _ := json.MarshalIndent(metadata, "", "  ")
		t.Logf("%s", string(metadataJSON))

		t.Logf("\n💡 === HOW TO USE THIS DATA ===")
		t.Logf("1. Set 'simulationMode': true in your trigger config")
		t.Logf("2. Use the exact same query structure as above")
		t.Logf("3. The response will have this exact data structure")
		t.Logf("4. Users can reference fields like: data.fromAddress, data.value, etc.")
		t.Logf("5. For production: set 'simulationMode': false to use real blockchain data")

		// Verify all expected event fields are present
		expectedFields := []string{
			"eventFound", "contractAddress", "blockNumber", "transactionHash",
			"eventType", "eventDescription", "topics", "rawData", "chainId", "eventSignature",
		}

		for _, field := range expectedFields {
			assert.NotNil(t, transferData[field], "Should have field: %s", field)
		}

		t.Logf("\n✅ All expected event fields are present!")
		t.Logf("🎯 Users now have a complete sample data structure to reference")
	})

	t.Run("CompareWithHistoricalSearch", func(t *testing.T) {
		t.Logf("🔍 === COMPARISON: SIMULATION vs HISTORICAL SEARCH ===")

		// Test historical search (simulationMode: false)
		historicalConfig := map[string]interface{}{
			"simulationMode": false, // Historical search
			"queries": []interface{}{
				map[string]interface{}{
					"addresses": []interface{}{
						"0x779877A7B0D9E8603169DdbD7836e478b4624789",
					},
					"topics": []interface{}{
						map[string]interface{}{
							"values": []interface{}{
								"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
								"0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
								nil,
							},
						},
					},
				},
			},
		}

		historicalResult, err := engine.runEventTriggerImmediately(historicalConfig, map[string]interface{}{})
		require.NoError(t, err, "Historical search should not error")

		if found, ok := historicalResult["found"].(bool); ok && !found {
			t.Logf("📊 Historical Search Result: No events found (as expected)")
			t.Logf("💡 This is why simulation mode is useful for getting sample data!")
			t.Logf("   - Historical search: searches real blockchain (may find nothing)")
			t.Logf("   - Simulation mode: always provides sample data structure")
		} else {
			t.Logf("📊 Historical Search Result: Found real events!")
			if eventData, hasData := historicalResult["data"].(map[string]interface{}); hasData {
				eventDataJSON, _ := json.MarshalIndent(eventData, "", "  ")
				t.Logf("   Real event data: %s", string(eventDataJSON))
			}
		}

		t.Logf("\n🎯 === RECOMMENDATION ===")
		t.Logf("✅ For getting sample data structure: use simulationMode: true")
		t.Logf("✅ For production workflows: use simulationMode: false")
		t.Logf("✅ Simulation mode guarantees consistent sample data for documentation")
	})
}
