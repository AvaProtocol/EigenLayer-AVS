# Analysis: Workflow Enable/Disable Toggle

## Background

The original design used a permanent "Canceled" state — once canceled, a workflow
could never be turned back on. The goal is to support a reversible
**Enabled/Disabled** toggle instead: disabling removes the workflow from operator
monitoring queues, and re-enabling sends it back.

## Current State of the Codebase

### Already Migrated

The codebase has **already been migrated** from permanent cancellation to a
toggleable Enabled/Disabled model. The protobuf `TaskStatus` enum
(`protobuf/avs.proto:331-339`) no longer contains a `Canceled` value:

```protobuf
enum TaskStatus {
  Enabled   = 0;
  Completed = 1;
  Failed    = 2;
  Running   = 4;
  Disabled  = 5;
}
```

The `SetTaskEnabled` gRPC RPC (`protobuf/avs.proto`, `aggregator/rpc_server.go:570-589`)
accepts a boolean `enabled` flag and delegates to `Engine.SetTaskEnabledByUser`
(`core/taskengine/engine.go:3102-3236`), which handles both directions:

| From State   | To Enabled            | To Disabled           |
|--------------|-----------------------|-----------------------|
| **Enabled**  | Idempotent no-op      | Allowed (notifies operators) |
| **Disabled** | **Allowed**           | Idempotent no-op      |
| **Running**  | No-op (already active)| Blocked               |
| **Completed**| Blocked (terminal)    | Blocked (terminal)    |
| **Failed**   | Blocked (terminal)    | Blocked (terminal)    |

### How Disable Works (Complete Path)

1. `SetTaskEnabledByUser(user, id, false)` — `engine.go:3174`
2. Task status set to `Disabled` — `model/task.go:297`
3. Persisted to storage as `t:i:<taskId>` — `storage/schema/task.go` (i = inactive)
4. Old storage key `t:a:<taskId>` deleted
5. Task removed from in-memory `n.tasks` map — `engine.go:3214-3215`
6. `MessageOp_DisableTask` queued via batched notification — `engine.go:3218`
7. Operator receives message, calls `RemoveCheck(taskID)` on all trigger monitors
   (event, block, time) — `operator/process_message.go:46-121`

### How Re-Enable Works (Current Implementation)

1. `SetTaskEnabledByUser(user, id, true)` — `engine.go:3172`
2. Task status set to `Enabled` — `model/task.go:292`
3. Persisted to storage as `t:a:<taskId>`
4. Old storage key `t:i:<taskId>` deleted
5. Task added back to in-memory `n.tasks` map — `engine.go:3210-3212`
6. **No immediate operator notification** — the 15-minute `StreamCheckToOperator`
   ticker loop eventually detects the untracked task and sends
   `MonitorTaskTrigger` to operators — `engine.go:1086-1377`

## Identified Gap: Re-Enable Notification Latency

| Operation  | Latency to operators |
|------------|---------------------|
| **Disable**  | Seconds (batched notification flush) |
| **Re-Enable**| **Up to 15 minutes** (next ticker cycle) |

When a task is re-enabled, only the in-memory map is updated (`engine.go:3208-3212`):

```go
if enabled {
    n.lock.Lock()
    n.tasks[task.Id] = task
    n.lock.Unlock()
    // No operator notification! Relies on 15-min ticker.
}
```

Versus the disable path (`engine.go:3213-3218`):

```go
} else {
    n.lock.Lock()
    delete(n.tasks, task.Id)
    n.lock.Unlock()
    n.notifyOperatorsTaskOperation(taskID, avsproto.MessageOp_DisableTask)
}
```

## Implementation Plan

### Step 1: Add Immediate Operator Notification on Re-Enable

After re-enabling a task and adding it to the in-memory map, send a
`MonitorTaskTrigger` message directly to connected operators with full task
metadata. This mirrors the pattern already used by
`instructOperatorImmediateTrigger` (`engine.go:2002-2068`).

**File**: `core/taskengine/engine.go`

In `SetTaskEnabledByUser`, after the enable branch at line 3209:

```go
if enabled {
    n.lock.Lock()
    n.tasks[task.Id] = task
    n.lock.Unlock()

    // NEW: Send MonitorTaskTrigger directly to operators
    n.sendMonitorTaskTriggerToOperators(task)
}
```

### Step 2: Extract `sendMonitorTaskTriggerToOperators` Helper

Factor the send logic into a reusable function that:

1. Iterates all connected operator streams (`n.operatorStreams`)
2. Builds `SyncMessagesResp` with `MonitorTaskTrigger` op and `TaskMetadata`
   (trigger, expiration, remain, startAt)
3. Sends to each operator via gRPC stream with timeout
4. Updates `trackSyncedTasks` on success
5. Respects operator capabilities (`supportsTaskTrigger`)

This function would be callable from both the ticker loop and
`SetTaskEnabledByUser`.

### Step 3: Clean Up Legacy References

- Remove dead error code `TASK_ALREADY_CANCELLED = 4002` from
  `protobuf/avs.proto:298` — canceled is no longer a valid state
- Resolve merge conflicts in
  `integration_test/activation_deactivation_sync_test.go` (lines 100-126)

### Summary of Files to Modify

| File | Change |
|------|--------|
| `core/taskengine/engine.go` | Add `sendMonitorTaskTriggerToOperators()`; call it from `SetTaskEnabledByUser` enable branch |
| `protobuf/avs.proto` | Remove `TASK_ALREADY_CANCELLED` (optional cleanup) |
| `integration_test/activation_deactivation_sync_test.go` | Resolve merge conflicts; add re-enable test |

## Testing Plan

### Unit Tests (`core/taskengine/`)

1. **Re-enable sends immediate operator notification**
   - Create engine + task + mock operator stream
   - Disable task → verify `DisableTask` sent
   - Re-enable task → verify `MonitorTaskTrigger` sent immediately with correct metadata

2. **Enable/disable round-trip preserves task data**
   - Create task with specific trigger config
   - Disable → re-enable → verify task data identical

3. **Idempotent enable on already-enabled task**
   - Call `SetTaskEnabled(true)` on enabled task
   - Returns success, no operator notification sent

4. **Idempotent disable on already-disabled task**
   - Call `SetTaskEnabled(false)` on disabled task
   - Returns success, no operator notification sent

5. **Terminal states remain blocked**
   - Completed/Failed tasks cannot be enabled or disabled

6. **Running task guard**
   - Running task cannot be disabled; enable returns idempotent success

### Integration Tests (`integration_test/`)

7. **Full enable → disable → re-enable cycle with operator stream**
   - Create task → `MonitorTaskTrigger` to operator
   - Disable → `DisableTask` to operator, triggers removed
   - Re-enable → `MonitorTaskTrigger` sent again, triggers re-registered

8. **Disable during operator disconnect**
   - Disable while operator offline
   - Operator reconnects → task NOT sent (still disabled)

9. **Enable during operator disconnect**
   - Re-enable while operator offline
   - Operator reconnects → task sent via normal ticker

10. **System disable (overload) then user re-enable**
    - Auto-disabled via `ReportEventOverload`
    - User re-enables → operators resume monitoring

### Storage Tests

11. **Storage key migration on status change**
    - Enable: key `t:a:<id>` exists
    - Disable: `t:a:<id>` deleted, `t:i:<id>` created
    - Re-enable: `t:i:<id>` deleted, `t:a:<id>` restored

12. **GetTaskByID finds disabled tasks**
    - Disabled tasks removed from in-memory map
    - `GetTaskByID` scans all status prefixes → finds disabled task
