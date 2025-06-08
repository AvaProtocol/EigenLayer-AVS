package apqueue

import (
	"fmt"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

type JobProcessor interface {
	Perform(j *Job) error
}

type Worker struct {
	q  *Queue
	db storage.Storage

	processorRegistry map[string]JobProcessor
	logger            sdklogging.Logger
}

func (w *Worker) RegisterProcessor(jobType string, processor JobProcessor) error {
	w.processorRegistry[jobType] = processor

	return nil
}

// A worker monitors queue, and use a processor to perform job
func NewWorker(q *Queue, db storage.Storage) *Worker {
	w := &Worker{
		q:      q,
		db:     db,
		logger: q.logger,

		processorRegistry: make(map[string]JobProcessor),
	}

	return w
}

// isOrphanedJob checks if the error indicates a deleted/missing task
func (w *Worker) isOrphanedJob(err error) bool {
	errorMsg := err.Error()
	return strings.Contains(errorMsg, "task not found in storage") ||
		strings.Contains(errorMsg, "task may have been deleted") ||
		strings.Contains(errorMsg, "storage key is incorrect")
}

// wake up and pop first item in the queue to process
func (w *Worker) ProcessSignal(jid uint64) {
	job, err := w.q.Dequeue()
	if err != nil {
		w.logger.Error("failed to dequeue", "error", err)
		return
	}

	// Single consolidated log for job processing start
	w.logger.Info("processing job", "job_id", jid, "task_id", job.Name, "job_type", job.Type)

	processor, ok := w.processorRegistry[job.Type]
	if ok {
		err = processor.Perform(job)
	} else {
		w.logger.Warn("unsupported job type", "job_id", jid, "task_id", job.Name, "job_type", job.Type)
		err = fmt.Errorf("unsupported job type: %s", job.Type)
	}

	if err == nil {
		if markErr := w.q.markJobDone(job, jobComplete); markErr != nil {
			w.logger.Error("failed to mark job as complete", "error", markErr, "job_id", jid)
		} else {
			w.logger.Info("job completed successfully", "job_id", jid, "task_id", job.Name)
		}
	} else {
		// Check if this is an orphaned job (task deleted)
		isOrphanedJob := w.isOrphanedJob(err)
		if isOrphanedJob {
			// Mark as permanently failed - don't retry orphaned jobs
			if markErr := w.q.markJobDone(job, jobFailed); markErr != nil {
				w.logger.Error("failed to mark orphaned job as failed", "error", markErr, "job_id", jid)
			}
			// Use Info level instead of Warn to avoid stack traces
			w.logger.Info("orphaned job removed (task deleted)",
				"job_id", jid,
				"task_id", job.Name,
				"reason", "task_not_found")
		} else {
			// Regular failure - could be retried
			if markErr := w.q.markJobDone(job, jobFailed); markErr != nil {
				w.logger.Error("failed to mark job as failed", "error", markErr, "job_id", jid)
			}
			w.logger.Error("job processing failed",
				"job_id", jid,
				"task_id", job.Name,
				"job_type", job.Type,
				"error", err.Error())
		}
	}
}

func (w *Worker) loop() {
	for {
		select {
		case jid := <-w.q.eventCh:
			w.ProcessSignal(jid)
		case <-w.q.closeCh: // loop was stopped
			return
		}
	}
}

func (w *Worker) MustStart() {
	go func() {
		w.loop()
	}()
}
