package apqueue

import (
	"time"
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

	q.logger.Info("starting orphaned jobs cleanup")

	// Get all jobs from different queues
	for _, status := range []jobStatus{jobPending, jobInProgress, jobFailed} {
		prefix := q.getQueueKeyPrefix(status)

		// Get all jobs with this status
		kvs, err := q.db.GetByPrefix(prefix)
		if err != nil {
			q.logger.Error("failed to get jobs for cleanup", "status", status, "error", err)
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

			// Check if task still exists
			taskKey := []byte("t:a:" + job.Name) // Check active tasks
			_, err = q.db.GetKey(taskKey)
			if err != nil {
				// Task doesn't exist - this is an orphaned job
				stats.OrphanedJobs++

				q.dbLock.Lock()
				if delErr := q.db.Delete(kv.Key); delErr != nil {
					q.logger.Error("failed to remove orphaned job", "job_id", job.ID, "task_id", job.Name, "error", delErr)
					stats.FailedCleanup++
				} else {
					stats.RemovedJobs++
					q.logger.Debug("removed orphaned job", "job_id", job.ID, "task_id", job.Name, "status", status)
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
