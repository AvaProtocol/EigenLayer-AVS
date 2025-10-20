# Execution Index Fix: Complete Implementation

This document describes the complete solution to execution index issues in the EigenLayer AVS system, covering both the initial race condition fix and the subsequent stability fix.

## Problem 1: Race Condition with Duplicate Indexes

### Initial Symptoms

Mixed blocking and non-blocking executions were producing duplicate execution indexes like `[0, 0, 1]` instead of the expected unique sequential indexes `[0, 1, 2]`.

### Root Cause

1. **Blocking executions**: Used `task.ExecutionCount - 1` for the index, which was set during task execution
2. **Non-blocking pending executions**: Used a hardcoded index of `0` when creating pending execution records
3. **Race condition**: Multiple executions could read the same `ExecutionCount` value before it was incremented, leading to duplicate indexes

## Solution 1: Atomic Index Assignment

### Implementation

Created a new atomic indexing system in `core/taskengine/execution_index.go`:

- **`AssignNextExecutionIndex(task)`**: Atomically assigns the next sequential index using BadgerDB's `IncCounter` operation
- **`GetCurrentExecutionIndex(task)`**: Retrieves current counter without incrementing
- **`ResetExecutionIndexCounter(task)`**: Resets counter for task deletion/cleanup

### Database-Level Atomicity

Uses BadgerDB's built-in atomic counter operations:
- `db.IncCounter(key, defaultValue)` - Atomically increments and returns new value
- `db.GetCounter(key, defaultValue)` - Safely reads counter value
- `db.SetCounter(key, value)` - Sets counter to specific value

### Integration Points

**Executor** (`executor.go`):
- Updated `NewExecutor` to accept engine reference
- Modified index assignment from `task.ExecutionCount - 1` to atomic assignment
- Added fallback to old method if atomic assignment fails

**Engine** (`engine.go`):
- Updated pending execution creation to use atomic indexing
- All calls to `NewExecutor` now pass engine reference

### Benefits

- **Thread-safe**: Atomic operations prevent race conditions
- **Sequential indexing**: Guarantees unique consecutive indexes starting from 0
- **Mixed execution support**: Works correctly for both blocking and non-blocking executions
- **Backward compatible**: Fallback to old method if atomic assignment fails

### Test Results

✅ **Sequential Execution Test**: Passes with indexes `[0, 1, 2, 3, 4]`
✅ **Mixed Execution Test**: Passes with unique sequential indexing
✅ **Atomic Assignment Test**: Passes with proper counter increment

Example output:
```
Execution 0: ID=01K7ZJ0F7007S2MMYP55WZJXAX, Index=0, ExecutionCount=1
Execution 1: ID=01K7ZJ0F77JGS5J4CWWKNGH0G1, Index=1, ExecutionCount=2
Execution 2: ID=01K7ZJ0F785NABYK904S563H91, Index=2, ExecutionCount=3
Execution 3: ID=01K7ZJ0F79MBX9K738XW7Z0C29, Index=3, ExecutionCount=4
Execution 4: ID=01K7ZJ0F7ASEFBMF5QFGMVJH1V, Index=4, ExecutionCount=5
```

## Problem 2: Execution Index Instability

### Symptoms

The atomic execution index assignment was not fully working for non-blocking executions:

- Non-blocking trigger returns immediate executionId with index 0 (pending state)
- After execution completion, the same executionId would have index 1
- This broke the guaranteed stability of execution indexes throughout their lifecycle

### Root Cause

The executor (`core/taskengine/executor.go`) was always calling `AssignNextExecutionIndex` even for executions that already had a pre-assigned atomic index stored in pending storage during non-blocking trigger creation.

## Solution 2: Check Pre-Assigned Indexes

### Implementation

Modified the executor to check for pre-assigned execution indexes before assigning new ones:

1. **Check pending storage first**: Look for pre-assigned index using `PendingExecutionKey`
2. **Use stored index if found**: Parse and use the pre-assigned atomic index
3. **Fallback to new assignment**: Only assign new index if no pre-assigned index exists

### Code Changes

**File**: `core/taskengine/executor.go`

