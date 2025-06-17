package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
)

func TestBuildEventTriggerOutputDefensiveProgramming(t *testing.T) {
	tests := []struct {
		name           string
		input          map[string]interface{}
		expectedResult bool
		description    string
	}{
		{
			name:           "Nil input",
			input:          nil,
			expectedResult: true,
			description:    "Should return empty EventTrigger_Output for nil input",
		},
		{
			name:           "Empty map",
			input:          map[string]interface{}{},
			expectedResult: true,
			description:    "Should return empty EventTrigger_Output for empty map",
		},
		{
			name: "Invalid types for all fields",
			input: map[string]interface{}{
				"found":        "not_a_bool",
				"transfer_log": "not_a_map",
				"evm_log":      12345,
			},
			expectedResult: true,
			description:    "Should handle invalid types gracefully",
		},
		{
			name: "Found=false",
			input: map[string]interface{}{
				"found": false,
			},
			expectedResult: true,
			description:    "Should return empty output when found=false",
		},
		{
			name: "Found=true but no event data",
			input: map[string]interface{}{
				"found": true,
			},
			expectedResult: true,
			description:    "Should return empty output when found=true but no event data",
		},
		{
			name: "Found=true with invalid transfer_log",
			input: map[string]interface{}{
				"found":        true,
				"transfer_log": "invalid_type",
			},
			expectedResult: true,
			description:    "Should handle invalid transfer_log type gracefully",
		},
		{
			name: "Found=true with valid transfer_log structure",
			input: map[string]interface{}{
				"found": true,
				"transfer_log": map[string]interface{}{
					"tokenName":        "TestToken",
					"tokenSymbol":      "TEST",
					"tokenDecimals":    uint32(18),
					"transactionHash":  "0x123",
					"address":          "0x456",
					"blockNumber":      uint64(12345),
					"blockTimestamp":   uint64(1234567890),
					"fromAddress":      "0x789",
					"toAddress":        "0xabc",
					"value":            "1000000000000000000",
					"valueFormatted":   "1.0",
					"transactionIndex": uint32(5),
					"logIndex":         uint32(2),
				},
			},
			expectedResult: true,
			description:    "Should properly parse valid transfer_log data",
		},
		{
			name: "Found=true with valid evm_log structure",
			input: map[string]interface{}{
				"found": true,
				"evm_log": map[string]interface{}{
					"address":          "0x123",
					"topics":           []string{"0xtopic1", "0xtopic2"},
					"data":             "0xdata",
					"blockNumber":      uint64(12345),
					"transactionHash":  "0x456",
					"transactionIndex": uint32(3),
					"blockHash":        "0x789",
					"index":            uint32(1),
					"removed":          false,
				},
			},
			expectedResult: true,
			description:    "Should properly parse valid evm_log data",
		},
		{
			name: "Found=true with transfer_log containing wrong types",
			input: map[string]interface{}{
				"found": true,
				"transfer_log": map[string]interface{}{
					"tokenName":       12345,
					"tokenSymbol":     true,
					"tokenDecimals":   "18",
					"transactionHash": nil,
					"blockNumber":     "not_a_number",
				},
			},
			expectedResult: true,
			description:    "Should handle wrong types in transfer_log gracefully",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := buildEventTriggerOutput(test.input)

			require.NotNil(t, result, "buildEventTriggerOutput should never return nil")

			require.IsType(t, &avsproto.EventTrigger_Output{}, result, "Should return correct type")

			t.Logf("✅ %s: %s", test.name, test.description)

			if test.name == "Found=true with valid transfer_log structure" {
				require.NotNil(t, result.GetTransferLog(), "TransferLog should be populated")
				transferLog := result.GetTransferLog()
				require.Equal(t, "TestToken", transferLog.TokenName)
				require.Equal(t, "TEST", transferLog.TokenSymbol)
				require.Equal(t, uint32(18), transferLog.TokenDecimals)
				require.Equal(t, "0x123", transferLog.TransactionHash)
			} else if test.name == "Found=true with valid evm_log structure" {
				require.NotNil(t, result.GetEvmLog(), "EvmLog should be populated")
				evmLog := result.GetEvmLog()
				require.Equal(t, "0x123", evmLog.Address)
				require.Equal(t, []string{"0xtopic1", "0xtopic2"}, evmLog.Topics)
				require.Equal(t, "0xdata", evmLog.Data)
				require.Equal(t, uint64(12345), evmLog.BlockNumber)
			} else if test.name == "Found=true with transfer_log containing wrong types" {
				require.NotNil(t, result.GetTransferLog(), "TransferLog should be created for malformed transfer_log data")
				transferLog := result.GetTransferLog()
				require.Equal(t, "", transferLog.TokenName, "TokenName should be empty for wrong type")
				require.Equal(t, "", transferLog.TokenSymbol, "TokenSymbol should be empty for wrong type")
				require.Equal(t, uint32(0), transferLog.TokenDecimals, "TokenDecimals should be 0 for wrong type")
			} else {
				require.Nil(t, result.GetTransferLog(), "TransferLog should be nil for invalid/empty input")
				require.Nil(t, result.GetEvmLog(), "EvmLog should be nil for invalid/empty input")
			}
		})
	}
}

