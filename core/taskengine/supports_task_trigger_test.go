package taskengine

import (
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

// TestSupportsTaskTrigger_Manual verifies that manual-trigger tasks are never
// considered streamable to operators. Manual triggers fire via the
// TriggerWorkflow RPC and execute entirely on the gateway, so the operator has
// no monitoring role for them (see issue #560).
func TestSupportsTaskTrigger_Manual(t *testing.T) {
	engine := &Engine{
		lock:             &sync.Mutex{},
		trackSyncedTasks: make(map[string]*operatorState),
	}

	operatorAddr := "0x1234567890123456789012345678901234567890"

	// Operator advertises support for every monitorable trigger type.
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: make(map[string]bool),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	manualTask := &model.Workflow{Task: &avsproto.Task{
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Manual{Manual: &avsproto.ManualTrigger{}},
		},
	}}

	// Manual triggers must be filtered out regardless of operator capabilities.
	assert.False(t, engine.supportsTaskTrigger(operatorAddr, manualTask),
		"manual trigger tasks should never be streamed to operators")
}

// TestSupportsTaskTrigger_MonitorableTypes is a regression guard ensuring the
// new manual-trigger branch does not affect the four monitorable trigger types.
func TestSupportsTaskTrigger_MonitorableTypes(t *testing.T) {
	engine := &Engine{
		lock:             &sync.Mutex{},
		trackSyncedTasks: make(map[string]*operatorState),
	}

	operatorAddr := "0x1234567890123456789012345678901234567890"
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: make(map[string]bool),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	cases := map[string]*avsproto.TaskTrigger{
		"event": {TriggerType: &avsproto.TaskTrigger_Event{Event: &avsproto.EventTrigger{}}},
		"block": {TriggerType: &avsproto.TaskTrigger_Block{Block: &avsproto.BlockTrigger{}}},
		"cron":  {TriggerType: &avsproto.TaskTrigger_Cron{Cron: &avsproto.CronTrigger{}}},
		"fixed": {TriggerType: &avsproto.TaskTrigger_FixedTime{FixedTime: &avsproto.FixedTimeTrigger{}}},
	}

	for name, trigger := range cases {
		task := &model.Workflow{Task: &avsproto.Task{Trigger: trigger}}
		assert.True(t, engine.supportsTaskTrigger(operatorAddr, task),
			"%s trigger should be streamable to a fully-capable operator", name)
	}
}
