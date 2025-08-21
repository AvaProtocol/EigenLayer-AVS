package trigger

import (
	"context"
	"fmt"
	"sync"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	gocron "github.com/go-co-op/gocron/v2"
	"github.com/robfig/cron/v3"
)

// cronParser is a shared cron parser configuration to ensure consistency
// and reduce duplication across the codebase
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

type TimeTrigger struct {
	*CommonTrigger

	// New unified registry (preferred)
	registry *TaskRegistry

	// Legacy job management (for backward compatibility)
	scheduler  gocron.Scheduler
	jobs       map[string]gocron.Job // map taskID to job for removal
	legacyMode bool

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[uint64]
}

func NewTimeTrigger(triggerCh chan TriggerMetadata[uint64], logger sdklogging.Logger) *TimeTrigger {
	scheduler, _ := gocron.NewScheduler(gocron.WithLocation(time.UTC))

	if scheduler == nil {
		panic("fail to create scheduler")
	}

	t := TimeTrigger{
		CommonTrigger: &CommonTrigger{
			done:     make(chan bool),
			shutdown: false,
			logger:   logger,
			mu:       sync.Mutex{},
		},
		registry:   NewTaskRegistry(),
		scheduler:  scheduler,
		jobs:       make(map[string]gocron.Job),
		legacyMode: false, // Start in new mode
		triggerCh:  triggerCh,
	}

	return &t
}

// ensureNewFormat ensures we're using the new TaskRegistry format
// This provides automatic conversion from legacy jobs map format
func (t *TimeTrigger) ensureNewFormat() {
	if t.legacyMode {
		t.logger.Info("🔄 Converting from legacy jobs map format to new TaskRegistry format")

		// Convert existing data
		t.convertFromJobsMap()

		// Clear old data
		t.jobs = make(map[string]gocron.Job)
		t.legacyMode = false

		t.logger.Info("✅ Successfully converted to new TaskRegistry format",
			"task_count", t.registry.GetTimeTaskCount())
	}
}

// ensureLegacyConversion consolidates legacy data detection and conversion
// This helper function eliminates code duplication across methods
func (t *TimeTrigger) ensureLegacyConversion() {
	t.detectLegacyData()
	t.ensureNewFormat()
}

// detectLegacyData checks if we have data in the old format
func (t *TimeTrigger) detectLegacyData() {
	hasLegacyData := len(t.jobs) > 0

	if hasLegacyData && t.registry.GetTimeTaskCount() == 0 {
		t.legacyMode = true
		t.logger.Info("🔍 Detected legacy jobs map data - will convert on next operation")
	}
}

// convertFromJobsMap converts old jobs map data to the new TaskRegistry
// This provides backward compatibility during migration
func (t *TimeTrigger) convertFromJobsMap() {
	for taskID, job := range t.jobs {
		// Create TimeTaskData for the new registry
		timeData := &TimeTaskData{
			Jobs:      []gocron.Job{job}, // Convert single job to slice
			Schedules: []string{},        // We can't recover original schedules from job
			Epochs:    []int64{},         // We can't recover original epochs from job
		}

		// We don't have the original TaskMetadata in the old format,
		// so we'll create a minimal one for compatibility
		taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
			TaskId: taskID,
		}

		// Add to new registry
		t.registry.AddTask(taskID, taskMetadata, nil, nil, timeData)

		t.logger.Debug("🔄 Converted legacy time task",
			"task_id", taskID)
	}
}

// calculateNextCronTime calculates the next execution time for a given cron expression
// to prevent immediate execution upon registration
func (t *TimeTrigger) calculateNextCronTime(cronExpr string) (time.Time, error) {
	schedule, err := cronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse cron expression %s: %w", cronExpr, err)
	}

	// Get the next execution time from now
	nextTime := schedule.Next(time.Now())
	t.logger.Debug("calculated next cron execution time",
		"cron", cronExpr,
		"current_time", time.Now().Format(time.RFC3339),
		"next_time", nextTime.Format(time.RFC3339))

	return nextTime, nil
}

func (t *TimeTrigger) epochToCron(epoch int64) string {
	// Convert epoch to time in UTC
	tm := time.Unix(epoch/1000, 0).UTC()
	// Create cron expression for specific time (minute hour day month *)
	// Standard cron format: minute hour day month dayofweek
	// But gocron expects: minute hour day month *
	return fmt.Sprintf("%d %d %d %d *", tm.Minute(), tm.Hour(), tm.Day(), int(tm.Month()))
}

