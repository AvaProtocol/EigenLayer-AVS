package taskengine

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// AAVE Supply event ABI for testing
const aaveSupplyEventABI = `[
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "reserve", "type": "address"},
			{"indexed": false, "internalType": "address", "name": "user", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "onBehalfOf", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "amount", "type": "uint256"},
			{"indexed": true, "internalType": "uint16", "name": "referralCode", "type": "uint16"}
		],
		"name": "Supply",
		"type": "event"
	}
]`

// AAVE multi-event ABI for testing (matches the client's 5-event ABI)
const aaveMultiEventABI = `[
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "reserve", "type": "address"},
			{"indexed": false, "internalType": "address", "name": "user", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "onBehalfOf", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "amount", "type": "uint256"},
			{"indexed": true, "internalType": "uint16", "name": "referralCode", "type": "uint16"}
		],
		"name": "Supply",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "reserve", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "user", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "to", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "amount", "type": "uint256"}
		],
		"name": "Withdraw",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "reserve", "type": "address"},
			{"indexed": false, "internalType": "address", "name": "user", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "onBehalfOf", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "amount", "type": "uint256"},
			{"indexed": false, "internalType": "uint8", "name": "interestRateMode", "type": "uint8"},
			{"indexed": false, "internalType": "uint256", "name": "borrowRate", "type": "uint256"},
			{"indexed": true, "internalType": "uint16", "name": "referralCode", "type": "uint16"}
		],
		"name": "Borrow",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "reserve", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "user", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "repayer", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "amount", "type": "uint256"},
			{"indexed": false, "internalType": "bool", "name": "useATokens", "type": "bool"}
		],
		"name": "Repay",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "address", "name": "collateralAsset", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "debtAsset", "type": "address"},
			{"indexed": true, "internalType": "address", "name": "user", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "debtToCover", "type": "uint256"},
			{"indexed": false, "internalType": "uint256", "name": "liquidatedCollateralAmount", "type": "uint256"},
			{"indexed": false, "internalType": "address", "name": "liquidator", "type": "address"},
			{"indexed": false, "internalType": "bool", "name": "receiveAToken", "type": "bool"}
		],
		"name": "LiquidationCall",
		"type": "event"
	}
]`

// helperConvertJSONABIToProtobuf converts a JSON ABI string to protobuf Values
// (simulates what the client sends)
func helperConvertJSONABIToProtobuf(t *testing.T, abiJSON string) []*structpb.Value {
	t.Helper()
	var abiItems []interface{}
	err := json.Unmarshal([]byte(abiJSON), &abiItems)
	require.NoError(t, err, "ABI JSON should be valid")

	values := make([]*structpb.Value, len(abiItems))
	for i, item := range abiItems {
		val, err := structpb.NewValue(item)
		require.NoError(t, err, "Should convert ABI item to protobuf Value")
		values[i] = val
	}
	return values
}

