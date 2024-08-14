package apqueue

import (
	"errors"
	"fmt"
	"sync"

	"github.com/AvaProtocol/ap-avs/storage"
)

type Queue struct {
	db storage.Storage

	seq    storage.Sequence
	dbLock sync.Mutex

	eventCh chan int
	quitCh  chan bool
}

// newQueue creates new ueue
func New(db storage.Storage) *Queue {
	q := Queue{
		db:     db,
		dbLock: sync.Mutex{},

		eventCh: make(chan int, 1000),
		closeCh: make(chan bool),
	}

	return &q
}

// start Queue, panic if there is any error
func (q *Queue) MustStart() error {
	var err error
	q.seq, err = q.db.GetSequence([]byte("apqueue"), 1000)

	if err != nil {
		panic(err)
	}

	go func() {
		for {
			select {
			case jid := <-q.eventCh:
				q.Dequeue()
			case <-q.closeCh: // loop was stopped
				return
			}
		}
	}()

	return err
}

// stop Queue and Release resources
func (q *Queue) Stop() error {
	// release sequence to avoid wasting counter
	err := q.seq.Release()
	if err != nil {
		return err
	}

	return nil
}

func getNextSeq(seq storage.Sequence) (num uint64, err error) {
	defer func() {
		r := recover()
		if r != nil {
			// recover from panic and send err instead
			err = r.(error)
		}
	}()

	num, err = seq.Next()
	return num, err
}

// enqueueJob enqueues a new Job to the Pending queue
func (q *Queue) Enqueue(externalID string, name string, data []byte) (uint64, error) {
	num, err := getNextSeq(q.seq)
	if err != nil {
		return 0, err
	}

	j := &Job{
		Name:      name,
		ExternaID: externalID,
		Data:      data,

		ID: num + 1,
	}
	jKey := getJobKey(jobPending, j.ID)

	b, err := encodeJob(j)
	if err != nil {
		return 0, err
	}

	err = q.db.Set([]byte(jKey), b)

	if err != nil {
		return 0, err
	}

	return j.ID, nil
}

func jIDString(jID uint64) string {
	return fmt.Sprintf("%020d", jID)
}

// dequeueJob moves the next pending job from the pending status to inprogress
func (q *Queue) Dequeue() (*Job, error) {
	var j *Job

	q.dbLock.Lock()
	defer q.dbLock.Unlock()

	prefix := []byte(getQueueKeyPrefix(jobPending))
	k, v, err := q.db.FirstKVHasPrefix(prefix)
	if err != nil {
		return nil, err
	}

	// there is no more job
	if k == nil {
		return nil, nil
	}

	j, err = decodeJob(v)
	if err != nil {
		return nil, err
	}

	// Move from from Pending queue to InProgress queue
	err = q.db.Move(k, getJobKey(jobInProgress, j.ID))

	return j, err
}

// markJobDone moves a job from the inprogress status to complete/failed
func (q *Queue) markJobDone(id uint64, status jobStatus) error {
	if status != jobComplete && status != jobFailed {
		return errors.New("Can only move to Complete or Failed Status")
	}

	src := getJobKey(jobInProgress, id)
	dest := getJobKey(status, id)
	q.dbLock.Lock()
	defer q.dbLock.Unlock()
	err := q.db.Move(src, dest)
	return err
}