func (t *TimeTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Ensure we're using the new format
	t.ensureLegacyConversion()

	taskID := check.TaskId

	// Function to be executed when trigger fires
	triggerFunc := func() {
		currentTime := time.Now().UnixMilli()
		t.logger.Info("time trigger fired", "task_id", taskID, "time", currentTime)
		t.triggerCh <- TriggerMetadata[uint64]{
			TaskID: taskID,
			Marker: uint64(currentTime),
		}
	}

	var jobs []gocron.Job
	var schedules []string
	var epochs []int64

	if fixedTime := check.GetTrigger().GetFixedTime(); fixedTime != nil {
		// Handle epoch-based scheduling
		epochs = fixedTime.GetConfig().GetEpochs()
		if len(epochs) == 0 {
			return fmt.Errorf("no epochs provided")
		}

		// Schedule a job for each epoch
		for _, epoch := range epochs {
			// If epoch is in the past, skip scheduling
			if epoch < time.Now().UnixMilli() {
				t.logger.Info("skipping past epoch", "task_id", taskID, "epoch", epoch)
				continue
			}

			cronExpr := t.epochToCron(epoch)
			job, err := t.scheduler.NewJob(
				gocron.CronJob(cronExpr, false),
				gocron.NewTask(triggerFunc),
				// In FixedTime, we want to run the job only once
				gocron.WithLimitedRuns(1),
			)
			if err != nil {
				return fmt.Errorf("failed to schedule epoch job: %w", err)
			}
			jobs = append(jobs, job)
		}
	} else if cronTrigger := check.GetTrigger().GetCron(); cronTrigger != nil {
		// Handle cron-based scheduling
		schedules = cronTrigger.GetConfig().GetSchedules()
		if len(schedules) == 0 {
			return fmt.Errorf("no cron expressions provided")
		}

		// Schedule a job for each cron expression
		for _, cronExpr := range schedules {
			if cronExpr == "" {
				continue
			}

			// Calculate the next execution time based on cron expression
			// to prevent immediate execution
			nextExecTime, err := t.calculateNextCronTime(cronExpr)
			if err != nil {
				t.logger.Error("failed to calculate next cron time, skipping invalid cron expression", "cron", cronExpr, "error", err)
				continue // Skip this invalid cron expression
			}

			// Schedule with calculated start time to prevent immediate execution
			job, err := t.scheduler.NewJob(
				gocron.CronJob(cronExpr, false),
				gocron.NewTask(triggerFunc),
				gocron.WithStartAt(gocron.WithStartDateTime(nextExecTime)),
			)
			if err != nil {
				return fmt.Errorf("failed to schedule cron job: %w", err)
			}
			jobs = append(jobs, job)
		}
	}

	// Store in new registry format
	timeData := &TimeTaskData{
		Jobs:      jobs,
		Schedules: schedules,
		Epochs:    epochs,
	}

	t.registry.AddTask(taskID, check, nil, nil, timeData)

	t.logger.Debug("✅ Added time task to registry",
		"task_id", taskID,
		"schedules_count", len(schedules),
		"epochs_count", len(epochs))

	return nil
}

// RemoveCheck removes a scheduled job associated with the given taskID.
// It locks the mutex to ensure thread-safe access to the jobs map, checks if
// a job exists for the provided taskID, and removes it from the scheduler and
// the jobs map. If the job does not exist, the method does nothing.
//
// Parameters:
// - taskID: The unique identifier of the task whose associated job should be removed.
//
// Returns:
// - An error if the job removal from the scheduler fails. The error is logged but not returned.
func (t *TimeTrigger) RemoveCheck(taskID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Ensure we're using the new format
	t.ensureLegacyConversion()

	// Get task from registry
	task, exists := t.registry.GetTask(taskID)
	if !exists || task == nil {
		t.logger.Debug("task not found in registry", "task_id", taskID)
		return nil
	}

	// Remove all jobs from scheduler if they exist
	if task.TimeData != nil && len(task.TimeData.Jobs) > 0 {
		for i, job := range task.TimeData.Jobs {
			if job != nil {
				if err := t.scheduler.RemoveJob(job.ID()); err != nil {
					t.logger.Error("failed to remove job", "task_id", taskID, "job_index", i, "error", err)
				}
			}
		}
	}

	// Remove from registry
	t.registry.RemoveTask(taskID)

	t.logger.Debug("✅ Removed time task from registry", "task_id", taskID)

	return nil
}

func (t *TimeTrigger) Run(ctx context.Context) error {
	t.scheduler.Start()

	go func() {
		for {
			select {
			case <-ctx.Done():
				if err := t.scheduler.Shutdown(); err != nil {
					t.logger.Error("failed to shutdown scheduler on context done", "error", err)
				}
				return
			case <-t.done:
				if err := t.scheduler.Shutdown(); err != nil {
					t.logger.Error("failed to shutdown scheduler on done signal", "error", err)
				}
				return
			}
		}
	}()

	return nil
}
