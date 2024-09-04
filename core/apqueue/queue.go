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

	eventCh chan uint64
	closeCh chan bool

	prefix string
}

type QueueOption struct {
	Prefix string
}

// newQueue creates new ueue
func New(db storage.Storage, opts *QueueOption) *Queue {
	q := Queue{
		db:     db,
		dbLock: sync.Mutex{},

		eventCh: make(chan uint64, 1000),
		closeCh: make(chan bool),
	}

	if opts != nil {
		if opts.Prefix == "" {
			q.prefix = "d"
		} else {
			q.prefix = opts.Prefix
		}
	}

	return &q
}

// start Queue, panic if there is any error
func (q *Queue) MustStart() error {
	var err error
	q.seq, err = q.db.GetSequence([]byte("q:seq:"+q.prefix), 1000)

	if err != nil {
		panic(err)
	}

	return err
}

// When a queue being kill abruptedly, there is pending job that isn't being
// process or stuck.
// Recover reclaims them and fire them off again
func (q *Queue) Recover() error {
	return nil
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
func (q *Queue) Enqueue(jobType string, name string, data []byte) (uint64, error) {
	num, err := getNextSeq(q.seq)
	if err != nil {
		return 0, err
	}

	j := &Job{
		Type: jobType,
		Name: name,
		Data: data,

		ID: num + 1,
	}
	jKey := q.getJobKey(jobPending, j.ID)

	b, err := encodeJob(j)
	if err != nil {
		return 0, err
	}

	err = q.db.Set([]byte(jKey), b)

	if err != nil {
		return 0, err
	}
	q.eventCh <- j.ID

	return j.ID, nil
}

// dequeueJob moves the next pending job from the pending status to inprogress
func (q *Queue) Dequeue() (*Job, error) {
	var j *Job

	q.dbLock.Lock()
	defer q.dbLock.Unlock()

	prefix := []byte(q.getQueueKeyPrefix(jobPending))
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
	err = q.db.Move(k, q.getJobKey(jobInProgress, j.ID))

	return j, err
}

// markJobDone moves a job from the inprogress status to complete/failed
func (q *Queue) markJobDone(job *Job, status jobStatus) error {
	id := job.ID
	if status != jobComplete && status != jobFailed {
		return errors.New("Can only move to Complete or Failed Status")
	}

	src := q.getJobKey(jobInProgress, id)
	dest := q.getJobKey(status, id)
	q.dbLock.Lock()
	defer q.dbLock.Unlock()

	err := q.db.Move(src, dest)
	return err
}

func (q *Queue) getQueueKeyPrefix(status jobStatus) []byte {
	return []byte(fmt.Sprintf("q:%s:%v:", q.prefix, status))
}

func (q *Queue) getJobKey(status jobStatus, jID uint64) []byte {
	return append(q.getQueueKeyPrefix(status), []byte(fmt.Sprintf("%020d", jID))...)
}