func TestSimulateGenericEvent_ABIAwareEncoding(t *testing.T) {
	logger := testutil.GetLogger()

	tc := &TenderlyClient{
		logger: logger,
	}

	t.Run("AAVE Supply event produces decodable Data", func(t *testing.T) {
		// Compute the Supply event signature: keccak256("Supply(address,address,address,uint256,uint16)")
		supplyEventSig := crypto.Keccak256Hash([]byte("Supply(address,address,address,uint256,uint16)"))

		abiValues := helperConvertJSONABIToProtobuf(t, aaveSupplyEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics: []string{
				supplyEventSig.Hex(),
				"", // reserve (wildcard)
				"0x0000000000000000000000005d814cc9e94b2656f59ee439d44aa1b6ca21434f", // onBehalfOf
				"", // referralCode (wildcard)
			},
			ContractAbi: abiValues,
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)
		require.NotNil(t, simulatedLog)

		// The Data field should NOT be all zeros now — it should have ABI-encoded non-indexed params
		// Supply has 2 non-indexed params: user (address) + amount (uint256)
		// ABI encoding: 32 bytes (address padded) + 32 bytes (uint256) = 64 bytes
		assert.Equal(t, 64, len(simulatedLog.Data),
			"Data should be 64 bytes (2 non-indexed params: address + uint256)")

		// Verify the data is decodable with the ABI
		parsedABI, err := abi.JSON(strings.NewReader(aaveSupplyEventABI))
		require.NoError(t, err)

		decodedData, err := parsedABI.Unpack("Supply", simulatedLog.Data)
		require.NoError(t, err, "ABI Unpack should succeed on the generated data")
		require.Len(t, decodedData, 2, "Should decode 2 non-indexed params (user, amount)")

		// Verify decoded types
		_, ok := decodedData[0].(common.Address) // user
		assert.True(t, ok, "First non-indexed param (user) should decode as address")

		_, ok = decodedData[1].(*big.Int) // amount
		assert.True(t, ok, "Second non-indexed param (amount) should decode as *big.Int")

		t.Logf("✅ Supply event simulation produces decodable data")
		t.Logf("   Data length: %d bytes", len(simulatedLog.Data))
		t.Logf("   Decoded user: %v", decodedData[0])
		t.Logf("   Decoded amount: %v", decodedData[1])
	})

	t.Run("AAVE Borrow event with multiple non-indexed params", func(t *testing.T) {
		// Borrow(address indexed reserve, address user, address indexed onBehalfOf, uint256 amount, uint8 interestRateMode, uint256 borrowRate, uint16 indexed referralCode)
		// Non-indexed: user (address), amount (uint256), interestRateMode (uint8), borrowRate (uint256)
		borrowEventSig := crypto.Keccak256Hash([]byte("Borrow(address,address,address,uint256,uint8,uint256,uint16)"))

		abiValues := helperConvertJSONABIToProtobuf(t, aaveMultiEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{borrowEventSig.Hex()},
			ContractAbi: abiValues,
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)

		// 4 non-indexed params: address(32) + uint256(32) + uint8(32) + uint256(32) = 128 bytes
		assert.Equal(t, 128, len(simulatedLog.Data),
			"Data should be 128 bytes (4 non-indexed params)")

		parsedABI, err := abi.JSON(strings.NewReader(aaveMultiEventABI))
		require.NoError(t, err)

		decodedData, err := parsedABI.Unpack("Borrow", simulatedLog.Data)
		require.NoError(t, err, "ABI Unpack should succeed for Borrow event")
		require.Len(t, decodedData, 4, "Should decode 4 non-indexed params")

		t.Logf("✅ Borrow event simulation produces decodable data with %d non-indexed params", len(decodedData))
	})

	t.Run("AAVE Repay event with bool non-indexed param", func(t *testing.T) {
		// Repay(address indexed reserve, address indexed user, address indexed repayer, uint256 amount, bool useATokens)
		// Non-indexed: amount (uint256), useATokens (bool)
		repayEventSig := crypto.Keccak256Hash([]byte("Repay(address,address,address,uint256,bool)"))

		abiValues := helperConvertJSONABIToProtobuf(t, aaveMultiEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{repayEventSig.Hex()},
			ContractAbi: abiValues,
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)

		// 2 non-indexed params: uint256(32) + bool(32) = 64 bytes
		assert.Equal(t, 64, len(simulatedLog.Data),
			"Data should be 64 bytes (2 non-indexed params: uint256 + bool)")

		parsedABI, err := abi.JSON(strings.NewReader(aaveMultiEventABI))
		require.NoError(t, err)

		decodedData, err := parsedABI.Unpack("Repay", simulatedLog.Data)
		require.NoError(t, err, "ABI Unpack should succeed for Repay event")
		require.Len(t, decodedData, 2, "Should decode 2 non-indexed params (amount, useATokens)")

		_, ok := decodedData[1].(bool)
		assert.True(t, ok, "useATokens should decode as bool")

		t.Logf("✅ Repay event simulation produces decodable data")
	})

	t.Run("Event with all indexed params produces empty Data", func(t *testing.T) {
		// Withdraw(address indexed reserve, address indexed user, address indexed to, uint256 amount)
		// Wait — Withdraw has one non-indexed (amount), so test with a custom all-indexed event
		allIndexedABI := `[{
			"anonymous": false,
			"inputs": [
				{"indexed": true, "internalType": "address", "name": "from", "type": "address"},
				{"indexed": true, "internalType": "address", "name": "to", "type": "address"}
			],
			"name": "Approval",
			"type": "event"
		}]`

		approvalSig := crypto.Keccak256Hash([]byte("Approval(address,address)"))
		abiValues := helperConvertJSONABIToProtobuf(t, allIndexedABI)

		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{approvalSig.Hex()},
			ContractAbi: abiValues,
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)

		assert.Empty(t, simulatedLog.Data, "Data should be empty when all params are indexed")

		t.Logf("✅ All-indexed event correctly produces empty Data")
	})

	t.Run("Fallback to empty data when no ABI provided", func(t *testing.T) {
		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{"0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"},
			ContractAbi: nil, // No ABI
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)

		// Should fall back to 32 zero bytes (backward compatible)
		assert.Equal(t, 32, len(simulatedLog.Data), "Should fallback to 32 zero bytes without ABI")

		t.Logf("✅ No-ABI fallback returns 32 zero bytes")
	})

	t.Run("Fallback when event signature does not match ABI", func(t *testing.T) {
		// Provide AAVE ABI but use a topic signature that doesn't match any event in it
		abiValues := helperConvertJSONABIToProtobuf(t, aaveSupplyEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{"0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
			ContractAbi: abiValues,
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)

		// Should fall back to 32 zero bytes
		assert.Equal(t, 32, len(simulatedLog.Data), "Should fallback to 32 zero bytes when sig doesn't match ABI")

		t.Logf("✅ Mismatched signature fallback returns 32 zero bytes")
	})
}

