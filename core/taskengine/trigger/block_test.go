package trigger

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestBlockTrigger_AddCheck_InvalidInterval(t *testing.T) {
	logger := testutil.GetLogger()
	triggerCh := make(chan TriggerMetadata[int64], 10)

	rpcOption := &RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestWsRPCURL(),
	}

	blockTrigger := NewBlockTrigger(rpcOption, triggerCh, logger)

	tests := []struct {
		name     string
		interval int64
		wantErr  bool
	}{
		{
			name:     "zero interval should fail",
			interval: 0,
			wantErr:  true,
		},
		{
			name:     "negative interval should fail",
			interval: -1,
			wantErr:  true,
		},
		{
			name:     "positive interval should succeed",
			interval: 10,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := &avsproto.SyncMessagesResp_TaskMetadata{
				TaskId: "test-task-" + tt.name,
				Trigger: &avsproto.TaskTrigger{
					TriggerType: &avsproto.TaskTrigger_Block{
						Block: &avsproto.BlockTrigger{
							Config: &avsproto.BlockTrigger_Config{
								Interval: tt.interval,
							},
						},
					},
				},
			}

			err := blockTrigger.AddCheck(check)

			if tt.wantErr && err == nil {
				t.Errorf("AddCheck() expected error for interval %d, but got none", tt.interval)
			}

			if !tt.wantErr && err != nil {
				t.Errorf("AddCheck() unexpected error for interval %d: %v", tt.interval, err)
			}

			if tt.wantErr && err != nil {
				t.Logf("AddCheck() correctly rejected interval %d with error: %v", tt.interval, err)
			}
		})
	}
}

func TestBlockTrigger_AddCheck_NilConfig(t *testing.T) {
	logger := testutil.GetLogger()
	triggerCh := make(chan TriggerMetadata[int64], 10)

	rpcOption := &RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestWsRPCURL(),
	}

	blockTrigger := NewBlockTrigger(rpcOption, triggerCh, logger)

	// Test with nil config (should default to 0 and fail)
	check := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId: "test-task-nil-config",
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: nil, // This will cause GetInterval() to return 0
				},
			},
		},
	}

	err := blockTrigger.AddCheck(check)

	if err == nil {
		t.Error("AddCheck() expected error for nil config, but got none")
	} else {
		t.Logf("AddCheck() correctly rejected nil config with error: %v", err)
	}
}

func TestBlockTrigger_AddCheck_ValidInterval(t *testing.T) {
	logger := testutil.GetLogger()
	triggerCh := make(chan TriggerMetadata[int64], 10)

	rpcOption := &RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestWsRPCURL(),
	}

	blockTrigger := NewBlockTrigger(rpcOption, triggerCh, logger)

	check := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId: "test-task-valid",
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: 10,
					},
				},
			},
		},
	}

	err := blockTrigger.AddCheck(check)

	if err != nil {
		t.Errorf("AddCheck() unexpected error for valid interval: %v", err)
	}

	// Verify the task was added to the registry
	if blockTrigger.registry.GetBlockTaskCount() != 1 {
		t.Error("Expected 1 block task in registry")
	}

	entry, exists := blockTrigger.registry.GetTask("test-task-valid")
	if !exists {
		t.Error("Task was not found in registry")
	}

	if entry.BlockData == nil {
		t.Error("Task does not have BlockData")
	}

	if entry.BlockData.Interval != 10 {
		t.Errorf("Expected interval 10, got %d", entry.BlockData.Interval)
	}
}
