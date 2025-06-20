package trigger

import (
	"fmt"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	gocron "github.com/go-co-op/gocron/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeTrigger_NewFormat(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Should start in new format
	assert.False(t, timeTrigger.legacyMode)
	assert.NotNil(t, timeTrigger.registry)
	assert.Equal(t, 0, timeTrigger.registry.GetTimeTaskCount())
}

func TestTimeTrigger_AddCheck_CronTrigger(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Create a cron trigger task
	cronConfig := &avsproto.CronTrigger_Config{
		Schedules: []string{"0 0 * * *"}, // Daily at midnight
	}
	cronTrigger := &avsproto.CronTrigger{
		Config: cronConfig,
	}
	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: cronTrigger,
		},
	}

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId:  "test-cron-task",
		Trigger: trigger,
	}

	// Add the task
	err := timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)

	// Verify task was added to registry
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	task, exists := timeTrigger.registry.GetTask("test-cron-task")
	require.True(t, exists)
	require.NotNil(t, task.TimeData)
	assert.Equal(t, []string{"0 0 * * *"}, task.TimeData.Schedules)
	assert.Empty(t, task.TimeData.Epochs)
	assert.NotNil(t, task.TimeData.Job)
}

func TestTimeTrigger_AddCheck_FixedTimeTrigger(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Create a fixed time trigger task with future epoch
	futureEpoch := time.Now().Add(24 * time.Hour).UnixMilli()
	fixedTimeConfig := &avsproto.FixedTimeTrigger_Config{
		Epochs: []int64{futureEpoch},
	}
	fixedTimeTrigger := &avsproto.FixedTimeTrigger{
		Config: fixedTimeConfig,
	}
	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_FixedTime{
			FixedTime: fixedTimeTrigger,
		},
	}

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId:  "test-fixed-time-task",
		Trigger: trigger,
	}

	// Add the task
	err := timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)

	// Verify task was added to registry
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	task, exists := timeTrigger.registry.GetTask("test-fixed-time-task")
	require.True(t, exists)
	require.NotNil(t, task.TimeData)
	assert.Empty(t, task.TimeData.Schedules)
	assert.Equal(t, []int64{futureEpoch}, task.TimeData.Epochs)
	assert.NotNil(t, task.TimeData.Job)
}

func TestTimeTrigger_AddCheck_PastEpochSkipped(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Create a fixed time trigger task with past epoch
	pastEpoch := time.Now().Add(-24 * time.Hour).UnixMilli()
	fixedTimeConfig := &avsproto.FixedTimeTrigger_Config{
		Epochs: []int64{pastEpoch},
	}
	fixedTimeTrigger := &avsproto.FixedTimeTrigger{
		Config: fixedTimeConfig,
	}
	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_FixedTime{
			FixedTime: fixedTimeTrigger,
		},
	}

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId:  "test-past-epoch-task",
		Trigger: trigger,
	}

	// Add the task
	err := timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)

	// Verify task was added to registry even though epoch was skipped
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	task, exists := timeTrigger.registry.GetTask("test-past-epoch-task")
	require.True(t, exists)
	require.NotNil(t, task.TimeData)
	assert.Equal(t, []int64{pastEpoch}, task.TimeData.Epochs)
}

func TestTimeTrigger_RemoveCheck(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Add a task first
	cronConfig := &avsproto.CronTrigger_Config{
		Schedules: []string{"0 0 * * *"},
	}
	cronTrigger := &avsproto.CronTrigger{
		Config: cronConfig,
	}
	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: cronTrigger,
		},
	}

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId:  "test-remove-task",
		Trigger: trigger,
	}

	err := timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	// Remove the task
	err = timeTrigger.RemoveCheck("test-remove-task")
	require.NoError(t, err)

	// Verify task was removed
	assert.Equal(t, 0, timeTrigger.registry.GetTimeTaskCount())

	_, exists := timeTrigger.registry.GetTask("test-remove-task")
	assert.False(t, exists)
}

func TestTimeTrigger_RemoveCheck_NonExistentTask(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Try to remove non-existent task
	err := timeTrigger.RemoveCheck("non-existent-task")
	require.NoError(t, err) // Should not error

	assert.Equal(t, 0, timeTrigger.registry.GetTimeTaskCount())
}

func TestTimeTrigger_LegacyMigration(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Simulate legacy data by creating a job manually
	scheduler, _ := gocron.NewScheduler(gocron.WithLocation(time.UTC))
	job, err := scheduler.NewJob(
		gocron.CronJob("0 0 * * *", false),
		gocron.NewTask(func() {}),
	)
	require.NoError(t, err)

	// Manually populate legacy data
	timeTrigger.jobs["legacy-task"] = job
	timeTrigger.legacyMode = false // Will be detected automatically

	// Add a new task - this should trigger migration
	cronConfig := &avsproto.CronTrigger_Config{
		Schedules: []string{"0 1 * * *"},
	}
	cronTrigger := &avsproto.CronTrigger{
		Config: cronConfig,
	}
	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: cronTrigger,
		},
	}

	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId:  "new-task",
		Trigger: trigger,
	}

	err = timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)

	// Should have migrated legacy data + new task
	assert.Equal(t, 2, timeTrigger.registry.GetTimeTaskCount())
	assert.False(t, timeTrigger.legacyMode)
	assert.Empty(t, timeTrigger.jobs) // Legacy data should be cleared

	// Verify both tasks exist
	_, exists := timeTrigger.registry.GetTask("legacy-task")
	assert.True(t, exists)

	_, exists = timeTrigger.registry.GetTask("new-task")
	assert.True(t, exists)
}

func TestTimeTrigger_RangeTimeTasks(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Add multiple tasks
	for i := 0; i < 3; i++ {
		cronConfig := &avsproto.CronTrigger_Config{
			Schedules: []string{"0 0 * * *"},
		}
		cronTrig := &avsproto.CronTrigger{
			Config: cronConfig,
		}
		trigger := &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Cron{
				Cron: cronTrig,
			},
		}

		taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
			TaskId:  fmt.Sprintf("task-%d", i),
			Trigger: trigger,
		}

		err := timeTrigger.AddCheck(taskMetadata)
		require.NoError(t, err)
	}

	// Test range iteration
	var taskIDs []string
	timeTrigger.registry.RangeTimeTasks(func(taskID string, entry *TaskEntry) bool {
		taskIDs = append(taskIDs, taskID)
		return true
	})

	assert.Equal(t, 3, len(taskIDs))
	assert.Contains(t, taskIDs, "task-0")
	assert.Contains(t, taskIDs, "task-1")
	assert.Contains(t, taskIDs, "task-2")
}

func TestTimeTrigger_EpochToCron(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()

	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Test epoch conversion - use UTC time
	epoch := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC).UnixMilli()
	cronExpr := timeTrigger.epochToCron(epoch)

	// Should create a cron expression for the specific time (5-field format)
	expected := "30 14 15 1 *" // minute hour day month *
	assert.Equal(t, expected, cronExpr)
}