func TestBuildEventTriggerOutputConsistencyWithOtherTriggerFunctions(t *testing.T) {
	t.Run("Nil handling consistency", func(t *testing.T) {
		eventResult := buildEventTriggerOutput(nil)
		blockResult := buildBlockTriggerOutput(nil)
		fixedTimeResult := buildFixedTimeTriggerOutput(nil)
		cronResult := buildCronTriggerOutput(nil)
		manualResult := buildManualTriggerOutput(nil)

		require.NotNil(t, eventResult, "buildEventTriggerOutput should handle nil gracefully")
		require.NotNil(t, blockResult, "buildBlockTriggerOutput should handle nil gracefully")
		require.NotNil(t, fixedTimeResult, "buildFixedTimeTriggerOutput should handle nil gracefully")
		require.NotNil(t, cronResult, "buildCronTriggerOutput should handle nil gracefully")
		require.NotNil(t, manualResult, "buildManualTriggerOutput should handle nil gracefully")

		t.Log("✅ All trigger build functions consistently handle nil inputs")
	})

	t.Run("Empty map handling consistency", func(t *testing.T) {
		emptyMap := map[string]interface{}{}

		eventResult := buildEventTriggerOutput(emptyMap)
		blockResult := buildBlockTriggerOutput(emptyMap)
		fixedTimeResult := buildFixedTimeTriggerOutput(emptyMap)
		cronResult := buildCronTriggerOutput(emptyMap)
		manualResult := buildManualTriggerOutput(emptyMap)

		require.NotNil(t, eventResult, "buildEventTriggerOutput should handle empty map gracefully")
		require.NotNil(t, blockResult, "buildBlockTriggerOutput should handle empty map gracefully")
		require.NotNil(t, fixedTimeResult, "buildFixedTimeTriggerOutput should handle empty map gracefully")
		require.NotNil(t, cronResult, "buildCronTriggerOutput should handle empty map gracefully")
		require.NotNil(t, manualResult, "buildManualTriggerOutput should handle empty map gracefully")

		require.Equal(t, uint64(0), blockResult.BlockNumber, "BlockTrigger should have default block number")
		require.Equal(t, uint64(0), fixedTimeResult.Timestamp, "FixedTimeTrigger should have default timestamp")
		require.Equal(t, uint64(0), cronResult.Timestamp, "CronTrigger should have default timestamp")
		require.Greater(t, manualResult.RunAt, uint64(0), "ManualTrigger should have current timestamp as default")

		t.Log("✅ All trigger build functions consistently handle empty maps with appropriate defaults")
	})
}

