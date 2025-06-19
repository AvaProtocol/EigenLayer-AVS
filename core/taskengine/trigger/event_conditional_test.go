package trigger

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Chainlink Price Feed ABI - AnswerUpdated event
const ChainlinkAggregatorABI = `[
  {
    "anonymous": false,
    "inputs": [
      {
        "indexed": true,
        "internalType": "int256",
        "name": "current",
        "type": "int256"
      },
      {
        "indexed": true,
        "internalType": "uint256",
        "name": "roundId",
        "type": "uint256"
      },
      {
        "indexed": false,
        "internalType": "uint256",
        "name": "updatedAt",
        "type": "uint256"
      }
    ],
    "name": "AnswerUpdated",
    "type": "event"
  }
]`

func TestEventTriggerConditionalFiltering(t *testing.T) {
	logger := &MockLogger{}

	// Create EventTrigger instance for testing (without network connections)
	eventTrigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: logger,
		},
	}

	// Define test cases
	testCases := []struct {
		name           string
		priceCondition *avsproto.EventCondition
		mockPrice      *big.Int
		shouldTrigger  bool
		description    string
	}{
		{
			name: "Price above threshold should trigger",
			priceCondition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
			mockPrice:     big.NewInt(250000000000), // $2500 with 8 decimals
			shouldTrigger: true,
			description:   "ETH price went above $2000 threshold",
		},
		{
			name: "Price below threshold should not trigger",
			priceCondition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
			mockPrice:     big.NewInt(150000000000), // $1500 with 8 decimals
			shouldTrigger: false,
			description:   "ETH price is below $2000 threshold",
		},
		{
			name: "Exact price match should trigger",
			priceCondition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "eq",
				Value:     "200000000000", // $2000 with 8 decimals
				FieldType: "int256",
			},
			mockPrice:     big.NewInt(200000000000), // Exactly $2000
			shouldTrigger: true,
			description:   "ETH price exactly matches $2000",
		},
		{
			name: "Price drop below threshold",
			priceCondition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "lt",
				Value:     "180000000000", // $1800 with 8 decimals
				FieldType: "int256",
			},
			mockPrice:     big.NewInt(175000000000), // $1750 with 8 decimals
			shouldTrigger: true,
			description:   "ETH price dropped below $1800",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create mock Chainlink AnswerUpdated event log
			mockLog := createChainlinkAnswerUpdatedLog(tc.mockPrice)

			// Create EventTrigger query with conditional filtering
			query := &avsproto.EventTrigger_Query{
				Addresses: []string{"0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419"}, // ETH/USD price feed
				Topics: []*avsproto.EventTrigger_Topics{
					{
						Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"}, // AnswerUpdated signature
					},
				},
				ContractAbi: ChainlinkAggregatorABI,
				Conditions:  []*avsproto.EventCondition{tc.priceCondition},
			}

			// Test the conditional filtering
			result := eventTrigger.logMatchesEventQuery(mockLog, query)

			if tc.shouldTrigger {
				assert.True(t, result, "Expected condition to match for: %s", tc.description)
				t.Logf("✅ %s - Condition matched as expected", tc.description)
			} else {
				assert.False(t, result, "Expected condition to NOT match for: %s", tc.description)
				t.Logf("🚫 %s - Condition correctly rejected", tc.description)
			}
		})
	}
}

func TestEventTriggerMultipleConditions(t *testing.T) {
	logger := &MockLogger{}

	eventTrigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: logger,
		},
	}

	// Test multiple conditions (AND logic)
	mockLog := createChainlinkAnswerUpdatedLogWithRound(big.NewInt(250000000000), big.NewInt(12345))

	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419"},
		Topics: []*avsproto.EventTrigger_Topics{
			{
				Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
			},
		},
		ContractAbi: ChainlinkAggregatorABI,
		Conditions: []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000", // Price > $2000
				FieldType: "int256",
			},
			{
				FieldName: "roundId",
				Operator:  "gt",
				Value:     "10000", // Round > 10000
				FieldType: "uint256",
			},
		},
	}

	result := eventTrigger.logMatchesEventQuery(mockLog, query)
	assert.True(t, result, "Multiple conditions should pass (price > $2000 AND round > 10000)")
	t.Logf("✅ Multiple conditions test passed")

	// Test case where one condition fails
	query.Conditions[1].Value = "20000" // Round > 20000 (should fail)
	result = eventTrigger.logMatchesEventQuery(mockLog, query)
	assert.False(t, result, "Should fail when one condition doesn't match")
	t.Logf("🚫 Multiple conditions correctly rejected when one fails")
}

