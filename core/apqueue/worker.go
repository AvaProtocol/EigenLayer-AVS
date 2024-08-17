package apqueue

import (
	"log"

	"github.com/AvaProtocol/ap-avs/storage"
)

type JobProcessor interface {
	Perform(j *Job) error
}

type Worker struct {
	q  *Queue
	db storage.Storage

	processorRegistry map[string]JobProcessor
}

func (w *Worker) RegisterProcessor(jobType string, processor JobProcessor) error {
	w.processorRegistry[jobType] = processor

	return nil
}

// A worker monitors queue, and use a processor to perform job
func NewWorker(q *Queue, db storage.Storage) *Worker {
	w := &Worker{
		q:                 q,
		db:                db,
		processorRegistry: make(map[string]JobProcessor),
	}

	return w
}

func (w *Worker) loop() {
	for {
		select {
		case jid := <-w.q.eventCh:
			log.Println("got jobid", jid, "start dequeue")
			job, err := w.q.Dequeue()
			if err != nil {
				log.Println("dequeue error", err)
			}

			processor, ok := w.processorRegistry[job.Type]
			log.Println("Now let process the job with a processor", job)
			if ok {
				err = processor.Perform(job)
			} else {
				log.Println("unknow job", job, string(job.Data))
			}

			if err == nil {
				w.q.markJobDone(job, jobComplete)
			} else {
				// TODO: move to a retry queue depend on what kind of error
				w.q.markJobDone(job, jobFailed)
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
