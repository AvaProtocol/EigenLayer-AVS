package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestManualTriggerTypePreservation(t *testing.T) {
	t.Run("should preserve JSON object types", func(t *testing.T) {
		// Create a complex JSON object
		jsonObjectData := map[string]interface{}{
			"name":   "test user",
			"age":    25,
			"active": true,
			"tags":   []interface{}{"tag1", "tag2"},
			"metadata": map[string]interface{}{
				"created": "2023-01-01",
				"version": 1.5,
			},
		}

		// Convert to protobuf Value
		protoValue, err := structpb.NewValue(jsonObjectData)
		require.NoError(t, err)

		// Create ManualTrigger.Config
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify the data field exists and preserves types
		require.Contains(t, result, "data")
		data := result["data"]
		require.NotNil(t, data)

		// Verify it's still a map (not converted to JSON string)
		dataMap, ok := data.(map[string]interface{})
		require.True(t, ok, "Data should be a map[string]interface{}, got %T", data)

		// Verify individual field types are preserved
		assert.Equal(t, "test user", dataMap["name"])
		assert.Equal(t, float64(25), dataMap["age"]) // JSON numbers become float64
		assert.Equal(t, true, dataMap["active"])

		// Verify nested array
		tags, ok := dataMap["tags"].([]interface{})
		require.True(t, ok)
		assert.Equal(t, "tag1", tags[0])
		assert.Equal(t, "tag2", tags[1])

		// Verify nested object
		metadata, ok := dataMap["metadata"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "2023-01-01", metadata["created"])
		assert.Equal(t, 1.5, metadata["version"])
	})

	t.Run("should preserve JSON array types", func(t *testing.T) {
		// Create a JSON array
		jsonArrayData := []interface{}{
			map[string]interface{}{"id": 1, "name": "item1"},
			map[string]interface{}{"id": 2, "name": "item2"},
		}

		// Convert to protobuf Value
		protoValue, err := structpb.NewValue(jsonArrayData)
		require.NoError(t, err)

		// Create ManualTrigger.Config
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify the data field exists and preserves array type
		require.Contains(t, result, "data")
		data := result["data"]
		require.NotNil(t, data)

		// Verify it's still an array (not converted to JSON string)
		dataArray, ok := data.([]interface{})
		require.True(t, ok, "Data should be a []interface{}, got %T", data)
		assert.Len(t, dataArray, 2)

		// Verify array elements
		item1, ok := dataArray[0].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, float64(1), item1["id"])
		assert.Equal(t, "item1", item1["name"])
	})

	t.Run("should preserve string types", func(t *testing.T) {
		// Create a simple string
		stringData := "Hello, this is a test string!"

		// Convert to protobuf Value
		protoValue, err := structpb.NewValue(stringData)
		require.NoError(t, err)

		// Create ManualTrigger.Config
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify the data field exists and preserves string type
		require.Contains(t, result, "data")
		data := result["data"]
		require.NotNil(t, data)

		// Verify it's still a string
		dataString, ok := data.(string)
		require.True(t, ok, "Data should be a string, got %T", data)
		assert.Equal(t, stringData, dataString)
	})

	t.Run("should preserve number types", func(t *testing.T) {
		// Create a number
		numberData := 42.5

		// Convert to protobuf Value
		protoValue, err := structpb.NewValue(numberData)
		require.NoError(t, err)

		// Create ManualTrigger.Config
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify the data field exists and preserves number type
		require.Contains(t, result, "data")
		data := result["data"]
		require.NotNil(t, data)

		// Verify it's still a number (float64 in Go JSON)
		dataNumber, ok := data.(float64)
		require.True(t, ok, "Data should be a float64, got %T", data)
		assert.Equal(t, numberData, dataNumber)
	})

	t.Run("should preserve boolean types", func(t *testing.T) {
		// Create a boolean
		booleanData := false

		// Convert to protobuf Value
		protoValue, err := structpb.NewValue(booleanData)
		require.NoError(t, err)

		// Create ManualTrigger.Config
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify the data field exists and preserves boolean type
		require.Contains(t, result, "data")
		data := result["data"]
		require.NotNil(t, data)

		// Verify it's still a boolean
		dataBool, ok := data.(bool)
		require.True(t, ok, "Data should be a bool, got %T", data)
		assert.Equal(t, booleanData, dataBool)
	})

	t.Run("should NOT convert JSON string to object (preserve string type)", func(t *testing.T) {
		// Create a JSON string (not a parsed object)
		jsonStringData := `{"this":"should","stay":"as","a":"string"}`

		// Convert to protobuf Value
		protoValue, err := structpb.NewValue(jsonStringData)
		require.NoError(t, err)

		// Create ManualTrigger.Config
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify the data field exists and preserves string type
		require.Contains(t, result, "data")
		data := result["data"]
		require.NotNil(t, data)

		// Verify it's still a string (NOT parsed as an object)
		dataString, ok := data.(string)
		require.True(t, ok, "Data should be a string, got %T", data)
		assert.Equal(t, jsonStringData, dataString)

		// Verify it's NOT a map
		_, isMap := data.(map[string]interface{})
		assert.False(t, isMap, "Data should NOT be parsed as a map")
	})

	t.Run("should preserve headers and pathParams", func(t *testing.T) {
		// Create test data
		testData := map[string]interface{}{"message": "test"}
		protoValue, err := structpb.NewValue(testData)
		require.NoError(t, err)

		// Create ManualTrigger.Config with headers and pathParams
		manualConfig := &avsproto.ManualTrigger_Config{
			Data: protoValue,
			Headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer token123",
			},
			PathParams: map[string]string{
				"userId": "123",
				"action": "test",
			},
		}

		// Create TaskTrigger
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: manualConfig,
				},
			},
		}

		// Test TaskTriggerToConfig function
		result := TaskTriggerToConfig(trigger)

		// Verify all fields are present
		require.Contains(t, result, "data")
		require.Contains(t, result, "headers")
		require.Contains(t, result, "pathParams")

		// Verify data preservation
		data := result["data"].(map[string]interface{})
		assert.Equal(t, "test", data["message"])

		// Verify headers preservation
		headers := result["headers"].(map[string]interface{})
		assert.Equal(t, "application/json", headers["Content-Type"])
		assert.Equal(t, "Bearer token123", headers["Authorization"])

		// Verify pathParams preservation
		pathParams := result["pathParams"].(map[string]interface{})
		assert.Equal(t, "123", pathParams["userId"])
		assert.Equal(t, "test", pathParams["action"])
	})
}
