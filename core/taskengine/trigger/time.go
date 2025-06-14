package trigger

import (
	"context"
	"fmt"
	"sync"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	gocron "github.com/go-co-op/gocron/v2"
)

type TimeTrigger struct {
	*CommonTrigger

	scheduler gocron.Scheduler
	jobs      map[string]gocron.Job // map taskID to job for removal

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
		scheduler: scheduler,
		jobs:      make(map[string]gocron.Job),
		triggerCh: triggerCh,
	}

	return &t
}

func (t *TimeTrigger) epochToCron(epoch int64) string {
	// Convert epoch to time
	tm := time.Unix(epoch/1000, 0)
	// Create cron expression for specific time
	return fmt.Sprintf("%d %d %d %d %d *", tm.Minute(), tm.Hour(), tm.Day(), tm.Month(), tm.Weekday())
}

func (t *TimeTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	t.mu.Lock()
	defer t.mu.Unlock()

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

	var job gocron.Job
	var err error

	if fixedTime := check.GetTrigger().GetFixedTime(); fixedTime != nil {
		// Handle epoch-based scheduling
		epochs := fixedTime.GetConfig().GetEpochs()
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
			job, err = t.scheduler.NewJob(
				gocron.CronJob(cronExpr, false),
				gocron.NewTask(triggerFunc),
				// In FixedTime, we want to run the job only once
				gocron.WithLimitedRuns(1),
			)
			if err != nil {
				return fmt.Errorf("failed to schedule epoch job: %w", err)
			}
			t.jobs[taskID] = job
		}
	} else if cronTrigger := check.GetTrigger().GetCron(); cronTrigger != nil {
		// Handle cron-based scheduling
		schedules := cronTrigger.GetConfig().GetSchedules()
		if len(schedules) == 0 {
			return fmt.Errorf("no cron expressions provided")
		}

		// Schedule a job for each cron expression
		for _, cronExpr := range schedules {
			if cronExpr == "" {
				continue
			}

			job, err = t.scheduler.NewJob(
				gocron.CronJob(cronExpr, false),
				gocron.NewTask(triggerFunc),
			)
			if err != nil {
				return fmt.Errorf("failed to schedule cron job: %w", err)
			}
			t.jobs[taskID] = job
		}
	}

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

	if job, exists := t.jobs[taskID]; exists {
		if err := t.scheduler.RemoveJob(job.ID()); err != nil {
			t.logger.Error("failed to remove job", "task_id", taskID, "error", err)
		}
		delete(t.jobs, taskID)
	}

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
