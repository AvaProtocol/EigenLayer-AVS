# Integration Tests for Operator Reconnection

This directory contains comprehensive integration tests for the operator reconnection and task assignment functionality.

## Test Files

### 1. `operator_reconnection_test.go`
**Comprehensive end-to-end reconnection testing**

- `TestOperatorReconnectionFlow`: Complete operator lifecycle testing
  - ✅ Operator connects and receives tasks
  - ✅ Operator disconnects gracefully
  - ✅ Operator reconnects and receives tasks again
  - ✅ Validates task reclamation behavior

- `TestOperatorReconnectionRaceCondition`: Rapid reconnection stress testing
  - ✅ Tests multiple rapid reconnections
  - ✅ Verifies no hanging connections
  - ✅ Ensures system stability under stress

### 2. `ticker_context_test.go`
**Ticker context and race condition testing**

- `TestTickerContextRaceCondition`: Context management testing
  - ✅ Tests ticker context cancellation
  - ✅ Verifies old tickers are properly canceled
  - ✅ Ensures no race conditions between old/new connections

- `TestOperatorConnectionStabilization`: Connection stability testing
  - ✅ Tests the 10-second stabilization period
  - ✅ Verifies tasks are sent after stabilization
  - ✅ Validates connection stability logic

### 3. `orphaned_task_reclamation_test.go` 
**Focused testing for specific fixes implemented**

- `TestOrphanedTaskReclamation`: Tests our core fixes
  - ✅ Orphaned task reclamation after disconnection
  - ✅ Multiple consecutive reconnections
  - ✅ Task assignment consistency
  - ✅ Verifies no tasks are lost during reconnection

- `TestMonotonicClockTaskReset`: MonotonicClock behavior testing
  - ✅ Same MonotonicClock reconnection handling
  - ✅ Lower MonotonicClock reconnection handling  
  - ✅ Task tracking reset verification

## Running the Tests

### Individual Tests
```bash
# Test specific reconnection scenario
cd integration_test
go test -v -run TestOrphanedTaskReclamation

# Test ticker context management
go test -v -run TestTickerContextRaceCondition

# Test connection stabilization
go test -v -run TestOperatorConnectionStabilization
```

### All Tests
```bash
cd integration_test
go test -v
```

### With Detailed Logging
```bash
cd integration_test
go test -v -args -verbose
```

## Test Coverage

### ✅ Scenarios Covered

1. **Normal Reconnection Flow**
   - Initial connection and task assignment
   - Clean disconnection 
   - Reconnection with task reclamation
   - Subscription recreation

2. **Edge Cases**
   - Same MonotonicClock reconnection
   - Lower MonotonicClock reconnection
   - Rapid consecutive reconnections
   - Multiple operators connecting simultaneously

3. **Race Conditions**
   - Ticker context race conditions
   - Task assignment race conditions
   - Connection cleanup race conditions

4. **Task Assignment Logic**
   - Orphaned task detection and reclamation
   - Round-robin assignment preservation
   - Task tracking reset behavior

### ✅ Fixes Validated

1. **Ticker Context Management**
   - Old tickers properly canceled on reconnection
   - Context-aware ticker loops
   - No hanging connections or goroutines

2. **MonotonicClock Task Reset**
   - Task tracking reset regardless of MonotonicClock value
   - Force reset on ANY reconnection
   - Consistent task delivery

3. **Orphaned Task Reclamation**
   - Tasks reclaimed from empty string assignment
   - Tasks reclaimed when not in assignment map
   - Prevention of task assignment override

## Mock Components

### MockSyncMessagesServer
Simulates operator GRPC stream connections:
- ✅ Task reception tracking
- ✅ Disconnection simulation
- ✅ Context management
- ✅ Error simulation

### SimpleMockServer  
Lightweight version for basic testing:
- ✅ Basic connection simulation
- ✅ Send/receive operations
- ✅ Context cancellation

## Test Data

Tests use simplified task structures that bypass complex validation:
- ✅ EventTrigger tasks for subscription testing
- ✅ Minimal node/edge structures
- ✅ Direct task injection via `AddTaskForTesting()`

## Expected Results

### Successful Test Run Output
```
✅ Task sent on initial connection
✅ Task reclaimed after first reconnection  
✅ Task reclaimed after second reconnection
✅ No hanging connections or race conditions
```

### Performance Expectations
- Each test completes in ~30-45 seconds (due to stabilization periods)
- No memory leaks or hanging goroutines
- Consistent task assignment across reconnections

## Debugging

### Common Issues

1. **Test Timeouts**: Usually indicates ticker context not being properly canceled
2. **Task Not Received**: Check orphaned task reclamation logic
3. **Hanging Connections**: Verify context cancellation in ticker loops

### Debug Logging
Tests include extensive logging:
- Connection start/end events  
- Task assignment and reclamation
- Ticker context management
- Error conditions

### Manual Verification
Run aggregator and operator manually to verify:
```bash
# Terminal 1: Start aggregator
./out/ap aggregator --config=config/aggregator.yaml

# Terminal 2: Start operator  
./out/ap operator --config=config/operator.yaml

# Terminal 3: Check status
./out/ap status

# Look for logs:
# - "🔄 Reclaimed orphaned task for operator"
# - "📤 Streamed tasks to operator"
# - "✅ Created new subscription during update"
``` 