# Execution Index Stability Fix

## Problem

The atomic execution index assignment feature introduced in the execution index fix was not fully working for non-blocking executions. The issue was that execution indexes were being recalculated during execution completion, causing them to change from the pre-assigned value.

### Symptoms

- Non-blocking trigger returns immediate executionId with index 0 (pending state)
- After execution completion, the same executionId would have index 1
- This broke the guaranteed stability of execution indexes throughout their lifecycle

### Root Cause

The executor (`core/taskengine/executor.go`) was always calling `AssignNextExecutionIndex` even for executions that already had a pre-assigned atomic index stored in pending storage during non-blocking trigger creation.

## Solution

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

## Testing

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

## Verification

All related tests pass:
- `TestNonBlockingExecutionFullLifecycle` - New test for full lifecycle stability
- `TestNonBlockingExecutionStability` - Existing test for pending state stability  
- `TestExecutionIndexIncrement` - Existing test for sequential execution indexing

## Impact

This fix ensures that:

1. **Non-blocking executions** maintain stable indexes throughout their complete lifecycle
2. **Blocking executions** continue to work correctly with atomic index assignment
3. **Sequential executions** maintain proper incremental indexing
4. **Backwards compatibility** is preserved with fallback mechanisms

The fix resolves the SDK test failure:
> "should maintain stable executionId from trigger response to getExecution for non-blocking"

## Files Modified

- `core/taskengine/executor.go` - Main fix for index stability
- `pkg/erc4337/preset/builder.go` - Build error fix (unrelated)
- `core/taskengine/non_blocking_full_lifecycle_test.go` - New comprehensive test

## Dependencies

- Added `strconv` import to `executor.go` for `ParseInt` function
- Utilizes existing `PendingExecutionKey` function from schema
- Compatible with existing atomic execution index infrastructure