package taskengine

import (
	"encoding/json"
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
				"found": "not_a_bool",
				"data":  12345,
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
			name: "Found=true with invalid data type",
			input: map[string]interface{}{
				"found": true,
				"data":  12345,
			},
			expectedResult: true,
			description:    "Should handle invalid data type gracefully",
		},
		{
			name: "Found=true with valid JSON data",
			input: map[string]interface{}{
				"found": true,
				"data": `{
					"tokenName": "TestToken",
					"tokenSymbol": "TEST",
					"tokenDecimals": 18,
					"transactionHash": "0x123",
					"address": "0x456",
					"blockNumber": 12345,
					"blockTimestamp": 1234567890,
					"fromAddress": "0x789",
					"toAddress": "0xabc",
					"value": "1000000000000000000",
					"valueFormatted": "1.0",
					"transactionIndex": 5,
					"logIndex": 2
				}`,
			},
			expectedResult: true,
			description:    "Should properly store valid JSON data",
		},
		{
			name: "Found=true with event log JSON data",
			input: map[string]interface{}{
				"found": true,
				"data": `{
					"address": "0x123",
					"topics": ["0xtopic1", "0xtopic2"],
					"data": "0xdata",
					"blockNumber": 12345,
					"transactionHash": "0x456",
					"transactionIndex": 3,
					"blockHash": "0x789",
					"logIndex": 1,
					"removed": false
				}`,
			},
			expectedResult: true,
			description:    "Should properly store valid event log JSON data",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := buildEventTriggerOutput(test.input)

			require.NotNil(t, result, "buildEventTriggerOutput should never return nil")
			require.IsType(t, &avsproto.EventTrigger_Output{}, result, "Should return correct type")

			t.Logf("✅ %s: %s", test.name, test.description)

			if test.name == "Found=true with valid JSON data" {
				require.NotEmpty(t, result.Data, "Data field should be populated")

				// Verify the JSON data can be parsed
				var eventData map[string]interface{}
				err := json.Unmarshal([]byte(result.Data), &eventData)
				require.NoError(t, err, "Data should be valid JSON")
				require.Equal(t, "TestToken", eventData["tokenName"])
				require.Equal(t, "TEST", eventData["tokenSymbol"])
				require.Equal(t, float64(18), eventData["tokenDecimals"])
			} else if test.name == "Found=true with event log JSON data" {
				require.NotEmpty(t, result.Data, "Data field should be populated")

				// Verify the JSON data can be parsed
				var eventData map[string]interface{}
				err := json.Unmarshal([]byte(result.Data), &eventData)
				require.NoError(t, err, "Data should be valid JSON")
				require.Equal(t, "0x123", eventData["address"])
				require.Equal(t, float64(12345), eventData["blockNumber"])
			} else {
				require.Empty(t, result.Data, "Data should be empty for invalid/empty input")
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
			name: "EventTrigger_Output with JSON transfer data",
			input: &avsproto.EventTrigger_Output{
				Data: `{
					"tokenName": "Test Token",
					"tokenSymbol": "TEST",
					"tokenDecimals": 18,
					"transactionHash": "0x1234567890abcdef",
					"address": "0xabcdef1234567890",
					"blockNumber": 12345678,
					"blockTimestamp": 1672531200,
					"fromAddress": "0x1111111111111111",
					"toAddress": "0x2222222222222222",
					"value": "1000000000000000000",
					"valueFormatted": "1.0",
					"transactionIndex": 5,
					"logIndex": 3
				}`,
			},
			description: "Should parse JSON data and map all fields including logIndex",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				require.Contains(t, result, "tokenName", "tokenName should be present")
				require.Equal(t, "Test Token", result["tokenName"])
				require.Contains(t, result, "tokenSymbol", "tokenSymbol should be present")
				require.Equal(t, "TEST", result["tokenSymbol"])
				require.Contains(t, result, "logIndex", "logIndex field should be present")
				require.Equal(t, float64(3), result["logIndex"], "logIndex should have correct value")
				require.Contains(t, result, "type", "type field should be present")
				require.Equal(t, "TRIGGER_TYPE_EVENT", result["type"])
			},
		},
		{
			name: "EventTrigger_Output with JSON event log data",
			input: &avsproto.EventTrigger_Output{
				Data: `{
					"address": "0xabcdef1234567890",
					"topics": ["0xtopic1", "0xtopic2", "0xtopic3"],
					"data": "0xdeadbeef",
					"blockNumber": 12345678,
					"transactionHash": "0x1234567890abcdef",
					"transactionIndex": 5,
					"blockHash": "0xblockhash123456",
					"logIndex": 3,
					"removed": false
				}`,
			},
			description: "Should parse JSON event log data and map all fields",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				require.Contains(t, result, "address", "address should be present")
				require.Equal(t, "0xabcdef1234567890", result["address"])
				require.Contains(t, result, "logIndex", "logIndex field should be present")
				require.Equal(t, float64(3), result["logIndex"], "logIndex should have correct value")
				require.Contains(t, result, "topics", "topics should be present")
				topics, ok := result["topics"].([]interface{})
				require.True(t, ok, "topics should be an array")
				require.Len(t, topics, 3, "should have 3 topics")
				require.Equal(t, "TRIGGER_TYPE_EVENT", result["type"])
			},
		},
		{
			name: "EventTrigger_Output with empty data",
			input: &avsproto.EventTrigger_Output{
				Data: "",
			},
			description: "Should handle empty data gracefully",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				require.Equal(t, "TRIGGER_TYPE_EVENT", result["type"], "Should still add trigger type")
				require.Len(t, result, 1, "Should only contain type field for empty data")
			},
		},
		{
			name: "EventTrigger_Output with invalid JSON",
			input: &avsproto.EventTrigger_Output{
				Data: `{"invalid": json}`,
			},
			description: "Should handle invalid JSON gracefully",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				require.Equal(t, "TRIGGER_TYPE_EVENT", result["type"], "Should still add trigger type")
				require.Len(t, result, 1, "Should only contain type field for invalid JSON")
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
