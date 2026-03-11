# Task Field Renovation — Final Plan

## 1. Design Principles

1. **`inputVariables.settings` holds ALL immutable task fields** — client-provided + server-enriched at creation
2. **`context`** (replaces `workflowContext`) holds only mutable Task-level runtime state written by the aggregator
3. **Task protobuf** remains the storage/API layer — fields copied from `settings` at creation
4. **Go code reads `vm.task.*` directly** — never from `context` or `settings` maps
5. **No storage backfill** — old workflows must be re-created by users

## 2. Two JS Variables

### `settings` — immutable definition (frozen at creation)

| Field | Set by | Description |
|---|---|---|
| `settings.name` | Client (REQUIRED) | Workflow name |
| `settings.runner` | Client (REQUIRED) | Smart wallet address |
| `settings.chain` | Client (REQUIRED) | Chain name (e.g. "sepolia") |
| `settings.chain_id` | Client (REQUIRED) | Chain ID (e.g. 11155111) |
| `settings.isSimulation` | Client (optional) | Simulation flag |
| `settings.uniswapv3_pool` | Client (optional) | Domain-specific config |
| `settings.uniswapv3_contracts` | Client (optional) | Domain-specific config |
| `settings.id` | Aggregator (on create) | Task ID |
| `settings.owner` | Aggregator (on create) | Owner EOA address |
| `settings.startAt` | Aggregator (from CreateTaskReq.start_at) | Task start time (ms) |
| `settings.expiredAt` | Aggregator (from CreateTaskReq.expired_at) | Task expiry time (ms) |
| `settings.maxExecution` | Aggregator (from CreateTaskReq.max_execution) | Max execution limit |

### `context` — mutable Task-level runtime state (updated each execution)

| Field | Persisted as | When written |
|---|---|---|
| `context.status` | `task.Status` | On state transitions |
| `context.executionCount` | `task.ExecutionCount` | Incremented before each execution |
| `context.lastRanAt` | `task.LastRanAt` | Updated before each execution |
| `context.completedAt` | `task.CompletedAt` | Set when task completes/fails |

Note: `executionIndex` is NOT a Task-level field. It belongs on the `Execution` message (`Execution.index`, protobuf field 6) — it's a per-execution sequential counter assigned by `AssignNextExecutionIndex()` and stored on the Execution object. It was previously stashed in `workflowContext` only as a plumbing hack for email subject formatting ("Run #X:") in `vm_runner_rest.go`. The proper fix is to pass it through the VM as a dedicated execution-scoped field (e.g. `vm.ExecutionIndex`), not as part of `context`.

## 3. Per-Field Migration Guide (for SDK)

### CreateTaskReq — what changes

| CreateTaskReq field | Current behavior | New behavior | SDK action |
|---|---|---|---|
| `name` (field 6) | Client sends, stored as `task.Name` | **Ignored.** Aggregator reads from `input_variables.settings.name` | Stop sending. Put in `input_variables.settings.name` instead |
| `smart_wallet_address` (field 3) | Client sends, stored as `task.SmartWalletAddress` | **Ignored.** Aggregator reads from `input_variables.settings.runner` | Stop sending. Put in `input_variables.settings.runner` instead |
| `start_at` (field 4) | Client sends | **No change.** Aggregator also copies into `settings.startAt` | Keep sending as top-level field |
| `expired_at` (field 5) | Client sends | **No change.** Aggregator also copies into `settings.expiredAt` | Keep sending as top-level field |
| `max_execution` (field 8) | Client sends | **No change.** Aggregator also copies into `settings.maxExecution` | Keep sending as top-level field |
| `trigger` (field 12) | Client sends | **No change** | No change |
| `nodes` (field 13) | Client sends | **No change** | No change |
| `edges` (field 14) | Client sends | **No change** | No change |
| `input_variables` (field 15) | Client sends (optional) | **MUST include `settings` key** with `name`, `runner`, `chain`, `chain_id` | Ensure `settings` is always populated |

### GetTask response — what changes

`GetTask` returns the full `Task` protobuf via `task.ToProtoBuf()`.

