package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildEventTriggerResponse_ConditionsMetFields verifies that both buildEventTriggerResponse
// and buildEventTriggerResponseWithSimulation include conditionsMet and matchedConditions fields.
func TestBuildEventTriggerResponse_ConditionsMetFields(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	methodCallData := map[string]interface{}{
		"current":  float64(4200),
		"decimals": float64(18),
	}

	queryMap := map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{
				"fieldName": "current",
				"operator":  "lt",
				"value":     "4000",
			},
		},
	}

	t.Run("conditions not met includes conditionsMet=false and matchedConditions", func(t *testing.T) {
		resp := engine.buildEventTriggerResponse(methodCallData, 1, nil, queryMap)

		assert.Equal(t, false, resp["success"])
		assert.Equal(t, false, resp["conditionsMet"])

		conditions, ok := resp["matchedConditions"].([]ConditionResult)
		require.True(t, ok, "matchedConditions should be []ConditionResult")
		require.Len(t, conditions, 1)
		assert.Equal(t, "current", conditions[0].FieldName)
		assert.Equal(t, "lt", conditions[0].Operator)
		assert.Equal(t, false, conditions[0].Passed)
	})

	t.Run("conditions met includes conditionsMet=true and matchedConditions", func(t *testing.T) {
		metQueryMap := map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"fieldName": "current",
					"operator":  "gt",
					"value":     "4000",
					"fieldType": "decimal",
				},
			},
		}

		resp := engine.buildEventTriggerResponse(methodCallData, 1, nil, metQueryMap)

		assert.Equal(t, true, resp["success"])
		assert.Equal(t, true, resp["conditionsMet"])

		conditions, ok := resp["matchedConditions"].([]ConditionResult)
		require.True(t, ok, "matchedConditions should be []ConditionResult")
		require.Len(t, conditions, 1)
		assert.Equal(t, "current", conditions[0].FieldName)
		assert.Equal(t, true, conditions[0].Passed)
	})

	t.Run("simulation variant also includes conditionsMet and matchedConditions", func(t *testing.T) {
		resp := engine.buildEventTriggerResponseWithSimulation(methodCallData, 1, nil, queryMap, true)

		assert.Equal(t, false, resp["conditionsMet"])
		conditions, ok := resp["matchedConditions"].([]ConditionResult)
		require.True(t, ok, "matchedConditions should be []ConditionResult")
		require.Len(t, conditions, 1)
		assert.Equal(t, false, conditions[0].Passed)
	})

	t.Run("no conditions returns empty matchedConditions and conditionsMet=true", func(t *testing.T) {
		emptyQueryMap := map[string]interface{}{}

		resp := engine.buildEventTriggerResponse(methodCallData, 1, nil, emptyQueryMap)

		assert.Equal(t, true, resp["conditionsMet"])
		conditions, ok := resp["matchedConditions"].([]ConditionResult)
		require.True(t, ok)
		assert.Empty(t, conditions)
	})
}
