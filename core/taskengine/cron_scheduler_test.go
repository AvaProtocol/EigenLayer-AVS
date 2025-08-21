package taskengine

import (
	"fmt"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCronSchedulerManager_Basic(t *testing.T) {
	// Create test dependencies
	db := testutil.TestMustDB()
	defer db.Close()

	logger := testutil.GetLogger()
	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})

	// Create cron scheduler
	cronScheduler := NewCronSchedulerManager(db, queue, logger)

	// Test basic initialization
	assert.NotNil(t, cronScheduler)
	assert.Equal(t, 0, cronScheduler.GetActiveJobs())
}

func TestCronSchedulerManager_AddCronTask(t *testing.T) {
	// Create test dependencies
	db := testutil.TestMustDB()
	defer db.Close()

	logger := testutil.GetLogger()
	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})

	// Create and start cron scheduler
	cronScheduler := NewCronSchedulerManager(db, queue, logger)
	err := cronScheduler.Start()
	require.NoError(t, err)
	defer cronScheduler.Stop()

	// Create a test cron task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:     "test-cron-task",
			Status: avsproto.TaskStatus_Active,
			Trigger: &avsproto.TaskTrigger{
				Id:   "test-trigger",
				Name: "Test Cron Trigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_CRON,
				TriggerType: &avsproto.TaskTrigger_Cron{
					Cron: &avsproto.CronTrigger{
						Config: &avsproto.CronTrigger_Config{
							Schedules: []string{"*/1 * * * *"}, // Every minute
						},
					},
				},
			},
		},
	}

	// Add task to scheduler
	err = cronScheduler.AddTask(task)
	require.NoError(t, err)

	// Verify task was added
	assert.Equal(t, 1, cronScheduler.GetActiveJobs())

	// Get job info
	jobInfo := cronScheduler.GetJobInfo()
	assert.Equal(t, 1, jobInfo["active_jobs_count"])

	jobs, ok := jobInfo["jobs"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, jobs, 1)
	assert.Equal(t, "test-cron-task", jobs[0]["task_id"])
}

func TestCronSchedulerManager_AddFixedTimeTask(t *testing.T) {
	// Create test dependencies
	db := testutil.TestMustDB()
	defer db.Close()

	logger := testutil.GetLogger()
	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})

	// Create and start cron scheduler
	cronScheduler := NewCronSchedulerManager(db, queue, logger)
	err := cronScheduler.Start()
	require.NoError(t, err)
	defer cronScheduler.Stop()

	// Create a test fixed-time task with future epoch
	futureTime := time.Now().Add(1 * time.Hour)
	task := &model.Task{
		Task: &avsproto.Task{
			Id:     "test-fixed-time-task",
			Status: avsproto.TaskStatus_Active,
			Trigger: &avsproto.TaskTrigger{
				Id:   "test-trigger",
				Name: "Test Fixed Time Trigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
				TriggerType: &avsproto.TaskTrigger_FixedTime{
					FixedTime: &avsproto.FixedTimeTrigger{
						Config: &avsproto.FixedTimeTrigger_Config{
							Epochs: []int64{futureTime.UnixMilli()},
						},
					},
				},
			},
		},
	}

	// Add task to scheduler
	err = cronScheduler.AddTask(task)
	require.NoError(t, err)

	// Verify task was added
	assert.Equal(t, 1, cronScheduler.GetActiveJobs())
}

func TestCronSchedulerManager_RemoveTask(t *testing.T) {
	// Create test dependencies
	db := testutil.TestMustDB()
	defer db.Close()

	logger := testutil.GetLogger()
	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})

	// Create and start cron scheduler
	cronScheduler := NewCronSchedulerManager(db, queue, logger)
	err := cronScheduler.Start()
	require.NoError(t, err)
	defer cronScheduler.Stop()

	// Create a test cron task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:     "test-remove-task",
			Status: avsproto.TaskStatus_Active,
			Trigger: &avsproto.TaskTrigger{
				Id:   "test-trigger",
				Name: "Test Remove Trigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_CRON,
				TriggerType: &avsproto.TaskTrigger_Cron{
					Cron: &avsproto.CronTrigger{
						Config: &avsproto.CronTrigger_Config{
							Schedules: []string{"*/1 * * * *"},
						},
					},
				},
			},
		},
	}

	// Add task to scheduler
	err = cronScheduler.AddTask(task)
	require.NoError(t, err)
	assert.Equal(t, 1, cronScheduler.GetActiveJobs())

	// Remove task from scheduler
	err = cronScheduler.RemoveTask("test-remove-task")
	require.NoError(t, err)
	assert.Equal(t, 0, cronScheduler.GetActiveJobs())
}

func TestCronSchedulerManager_ShouldScheduleTask(t *testing.T) {
	// Create test dependencies
	db := testutil.TestMustDB()
	defer db.Close()

	logger := testutil.GetLogger()
	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})

	cronScheduler := NewCronSchedulerManager(db, queue, logger)

	// Test cron task should be scheduled
	cronTask := &model.Task{
		Task: &avsproto.Task{
			Status: avsproto.TaskStatus_Active,
			Trigger: &avsproto.TaskTrigger{
				Type: avsproto.TriggerType_TRIGGER_TYPE_CRON,
				TriggerType: &avsproto.TaskTrigger_Cron{
					Cron: &avsproto.CronTrigger{},
				},
			},
		},
	}
	assert.True(t, cronScheduler.shouldScheduleTask(cronTask))

	// Test fixed-time task should be scheduled
	fixedTimeTask := &model.Task{
		Task: &avsproto.Task{
			Status: avsproto.TaskStatus_Active,
			Trigger: &avsproto.TaskTrigger{
				Type: avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
				TriggerType: &avsproto.TaskTrigger_FixedTime{
					FixedTime: &avsproto.FixedTimeTrigger{},
				},
			},
		},
	}
	assert.True(t, cronScheduler.shouldScheduleTask(fixedTimeTask))

	// Test manual task should not be scheduled
	manualTask := &model.Task{
		Task: &avsproto.Task{
			Status: avsproto.TaskStatus_Active,
			Trigger: &avsproto.TaskTrigger{
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
			},
		},
	}
	assert.False(t, cronScheduler.shouldScheduleTask(manualTask))

	// Test inactive task should not be scheduled
	inactiveTask := &model.Task{
		Task: &avsproto.Task{
			Status: avsproto.TaskStatus_Canceled,
			Trigger: &avsproto.TaskTrigger{
				Type: avsproto.TriggerType_TRIGGER_TYPE_CRON,
			},
		},
	}
	assert.False(t, cronScheduler.shouldScheduleTask(inactiveTask))
}

func TestCronSchedulerManager_EpochToCron(t *testing.T) {
	// Create test dependencies
	db := testutil.TestMustDB()
	defer db.Close()

	logger := testutil.GetLogger()
	queue := apqueue.New(db, logger, &apqueue.QueueOption{Prefix: "test"})

	cronScheduler := NewCronSchedulerManager(db, queue, logger)

	// Test epoch conversion
	// January 1, 2024, 12:30:00 UTC = 1704110200000 milliseconds
	epoch := int64(1704110200000)
	cronExpr := cronScheduler.epochToCron(epoch)

	// Parse the expected time in UTC to get the correct cron expression
	expectedTime := time.Unix(epoch/1000, 0).UTC()
	expectedCron := fmt.Sprintf("%d %d %d %d *", expectedTime.Minute(), expectedTime.Hour(), expectedTime.Day(), int(expectedTime.Month()))
	assert.Equal(t, expectedCron, cronExpr)
}
