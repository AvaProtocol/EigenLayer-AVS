package trigger

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
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

			// Create a mock Check with pre-parsed ABI for testing
			check := &Check{
				Queries:    []*avsproto.EventTrigger_Query{query},
				ParsedABIs: make(map[int]*abi.ABI),
			}

			// Convert Check to EventTaskData for the new API
			eventData := &EventTaskData{
				Queries:    check.Queries,
				ParsedABIs: check.ParsedABIs,
			}

			// Test the conditional filtering
			result := eventTrigger.logMatchesEventQuery(mockLog, query, eventData, 0)

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

	// Create a mock Check for testing
	check := &Check{
		Queries:    []*avsproto.EventTrigger_Query{query},
		ParsedABIs: make(map[int]*abi.ABI),
	}

	// Convert Check to EventTaskData for the new API
	eventData := &EventTaskData{
		Queries:    check.Queries,
		ParsedABIs: check.ParsedABIs,
	}

	result := eventTrigger.logMatchesEventQuery(mockLog, query, eventData, 0)
	assert.True(t, result, "Multiple conditions should pass (price > $2000 AND round > 10000)")
	t.Logf("✅ Multiple conditions test passed")

	// Test case where one condition fails
	query.Conditions[1].Value = "20000" // Round > 20000 (should fail)
	result = eventTrigger.logMatchesEventQuery(mockLog, query, eventData, 0)
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

	// Create a mock Check for testing
	check := &Check{
		Queries:    []*avsproto.EventTrigger_Query{query},
		ParsedABIs: make(map[int]*abi.ABI),
	}

	// Convert Check to EventTaskData for the new API
	eventData := &EventTaskData{
		Queries:    check.Queries,
		ParsedABIs: check.ParsedABIs,
	}

	result := eventTrigger.logMatchesEventQuery(mockLog, query, eventData, 0)
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

	// Create a mock Check for testing
	check := &Check{
		Queries:    []*avsproto.EventTrigger_Query{query},
		ParsedABIs: make(map[int]*abi.ABI),
	}

	// Convert Check to EventTaskData for the new API
	eventData := &EventTaskData{
		Queries:    check.Queries,
		ParsedABIs: check.ParsedABIs,
	}

	result := eventTrigger.logMatchesEventQuery(mockLog, query, eventData, 0)
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

func TestSignedIntegerConditions(t *testing.T) {
	logger := &MockLogger{}

	eventTrigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: logger,
		},
	}

	// Test cases for signed integer handling
	testCases := []struct {
		name        string
		fieldValue  *big.Int
		condition   *avsproto.EventCondition
		shouldMatch bool
		description string
	}{
		{
			name:       "Positive value comparison",
			fieldValue: big.NewInt(1000),
			condition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "gt",
				Value:     "500",
				FieldType: "int256",
			},
			shouldMatch: true,
			description: "Positive 1000 > 500",
		},
		{
			name:       "Negative value comparison - greater than negative",
			fieldValue: big.NewInt(-100),
			condition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "gt",
				Value:     "-200",
				FieldType: "int256",
			},
			shouldMatch: true,
			description: "Negative -100 > -200",
		},
		{
			name:       "Negative value comparison - less than positive",
			fieldValue: big.NewInt(-100),
			condition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "lt",
				Value:     "50",
				FieldType: "int256",
			},
			shouldMatch: true,
			description: "Negative -100 < 50",
		},
		{
			name:       "Negative value equality",
			fieldValue: big.NewInt(-12345),
			condition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "eq",
				Value:     "-12345",
				FieldType: "int256",
			},
			shouldMatch: true,
			description: "Negative -12345 == -12345",
		},
		{
			name:       "Large negative value (close to int256 min)",
			fieldValue: new(big.Int).Neg(new(big.Int).Exp(big.NewInt(2), big.NewInt(255), nil)), // -2^255
			condition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "lt",
				Value:     "0",
				FieldType: "int256",
			},
			shouldMatch: true,
			description: "Very large negative value < 0",
		},
		{
			name:       "Zero comparison",
			fieldValue: big.NewInt(0),
			condition: &avsproto.EventCondition{
				FieldName: "current",
				Operator:  "eq",
				Value:     "0",
				FieldType: "int256",
			},
			shouldMatch: true,
			description: "Zero == 0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create mock log with signed integer value
			mockLog := createSignedIntegerEventLog(tc.fieldValue)

			// Create query with signed integer condition
			query := &avsproto.EventTrigger_Query{
				Addresses: []string{"0x1234567890123456789012345678901234567890"},
				Topics: []*avsproto.EventTrigger_Topics{
					{
						Values: []string{mockLog.Topics[0].Hex()}, // Use actual signature from mock log
					},
				},
				ContractAbi: createSignedIntegerABI(),
				Conditions:  []*avsproto.EventCondition{tc.condition},
			}

			// Create a mock Check
			check := &Check{
				Queries:    []*avsproto.EventTrigger_Query{query},
				ParsedABIs: make(map[int]*abi.ABI),
			}

			// Convert Check to EventTaskData for the new API
			eventData := &EventTaskData{
				Queries:    check.Queries,
				ParsedABIs: check.ParsedABIs,
			}

			// Test the condition evaluation
			result := eventTrigger.logMatchesEventQuery(mockLog, query, eventData, 0)

			if tc.shouldMatch {
				assert.True(t, result, "Expected condition to match for: %s", tc.description)
				t.Logf("✅ %s - Condition matched as expected", tc.description)
			} else {
				assert.False(t, result, "Expected condition to NOT match for: %s", tc.description)
				t.Logf("🚫 %s - Condition correctly rejected", tc.description)
			}
		})
	}
}

