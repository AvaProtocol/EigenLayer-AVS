package schema

import (
	"fmt"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TaskStatusToStorageKey converts a task status enum to its storage key prefix
// c: completed - task is completed and no longer being checked for trigger anymore
// f: failed - task failed to execute, and no longer being checked for trigger anymore
// x: running - task is currently executing
// i: disabled - task is turned off by user, no longer being checked for trigger
// a: enabled - task is enabled, and will be checked for triggering. task may have executed zero or more times depending on repeatable or not
func TaskStatusToStorageKey(v avsproto.TaskStatus) string {
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

// TaskStorageKey constructs a task storage key for the given task ID and status
func TaskStorageKey(id string, status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(status), id))
}

// TaskByStatusStoragePrefix returns the storage prefix for all tasks with the given status
func TaskByStatusStoragePrefix(status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%s:", TaskStatusToStorageKey(status)))
}
