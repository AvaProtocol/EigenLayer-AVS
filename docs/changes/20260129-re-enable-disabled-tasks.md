# Re-enable Disabled Tasks - Implementation Plan

## Summary
- Goal: Allow users to re-enable a previously disabled task and have operators resume monitoring without requiring reconnection or restart.
- Current: We support `Enabled/Disabled` statuses. Disabling immediately notifies operators (`DisableTask`) to remove checks. Re-enabling currently relies on natural resync; operators don't get an immediate instruction.
- Target: When a task is re-enabled, operators should promptly resume monitoring the trigger(s) for that task in a push-driven manner.

## Scope
- Re-enable API: keep using `SetTaskEnabled(id, true)`
- Operator resume: push a message to operators to re-install checks immediately
- Idempotency and safety on repeated enable/disable toggles

## Design

### Protobuf
- Keep `aggregator.Node` `MessageOp` unchanged:
  - Reuse `MonitorTaskTrigger` to (re-)assign monitoring on enable
  - Continue using `DisableTask` for disabling
- Rationale: `MonitorTaskTrigger` already includes `TaskMetadata` (trigger config, start/expire, remain), sufficient for operators to (re)install checks. Avoids introducing a new `EnableTask` op that duplicates semantics.

### Aggregator (Engine)
- `Engine.SetTaskEnabledByUser(user, taskID, enabled)`
  - On `enabled == true` and state transition `Disabled -> Enabled`:
    - Persist new status (`Enabled`), update in-memory map.
    - Immediately push `MonitorTaskTrigger` to all online operators with a minimal `TaskMetadata` payload for the task:
      - `task_id`, `trigger`, `start_at`, `expired_at`, `remain` (derived)
    - Idempotency: If already `Enabled`, return 200 OK with "enabled", no push.
  - On `enabled == false`, behavior unchanged: persist, remove from in-memory active map, push `DisableTask` to operators.
- Batch pipeline:
  - Extend existing notification batching to include `MonitorTaskTrigger` items.
  - Ensure notification ordering per operator is preserved: disable followed later by enable must result in final `MonitorTaskTrigger` applied.

### Aggregator (RPC Server)
- No API surface change; continue exposing `SetTaskEnabled` (already available).
- Logging: add message for "enabled=true" path indicating operator notifications were queued.

### Operator
- `StreamMessages` / `processMessage` (already handles `MonitorTaskTrigger`):
  - Ensure idempotent `AddCheck` for block/cron/event triggers.
  - If a check exists for the `task_id`, refresh/replace it (update scheduling intervals or filter queries if changed).
  - Continue to remove checks on `DisableTask`.

### Storage
- No schema change. Status mapping stays:
  - `a` (Enabled), `i` (Disabled), `x` (Running), `c` (Completed), `f` (Failed)
- On enable:
  - Move record from `i` to `a` prefix; delete old entry; update user status index.

### Telemetry & Observability
- Add counters:
  - `task_enable_total`, `task_disable_total`
  - `operator_monitor_assign_total`, `operator_monitor_remove_total`
- Add structured logs on enable path with `task_id`, `trigger_type`, and operator address for each notification batch.

### Concurrency & Idempotency
- Terminal guards: enabling from `Completed/Failed` returns error (unchanged).
- Running guard: enabling a `Running` task is idempotent (return "enabled").
- Double-send protection: sending multiple `MonitorTaskTrigger` is safe; operator `AddCheck` must be idempotent.

## Backward Compatibility
- Operators already support `MonitorTaskTrigger`. No change needed to wire format.
- SDKs already call `SetTaskEnabled(id, true/false)`. Behavior is strictly improved.

## SDK Touchpoints
- JS SDK (ava-sdk-js)
  - Method `setWorkflowEnabled(id, enabled)` already exists.
  - Optionally add aliases: `enableWorkflow(id)` / `disableWorkflow(id)` as sugar (non-breaking).
  - Docs: update examples to show re-enable flow.

## Test Plan

### Unit (Aggregator)
- Engine:
  - Enable path sends `MonitorTaskTrigger` when transitioning `Disabled -> Enabled`.
  - Idempotent when `Enabled -> Enabled` (no push).
  - Disallowed from `Completed/Failed` (error, no push).
  - Disable path continues to push `DisableTask`.
- Notification batching:
  - Mixed batch with `DisableTask` then `MonitorTaskTrigger` for same task—final state yields reinstalled checks on operator.

### Unit (Operator)
- `processMessage` with `MonitorTaskTrigger` twice: second call updates/replaces prior check without duplication.
- `DisableTask` removes all checks for the task.

### Integration
- Create task (Enabled by default) → verify operator monitors.
- Disable via `SetTaskEnabled(false)` → verify operator removes checks.
- Enable via `SetTaskEnabled(true)` → verify `MonitorTaskTrigger` is received and checks reappear (without reconnection).
- Idempotency: enabling twice yields single set of checks.
- Persistence: after enable, restart operator, it should continue monitoring (existing behavior).

## Rollout
- Phase 1 (server-only):
  - Implement enable push via `MonitorTaskTrigger`, instrumentation, unit tests.
  - No changes required for operator protobuf—only logic idempotency if needed.
- Phase 2 (integration & docs):
  - Add end-to-end integration test.
  - Update user docs and examples to include re-enable scenario.
- Phase 3 (optional sugar):
  - Add SDK sugar methods: `enableWorkflow`, `disableWorkflow`.

## Risks & Mitigations
- Risk: Operator double-registration. Mitigation: idempotent `AddCheck` and replace semantics.
- Risk: Notification storms for enable toggles. Mitigation: debounced batches and notification coalescing per operator.
- Risk: Conflicts with Running state. Mitigation: guard as idempotent "enabled".

## Open Questions
- Do we need an explicit `EnableTask` op? Proposed: no; `MonitorTaskTrigger` is sufficient and already supported.
- Should operator reconcile on `MonitorTaskTrigger` against disabled status? Proposed: operators treat aggregator instruction as source of truth; aggregator ensures status consistency.

## Acceptance Criteria
- Enabling a disabled task immediately reinstates monitoring on all connected operators without reconnection.
- No duplicate checks on repeated enable commands.
- All new and existing unit/integration tests pass.


