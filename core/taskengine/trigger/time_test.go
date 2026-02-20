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
	assert.NotEmpty(t, task.TimeData.Jobs)
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
	assert.NotEmpty(t, task.TimeData.Jobs)
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

// TestTimeTrigger_calculateNextCronTime_PastStartAt verifies that when startAt
// is in the past (e.g. task re-enabled after disable), the reference time falls
// back to now so the next trigger is at the next cron-aligned time after now.
func TestTimeTrigger_calculateNextCronTime_PastStartAt(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// startAt is 5 minutes ago — simulates a re-enabled task whose
	// original creation time is in the past.
	pastStartAt := time.Now().Add(-5 * time.Minute).UnixMilli()

	now := time.Now()
	// Every 10 minutes: the cron library aligns to :00, :10, :20 etc.
	nextTime, err := timeTrigger.calculateNextCronTime("*/10 * * * *", pastStartAt)
	require.NoError(t, err)

	// The next trigger must be in the future (after the reference = now,
	// not the past startAt) and at most 10 minutes away (the cron interval).
	assert.True(t, nextTime.After(now),
		"next trigger should be in the future, got %s", nextTime)
	assert.True(t, nextTime.Sub(now) <= 10*time.Minute,
		"next trigger should be within one interval (10 min), got %s from now", nextTime.Sub(now))
}

// TestTimeTrigger_calculateNextCronTime_FutureStartAt verifies that when
// startAt is in the future, it is used as the reference time so the trigger
// respects the scheduled future start.
func TestTimeTrigger_calculateNextCronTime_FutureStartAt(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// startAt is 30 minutes in the future
	futureStartAt := time.Now().Add(30 * time.Minute).UnixMilli()

	nextTime, err := timeTrigger.calculateNextCronTime("*/10 * * * *", futureStartAt)
	require.NoError(t, err)

	// schedule.Next() returns the next occurrence strictly after the reference,
	// so the next trigger must be after futureStartAt.
	futureTime := time.UnixMilli(futureStartAt)
	assert.True(t, nextTime.After(futureTime),
		"next trigger (%s) should be after futureStartAt (%s)", nextTime, futureTime)
}

// TestTimeTrigger_calculateNextCronTime_ZeroStartAt verifies that when startAt
// is 0 (not provided), the reference time falls back to now.
func TestTimeTrigger_calculateNextCronTime_ZeroStartAt(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	nextTime, err := timeTrigger.calculateNextCronTime("*/10 * * * *", 0)
	require.NoError(t, err)

	assert.True(t, nextTime.After(time.Now()),
		"next trigger should be in the future, got %s", nextTime)
}

// TestTimeTrigger_DisableEnableResetsTimer tests the full disable → enable
// cycle and verifies the cron timer resets so the next trigger is a full
// interval from the re-enable moment rather than from the original startAt.
func TestTimeTrigger_DisableEnableResetsTimer(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	// Task was originally created 5 minutes ago with a 10-minute interval
	pastStartAt := time.Now().Add(-5 * time.Minute).UnixMilli()

	cronConfig := &avsproto.CronTrigger_Config{
		Schedules: []string{"*/10 * * * *"},
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
		TaskId:  "test-disable-enable",
		Trigger: trigger,
		StartAt: pastStartAt,
	}

	// Step 1: Add the task (simulates initial creation or re-enable)
	err := timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	// Step 2: Disable — removes the task from scheduling
	err = timeTrigger.RemoveCheck("test-disable-enable")
	require.NoError(t, err)
	assert.Equal(t, 0, timeTrigger.registry.GetTimeTaskCount())

	// Step 3: Re-enable — adds the task back; startAt is still in the past
	err = timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	// Verify via calculateNextCronTime: with a past startAt, the next trigger
	// should be computed from now (next cron-aligned time), not from the
	// stale startAt. Without the fix, startAt (5 min ago) + */10 alignment
	// could produce a next tick only seconds away.
	now := time.Now()
	nextTime, err := timeTrigger.calculateNextCronTime("*/10 * * * *", pastStartAt)
	require.NoError(t, err)
	assert.True(t, nextTime.After(now),
		"after re-enable, next trigger should be in the future, got %s", nextTime)
	assert.True(t, nextTime.Sub(now) <= 10*time.Minute,
		"after re-enable, next trigger should be within one interval (10 min), got %s from now",
		nextTime.Sub(now))
}

// TestTimeTrigger_AddCheckTwiceNoJobLeak verifies that calling AddCheck twice
// for the same task without an intervening RemoveCheck does not leak gocron
// jobs. The old jobs should be cleaned up before new ones are created.
func TestTimeTrigger_AddCheckTwiceNoJobLeak(t *testing.T) {
	triggerCh := make(chan TriggerMetadata[uint64], 10)
	logger := testutil.GetLogger()
	timeTrigger := NewTimeTrigger(triggerCh, logger)

	cronConfig := &avsproto.CronTrigger_Config{
		Schedules: []string{"*/10 * * * *"},
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
		TaskId:  "test-duplicate-add",
		Trigger: trigger,
	}

	// First AddCheck
	err := timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount())

	task1, _ := timeTrigger.registry.GetTask("test-duplicate-add")
	require.NotNil(t, task1.TimeData)
	firstJobCount := len(task1.TimeData.Jobs)
	assert.Equal(t, 1, firstJobCount, "should have exactly 1 job after first AddCheck")

	// Second AddCheck without RemoveCheck — should NOT leak the first job
	err = timeTrigger.AddCheck(taskMetadata)
	require.NoError(t, err)
	assert.Equal(t, 1, timeTrigger.registry.GetTimeTaskCount(),
		"registry should still have exactly 1 task")

	task2, _ := timeTrigger.registry.GetTask("test-duplicate-add")
	require.NotNil(t, task2.TimeData)
	assert.Equal(t, 1, len(task2.TimeData.Jobs),
		"should still have exactly 1 job, not 2 (no leak)")

	// Cleanup should work without errors
	err = timeTrigger.RemoveCheck("test-duplicate-add")
	require.NoError(t, err)
	assert.Equal(t, 0, timeTrigger.registry.GetTimeTaskCount())
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
