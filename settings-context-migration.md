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
| `settings.chain` | Client (recommended) | Chain name (e.g. "sepolia") — not yet enforced by server |
| `settings.chain_id` | Client (recommended) | Chain ID (e.g. 11155111) — not yet enforced by server |
| `settings.isSimulation` | Client (optional) | Simulation flag |
| `settings.tokens` | Client (recommended) | Array of token contract addresses used by this workflow (see §7) |
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
| `input_variables` (field 15) | Client sends (optional) | **MUST include `settings` key** with `name`, `runner` (required); `chain`, `chain_id` (recommended) | Ensure `settings` is always populated |

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

### Simulation (SimulateTask / simulateWorkflow) — what changes

| Aspect | Current behavior | New behavior | SDK action |
|---|---|---|---|
| `input_variables.settings` | Optional — aggregator fell back to `taskName := "Workflow"` if missing | **REQUIRED — same as CreateTask.** Aggregator validates `settings.name` and `settings.runner` via shared `ValidateInputVariablesSettings()`. Rejects with `InvalidArgument` if missing. | SDK must always pass `inputVariables.settings` with `name` and `runner` |
| `input_variables.settings.name` | Ignored (hardcoded to `"Workflow"`) | **REQUIRED.** Used as `task.Name` on the simulation task object | Pass workflow name in `settings.name` |
| `input_variables.settings.runner` | Ignored (not set on simulation task) | **REQUIRED.** Used as `task.SmartWalletAddress` on the simulation task object | Pass smart wallet address in `settings.runner` |
| Validation | No validation — any or no inputVariables accepted | **Dual-layer validation**: RPC layer (`rpc_server.go`) validates protobuf, engine layer (`engine.go`) validates Go native types | No SDK change needed (server rejects bad requests) |
| `workflowContext` | Not available in simulation | Replaced by `context` (also not available in simulation) | No change |

### RunNodeWithInputs — what changes

| Aspect | Current behavior | New behavior | SDK action |
|---|---|---|---|
| `input_variables.settings` | Client sends `runner`, `chain`, `chain_id` | **Same — no change** | No change (already uses settings for runner context) |

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
       ├─ input_variables.settings.chain        ← recommended (not yet enforced)
       ├─ input_variables.settings.chain_id     ← recommended (not yet enforced)
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

### Phase 1: Aggregator — require settings, enrich, and copy to Task fields ✅

**File: `model/task.go`**

- `ValidateInputVariablesSettings()` — shared validation for both CreateTask and SimulateTask: requires `settings.name` (non-empty) and `settings.runner` (valid hex address)
- `NewTaskFromProtobuf()` — validates, enriches settings with `id`, `owner`, `startAt`, `expiredAt`, `maxExecution`, copies to Task fields
- `enrichSettings()` — writes server fields back into `inputVariables.settings`
- `body.Name` and `body.SmartWalletAddress` are **ignored** — all reads come from `settings`

**File: `aggregator/rpc_server.go` (`SimulateTask`)**

- Calls `model.ValidateInputVariablesSettings(req.InputVariables)` before processing — same validation as CreateTask
- Rejects with `InvalidArgument` if `settings.name` or `settings.runner` is missing

**File: `core/taskengine/engine.go` (`SimulateTask`)**

- Engine-level validation of `inputVariables["settings"]` with `name` and `runner` (for Go unit tests that call engine directly)
- Sets `task.Name` and `task.SmartWalletAddress` from settings on the simulation task object
- No more `"Workflow"` fallback name

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

### Phase 4: Client and SDK updates ✅

**SDK (ava-sdk-js):**
- `Workflow.toRequest()`: passes `inputVariables` transparently (callers provide `settings` with `name` and `runner`)
- `toRequest()` no longer calls `setName()` or `setSmartWalletAddress()` on CreateTaskReq
- `simulateWorkflow()`: all calls now pass `inputVariables.settings` with `name` and `runner`
- Test helpers: `getSettings(runner, name)` returns `{ name, runner, chain_id, chain }`; `getInputVariables(name, runner)` wraps in `{ settings: ... }`
- All tests updated to use `inputVariables.settings` as the primary way to set workflow metadata

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

## 7. `settings.tokens` — Token Metadata for Formatting

### Purpose

When a workflow interacts with ERC20 tokens, the aggregator needs to know each token's `symbol` and `decimals` so that context-memory can format raw on-chain values into human-readable amounts (e.g., `1500000000` → `1,500 USDC`).

The client declares the token contract addresses it uses via `settings.tokens`. The aggregator reads this list, calls `GetTokenMetadata()` for each address, and includes the results in the `tokenMetadata` field of the context-memory summarize request.

### Field Definition

| Field | Type | Required | Description |
|---|---|---|---|
| `settings.tokens` | `string[]` | Recommended | Array of ERC20 token contract addresses used by this workflow |

### When to Include

Include `settings.tokens` whenever your workflow interacts with ERC20 tokens:
- Event triggers watching Transfer/Approval events on token contracts
- ContractWrite nodes calling `approve`, `transfer`, etc.
- ContractRead nodes reading `balanceOf`, `allowance`, etc.
- Uniswap swap workflows (input/output tokens)

Without `settings.tokens`, context-memory falls back to default 18 decimals, which produces incorrect formatting for tokens like USDC (6 decimals) or WBTC (8 decimals).

### Examples

**Event trigger watching USDC transfers:**

```json
{
  "inputVariables": {
    "settings": {
      "name": "USDC Transfer Alert",
      "runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
      "chain": "sepolia",
      "chain_id": 11155111,
      "tokens": [
        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
      ]
    }
  }
}
```

**Uniswap swap with two tokens:**

```json
{
  "inputVariables": {
    "settings": {
      "name": "Stoploss USDC->WETH",
      "runner": "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
      "chain": "ethereum",
      "chain_id": 1,
      "tokens": [
        "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
        "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"
      ]
    }
  }
}
```

**Multiple tokens in a DeFi workflow (AAVE health factor alert):**

```json
{
  "inputVariables": {
    "settings": {
      "name": "AAVE Health Factor Alert",
      "runner": "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
      "chain": "ethereum",
      "chain_id": 1,
      "tokens": [
        "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
        "0xdAC17F958D2ee523a2206206994597C13D831ec7",
        "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599"
      ]
    }
  }
}
```

### Aggregator Behavior

In `buildRequest()` (`summarizer_context_memory.go`), the aggregator:

1. Reads `settings.tokens` from `vm.vars["settings"]`
2. For each address, calls `TokenEnrichmentService.GetTokenMetadata(addr)`
3. Adds results to the request-level `tokenMetadata` map (keyed by lowercase address)
4. Skips addresses already resolved from per-step inference (deduplication)
5. Silently skips if `TokenEnrichmentService` is unavailable (no error)

This supplements — not replaces — the existing per-step token metadata inference (from CONTRACT_WRITE configs and `uniswapv3_pool.tokens`). `settings.tokens` is the preferred explicit mechanism; per-step inference remains as a fallback for older workflows.
