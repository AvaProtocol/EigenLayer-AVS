package trigger

import (
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

func TestEpochToCron(t *testing.T) {
	logger := sdklogging.NewNoopLogger()
	triggerCh := make(chan TriggerMetadata[uint64], 1)
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	testCases := []struct {
		name           string
		epochMillis    int64
		expectedCron   string
	}{
		{
			name:           "midnight UTC in milliseconds",
			epochMillis:    1743724800000, // 2025-04-04 00:00:00 UTC in milliseconds
			expectedCron:   "0 0 4 4 5 *", // minute, hour, day, month, weekday
		},
		{
			name:           "noon UTC in milliseconds",
			epochMillis:    1743768000000, // 2025-04-04 12:00:00 UTC in milliseconds
			expectedCron:   "0 12 4 4 5 *", // minute, hour, day, month, weekday
		},
		{
			name:           "specific time in milliseconds",
			epochMillis:    1743771845000, // 2025-04-04 13:04:05 UTC in milliseconds
			expectedCron:   "4 13 4 4 5 *", // minute, hour, day, month, weekday
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := timeTrigger.epochToCron(tc.epochMillis)
			if result != tc.expectedCron {
				t.Errorf("Expected cron expression %s, got %s", tc.expectedCron, result)
			}
		})
	}
}

func TestAddCheckWithMillisecondTimestamps(t *testing.T) {
	logger := sdklogging.NewNoopLogger()
	triggerCh := make(chan TriggerMetadata[uint64], 1)
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	futureTimeMillis := time.Now().Add(1 * time.Minute).UnixMilli()

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId: "test-task-id",
		Trigger: &avsproto.TaskTrigger{
			Type: avsproto.TriggerType_FixedTime,
			TriggerType: &avsproto.TaskTrigger_FixedTime{
				FixedTime: &avsproto.FixedTimeTrigger{
					Epochs: []int64{futureTimeMillis},
				},
			},
		},
	}

	err := timeTrigger.AddCheck(taskMetadata)
	if err != nil {
		t.Fatalf("Failed to add check: %v", err)
	}

	if len(timeTrigger.jobs) != 1 {
		t.Errorf("Expected 1 job to be created, got %d", len(timeTrigger.jobs))
	}

	timeTrigger.Remove(taskMetadata)
}

func TestSkipPastEpochs(t *testing.T) {
	logger := sdklogging.NewNoopLogger()
	triggerCh := make(chan TriggerMetadata[uint64], 1)
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	pastTimeMillis := time.Now().Add(-1 * time.Hour).UnixMilli()

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId: "test-task-id",
		Trigger: &avsproto.TaskTrigger{
			Type: avsproto.TriggerType_FixedTime,
			TriggerType: &avsproto.TaskTrigger_FixedTime{
				FixedTime: &avsproto.FixedTimeTrigger{
					Epochs: []int64{pastTimeMillis},
				},
			},
		},
	}

	err := timeTrigger.AddCheck(taskMetadata)
	if err != nil {
		t.Fatalf("Failed to add check: %v", err)
	}

	if len(timeTrigger.jobs) != 0 {
		t.Errorf("Expected 0 jobs to be created for past epochs, got %d", len(timeTrigger.jobs))
	}
}
