## Active/Inactive Task Status Migration (Major Change)

### Summary
- Replace irreversible “Canceled” with reversible “Active/Inactive”.
- Deactivation turns a task off (not monitored/executed) but can be reactivated later.
- Aggregator is source of truth; Operators react to control/assignment messages.

### Goals
- Make workflows/tasks togglable (on/off) without losing definition/history.
- Keep operator monitoring consistent and immediate on deactivation; seamless on activation.
- Maintain clear invariants for terminal states (Completed/Failed).

### Scope (Breaking)
- Introduce `TaskStatus.Inactive`.
- Remove `CancelTask` RPC and `CancelTaskResp`.
- Replace `MessageOp_CancelTask` with `MessageOp_DeactivateTask`.
- Add an explicit API to toggle activity: `SetTaskActive(id, active)`.

---

### Protocol (protobuf) Changes
- Enum
```proto
enum TaskStatus {
  Active    = 0;
  Completed = 1;
  Failed    = 2;
  Running   = 4;
  Inactive  = 5;  // new, replaces “Canceled” semantics
}
```

- Service
```proto
message SetTaskActiveReq { string id = 1; bool active = 2; }
message SetTaskActiveResp {
  bool success = 1;
  string status = 2;           // "active" | "inactive" | "not_found" | "error"
  string message = 3;
  string id = 4;
  string previous_status = 5;
  int64 updated_at = 6;        // ms
}

service Aggregator {
  // ... existing RPCs ...
  rpc SetTaskActive(SetTaskActiveReq) returns (SetTaskActiveResp);
}
```

- Operator control messages
```proto
enum MessageOp {
  Unset              = 0;
  MonitorTaskTrigger = 1; // unchanged
  DeactivateTask     = 2; // replaces CancelTask
  DeleteTask         = 3; // unchanged
  CompletedTask      = 4; // unchanged
}
```

Note: `CancelTask` RPC/messages are removed in this major version. Clients must use `SetTaskActive`.

---

### Aggregator (Engine + RPC)
- Engine
  - Implement `SetTaskActiveByUser(user, taskID string, active bool)`:
    - Lookup task; return `not_found` if missing.
    - If `active == false` (deactivate):
      - Allowed only when current status is `Active`.
      - Change to `Inactive`, persist, move storage key to inactive bucket, remove from `n.tasks`.
      - Send `MessageOp_DeactivateTask` so Operators remove checks immediately.
    - If `active == true` (activate):
      - Allowed only when current status is `Inactive`.
      - Change to `Active`, persist, move storage key to active bucket, add back to `n.tasks`.
      - Do NOT send a direct control message; activation is handled by the existing stream via `MonitorTaskTrigger` (includes full TaskMetadata).
    - Disallow toggles from `Completed`/`Failed` (terminal).
    - Keep current policy: disallow deactivation if `Running`.
  - Remove `CancelTaskByUser` and any internal callers.
  - Reuse existing assignment/streaming loop: once a task is active and present in `n.tasks`, Operators receive `MonitorTaskTrigger` on next sync tick.

- RPC server
  - Add handlers:
    - `SetTaskActive(ctx, req) -> SetTaskActiveResp`
  - Remove the `CancelTask` handler entirely.

- Notifications & Stream Semantics
  - Deactivation: explicit push via `MessageOp_DeactivateTask` for immediate stop.
  - Activation: implicit via stream reassignment → `MonitorTaskTrigger` with TaskMetadata.
  - Optional: nudge reassignment immediately after activation for faster pickup (optimization).

---

### Operator Changes
- On `MessageOp_DeactivateTask`:
  - Resolve `task_id` (from TaskMetadata if present; else fallback to message id if valid).
  - `RemoveCheck(task_id)` for event/block/time triggers.
- Activation path unchanged:
  - Operators resume monitoring when `MonitorTaskTrigger` arrives via stream (includes complete TaskMetadata).

---

### Storage & Indexing
- Map `TaskStatus.Inactive` to a distinct storage prefix (e.g., `"i"`).
- Adjust scans and list endpoints to properly include/exclude inactive tasks where appropriate.
- Migration strategy:
  - If legacy `Canceled` data exists, one-time key move from `"l"` → `"i"` with status rewrite to `Inactive`.
  - Alternatively, treat legacy `Canceled` as `Inactive` in readers and rewrite on first update.

---

### SDK & API Clients
- Enums: add `Inactive`; remove references to `Canceled`.
- Methods:
  - `setTaskActive(id: string, active: boolean)`
- Update types for new `SetTaskActiveResp`.
- UI labels: show “Active” / “Inactive”; keep Completed/Failed; show Running as transient.

---

### Tests
- Unit tests:
  - Deactivate active → success; Operator receives `DeactivateTask`; checks removed.
  - Activate inactive → success; stream sends `MonitorTaskTrigger`; checks re-added.
  - Idempotency: setting active on an already active task returns success with status `"active"`; setting inactive on an already inactive task returns success with status `"inactive"`.
  - Disallowed toggles from Completed/Failed (and Running for deactivation).
- Integration tests:
  - End-to-end activation/deactivation across aggregator/operator stream.
  - Reassignment on activation within acceptable time bounds.

---

### Rollout Plan
1) Protobuf update and codegen (Go, TS): add `Inactive`, new `SetTaskActive` RPC, replace Cancel semantics.
2) Engine & RPC server: implement `SetTaskActiveByUser` and handlers; remove cancel codepaths.
3) Operator: handle `DeactivateTask`; verify activation via `MonitorTaskTrigger` streaming.
4) Storage: add `"i"` prefix; optional migration of legacy `"l"` → `"i"`.
5) SDK: update enums, methods, responses; release major version.
6) Tests/CI: update and add coverage.
7) Docs/Changelog: mark breaking changes, upgrade notes.

---

### Open Decisions
- Executing deactivation: keep current “cannot deactivate while executing” to avoid mid-run cancellation side-effects.
- Immediate activation broadcast: optional optimization; default to stream-driven activation to ensure full metadata delivery.

---

### Appendix: Example Status Transitions
- Active → Deactivate → Inactive (Operator receives DeactivateTask, stops monitoring)
- Inactive → Activate → Active (Operator receives MonitorTaskTrigger on next sync, resumes)
- Completed/Failed → (no toggles allowed)


