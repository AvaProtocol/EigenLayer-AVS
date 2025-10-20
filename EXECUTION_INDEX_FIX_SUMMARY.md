# Execution Index Race Condition Fix

## Problem Description

The original issue was that mixed blocking and non-blocking executions were producing duplicate execution indexes like `[0, 0, 1]` instead of the expected unique sequential indexes `[0, 1, 2]`.

### Root Cause

1. **Blocking executions**: Used `task.ExecutionCount - 1` for the index, which was set during task execution
2. **Non-blocking pending executions**: Used a hardcoded index of `0` when creating pending execution records
3. **Race condition**: Multiple executions could read the same `ExecutionCount` value before it was incremented, leading to duplicate indexes

## Solution Implemented

### 1. Atomic Index Assignment

Created a new atomic indexing system in `core/taskengine/execution_index.go`:

- **`AssignNextExecutionIndex(task)`**: Atomically assigns the next sequential index using BadgerDB's `IncCounter` operation
- **`GetCurrentExecutionIndex(task)`**: Retrieves current counter without incrementing
- **`ResetExecutionIndexCounter(task)`**: Resets counter for task deletion/cleanup

### 2. Database-Level Atomicity

Uses BadgerDB's built-in atomic counter operations:
- `db.IncCounter(key, defaultValue)` - Atomically increments and returns new value
- `db.GetCounter(key, defaultValue)` - Safely reads counter value
- `db.SetCounter(key, value)` - Sets counter to specific value

### 3. Integration Points Updated

**Executor** (`executor.go`):
- Updated `NewExecutor` to accept engine reference
- Modified index assignment from `task.ExecutionCount - 1` to atomic assignment
- Added fallback to old method if atomic assignment fails

**Engine** (`engine.go`):
- Updated pending execution creation to use atomic indexing
- All calls to `NewExecutor` now pass engine reference

**Tests**:
- Updated all test files to use `NewExecutorForTesting` helper
- Created comprehensive tests for atomic indexing behavior

### 4. Key Benefits

- **Thread-safe**: Atomic operations prevent race conditions
- **Sequential indexing**: Guarantees unique consecutive indexes starting from 0
- **Mixed execution support**: Works correctly for both blocking and non-blocking executions
- **Backward compatible**: Fallback to old method if atomic assignment fails

## Files Modified

### Core Implementation
- `core/taskengine/execution_index.go` - New atomic indexing logic
- `core/taskengine/executor.go` - Updated to use atomic indexing
- `core/taskengine/engine.go` - Updated NewExecutor calls and pending execution logic

### Test Updates
- `core/taskengine/execution_index_test.go` - Original test updated to use engine
- `core/taskengine/execution_index_mixed_test.go` - New comprehensive tests
- All executor test files - Updated to use `NewExecutorForTesting`

## Test Results

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

## Impact

This fix resolves the original race condition issue where users experienced duplicate execution indexes when mixing blocking and non-blocking executions. The solution ensures:

1. **Unique sequential indexes** for all executions within a workflow
2. **Thread-safe operation** for concurrent execution triggers  
3. **Correct ordering** that clients can rely on for execution chronology
4. **No breaking changes** to existing API or client behavior

The implementation uses BadgerDB's atomic operations to provide database-level consistency, eliminating the race condition at its source.