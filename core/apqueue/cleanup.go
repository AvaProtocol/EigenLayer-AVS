package apqueue

import (
	"fmt"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
	badger "github.com/dgraph-io/badger/v4"
)

// CleanupStats holds statistics about the cleanup operation
type CleanupStats struct {
	TotalJobs     int
	OrphanedJobs  int
	RemovedJobs   int
	FailedCleanup int
	Duration      time.Duration
}

// CleanupOrphanedJobs removes jobs for tasks that no longer exist
func (q *Queue) CleanupOrphanedJobs() (*CleanupStats, error) {
	startTime := time.Now()
	stats := &CleanupStats{}

	q.logger.Debug("starting orphaned jobs cleanup")

	// Get all jobs from different queues
	for _, status := range []jobStatus{jobPending, jobInProgress, jobFailed} {
		prefix := q.getQueueKeyPrefix(status)

		// Get all jobs with this status
		kvs, err := q.db.GetByPrefix(prefix)
		if err != nil {
			q.logger.Error("failed to get jobs for cleanup", "status", status.HumanReadable(), "error", err)
			stats.FailedCleanup++
			continue
		}

		for _, kv := range kvs {
			stats.TotalJobs++

			// Decode the job to get task ID
			job, err := decodeJob(kv.Value)
			if err != nil {
				q.logger.Error("failed to decode job during cleanup", "key", string(kv.Key), "error", err)
				stats.FailedCleanup++
				continue
			}

			// Check if task exists in any status
			// Tasks can be in: enabled (a), completed (c), failed (f), disabled (i), running (x)
			taskStatuses := []avsproto.TaskStatus{
				avsproto.TaskStatus_Enabled,
				avsproto.TaskStatus_Completed,
				avsproto.TaskStatus_Failed,
				avsproto.TaskStatus_Disabled,
				avsproto.TaskStatus_Running,
			}

			taskExists := false
			var lastErr error
			var checkedStatuses []string

			for _, taskStatus := range taskStatuses {
				taskKey := storageschema.TaskStorageKey(job.Name, taskStatus)
				checkedStatuses = append(checkedStatuses, string(taskKey))
				_, err := q.db.GetKey(taskKey)
				if err == nil {
					// Task exists in this status
					taskExists = true
					break
				}
				lastErr = err
			}

			if !taskExists {
				// Task doesn't exist in any status - this is an orphaned job
				stats.OrphanedJobs++

				// Determine failure reason
				failureReason := "task_not_found_any_status"
				if lastErr != nil {
					if lastErr == badger.ErrKeyNotFound {
						failureReason = "task_not_found_in_storage"
					} else {
						failureReason = fmt.Sprintf("storage_error: %v", lastErr)
					}
				}

				q.dbLock.Lock()
				if delErr := q.db.Delete(kv.Key); delErr != nil {
					q.logger.Error("failed to remove orphaned job", "job_id", job.ID, "task_id", job.Name, "error", delErr)
					stats.FailedCleanup++
				} else {
					stats.RemovedJobs++
					q.logger.Debug("removed orphaned job",
						"job_id", job.ID,
						"task_id", job.Name,
						"status", status.HumanReadable(),
						"failure_reason", failureReason,
						"checked_statuses", checkedStatuses,
						"last_error", lastErr)
				}
				q.dbLock.Unlock()
			}
		}
	}

	stats.Duration = time.Since(startTime)

	q.logger.Info("orphaned jobs cleanup completed",
		"total_jobs", stats.TotalJobs,
		"orphaned_jobs", stats.OrphanedJobs,
		"removed_jobs", stats.RemovedJobs,
		"failed_cleanup", stats.FailedCleanup,
		"duration_ms", stats.Duration.Milliseconds())

	return stats, nil
}

// SchedulePeriodicCleanup runs cleanup every interval
func (q *Queue) SchedulePeriodicCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ticker.C:
				if stats, err := q.CleanupOrphanedJobs(); err != nil {
					q.logger.Error("periodic cleanup failed", "error", err)
				} else if stats.RemovedJobs > 0 {
					q.logger.Info("periodic cleanup removed orphaned jobs",
						"removed_jobs", stats.RemovedJobs)
				}
			case <-q.closeCh:
				ticker.Stop()
				return
			}
		}
	}()
}
