# Execution Status Redesign: Replace PARTIAL_SUCCESS with SUCCESS/FAILED/ERROR

- **Date**: 2026-04-13
- **Status**: Implemented
- **Branch**: `fix/remove-dead-success-bool`

## Context

The `EXECUTION_STATUS_PARTIAL_SUCCESS` enum was used for two unrelated scenarios:

1. **Branch skips** — the workflow has conditional branches; some nodes were not
   executed because the branch condition routed elsewhere. Nothing failed.
2. **Step failures** — one or more nodes actually failed during execution
   (e.g., an ERC-20 transfer reverted).

Clients had to work around this by inspecting every step individually:

```ts
const isConditionalSkip =
  status === ExecutionStatus.PartialSuccess &&
  steps.every((step) => step.success);
```

This workaround should no longer be necessary.

## Decision

Three execution statuses, orthogonal to step count:

| Scenario                        | Status    | `steps.length` vs task node count | `execution.error`                  |
|---------------------------------|-----------|-----------------------------------|------------------------------------|
| All nodes ran, all succeeded    | `SUCCESS` | equal                             | empty                              |
| Branch skipped nodes, all OK    | `SUCCESS` | less than total                   | empty                              |
| Some nodes failed               | `FAILED`  | any                               | `"N of M steps failed: node1, …"`  |
| All nodes failed                | `FAILED`  | equal                             | `"N of N steps failed: node1, …"`  |
| No steps executed               | `FAILED`  | zero                              | `"no execution steps found"`       |
| System-level failure (VM crash) | `ERROR`   | zero (or partial if crash mid-run) | `"VM execution error: …"`          |

**How to determine what happened:**

- **`status`** answers: did the workflow succeed?
  - `SUCCESS` — yes, every executed step passed. Branch skips are normal.
  - `FAILED` — no, at least one step failed. Check `execution.error` and
    individual `step.success` / `step.error` for details.
  - `ERROR` — the system could not run the workflow at all (compilation
    failure, VM crash). This is not a user-fixable workflow issue.

- **`steps` array** answers: what ran and what was skipped?
  - Compare `steps.length` against the task's total node count to know
    how many nodes were skipped by branching.
  - Each step has `success`, `error`, and `name` for per-node detail.

- **`execution.error`** answers: what went wrong?
  - Empty string when `status` is `SUCCESS`.
  - Contains a summary like `"1 of 5 steps failed: loop1"` when `FAILED`.
  - Contains the system error message when `ERROR`.

## Proto Changes

```protobuf
enum ExecutionStatus {
  EXECUTION_STATUS_UNSPECIFIED = 0;
  EXECUTION_STATUS_PENDING     = 1;
  EXECUTION_STATUS_SUCCESS     = 2;
  EXECUTION_STATUS_FAILED      = 3;
  reserved 4;
  reserved "EXECUTION_STATUS_PARTIAL_SUCCESS";
  EXECUTION_STATUS_ERROR       = 5;
}
```

- Enum value `4` is reserved and will not be reused.
- New enum value `ERROR = 5` for system-level failures.

## How the Client Interprets Execution Status

### Final model (shipped in `@avaprotocol/sdk-js@2.17.0`)

The SDK exposes four runtime statuses, with `PartialSuccess` removed:

```ts
export enum ExecutionStatus {
  Unspecified = "unspecified",
  Pending = "pending",
  Success = "success",
  Failed = "failed",
  Error = "error",
}
```

**Client interpretation table:**

| `status`  | Interpretation                                 | `steps.length` vs node count  | UI treatment                                             |
|-----------|------------------------------------------------|-------------------------------|----------------------------------------------------------|
| `Success` | All executed steps passed                      | equal                         | Green — "Simulation completed — No funds moved"          |
| `Success` | All executed steps passed, branch skipped some | less than total               | Green — "Simulation completed — Some steps skipped by condition" |
| `Failed`  | At least one step failed                       | any                           | Yellow — "Simulation completed with errors — No funds moved" |
| `Error`   | System-level failure (VM crash, compilation)   | zero or partial               | Red — "Simulation failed — System error"                 |
| `Pending` | Still running                                  | n/a                           | Neutral — loading state                                  |

**Key rule:** `status` alone answers "did it succeed?". Branch skip
detection is derived from the `steps` array — compare `steps.length`
against the workflow's total executable node count (excluding
`workflowSettings`) and surface "N steps skipped by condition" as an
informational note only.