func TestTwosComplementConversion(t *testing.T) {
	logger := &MockLogger{}

	eventTrigger := &EventTrigger{
		CommonTrigger: &CommonTrigger{
			logger: logger,
		},
	}

	// Test two's complement conversion directly
	testCases := []struct {
		name           string
		hexValue       string
		expectedSigned string
		description    string
	}{
		{
			name:           "Positive value (no conversion needed)",
			hexValue:       "0x0000000000000000000000000000000000000000000000000000000000000064", // 100
			expectedSigned: "100",
			description:    "Positive 100 should remain 100",
		},
		{
			name:           "Small negative value",
			hexValue:       "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff9c", // -100 in two's complement
			expectedSigned: "-100",
			description:    "Two's complement -100 should be converted correctly",
		},
		{
			name:           "Large negative value",
			hexValue:       "0x8000000000000000000000000000000000000000000000000000000000000000", // -2^255 (most negative int256)
			expectedSigned: "-57896044618658097711785492504343953926634992332820282019728792003956564819968",
			description:    "Most negative int256 value",
		},
		{
			name:           "Negative one",
			hexValue:       "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", // -1 in two's complement
			expectedSigned: "-1",
			description:    "Two's complement -1 should be converted correctly",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a common.Hash from hex value
			hash := common.HexToHash(tc.hexValue)

			// Test the signed integer evaluation directly
			condition := &avsproto.EventCondition{
				FieldName: "testField",
				Operator:  "eq",
				Value:     tc.expectedSigned,
				FieldType: "int256",
			}

			result := eventTrigger.evaluateIntCondition(hash, condition.Operator, condition.Value)

			assert.True(t, result, "Two's complement conversion failed for %s", tc.description)
			t.Logf("✅ %s - Hex %s correctly converted to signed %s", tc.description, tc.hexValue, tc.expectedSigned)
		})
	}
}

// Helper function to create a mock event log with signed integer value
func createSignedIntegerEventLog(value *big.Int) types.Log {
	// Parse the ABI to get the correct event signature
	abiString := createSignedIntegerABI()
	parsedABI, err := abi.JSON(strings.NewReader(abiString))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse test ABI: %v", err))
	}

	// Get the correct event signature
	var eventSignature common.Hash
	for _, event := range parsedABI.Events {
		eventSignature = event.ID
		break // We only have one event in our test ABI
	}

	// Convert signed value to two's complement representation
	var valueHash common.Hash

	if value.Sign() >= 0 {
		// Positive value - direct conversion
		valueBytes := value.Bytes()
		copy(valueHash[32-len(valueBytes):], valueBytes)
	} else {
		// Negative value - convert to two's complement
		// For negative values: two's complement = 2^256 + value
		maxInt256 := new(big.Int)
		maxInt256.Exp(big.NewInt(2), big.NewInt(256), nil)   // 2^256
		twosComplement := new(big.Int).Add(maxInt256, value) // 2^256 + negative_value

		valueBytes := twosComplement.Bytes()
		copy(valueHash[32-len(valueBytes):], valueBytes)
	}

	return types.Log{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		Topics: []common.Hash{
			eventSignature, // Use the correct event signature from ABI
			valueHash,      // The signed integer value in two's complement
		},
		Data:        []byte{},
		BlockNumber: 12345,
		TxHash:      common.HexToHash("0xabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef"),
		Index:       0,
	}
}

// Helper function to create a simple ABI for signed integer testing
func createSignedIntegerABI() string {
	return `[
		{
			"anonymous": false,
			"inputs": [
				{
					"indexed": true,
					"internalType": "int256",
					"name": "current",
					"type": "int256"
				}
			],
			"name": "SignedValueEvent",
			"type": "event"
		}
	]`
}
