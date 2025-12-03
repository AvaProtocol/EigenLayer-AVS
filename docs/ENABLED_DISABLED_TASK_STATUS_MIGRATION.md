## Enabled/Disabled Task Status Migration (Major Change)

### Summary
- Replace irreversible “Canceled” and previous “Active/Inactive” wording with “Enabled/Disabled” lifecycle.
- Disabling turns a task off (not monitored/executed) but can later be enabled again.
- Aggregator is source of truth; Operators react to control/assignment messages.

### Goals
- Make workflows/tasks togglable (on/off) without losing definition/history.
- Keep operator monitoring consistent and immediate on disable; seamless on enable.
- Maintain clear invariants for terminal states (Completed/Failed).

### Scope (Breaking)
- Introduce `TaskStatus.Enabled` and `TaskStatus.Disabled` (replaces Active/Inactive naming).
- Remove `CancelTask` RPC and `CancelTaskResp`.
- Replace `MessageOp_CancelTask`/`MessageOp_DeactivateTask` with `MessageOp_DisableTask`.
- Add an explicit API to toggle enabled state: `SetTaskEnabled(id, enabled)`.

---

### Protocol (protobuf) Changes
- Enum
```proto
enum TaskStatus {
  Enabled   = 0;
  Completed = 1;
  Failed    = 2;
  Running   = 4;
  Disabled  = 5;  // replaces “Inactive/Canceled” semantics
}
```

- Service
```proto
message SetTaskEnabledReq { string id = 1; bool enabled = 2; }
message SetTaskEnabledResp {
  bool success = 1;
  string status = 2;           // "enabled" | "disabled" | "not_found" | "error"
  string message = 3;
  string id = 4;
  string previous_status = 5;
  int64 updated_at = 6;        // ms
}

service Aggregator {
  // ... existing RPCs ...
  rpc SetTaskEnabled(SetTaskEnabledReq) returns (SetTaskEnabledResp);
}
```

- Operator control messages
```proto
enum MessageOp {
  Unset              = 0;
  MonitorTaskTrigger = 1; // unchanged
  DisableTask        = 2; // replaces CancelTask/DeactivateTask
  DeleteTask         = 3; // unchanged
  CompletedTask      = 4; // unchanged
}
```

Note: `CancelTask` RPC/messages are removed in this major version. Clients must use `SetTaskEnabled`.

---

### Aggregator (Engine + RPC)
- Engine
  - Implement `SetTaskEnabledByUser(user, taskID string, enabled bool)`:
    - Lookup task; return `not_found` if missing.
    - If `enabled == false` (disable):
      - Allowed when current status is `Enabled` (not terminal, not running).
      - Change to `Disabled`, persist, move storage key to disabled bucket, remove from `n.tasks`.
      - Send `MessageOp_DisableTask` so Operators remove checks immediately.
    - If `enabled == true` (enable):
      - Allowed when current status is `Disabled` (not terminal).
      - Change to `Enabled`, persist, move storage key to enabled bucket, add back to `n.tasks`.
      - Do NOT send a direct control message; enabling is handled by the existing stream via `MonitorTaskTrigger` (includes full TaskMetadata) on the next assignment cycle.
    - Disallow toggles from `Completed`/`Failed` (terminal).
    - Keep current policy: disallow disabling if `Running` (guard).
  - Remove `CancelTaskByUser` and any internal callers.
  - Reuse existing assignment/streaming loop: once a task is enabled and present in `n.tasks`, Operators receive `MonitorTaskTrigger` on the next sync tick.

- RPC server
  - Handlers:
    - `SetTaskEnabled(ctx, req) -> SetTaskEnabledResp`
  - Remove any legacy `CancelTask` handler entirely.

- Notifications & Stream Semantics
  - Disable: explicit push via `MessageOp_DisableTask` for immediate stop.
  - Enable: implicit via stream reassignment → `MonitorTaskTrigger` with TaskMetadata.
  - Optional: nudge reassignment after enable for faster pickup (optimization).

---

### Operator Changes
- On `MessageOp_DisableTask`:
  - Resolve `task_id` (from TaskMetadata if present; else fallback to message id if valid).
  - `RemoveCheck(task_id)` for event/block/time triggers.
- Enable path unchanged:
  - Operators resume monitoring when `MonitorTaskTrigger` arrives via stream (includes complete TaskMetadata).

---

### Storage & Indexing
- Map `TaskStatus.Disabled` to a distinct storage prefix (e.g., `"i"`).
- Map `TaskStatus.Enabled` to `"a"`.
- Adjust scans and list endpoints to properly include/exclude disabled tasks where appropriate.
- Migration strategy:
  - If legacy `Canceled`/`Inactive` data exists, one-time key move to `"i"` with status rewrite to `Disabled`.
  - Alternatively, treat legacy values as `Disabled` in readers and rewrite on first update.

---

### SDK & API Clients
- Enums: use `Enabled`/`Disabled`; remove references to `Active`/`Inactive` and `Canceled`.
- Methods:
  - `setTaskEnabled(id: string, enabled: boolean)`
- Update types for new `SetTaskEnabledResp`.
- UI labels: show “Enabled” / “Disabled”; keep Completed/Failed; show Running as transient.

---

### Tests
- Unit tests:
  - Disable enabled → success; Operator receives `DisableTask`; checks removed.
  - Enable disabled → success; stream sends `MonitorTaskTrigger`; checks re-added.
  - Idempotency: setting enabled on an already enabled task returns success with status `"enabled"`; setting disabled on an already disabled task returns success with status `"disabled"`.
  - Disallowed toggles from Completed/Failed (and disabling when Running).
- Integration tests:
  - End-to-end enable/disable across aggregator/operator stream.
  - Reassignment on enable within acceptable time bounds.

---

### Rollout Plan
1) Protobuf update and codegen (Go, TS): add `Enabled/Disabled`, new `SetTaskEnabled` RPC, replace Cancel/Deactivate semantics.
2) Engine & RPC server: implement `SetTaskEnabledByUser` and handlers; remove cancel codepaths.
3) Operator: handle `DisableTask`; verify enabling via `MonitorTaskTrigger` streaming.
4) Storage: add `"i"` prefix for disabled; optional migration of legacy keys.
5) SDK: update enums, methods, responses; release major version.
6) Tests/CI: update and add coverage.
7) Docs/Changelog: mark breaking changes, upgrade notes.

---

### Open Decisions
- Executing disable: keep “cannot disable while running” to avoid mid-run cancellation side-effects.
- Immediate enable broadcast: optional optimization; default to stream-driven enable to ensure full metadata delivery.

---

### Appendix: Example Status Transitions
- Enabled → Disable → Disabled (Operator receives DisableTask, stops monitoring)
- Disabled → Enable → Enabled (Operator receives MonitorTaskTrigger on next sync, resumes)
- Completed/Failed → (no toggles allowed)


