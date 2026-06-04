package schema

import (
	"fmt"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// On-disk key layout note: the leading "t:" prefix is preserved for storage
// backwards-compatibility with pre-rename data. Renaming the keys themselves
// would be a breaking migration; renaming the Go function names is not.

// WorkflowStatusToStorageKey maps a workflow status enum to its on-disk key
// segment.
//
//	c: completed — terminal, no longer triggered
//	f: failed    — terminal, no longer triggered
//	x: running   — currently executing
//	i: disabled  — paused by user, not triggered
//	a: enabled   — actively triggered
func WorkflowStatusToStorageKey(v avsproto.TaskStatus) string {
	switch v {
	case avsproto.TaskStatus_Completed:
		return "c"
	case avsproto.TaskStatus_Failed:
		return "f"
	case avsproto.TaskStatus_Disabled:
		return "i"
	case avsproto.TaskStatus_Running:
		return "x"
	case avsproto.TaskStatus_Enabled:
		return "a"
	default:
		return "a"
	}
}

// WorkflowStorageKey constructs the single-chain workflow storage key for the
// given workflow ID and status.
func WorkflowStorageKey(id string, status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%s:%s", WorkflowStatusToStorageKey(status), id))
}

// WorkflowByStatusStoragePrefix returns the single-chain prefix that scans all
// workflows with the given status.
func WorkflowByStatusStoragePrefix(status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%s:", WorkflowStatusToStorageKey(status)))
}

// ChainWorkflowStorageKey constructs a chain-scoped workflow storage key for
// gateway mode, where one DB holds rows across multiple chain buckets.
func ChainWorkflowStorageKey(chainID int64, id string, status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%d:%s:%s", chainID, WorkflowStatusToStorageKey(status), id))
}

// ChainWorkflowByStatusStoragePrefix returns the chain-scoped prefix that scans
// all workflows of the given chain + status.
func ChainWorkflowByStatusStoragePrefix(chainID int64, status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%d:%s:", chainID, WorkflowStatusToStorageKey(status)))
}