func TestBuildTriggerDataMapFromProtobufEventTriggerComprehensive(t *testing.T) {
	tests := []struct {
		name        string
		input       interface{}
		description string
		verifyFunc  func(t *testing.T, result map[string]interface{})
	}{
		{
			name:        "Nil EventTrigger_Output",
			input:       nil,
			description: "Should handle nil input gracefully and add trigger type",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				require.Equal(t, "TRIGGER_TYPE_EVENT", result["type"], "Should add trigger type for nil input")
				require.Len(t, result, 1, "Should only contain type field for nil input")
			},
		},
		{
			name: "EventTrigger_Output with complete TransferLog",
			input: &avsproto.EventTrigger_Output{
				OutputType: &avsproto.EventTrigger_Output_TransferLog{
					TransferLog: &avsproto.EventTrigger_TransferLogOutput{
						TokenName:        "Test Token",
						TokenSymbol:      "TEST",
						TokenDecimals:    18,
						TransactionHash:  "0x1234567890abcdef",
						Address:          "0xabcdef1234567890",
						BlockNumber:      12345678,
						BlockTimestamp:   1672531200,
						FromAddress:      "0x1111111111111111",
						ToAddress:        "0x2222222222222222",
						Value:            "1000000000000000000",
						ValueFormatted:   "1.0",
						TransactionIndex: 5,
						LogIndex:         3,
					},
				},
			},
			description: "Should map all TransferLog fields including the critical log_index field",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"tokenName":        "Test Token",
					"tokenSymbol":      "TEST",
					"tokenDecimals":    uint32(18),
					"transactionHash":  "0x1234567890abcdef",
					"address":          "0xabcdef1234567890",
					"blockNumber":      uint64(12345678),
					"blockTimestamp":   uint64(1672531200),
					"fromAddress":      "0x1111111111111111",
					"toAddress":        "0x2222222222222222",
					"value":            "1000000000000000000",
					"valueFormatted":   "1.0",
					"transactionIndex": uint32(5),
					"logIndex":         uint32(3),
					"type":             "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "All TransferLog fields should be properly mapped")

				require.Contains(t, result, "logIndex", "logIndex field should be present")
				require.Equal(t, uint32(3), result["logIndex"], "logIndex should have correct value")
			},
		},
		{
			name: "EventTrigger_Output with complete EvmLog",
			input: &avsproto.EventTrigger_Output{
				OutputType: &avsproto.EventTrigger_Output_EvmLog{
					EvmLog: &avsproto.Evm_Log{
						Address:          "0xabcdef1234567890",
						Topics:           []string{"0xtopic1", "0xtopic2", "0xtopic3"},
						Data:             "0xdeadbeef",
						BlockNumber:      12345678,
						TransactionHash:  "0x1234567890abcdef",
						TransactionIndex: 5,
						BlockHash:        "0xblockhash123456",
						Index:            3,
						Removed:          false,
					},
				},
			},
			description: "Should map all EvmLog fields including log_index",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"blockNumber":      uint64(12345678),
					"logIndex":         uint32(3),
					"transactionHash":  "0x1234567890abcdef",
					"address":          "0xabcdef1234567890",
					"topics":           []string{"0xtopic1", "0xtopic2", "0xtopic3"},
					"data":             "0xdeadbeef",
					"blockHash":        "0xblockhash123456",
					"transactionIndex": uint32(5),
					"removed":          false,
					"type":             "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "All EvmLog fields should be properly mapped")

				require.Contains(t, result, "logIndex", "logIndex field should be present")
				require.Equal(t, uint32(3), result["logIndex"], "logIndex should have correct value")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_EVENT, test.input, nil)

			require.NotNil(t, result, "buildTriggerDataMapFromProtobuf should never return nil")

			test.verifyFunc(t, result)

			t.Logf("✅ %s: %s", test.name, test.description)
		})
	}
}

func TestBuildTriggerDataMapFromProtobufFieldCompleteness(t *testing.T) {
	t.Run("All trigger types should add type field", func(t *testing.T) {
		triggerTypes := []avsproto.TriggerType{
			avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
			avsproto.TriggerType_TRIGGER_TYPE_CRON,
			avsproto.TriggerType_TRIGGER_TYPE_EVENT,
			avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
			avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		}

		for _, triggerType := range triggerTypes {
			result := buildTriggerDataMapFromProtobuf(triggerType, nil, nil)
			require.Contains(t, result, "type", "All trigger types should include type field")
			require.Equal(t, triggerType.String(), result["type"], "Type field should match trigger type")
		}

		t.Log("✅ All trigger types consistently add type field")
	})
}
