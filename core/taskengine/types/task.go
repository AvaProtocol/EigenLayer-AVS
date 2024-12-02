package types

type TaskID string

type StepExecution struct {
	NodeID string
	Logs   []string
	Result any
}
