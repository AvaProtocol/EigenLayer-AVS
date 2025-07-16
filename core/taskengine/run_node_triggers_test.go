package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestRunNodeImmediately_TriggerTypes(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	testCases := []struct {
		name       string
		nodeConfig map[string]interface{}
	}{
		{
			name: "Block Trigger",
			nodeConfig: map[string]interface{}{
				"triggerType": "block",
				"interval":    1,
			},
		},
		{
			name: "Manual Trigger",
			nodeConfig: map[string]interface{}{
				"triggerType": "manual",
			},
		},
		{
			name: "Cron Trigger",
			nodeConfig: map[string]interface{}{
				"triggerType":    "cron",
				"cronExpression": "0 0 * * *",
			},
		},
		{
			name: "Fixed Time Trigger",
			nodeConfig: map[string]interface{}{
				"triggerType": "fixedTime",
				"timestamp":   1672531200,
			},
		},
		{
			name: "Event Trigger",
			nodeConfig: map[string]interface{}{
				"triggerType":     "event",
				"contractAddress": "0x1234567890123456789012345678901234567890",
				"eventSignature":  "Transfer(address,address,uint256)",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.RunNodeImmediately("trigger", tc.nodeConfig, map[string]interface{}{})

			if err != nil {
				t.Errorf("Expected no error for %s, got: %v", tc.name, err)
			}

			if result == nil {
				t.Errorf("Expected result for %s, got nil", tc.name)
			}
		})
	}
}

func TestRunTriggerRPC_ManualTrigger(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"triggerType": "manual",
	}

	result, err := engine.RunNodeImmediately("manualTrigger", nodeConfig, map[string]interface{}{})

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}
}
