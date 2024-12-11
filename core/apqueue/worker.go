package apqueue

import (
	"github.com/AvaProtocol/ap-avs/storage"

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

func (w *Worker) loop() {
	for {
		select {
		case jid := <-w.q.eventCh:
			w.logger.Info("process job from queue", "job_id", jid)
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
				w.q.markJobDone(job, jobComplete)
				w.logger.Info("succesfully perform job", "job_id", jid, "task_id", job.Name)
			} else {
				// TODO: move to a retry queue depend on what kind of error
				w.q.markJobDone(job, jobFailed)
				w.logger.Error("failed to perform job", "error", err, "job_id", jid, "task_id", job.Name)
			}
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
