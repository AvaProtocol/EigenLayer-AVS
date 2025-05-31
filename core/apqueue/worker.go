package apqueue

import (
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

// wake up and pop first item in the queue to process
func (w *Worker) ProcessSignal(jid uint64) {
	w.logger.Info("process job from queue", "signal", jid)
	job, err := w.q.Dequeue()
	if err != nil {
		w.logger.Error("failed to dequeue", "error", err)
	}

	processor, ok := w.processorRegistry[job.Type]
	if ok {
		err = processor.Perform(job)
	} else {
		w.logger.Info("unsupported job", "job", string(job.Data))
	}
	w.logger.Info("decoded job", "job_id", jid, "jobName", job.Name, "jobdata", string(job.Data))

	if err == nil {
		if markErr := w.q.markJobDone(job, jobComplete); markErr != nil {
			w.logger.Error("failed to mark job as complete", "error", markErr, "job_id", jid)
		} else {
			w.logger.Info("successfully perform job", "job_id", jid, "task_id", job.Name)
		}
	} else {
		// TODO: move to a retry queue depend on what kind of error
		if markErr := w.q.markJobDone(job, jobFailed); markErr != nil {
			w.logger.Error("failed to mark job as failed", "error", markErr, "job_id", jid)
		}
		w.logger.Info("Job processing failed",
			"job_id", jid,
			"task_id", job.Name,
			"job_type", job.Type,
			"failure_reason", err.Error(),
			"job_data", string(job.Data))
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
