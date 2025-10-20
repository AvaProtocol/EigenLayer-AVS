package taskengine

// EXECUTION INDEX RACE CONDITION FIX
//
// This file implements atomic execution index assignment to resolve a race condition
// where mixed blocking and non-blocking executions could produce duplicate indexes
// like [0, 0, 1] instead of the expected unique sequential indexes [0, 1, 2].
//
// The root cause was:
// - Blocking executions used task.ExecutionCount - 1 for index assignment
// - Non-blocking pending executions used hardcoded index 0
// - Race condition: multiple executions could read same ExecutionCount before increment
//
// Solution:
// - Use BadgerDB's atomic counter operations (IncCounter/GetCounter/SetCounter)
// - Maintain per-workflow counter in storage with key "execution_index_counter:<taskID>"
// - Atomic increment returns next index, ensuring uniqueness and sequentiality
// - Works for both blocking and non-blocking executions
//
// Thread Safety:
// BadgerDB's counter operations are atomic at the database level, preventing
// race conditions even with concurrent execution triggers.

import (
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// ExecutionIndexCounterKey creates a storage key for the atomic execution index counter
// This key is used to maintain a per-workflow counter for assigning sequential execution indexes
func ExecutionIndexCounterKey(taskID string) []byte {
	return []byte(fmt.Sprintf("execution_index_counter:%s", taskID))
}

// AssignNextExecutionIndex atomically assigns the next execution index for a given task/workflow.
// This ensures that all executions (blocking and non-blocking) get unique sequential indexes
// starting from 0 for each workflow.
//
// The function uses the database's atomic increment operation to ensure thread-safety and consistency
// even when multiple executions are triggered simultaneously.
//
// Returns the assigned index (0-based) and any error that occurred.
func (n *Engine) AssignNextExecutionIndex(task *model.Task) (int64, error) {
	counterKey := ExecutionIndexCounterKey(task.Id)

	// Use the database's atomic increment operation to get the next index
	// This starts from 0 for new workflows and increments by 1 each time
	nextCounterValue, err := n.db.IncCounter(counterKey, 0) // defaultValue=0 means start from 1, so index will be 0-based
	if err != nil {
		return 0, fmt.Errorf("failed to atomically increment execution index counter for task %s: %w", task.Id, err)
	}

	// The counter value is 1-based, but we want 0-based indexes
	assignedIndex := int64(nextCounterValue - 1)

	if n.logger != nil {
		n.logger.Debug("Assigned execution index",
			"task_id", task.Id,
			"assigned_index", assignedIndex,
			"counter_value", nextCounterValue)
	}

	return assignedIndex, nil
}

// GetCurrentExecutionIndex returns the current execution index counter for a task
// without incrementing it. This is useful for pending executions that need to know
// what their index will be.
//
// Returns the current index value and any error that occurred.
func (n *Engine) GetCurrentExecutionIndex(task *model.Task) (int64, error) {
	counterKey := ExecutionIndexCounterKey(task.Id)

	// Get the current counter value using the database's counter functionality
	currentCounterValue, err := n.db.GetCounter(counterKey, 0) // defaultValue=0 for new workflows
	if err != nil {
		return 0, fmt.Errorf("failed to get execution index counter for task %s: %w", task.Id, err)
	}

	// If counter is 0, the next assigned index would be 0
	// If counter is N, the next assigned index would be N
	return int64(currentCounterValue), nil
}

// ResetExecutionIndexCounter resets the execution index counter for a task.
// This should be called when a task is deleted or when starting fresh.
//
// Returns any error that occurred.
func (n *Engine) ResetExecutionIndexCounter(task *model.Task) error {
	counterKey := ExecutionIndexCounterKey(task.Id)

	// Reset the counter to 0 using SetCounter
	if err := n.db.SetCounter(counterKey, 0); err != nil {
		return fmt.Errorf("failed to reset execution index counter for task %s: %w", task.Id, err)
	}

	if n.logger != nil {
		n.logger.Debug("Reset execution index counter", "task_id", task.Id)
	}

	return nil
}