| GetTask response field | Current behavior | New behavior | SDK action |
|---|---|---|---|
| `id` (field 1) | Returned | **No change** | No change |
| `owner` (field 2) | Returned | **No change** | No change |
| `smart_wallet_address` (field 3) | Returned | **No change** (populated from `settings.runner` on create) | No change |
| `start_at` (field 4) | Returned | **No change** | No change |
| `expired_at` (field 5) | Returned | **No change** | No change |
| `name` (field 6) | Returned | **No change** (populated from `settings.name` on create) | No change |
| `completed_at` (field 7) | Returned | **No change** | No change |
| `max_execution` (field 8) | Returned | **No change** | No change |
| `execution_count` (field 9) | Returned | **No change** | No change |
| `last_ran_at` (field 10) | Returned | **No change** | No change |
| `status` (field 11) | Returned | **No change** | No change |
| `trigger` (field 12) | Returned | **No change** | No change |
| `nodes` (field 13) | Returned | **No change** | No change |
| `edges` (field 14) | Returned | **No change** | No change |
| `input_variables` (field 15) | Returned | **Now contains enriched `settings`** with server fields (id, owner, startAt, expiredAt, maxExecution) | SDK can read `settings` from `input_variables` if needed |

**Summary**: GetTask response is fully backward-compatible. The only observable change is that `input_variables.settings` now contains additional server-enriched fields.

### ListTasks response — what changes

`ListTasks` returns a filtered `Task` with fields selectively populated (`engine.go:2116-2139`).

| ListTasks response field | Current behavior | New behavior | SDK action |
|---|---|---|---|
| `id` (field 1) | Always included | **No change** | No change |
| `owner` (field 2) | Always included | **No change** | No change |
| `smart_wallet_address` (field 3) | Always included | **No change** | No change |
| `start_at` (field 4) | Always included | **No change** | No change |
| `expired_at` (field 5) | Always included | **No change** | No change |
| `name` (field 6) | Always included | **No change** | No change |
| `completed_at` (field 7) | Always included | **No change** | No change |
| `max_execution` (field 8) | Always included | **No change** | No change |
| `execution_count` (field 9) | Always included | **No change** | No change |
| `last_ran_at` (field 10) | Always included | **No change** | No change |
| `status` (field 11) | Always included | **No change** | No change |
| `trigger` (field 12) | Always included | **No change** | No change |
| `nodes` (field 13) | Only if `IncludeNodes` flag set | **No change** | No change |
| `edges` (field 14) | Only if `IncludeEdges` flag set | **No change** | No change |
| `input_variables` (field 15) | **Not included** in ListTasks | **No change** (still not included) | No change |

**Summary**: ListTasks response is fully backward-compatible. No changes.

### Simulation (RunNodeWithInputs / TriggerTask) — what changes

| Aspect | Current behavior | New behavior | SDK action |
|---|---|---|---|
| `input_variables.settings` | Client sends `name`, `runner`, `chain`, `chain_id` | **Same — no change** | No change (simulation already uses settings) |
| `workflowContext` | Not available in simulation | Replaced by `context` (also not available in simulation) | No change |

### JS variable access in workflow nodes — what changes

| Old variable | New variable | Migration |
|---|---|---|
| `{{workflowContext.name}}` | `{{settings.name}}` | User must update custom code nodes |
| `{{workflowContext.owner}}` | `{{settings.owner}}` | User must update custom code nodes |
| `{{workflowContext.runner}}` | `{{settings.runner}}` | User must update custom code nodes |
| `{{workflowContext.smartWalletAddress}}` | `{{settings.runner}}` | User must update custom code nodes |
| `{{workflowContext.eoaAddress}}` | `{{settings.owner}}` | User must update custom code nodes |
| `{{workflowContext.id}}` | `{{settings.id}}` | User must update custom code nodes |
| `{{workflowContext.startAt}}` | `{{settings.startAt}}` | User must update custom code nodes |
| `{{workflowContext.expiredAt}}` | `{{settings.expiredAt}}` | User must update custom code nodes |
| `{{workflowContext.maxExecution}}` | `{{settings.maxExecution}}` | User must update custom code nodes |
| `{{workflowContext.chain}}` | `{{settings.chain}}` | User must update custom code nodes |
| `{{workflowContext.status}}` | `{{context.status}}` | User must update custom code nodes |
| `{{workflowContext.executionCount}}` | `{{context.executionCount}}` | User must update custom code nodes |
| `{{workflowContext.lastRanAt}}` | `{{context.lastRanAt}}` | User must update custom code nodes |
| `{{workflowContext.completedAt}}` | `{{context.completedAt}}` | User must update custom code nodes |

## 4. Data Flow