### Where this logic lives in Studio

1. **Simulation status label** (`components/CanvasToolbarBottom/index.tsx`):
   ```ts
   const status = simulationResult?.status;
   const isSuccess = status === ExecutionStatus.Success;
   const isSystemError = status === ExecutionStatus.Error;
   const hasStepErrors = !isSuccess;
   // Branch skip detection: Success with fewer executed steps than workflow nodes
   const executionNodeCount = nodes.filter((n) => n.type !== NodeType.workflowSettings).length;
   const executedStepCount = simulationResult?.steps?.length ?? 0;
   const hasSkippedSteps = isSuccess && executedStepCount < executionNodeCount;
   ```
   - `isSuccess + hasSkippedSteps` → green label "Some steps skipped by condition"
   - `isSuccess` → green label "No funds moved"
   - `isSystemError` → red label "System error"
   - `Failed` → yellow label "Simulation completed with errors"

2. **Execution time color** (`getExecutionTimeDisplay` in the same file):
   `Success` → green, `Failed`/`Error` → red, `Pending` → yellow.

3. **Execution history chips** (`components/workflows/StatusChips.tsx`):
   - `Success` → "Completed" (green check)
   - `Failed` → "Has Error" (red X)
   - `Error` → "System Error" (red triangle-alert)

4. **Execution modal header** (`components/workflows/ExecutionModal.tsx`):
   - `Success` → green "Success"
   - `Failed` → red "Has Error"
   - `Error` → red "System Error"

5. **Manual run callback** (`components/workflows/WorkflowControlButton.tsx`):
   Only `Success` triggers the success toast; `Failed` and `Error` both
   route to the error callback.

6. **Simulation gate for deploy** (`app/types/simulation.ts`):
   Only `ExecutionStatus.Success` passes the gate. Branch skips now pass
   because they return `Success` natively — no client workaround needed.

### Migration summary

The Studio migration from the `PartialSuccess` workaround to the final
SDK model removed:

- The `isConditionalSkip` heuristic (`status === PartialSuccess &&
  steps.every(s => s.success)`) — no longer needed because branch skips
  return `Success` directly from the backend.
- The `PartialSuccess` case branches in `StatusChips.tsx`,
  `ExecutionModal.tsx`, `CanvasToolbarBottom getExecutionTimeDisplay()`,
  and `WorkflowControlButton.tsx`.

And added:

- A new `Error` status branch in all four consumers, distinct from
  `Failed` — `Failed` means the workflow ran but a step reverted;
  `Error` means the VM could not run the workflow at all.
- Branch-skip detection via `steps.length < executionNodeCount` in the
  simulation status label, surfacing the skip count as a green
  informational message rather than a warning.

## SDK Changes (ava-sdk-js)

**PR**: https://github.com/AvaProtocol/ava-sdk-js/pull/212

### TypeScript enum update

```typescript
// packages/types/src/enums.ts
export enum ExecutionStatus {
  Unspecified = "unspecified",
  Pending = "pending",
  Success = "success",
  Failed = "failed",
  Error = "error",       // NEW — replaces PartialSuccess
}
```

### Backward compatibility

The SDK conversion functions handle legacy proto value `4` (retired
`PARTIAL_SUCCESS`) by mapping it to `ExecutionStatus.Failed`:

```typescript
case ProtobufExecutionStatus.EXECUTION_STATUS_FAILED:
case 4 as ProtobufExecutionStatus: // legacy PARTIAL_SUCCESS
  return ExecutionStatus.Failed;
```

This ensures stored executions written before the migration are
displayed correctly without requiring a data migration.

### Test updates (18 files)

- All `ExecutionStatus.PartialSuccess` assertions replaced with
  `ExecutionStatus.Failed` (step failures) or `ExecutionStatus.Success`
  (branch skips)
- `partialSuccess.test.ts` rewritten — test names and expectations
  aligned with the new three-value model

## Consequences

- Branch-skip workflows stop surfacing as warnings in the UI.
- The `steps` array is the source of truth for what executed and what
  was skipped — no status-level signal needed for coverage.
- Email summaries for branch-skip workflows now show a green success
  badge with a note like "3 nodes skipped by Branch condition" instead
  of a yellow warning badge.
- Legacy stored executions with proto value `4` are transparently
  mapped to `Failed` by the SDK — no data migration required.
