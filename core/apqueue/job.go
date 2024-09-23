package apqueue

import (
	"encoding/json"
)

// jobStatus Enum Type
type jobStatus string

const (
	// Use a single char to save space
	// jobPending : waiting to be processed
	jobPending jobStatus = "q"
	// jobInProgress : processing in progress
	jobInProgress = "p"
	// jobComplete : processing complete
	jobComplete = "c"
	// jobFailed : processing errored out
	jobFailed = "f"
)

type Job struct {
	Type string `json:"t"`
	Name string `json:"n"`
	Data []byte `json:"d"`

	EnqueueAt uint64 `json:"q,omitempty"`
	Attempt   uint64 `json:"a,omitempty"`

	// id of the job in the queue system
	// This ID is generate by this package in a sequence. The ID is gurantee to
	// be unqiue and can be used as part of the signature as nonce
	ID uint64 `json:"id"`
}

func encodeJob(j *Job) ([]byte, error) {
	return json.Marshal(j)
}

func decodeJob(b []byte) (*Job, error) {
	j := Job{}
	err := json.Unmarshal(b, &j)
	return &j, err
}
