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
(`protobuf/avs.proto`, `enum TaskStatus`) no longer contains a `Canceled` value:

```protobuf
enum TaskStatus {
  Enabled   = 0;
  Completed = 1;
  Failed    = 2;
  Running   = 4;
  Disabled  = 5;
}
```

The `SetTaskEnabled` gRPC RPC (`protobuf/avs.proto`, `aggregator/rpc_server.go`)
accepts a boolean `enabled` flag and delegates to `Engine.SetTaskEnabledByUser`
(`core/taskengine/engine.go`), which handles both directions:

| From State   | To Enabled            | To Disabled           |
|--------------|-----------------------|-----------------------|
| **Enabled**  | Idempotent no-op      | Allowed (notifies operators) |
| **Disabled** | **Allowed**           | Idempotent no-op      |
| **Running**  | No-op (already active)| Blocked               |
| **Completed**| Blocked (terminal)    | Blocked (terminal)    |
| **Failed**   | Blocked (terminal)    | Blocked (terminal)    |

### How Disable Works (Complete Path)

1. `SetTaskEnabledByUser(user, id, false)` — `engine.go`, in `SetTaskEnabledByUser`
2. Task status set to `Disabled` — `model/task.go`, `SetDisabled()`
3. Persisted to storage as `t:i:<taskId>` — `storage/schema/task.go` (i = inactive)
4. Old storage key `t:a:<taskId>` deleted
5. Task removed from in-memory `n.tasks` map
6. `MessageOp_DisableTask` queued via batched notification
7. Operator receives message, calls `RemoveCheck(taskID)` on all trigger monitors
   (event, block, time) — `operator/process_message.go`

### How Re-Enable Works (Current Implementation)

1. `SetTaskEnabledByUser(user, id, true)` — `engine.go`, in `SetTaskEnabledByUser`
2. Task status set to `Enabled` — `model/task.go`, `SetEnabled()`
3. Persisted to storage as `t:a:<taskId>`
4. Old storage key `t:i:<taskId>` deleted
5. Task added back to in-memory `n.tasks` map
6. `sendMonitorTaskTriggerToOperators(task)` sends `MonitorTaskTrigger` to all
   connected operators immediately — `engine.go`, in `sendMonitorTaskTriggerToOperators`

## Identified Gap: Re-Enable Notification Latency

| Operation  | Latency to operators |
|------------|---------------------|
| **Disable**  | Seconds (batched notification flush) |
| **Re-Enable**| **Up to 15 minutes** (next ticker cycle) |

When a task is re-enabled, the in-memory map is updated and operators are
notified immediately (in `SetTaskEnabledByUser`):

```go
if enabled {
    n.lock.Lock()
    n.tasks[task.Id] = task
    n.lock.Unlock()

    // Send MonitorTaskTrigger directly to operators
    n.sendMonitorTaskTriggerToOperators(task)
}
```

The disable path (also in `SetTaskEnabledByUser`):

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
`instructOperatorImmediateTrigger` (in `engine.go`).

**File**: `core/taskengine/engine.go`

In `SetTaskEnabledByUser`, after the enable branch:

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

- Deprecate error code `TASK_ALREADY_CANCELLED = 4002` in
  `protobuf/avs.proto` — cancelled state replaced by Disabled
- Resolve merge conflicts in
  `integration_test/activation_deactivation_sync_test.go`

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
