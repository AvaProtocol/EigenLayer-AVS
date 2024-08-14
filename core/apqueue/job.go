package apqueue

import (
	"encoding/json"
	"fmt"
)

// jobStatus Enum Type
type jobStatus uint8

const (
	// jobPending : waiting to be processed
	jobPending jobStatus = iota
	// jobInProgress : processing in progress
	jobInProgress
	// jobComplete : processing complete
	jobComplete
	// jobFailed : processing errored out
	jobFailed
)

type Job [T]struct {
	// external reference id. example if this is a job to run a task, external id hold the data
	// This allow us to do fast lookup without decode job data
	ExternaID string
	Name      string
	Data      T
	Type      string

	// id of the job in the queue system
	// This ID is generate by this package in a sequence. The ID is gurantee to
	// be unqiue and can be used as part of the signature as nonce
	ID uint64
}

func encodeJob(j *Job) ([]byte, error) {
	return json.Marshal(j)
}

func decodeJob(b []byte) (*Job, error) {
	var j *Job
	err := json.Unmarshal(b, j)
	return j, err
}

func getQueueKeyPrefix(status jobStatus) string {
	return fmt.Sprintf("apq:%v:", status)
}

func getJobKey(status jobStatus, jID uint64) []byte {
	return []byte(getQueueKeyPrefix(status) + jIDString(jID))
}