func TestEventTriggerWithoutConditions(t *testing.T) {
	logger := &MockLogger{}

	eventTrigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: logger,
		},
	}

	mockLog := createChainlinkAnswerUpdatedLog(big.NewInt(250000000000))

	// Query without conditions (should work like before)
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419"},
		Topics: []*avsproto.EventTrigger_Topics{
			{
				Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
			},
		},
		// No ContractAbi or Conditions - should default to basic filtering
	}

	result := eventTrigger.logMatchesEventQuery(mockLog, query)
	assert.True(t, result, "Should match when no conditions are specified (backward compatibility)")
	t.Logf("✅ Backward compatibility test passed")
}

func TestEventTriggerInvalidABI(t *testing.T) {
	logger := &MockLogger{}

	eventTrigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: logger,
		},
	}

	mockLog := createChainlinkAnswerUpdatedLog(big.NewInt(250000000000))

	// Query with invalid ABI
	query := &avsproto.EventTrigger_Query{
		Addresses: []string{"0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419"},
		Topics: []*avsproto.EventTrigger_Topics{
			{
				Values: []string{"0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"},
			},
		},
		ContractAbi: "invalid json abi",
		Conditions: []*avsproto.EventCondition{
			{
				FieldName: "current",
				Operator:  "gt",
				Value:     "200000000000",
				FieldType: "int256",
			},
		},
	}

	result := eventTrigger.logMatchesEventQuery(mockLog, query)
	assert.False(t, result, "Should fail gracefully with invalid ABI")
	t.Logf("✅ Invalid ABI handled gracefully")
}

// Helper function to create a mock Chainlink AnswerUpdated event log
func createChainlinkAnswerUpdatedLog(price *big.Int) types.Log {
	return createChainlinkAnswerUpdatedLogWithRound(price, big.NewInt(12345))
}

// Helper function to create a mock Chainlink AnswerUpdated event log with custom round
func createChainlinkAnswerUpdatedLogWithRound(price *big.Int, roundId *big.Int) types.Log {
	// AnswerUpdated event signature: AnswerUpdated(int256 indexed current, uint256 indexed roundId, uint256 updatedAt)
	eventSignature := common.HexToHash("0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f")

	// Convert price to 32-byte hash (indexed parameter)
	priceHash := common.BytesToHash(common.LeftPadBytes(price.Bytes(), 32))

	// Convert roundId to 32-byte hash (indexed parameter)
	roundIdHash := common.BytesToHash(common.LeftPadBytes(roundId.Bytes(), 32))

	// updatedAt is non-indexed, so it goes in the data field
	updatedAt := big.NewInt(time.Now().Unix())
	updatedAtBytes := common.LeftPadBytes(updatedAt.Bytes(), 32)

	return types.Log{
		Address: common.HexToAddress("0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419"), // ETH/USD Chainlink feed
		Topics: []common.Hash{
			eventSignature, // Event signature
			priceHash,      // current (indexed)
			roundIdHash,    // roundId (indexed)
		},
		Data:        updatedAtBytes, // updatedAt (non-indexed)
		BlockNumber: 12345,
		TxHash:      common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Index:       0,
	}
}

func TestChainlinkPriceFeedIntegration(t *testing.T) {
	t.Logf("🔗 Testing Chainlink Price Feed Integration")

	// Example usage scenario: Trigger when ETH price goes above $2500
	testPrice := big.NewInt(270000000000) // $2700 with 8 decimals
	mockLog := createChainlinkAnswerUpdatedLog(testPrice)

	// Print details for manual verification
	t.Logf("📊 Mock Event Details:")
	t.Logf("   Contract: %s (ETH/USD Chainlink Feed)", mockLog.Address.Hex())
	t.Logf("   Event Signature: %s", mockLog.Topics[0].Hex())
	t.Logf("   Price (indexed): %s", mockLog.Topics[1].Hex())
	t.Logf("   Round ID (indexed): %s", mockLog.Topics[2].Hex())
	t.Logf("   Updated At (data): %s", common.BytesToHash(mockLog.Data).Hex())

	// Convert back to verify
	priceFromLog := new(big.Int).SetBytes(mockLog.Topics[1].Bytes())
	t.Logf("   Decoded Price: $%.2f", float64(priceFromLog.Int64())/1e8)

	assert.Equal(t, testPrice.String(), priceFromLog.String(), "Price should be preserved correctly")
	t.Logf("✅ Chainlink integration test completed successfully")
}
