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

## How the Client Should Interpret Execution Status

### Current state (SDK still has `PartialSuccess`)

The SDK (`@avaprotocol/types`) still exposes four runtime statuses:
`Success`, `PartialSuccess`, `Failed`, `Pending`. The backend returns
`partialSuccess` for **both** conditional branch skips and actual step
failures, so the client must disambiguate.

**Decision tree for `PartialSuccess`:**

```
status === PartialSuccess
  └─ steps.every(s => s.success) ?
       ├─ YES → conditional skip (treat as success)
       └─ NO  → real step failure (treat as error/warning)
```

**Full client interpretation table (current SDK):**

| `status`         | `steps.every(s => s.success)` | Interpretation         | UI treatment                                 |
|------------------|-------------------------------|------------------------|----------------------------------------------|
| `Success`        | true                          | All nodes ran, all OK  | Green — "Simulation completed"               |
| `PartialSuccess` | true                          | Branch skipped nodes   | Green — "Simulation completed — Some steps skipped by condition" |
| `PartialSuccess` | false                         | Real step failure(s)   | Yellow — "Simulation completed with errors"  |
| `Failed`         | false                         | All-or-nothing failure | Red — "Simulation completed with errors"     |
| `Pending`        | n/a                           | Still running          | Neutral — loading state                      |

**Where this logic lives in Studio today:**

1. **Simulation status label** (`components/CanvasToolbarBottom/index.tsx`):
   ```ts
   const isSuccess = simulationResult?.status === ExecutionStatus.Success;
   const isFailed = simulationResult?.status === ExecutionStatus.Failed;
   const isConditionalSkip =
     simulationResult?.status === ExecutionStatus.PartialSuccess &&
     simulationResult?.steps?.every((step: StepProps) => step.success);
   const hasStepErrors = !isSuccess && !isConditionalSkip;
   ```
   - `isConditionalSkip` → green label, normal "Deploy" button
   - `hasStepErrors` → yellow/red label, "Deploy Anyway" button with caution tooltip

2. **Simulation gate for deploy** (`app/types/simulation.ts`):
   Only `ExecutionStatus.Success` passes the gate. `PartialSuccess` (even
   conditional skips) currently fails the gate — this needs updating after
   the SDK change so that the gate also passes for `Success` with skipped
   steps.

3. **Execution time display** (`CanvasToolbarBottom getExecutionTimeDisplay`):
   Maps status to text color: `Success` → green, `PartialSuccess` → yellow,
   `Failed` → red, `Pending` → yellow.

4. **Execution history chips** (`components/workflows/StatusChips.tsx`):
   - `Success` → "Completed" (green check)
   - `PartialSuccess` → "Partial Success" (yellow alert)
   - `Failed` → "Has Error" (red X)

5. **Execution modal header** (`components/workflows/ExecutionModal.tsx`):
   - `Success` → green "Success"
   - `PartialSuccess` → yellow "Partial Success"
   - `Failed` → red "Has Error"

6. **Manual run callback** (`components/workflows/WorkflowControlButton.tsx`):
   Treats both `Success` and `PartialSuccess` as a successful run for
   the toast notification. This is the only place that already collapses
   the two into a single positive path.

### Target state (after SDK upgrade)

Once the SDK ships the `SUCCESS`/`FAILED`/`ERROR` redesign:

| `status`  | Interpretation                          | UI treatment                                |
|-----------|-----------------------------------------|---------------------------------------------|
| `Success` | All executed steps passed (skips OK)    | Green — "Simulation completed"              |
| `Failed`  | At least one step failed                | Red — "Simulation completed with errors"    |
| `Error`   | System-level failure (VM crash, etc.)   | Red — "Simulation failed — system error"    |
| `Pending` | Still running                           | Neutral — loading state                     |

**Branch skip detection** moves from status-level to the `steps` array:
compare `steps.length` against the workflow's total node count. If
`steps.length < nodeCount` and `status === Success`, display an
informational note like "N steps skipped by condition".

### SDK/Client Migration Checklist

After upgrading `@avaprotocol/types`:

1. **Remove `isConditionalSkip` workaround** in `CanvasToolbarBottom` — `Success`
   already covers this case.
2. **Remove `PartialSuccess` branches** from `StatusChips.tsx`,
   `ExecutionModal.tsx`, `getExecutionTimeDisplay()`.
3. **Add `Error` handling** — new status for system-level failures.
   Display distinctly from `Failed` (e.g., "System error — contact support"
   vs "Execution failed — check step details").
4. **Update simulation gate** (`app/types/simulation.ts`) — `Success` is the
   only passing status (same as today, but no ambiguity).
5. **Update `WorkflowControlButton`** — remove `PartialSuccess` from the
   success path; add `Error` to the failure path.
6. **Update Storybook mocks** — replace `ExecutionStatus.PartialSuccess`
   references with `Failed` or `Success` as appropriate.

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