```
Client (Next.js / SDK)
  └─ CreateTaskReq
       ├─ input_variables.settings.name        ← REQUIRED
       ├─ input_variables.settings.runner       ← REQUIRED
       ├─ input_variables.settings.chain        ← REQUIRED
       ├─ input_variables.settings.chain_id     ← REQUIRED
       ├─ input_variables.settings.*            ← optional client fields
       ├─ start_at, expired_at, max_execution   ← top-level scheduling fields
       ├─ trigger, nodes, edges                 ← workflow definition
       └─ name, smart_wallet_address            ← NO LONGER SENT

Aggregator (on CreateTask)
  ├─ Validates: settings.name, settings.runner MUST be present
  ├─ Enriches settings with server fields:
  │     settings.id           = generated task ID
  │     settings.owner        = authenticated user EOA
  │     settings.startAt      = CreateTaskReq.start_at
  │     settings.expiredAt    = CreateTaskReq.expired_at
  │     settings.maxExecution = CreateTaskReq.max_execution
  ├─ Copies to Task protobuf for storage/API backward compat:
  │     task.Name              = settings.name
  │     task.SmartWalletAddress = settings.runner
  │     task.Id, task.Owner, etc. = as before
  └─ Stores enriched input_variables (settings is now complete)

VM (at execution time)
  ├─ settings  ← loaded from task.input_variables (complete immutable snapshot)
  ├─ context   ← built from mutable Task fields at VM init:
  │     context.status         = task.Status
  │     context.executionCount = task.ExecutionCount
  │     context.lastRanAt      = task.LastRanAt
  │     context.completedAt    = task.CompletedAt
  └─ Go code reads vm.task.* for internal business logic
```

## 5. Implementation Phases

### Phase 1: Aggregator — require settings, enrich, and copy to Task fields

**File: `model/task.go` (`NewTaskFromProtobuf`)**

- Validate `inputVariables.settings` exists with `name` and `runner`
- Enrich settings with `id`, `owner`, `startAt`, `expiredAt`, `maxExecution`
- Write enriched settings back to `inputVariables`
- Copy `settings.name` → `task.Name`, `settings.runner` → `task.SmartWalletAddress`
- Ignore `body.Name` and `body.SmartWalletAddress`

### Phase 2: Aggregator — Go code reads vm.task directly

- Refactor summarizers, runners, executor to read `vm.task.*` instead of `workflowContext` or `settings` maps
- Remove `workflowContext` reads from all Go files
- `chain` / `chain_id` continue to be read from `settings` via `vm.vars["settings"]`
- Pass `executionIndex` through `vm.ExecutionIndex` field instead of stashing in workflowContext map

### Phase 3: Replace workflowContext with context

**File: `vm.go:393-408`**

```go
// BEFORE (13 fields, mostly duplicating settings):
workflowContext := map[string]interface{}{
    "id": task.Id, "name": task.Name, "owner": task.Owner, ...
}
v.AddVar("workflowContext", workflowContext)

// AFTER (4 fields, mutable Task-level runtime state only):
context := map[string]interface{}{
    "status":         getTaskStatusString(task.Status),
    "executionCount": task.ExecutionCount,
    "lastRanAt":      task.LastRanAt,
    "completedAt":    task.CompletedAt,
}
v.AddVar("context", context)
```

`settings` is already loaded from `task.InputVariables` at vm.go:413-418.

### Phase 4: Client and SDK updates

**SDK (ava-sdk-js):**
- `Workflow.toRequest()`: populate `inputVariables.settings` with `name` and `runner`
- Stop sending `body.Name` and `body.SmartWalletAddress` as top-level fields
- Tests: use `inputVariables.settings` as the primary way to set workflow metadata

**Next.js Studio:**
- WorkflowSettings node: already sends settings — no change needed
- Stop sending `body.Name` / `body.SmartWalletAddress` in create workflow calls
- Update `variableReferenceUtils.ts`: replace `workflowContext` with `context` and `settings`
- Update `ExecutionModal.tsx`: use `context` for runtime state

### No storage backfill

Old tasks without enriched `settings` will not be migrated. Users re-create their workflows on the client-side.

## 6. Summary

| Category | JS Variable | Contains | Mutable? |
|---|---|---|---|
| **Immutable definition** | `settings` | id, name, owner, runner, chain, chain_id, startAt, expiredAt, maxExecution, isSimulation, ... | No — frozen at creation |
| **Mutable runtime state** | `context` | status, executionCount, lastRanAt, completedAt | Yes — updated by aggregator each execution |
| **Per-execution** | `vm.ExecutionIndex` | Execution.index (0-based sequential counter) | Scoped to single execution, not Task-level |
| **Deprecated** | `workflowContext` | All 13 fields | Replaced by `settings` + `context` |
| **Storage/API** | Task protobuf | All fields (copied from settings at creation, context at runtime) | Backward-compatible — no protobuf schema change |
