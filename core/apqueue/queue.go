package apqueue

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	badger "github.com/dgraph-io/badger/v4"
)

type Queue struct {
	db     storage.Storage
	dbLock sync.Mutex

	closeCh  chan bool
	eventCh  chan uint64
	stopOnce sync.Once
	deferred sync.WaitGroup

	prefix string

	seq    storage.Sequence
	logger sdklogging.Logger

	// chainIDs is the list of chain ids this queue should consult when
	// cleaning up orphaned jobs — each task can live under any chain's
	// chain-scoped storage prefix. Empty/nil falls back to the legacy
	// single-chain lookup for backward compat.
	chainIDs []int64
}

type QueueOption struct {
	Prefix   string
	ChainIDs []int64
}

// newQueue creates new ueue
func New(db storage.Storage, logger sdklogging.Logger, opts *QueueOption) *Queue {
	q := Queue{
		db:     db,
		dbLock: sync.Mutex{},

		eventCh: make(chan uint64, 1000),
		closeCh: make(chan bool),

		logger: logger,
	}

	if opts != nil {
		if opts.Prefix == "" {
			q.prefix = "d"
		} else {
			q.prefix = opts.Prefix
		}
		q.chainIDs = opts.ChainIDs
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

	rows, restoreErr := q.db.GetByPrefix(q.getQueueKeyPrefix(jobDeferred))
	if restoreErr != nil {
		return restoreErr
	}
	for _, row := range rows {
		j, e := decodeJob(row.Value)
		if e != nil {
			return e
		}
		if e = q.db.Move(row.Key, q.getJobKey(jobPending, j.ID)); e != nil {
			return e
		}
	}
	// A prior process may have moved a deferred row to pending and stopped
	// before delivering its in-memory wakeup. Only pending rows are restored;
	// replaying an in-progress execution could duplicate its side effects.
	rows, restoreErr = q.db.GetByPrefix(q.getQueueKeyPrefix(jobPending))
	if restoreErr != nil {
		return restoreErr
	}
	restored := make([]uint64, 0, len(rows))
	for _, row := range rows {
		job, decodeErr := decodeJob(row.Value)
		if decodeErr != nil {
			return decodeErr
		}
		restored = append(restored, job.ID)
	}
	// Do not drop wakeups when recovery exceeds the channel buffer. The
	// producer waits for workers, and Stop also releases it.
	q.deferred.Add(1)
	go func() {
		defer q.deferred.Done()
		for _, id := range restored {
			select {
			case q.eventCh <- id:
			case <-q.closeCh:
				return
			}
		}
	}()

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
	q.dbLock.Lock()
	q.stopOnce.Do(func() { close(q.closeCh) })
	q.dbLock.Unlock()
	q.deferred.Wait()
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
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("unknown panic: %v", r)
			}
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

// markJobDone moves a job from the inprogress status to complete/failed.
//
// If the inprogress key is missing, that means CleanupOrphanedJobs (which
// scans pending/inprogress/failed for jobs whose task no longer exists)
// already deleted it out from under us — the task was removed while this
// worker held the job. Treat that as a benign no-op so the caller doesn't
// log Error and trigger a Sentry capture for an expected race.
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
	if err != nil && isKeyNotFound(err) {
		return nil
	}
	return err
}

// isKeyNotFound reports whether err is BadgerDB's "Key not found". The
// storage.Storage interface surfaces the badger error verbatim from Move,
// so errors.Is catches the sentinel and the string-equality fallback
// covers cases where a wrapper has flattened the error into a plain
// errors.New with the same message.
func isKeyNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, badger.ErrKeyNotFound) {
		return true
	}
	return err.Error() == badger.ErrKeyNotFound.Error()
}

func (q *Queue) getQueueKeyPrefix(status jobStatus) []byte {
	return []byte(fmt.Sprintf("q:%s:%v:", q.prefix, status))
}

func (q *Queue) getJobKey(status jobStatus, jID uint64) []byte {
	return append(q.getQueueKeyPrefix(status), []byte(fmt.Sprintf("%d", jID))...)
}

// deferJob keeps a paused run outside the pending queue to avoid starving other
// wallets. A restart restores deferred rows; no execution counter is advanced.
func (q *Queue) deferJob(job *Job, delay time.Duration) error {
	key := q.getJobKey(jobDeferred, job.ID)
	q.dbLock.Lock()
	err := q.db.Move(q.getJobKey(jobInProgress, job.ID), key)
	if err != nil {
		q.dbLock.Unlock()
		return err
	}
	select {
	case <-q.closeCh:
		q.dbLock.Unlock()
		return nil // the durable deferred row is recovered at the next start
	default:
	}
	q.deferred.Add(1)
	q.dbLock.Unlock()
	go q.resumeDeferred(key, job.ID, delay)
	return nil
}

func (q *Queue) resumeDeferred(key []byte, id uint64, delay time.Duration) {
	defer q.deferred.Done()
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-q.closeCh:
			return
		case <-ticker.C:
			q.dbLock.Lock()
			err := q.db.Move(key, q.getJobKey(jobPending, id))
			q.dbLock.Unlock()
			if isKeyNotFound(err) { // task cleanup won the race
				return
			}
			if err != nil {
				q.logger.Error("failed to resume paused job", "job_id", id, "error", err)
				continue
			}
			select {
			case q.eventCh <- id:
			case <-q.closeCh:
			}
			return
		}
	}
}