```go
// Check if there's a pre-assigned execution index from non-blocking trigger first
var executionIndex int64
pendingKey := PendingExecutionKey(task, queueData.ExecutionID)
if pendingData, err := x.db.GetKey(pendingKey); err == nil {
    // Try to parse the pre-assigned index from pending storage
    if storedIndex, parseErr := strconv.ParseInt(string(pendingData), 10, 64); parseErr == nil {
        executionIndex = storedIndex
        x.logger.Info("Using pre-assigned execution index from pending storage", 
            "task_id", task.Id, "execution_id", queueData.ExecutionID, "index", executionIndex)
    } else {
        // Pending data exists but not a valid index, assign new atomic index
        newIndex, indexErr := x.engine.AssignNextExecutionIndex(task)
        if indexErr != nil {
            x.logger.Error("Failed to assign execution index", "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", indexErr)
            executionIndex = task.ExecutionCount - 1 // Fallback
        } else {
            executionIndex = newIndex
        }
        x.logger.Debug("Assigned new execution index (pending data not an index)", 
            "task_id", task.Id, "execution_id", queueData.ExecutionID, "index", executionIndex)
    }
} else {
    // No pending data found, assign new atomic index for blocking executions
    newIndex, indexErr := x.engine.AssignNextExecutionIndex(task)
    if indexErr != nil {
        x.logger.Error("Failed to assign execution index", "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", indexErr)
        executionIndex = task.ExecutionCount - 1 // Fallback
    } else {
        executionIndex = newIndex
    }
    x.logger.Debug("Assigned new execution index (no pending data)", 
        "task_id", task.Id, "execution_id", queueData.ExecutionID, "index", executionIndex)
}
```

### Testing

Created comprehensive test (`TestNonBlockingExecutionFullLifecycle`) that:

1. Pre-assigns an atomic execution index and stores it in pending storage
2. Executes the task using the executor
3. Verifies the final execution index matches the pre-assigned index
4. Detects when the bug occurs (index changes during lifecycle)

### Test Results

**Before Fix:**
```
Pre-assigned index: 0
Final execution index: 1
❌ CRITICAL BUG DETECTED: Execution index changed during lifecycle!
```

**After Fix:**
```
Pre-assigned index: 0
Final execution index: 0
✅ Index stability verified: execution index remained 0 throughout lifecycle
```

### Verification

All related tests pass:
- `TestNonBlockingExecutionFullLifecycle` - New test for full lifecycle stability
- `TestNonBlockingExecutionStability` - Existing test for pending state stability  
- `TestExecutionIndexIncrement` - Existing test for sequential execution indexing

## Complete Impact

The combined fixes ensure that:

1. **Unique sequential indexes** for all executions within a workflow
2. **Thread-safe operation** for concurrent execution triggers
3. **Stable indexes** throughout the complete execution lifecycle
4. **Non-blocking executions** maintain stable indexes from trigger response to completion
5. **Blocking executions** continue to work correctly with atomic index assignment
6. **Sequential executions** maintain proper incremental indexing
7. **Correct ordering** that clients can rely on for execution chronology
8. **No breaking changes** to existing API or client behavior
9. **Backwards compatibility** is preserved with fallback mechanisms

The implementation uses BadgerDB's atomic operations to provide database-level consistency, eliminating the race condition at its source, while the stability fix ensures indexes remain constant throughout an execution's lifecycle.

This resolves both:
- The original race condition issue with duplicate indexes in mixed execution scenarios
- The SDK test failure: "should maintain stable executionId from trigger response to getExecution for non-blocking"

## Files Modified

### Core Implementation
- `core/taskengine/execution_index.go` - Atomic indexing logic
- `core/taskengine/executor.go` - Uses atomic indexing and checks pre-assigned indexes
- `core/taskengine/engine.go` - Updated NewExecutor calls and pending execution logic
- `pkg/erc4337/preset/builder.go` - Build error fix (unrelated)

### Test Files
- `core/taskengine/execution_index_test.go` - Original test updated to use engine
- `core/taskengine/execution_index_mixed_test.go` - Comprehensive mixed execution tests
- `core/taskengine/non_blocking_full_lifecycle_test.go` - New lifecycle stability test
- All executor test files - Updated to use `NewExecutorForTesting`

## Dependencies

- Added `strconv` import to `executor.go` for `ParseInt` function
- Utilizes existing `PendingExecutionKey` function from schema
- Compatible with existing atomic execution index infrastructure
- Uses BadgerDB's atomic counter operations

