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

## SDK/Client Migration

1. Remove any `PartialSuccess` handling or `isConditionalSkip` workarounds.
2. Treat `SUCCESS` as the only positive outcome. Branch skips no longer
   produce a warning status.
3. Treat `FAILED` as the single status for any node-level execution failure,
   regardless of whether some or all steps failed.
4. Treat `ERROR` as a system-level problem (not caused by the workflow
   configuration itself).

## Consequences

- Branch-skip workflows stop surfacing as warnings in the UI.
- The `steps` array is the source of truth for what executed and what
  was skipped — no status-level signal needed for coverage.
- Email summaries for branch-skip workflows now show a green success
  badge with a note like "3 nodes skipped by Branch condition" instead
  of a yellow warning badge.
