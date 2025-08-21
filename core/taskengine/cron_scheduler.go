package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	gocron "github.com/go-co-op/gocron/v2"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

// CronSchedulerManager manages all cron-triggered workflows
type CronSchedulerManager struct {
	db        storage.Storage
	queue     *apqueue.Queue
	logger    sdklogging.Logger
	scheduler gocron.Scheduler
	jobs      map[string]gocron.Job // taskID -> job
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewCronSchedulerManager creates a new cron scheduler manager
func NewCronSchedulerManager(db storage.Storage, queue *apqueue.Queue, logger sdklogging.Logger) *CronSchedulerManager {
	scheduler, err := gocron.NewScheduler(gocron.WithLocation(time.UTC))
	if err != nil {
		panic(fmt.Errorf("failed to create cron scheduler: %w", err))
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &CronSchedulerManager{
		db:        db,
		queue:     queue,
		logger:    logger,
		scheduler: scheduler,
		jobs:      make(map[string]gocron.Job),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start initializes the cron scheduler and loads existing active cron tasks
func (csm *CronSchedulerManager) Start() error {
	csm.logger.Info("🕐 Starting Cron Scheduler Manager")

	// Start the underlying scheduler
	csm.scheduler.Start()

	// Load all active cron tasks from database
	if err := csm.loadActiveCronTasks(); err != nil {
		csm.logger.Error("Failed to load active cron tasks", "error", err)
		return fmt.Errorf("failed to load active cron tasks: %w", err)
	}

	csm.logger.Info("✅ Cron Scheduler Manager started successfully", "active_jobs", len(csm.jobs))
	return nil
}

// Stop shuts down the cron scheduler
func (csm *CronSchedulerManager) Stop() error {
	csm.logger.Info("🛑 Stopping Cron Scheduler Manager")

	csm.cancel()

	csm.mu.Lock()
	defer csm.mu.Unlock()

	if err := csm.scheduler.Shutdown(); err != nil {
		csm.logger.Error("Failed to shutdown cron scheduler", "error", err)
		return fmt.Errorf("failed to shutdown cron scheduler: %w", err)
	}

	// Clear jobs map
	csm.jobs = make(map[string]gocron.Job)

	csm.logger.Info("✅ Cron Scheduler Manager stopped successfully")
	return nil
}

// loadActiveCronTasks loads all active tasks with cron triggers from the database
func (csm *CronSchedulerManager) loadActiveCronTasks() error {
	// Get all active tasks
	kvs, err := csm.db.GetByPrefix(TaskByStatusStoragePrefix(avsproto.TaskStatus_Active))
	if err != nil {
		return fmt.Errorf("failed to get active tasks: %w", err)
	}

	loadedCount := 0
	for _, item := range kvs {
		task := &model.Task{
			Task: &avsproto.Task{},
		}

		if err := protojson.Unmarshal(item.Value, task); err != nil {
			csm.logger.Warn("Failed to unmarshal task during cron scheduler startup",
				"storage_key", string(item.Key), "error", err)
			continue
		}

		// Only process cron and fixed-time triggered tasks
		if csm.shouldScheduleTask(task) {
			if err := csm.scheduleTask(task); err != nil {
				csm.logger.Error("Failed to schedule cron task during startup",
					"task_id", task.Id, "error", err)
				continue
			}
			loadedCount++
		}
	}

	csm.logger.Info("📋 Loaded cron tasks from database", "loaded_count", loadedCount)
	return nil
}

// shouldScheduleTask determines if a task should be scheduled by the cron manager
func (csm *CronSchedulerManager) shouldScheduleTask(task *model.Task) bool {
	if task.Status != avsproto.TaskStatus_Active {
		return false
	}

	trigger := task.Trigger
	if trigger == nil {
		return false
	}

	// Schedule cron triggers and fixed-time triggers
	return trigger.GetCron() != nil || trigger.GetFixedTime() != nil
}

// AddTask adds a new cron task to the scheduler
func (csm *CronSchedulerManager) AddTask(task *model.Task) error {
	if !csm.shouldScheduleTask(task) {
		csm.logger.Debug("Task does not need cron scheduling", "task_id", task.Id,
			"trigger_type", task.Trigger.Type.String())
		return nil
	}

	return csm.scheduleTask(task)
}

// RemoveTask removes a task from the cron scheduler
func (csm *CronSchedulerManager) RemoveTask(taskID string) error {
	csm.mu.Lock()
	defer csm.mu.Unlock()

	job, exists := csm.jobs[taskID]
	if !exists {
		csm.logger.Debug("Task not found in cron scheduler", "task_id", taskID)
		return nil
	}

	// Remove job from scheduler
	if err := csm.scheduler.RemoveJob(job.ID()); err != nil {
		csm.logger.Error("Failed to remove job from scheduler", "task_id", taskID, "error", err)
		return fmt.Errorf("failed to remove job from scheduler: %w", err)
	}

	// Remove from our tracking map
	delete(csm.jobs, taskID)

	csm.logger.Info("🗑️ Removed cron task from scheduler", "task_id", taskID)
	return nil
}

// scheduleTask creates and schedules a cron job for the given task
func (csm *CronSchedulerManager) scheduleTask(task *model.Task) error {
	csm.mu.Lock()
	defer csm.mu.Unlock()

	taskID := task.Id
	trigger := task.Trigger

	// Create the job function that will be executed when the cron triggers
	triggerFunc := func() {
		csm.executeCronTask(taskID)
	}

	var job gocron.Job
	var err error

	if cronTrigger := trigger.GetCron(); cronTrigger != nil {
		// Handle cron-based scheduling
		schedules := cronTrigger.GetConfig().GetSchedules()
		if len(schedules) == 0 {
			return fmt.Errorf("no cron expressions provided for task %s", taskID)
		}

		// For now, use the first cron expression (we can extend this later for multiple schedules)
		cronExpr := schedules[0]
		if cronExpr == "" {
			return fmt.Errorf("empty cron expression for task %s", taskID)
		}

		job, err = csm.scheduler.NewJob(
			gocron.CronJob(cronExpr, false),
			gocron.NewTask(triggerFunc),
		)
		if err != nil {
			return fmt.Errorf("failed to schedule cron job for task %s: %w", taskID, err)
		}

		nextRun, _ := job.NextRun()
		csm.logger.Info("📅 Scheduled cron task",
			"task_id", taskID,
			"cron_expression", cronExpr,
			"next_run", nextRun)

	} else if fixedTime := trigger.GetFixedTime(); fixedTime != nil {
		// Handle epoch-based scheduling
		epochs := fixedTime.GetConfig().GetEpochs()
		if len(epochs) == 0 {
			return fmt.Errorf("no epochs provided for fixed-time task %s", taskID)
		}

		// Schedule for each epoch that's in the future
		scheduledCount := 0
		for _, epoch := range epochs {
			// If epoch is in the past, skip scheduling
			if epoch < time.Now().UnixMilli() {
				csm.logger.Info("Skipping past epoch", "task_id", taskID, "epoch", epoch)
				continue
			}

			// Convert epoch to cron expression
			cronExpr := csm.epochToCron(epoch)
			job, err = csm.scheduler.NewJob(
				gocron.CronJob(cronExpr, false),
				gocron.NewTask(triggerFunc),
				// For fixed time, run only once
				gocron.WithLimitedRuns(1),
			)
			if err != nil {
				csm.logger.Error("Failed to schedule fixed-time job",
					"task_id", taskID, "epoch", epoch, "error", err)
				continue
			}
			scheduledCount++
		}

		if scheduledCount == 0 {
			return fmt.Errorf("no future epochs to schedule for fixed-time task %s", taskID)
		}

		nextRun, _ := job.NextRun()
		csm.logger.Info("⏰ Scheduled fixed-time task",
			"task_id", taskID,
			"scheduled_epochs", scheduledCount,
			"next_run", nextRun)
	}

	// Store the job in our tracking map
	csm.jobs[taskID] = job

	return nil
}

// epochToCron converts epoch timestamp to cron expression
func (csm *CronSchedulerManager) epochToCron(epoch int64) string {
	// Convert epoch to time in UTC
	tm := time.Unix(epoch/1000, 0).UTC()
	// Create cron expression for specific time (minute hour day month *)
	return fmt.Sprintf("%d %d %d %d *", tm.Minute(), tm.Hour(), tm.Day(), int(tm.Month()))
}

// executeCronTask queues a task for execution when its cron trigger fires
func (csm *CronSchedulerManager) executeCronTask(taskID string) {
	csm.logger.Info("🔥 Cron trigger fired", "task_id", taskID, "timestamp", time.Now().UnixMilli())

	// Create execution job data using the same structure as the engine
	queueTaskData := QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_CRON,
		TriggerOutput: map[string]interface{}{
			"timestamp":     time.Now().UnixMilli(),
			"timestamp_iso": time.Now().UTC().Format(time.RFC3339),
		},
		ExecutionID: ulid.Make().String(),
	}

	// Serialize to JSON
	data, err := json.Marshal(queueTaskData)
	if err != nil {
		csm.logger.Error("Failed to serialize cron job data", "task_id", taskID, "error", err)
		return
	}

	// Queue the task for execution
	if _, err := csm.queue.Enqueue(JobTypeExecuteTask, taskID, data); err != nil {
		csm.logger.Error("Failed to queue cron-triggered task for execution",
			"task_id", taskID, "error", err)
		return
	}

	csm.logger.Info("✅ Cron-triggered task queued for execution", "task_id", taskID, "execution_id", queueTaskData.ExecutionID)
}

// GetActiveJobs returns the count of active cron jobs
func (csm *CronSchedulerManager) GetActiveJobs() int {
	csm.mu.RLock()
	defer csm.mu.RUnlock()
	return len(csm.jobs)
}

// GetJobInfo returns information about scheduled jobs
func (csm *CronSchedulerManager) GetJobInfo() map[string]interface{} {
	csm.mu.RLock()
	defer csm.mu.RUnlock()

	info := make(map[string]interface{})
	info["active_jobs_count"] = len(csm.jobs)

	jobs := make([]map[string]interface{}, 0, len(csm.jobs))
	for taskID, job := range csm.jobs {
		nextRun, _ := job.NextRun()
		jobInfo := map[string]interface{}{
			"task_id":  taskID,
			"job_id":   job.ID().String(),
			"next_run": nextRun.Format(time.RFC3339),
		}
		jobs = append(jobs, jobInfo)
	}
	info["jobs"] = jobs

	return info
}
