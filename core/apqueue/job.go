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

// String returns a human-readable status name for logging
func (s jobStatus) String() string {
	switch s {
	case jobPending:
		return "pending"
	case jobInProgress:
		return "in_progress"
	case jobComplete:
		return "complete"
	case jobFailed:
		return "failed"
	default:
		return string(s) // fallback to raw value
	}
}

// HumanReadable returns a descriptive status name for logging
func (s jobStatus) HumanReadable() string {
	switch s {
	case jobPending:
		return "⏳ pending"
	case jobInProgress:
		return "🔄 in_progress"
	case jobComplete:
		return "✅ complete"
	case jobFailed:
		return "❌ failed"
	default:
		return string(s) // fallback to raw value
	}
}

type Job struct {
	Type string `json:"t"`
	Name string `json:"n"`
	Data []byte `json:"d"`

	EnqueueAt uint64 `json:"q,omitempty"`
	Attempt   uint64 `json:"a,omitempty"`

	// id of the job in the queue system
	// This ID is generate by this package in a sequence. The ID is guarantee to
	// be unique and can be used as part of the signature as nonce
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