func TestSimulateGenericEvent_EndToEnd_SharedEnrichment(t *testing.T) {
	logger := testutil.GetLogger()

	tc := &TenderlyClient{
		logger: logger,
	}

	t.Run("Supply event log is decodable by shared enrichment", func(t *testing.T) {
		supplyEventSig := crypto.Keccak256Hash([]byte("Supply(address,address,address,uint256,uint16)"))

		abiValues := helperConvertJSONABIToProtobuf(t, aaveSupplyEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses: []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics: []string{
				supplyEventSig.Hex(),
				"", // reserve (wildcard)
				"0x0000000000000000000000005d814cc9e94b2656f59ee439d44aa1b6ca21434f",
				"", // referralCode (wildcard)
			},
			ContractAbi: abiValues,
		}

		ctx := context.Background()
		simulatedLog, err := tc.simulateGenericEvent(ctx, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5", query, 8453)
		require.NoError(t, err)

		// Now feed the simulated log through the shared enrichment pipeline
		enrichmentParams := SharedEventEnrichmentParams{
			EventLog:    simulatedLog,
			ContractABI: abiValues,
			Query:       query,
			Logger:      logger,
			ChainID:     8453,
		}

		result, err := EnrichEventWithTokenMetadata(enrichmentParams)
		require.NoError(t, err, "Shared enrichment should succeed with ABI-encoded data")

		// The result should have structured event data, not raw log data
		assert.NotNil(t, result.ParsedData, "ParsedData should not be nil")
		assert.Equal(t, "Supply", result.EventName, "Should identify as Supply event")
		assert.False(t, result.IsTransferEvent, "Should not be identified as Transfer")

		// Check that the parsed data has the Supply event with named fields
		supplyData, exists := result.ParsedData["Supply"]
		assert.True(t, exists, "ParsedData should contain 'Supply' key")

		if supplyMap, ok := supplyData.(map[string]interface{}); ok {
			// Verify named fields exist
			_, hasUser := supplyMap["user"]
			_, hasAmount := supplyMap["amount"]
			assert.True(t, hasUser, "Supply data should have 'user' field")
			assert.True(t, hasAmount, "Supply data should have 'amount' field")

			t.Logf("✅ Supply event enrichment produced named fields:")
			for k, v := range supplyMap {
				t.Logf("   %s: %v (%T)", k, v, v)
			}
		} else {
			t.Errorf("Supply data should be a map, got %T", supplyData)
		}

		// Raw event data should also be populated
		assert.NotEmpty(t, result.RawEventData, "RawEventData should be populated")
		assert.Equal(t, "0xA238Dd80C259a72e81d7e4664a9801593F98d1c5",
			result.RawEventData["address"], "RawEventData should have contract address")
	})
}

func TestGenerateSampleValue(t *testing.T) {
	tests := []struct {
		name     string
		abiType  string
		validate func(t *testing.T, val interface{})
	}{
		{
			name:    "uint256",
			abiType: "uint256",
			validate: func(t *testing.T, val interface{}) {
				bigVal, ok := val.(*big.Int)
				require.True(t, ok, "uint256 should produce *big.Int")
				assert.True(t, bigVal.Sign() > 0, "uint256 sample should be positive")
			},
		},
		{
			name:    "uint8",
			abiType: "uint8",
			validate: func(t *testing.T, val interface{}) {
				_, ok := val.(uint8)
				assert.True(t, ok, "uint8 should produce uint8")
			},
		},
		{
			name:    "address",
			abiType: "address",
			validate: func(t *testing.T, val interface{}) {
				addr, ok := val.(common.Address)
				assert.True(t, ok, "address should produce common.Address")
				assert.NotEqual(t, common.Address{}, addr, "address should not be zero")
			},
		},
		{
			name:    "bool",
			abiType: "bool",
			validate: func(t *testing.T, val interface{}) {
				_, ok := val.(bool)
				assert.True(t, ok, "bool should produce bool")
			},
		},
		{
			name:    "int256",
			abiType: "int256",
			validate: func(t *testing.T, val interface{}) {
				bigVal, ok := val.(*big.Int)
				require.True(t, ok, "int256 should produce *big.Int")
				assert.True(t, bigVal.Sign() > 0, "int256 sample should be positive")
			},
		},
		{
			name:    "bytes32",
			abiType: "bytes32",
			validate: func(t *testing.T, val interface{}) {
				arr, ok := val.([32]byte)
				assert.True(t, ok, "bytes32 should produce [32]byte, got %T", val)
				_ = arr
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Parse the type using go-ethereum's ABI type parser
			abiType, err := abi.NewType(tc.abiType, "", nil)
			require.NoError(t, err)

			val := generateSampleValue(abiType)
			require.NotNil(t, val)
			tc.validate(t, val)

			// Verify the value can be ABI-packed
			args := abi.Arguments{{Type: abiType}}
			packed, err := args.Pack(val)
			assert.NoError(t, err, "Sample value should be ABI-packable")
			assert.True(t, len(packed) > 0, "Packed data should not be empty")

			t.Logf("✅ %s: value=%v, packed_len=%d", tc.name, val, len(packed))
		})
	}
}

// TestBuildABIEncodedEventData_Integration tests the full pipeline from query to encoded data
func TestBuildABIEncodedEventData_Integration(t *testing.T) {
	logger := testutil.GetLogger()
	tc := &TenderlyClient{logger: logger}

	t.Run("LiquidationCall with 4 non-indexed params", func(t *testing.T) {
		// LiquidationCall(address indexed collateralAsset, address indexed debtAsset, address indexed user,
		//                  uint256 debtToCover, uint256 liquidatedCollateralAmount, address liquidator, bool receiveAToken)
		liquidationSig := crypto.Keccak256Hash([]byte("LiquidationCall(address,address,address,uint256,uint256,address,bool)"))

		abiValues := helperConvertJSONABIToProtobuf(t, aaveMultiEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{liquidationSig.Hex()},
			ContractAbi: abiValues,
		}

		topics := []common.Hash{liquidationSig}
		data := tc.buildABIEncodedEventData(query, topics)

		// 4 non-indexed: uint256(32) + uint256(32) + address(32) + bool(32) = 128 bytes
		assert.Equal(t, 128, len(data),
			"LiquidationCall should produce 128 bytes for 4 non-indexed params")

		// Verify decodability
		parsedABI, err := abi.JSON(strings.NewReader(aaveMultiEventABI))
		require.NoError(t, err)

		decoded, err := parsedABI.Unpack("LiquidationCall", data)
		require.NoError(t, err, "Should successfully unpack LiquidationCall data")
		assert.Len(t, decoded, 4, "Should decode 4 non-indexed params")

		t.Logf("✅ LiquidationCall: %d non-indexed params decoded successfully", len(decoded))
		for i, v := range decoded {
			t.Logf("   param[%d]: %v (%T)", i, v, v)
		}
	})

	t.Run("Withdraw with 1 non-indexed param", func(t *testing.T) {
		// Withdraw(address indexed reserve, address indexed user, address indexed to, uint256 amount)
		withdrawSig := crypto.Keccak256Hash([]byte("Withdraw(address,address,address,uint256)"))

		abiValues := helperConvertJSONABIToProtobuf(t, aaveMultiEventABI)

		query := &avsproto.EventTrigger_Query{
			Addresses:   []string{"0xA238Dd80C259a72e81d7e4664a9801593F98d1c5"},
			Topics:      []string{withdrawSig.Hex()},
			ContractAbi: abiValues,
		}

		topics := []common.Hash{withdrawSig}
		data := tc.buildABIEncodedEventData(query, topics)

		// 1 non-indexed: uint256(32) = 32 bytes
		assert.Equal(t, 32, len(data),
			"Withdraw should produce 32 bytes for 1 non-indexed param")

		parsedABI, err := abi.JSON(strings.NewReader(aaveMultiEventABI))
		require.NoError(t, err)

		decoded, err := parsedABI.Unpack("Withdraw", data)
		require.NoError(t, err, "Should successfully unpack Withdraw data")
		assert.Len(t, decoded, 1, "Should decode 1 non-indexed param (amount)")

		t.Logf("✅ Withdraw: decoded amount = %v", decoded[0])
	})
}
