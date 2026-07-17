# Per-Workflow State (`{{state.*}}`) + REST `options.auth` Providers

## Summary

Two generic, reusable gateway extensions (PR #662, merged to `staging` 2026-07-16). Design record:
`PLAN_WORKFLOW_STATE_GUARDIAN_MONITORING.md`. The first consumer is the studio "guardian" wallet-risk
monitor, but neither extension is guardian-specific.

1. **`{{state.*}}` — durable, cross-run, per-workflow state.** A `customCode` node can call
   `state.get/set/list` to persist arbitrary client-defined JSON that survives across scheduled runs,
   scoped to the workflow (`taskId`) — not the user. This is what lets a monitor alert on *new*
   findings only (diff current-vs-last-seen) without any studio callback in the hot path.
2. **REST `options.auth` providers.** A `restApi` node can set
   `config.options.auth = { provider: "goplus" }`; the gateway mints/attaches the provider credential
   server-side so the secret never lives in the (client-visible) workflow JSON. GoPlus is provider #1.

## Extension 1 — `{{state.*}}`

- **Storage**: new `wfstate:<taskId>:<stateKey>` BadgerDB namespace (`core/taskengine/schema.go`
  `WorkflowStateKey` / `WorkflowStatePrefix`). Scoped by `taskId` only, so a user's multiple monitoring
  workflows never collide and a workflow's state can be wiped exactly on deletion. Additive — no
  migration (`make storage-check` clean).
- **Binding**: `state.get/set/list` bound into the goja runtime in both `NewJSProcessor` and
  `NewJSProcessorWithIsolatedVars` (`core/taskengine/vm_runner_customcode.go` `installStateBinding`),
  over `vm.db`. Bound after the step vars so a node named `state` can't shadow it.
- **Non-persistent mode**: in simulation, single-node runs (no `taskId`), or when no DB is wired,
  reads/writes use a VM-scoped scratch map (`vm.stateScratch`, mutex-guarded, `core/taskengine/vm.go`)
  so `nodes:run` / `workflows:simulate` previews never mutate live state, yet read-after-write and
  cross-node coherence still hold within one run.
- **Deterministic `list`**: results are sorted in both the DB and scratch paths so simulation and real
  execution agree on ordering.
- **Lifecycle**: cascade-deleted on task teardown (`DeleteWorkflowByUser`, `core/taskengine/engine.go`);
  a `ListKeys` failure is logged rather than silently orphaning state.
- **Namespace convention**: `{{state.*}}` is the mutable, client-defined, read/write namespace parallel
  to `{{settings.*}}` (immutable config) and `{{context.*}}` (read-only runtime).

## Extension 2 — REST `options.auth` (GoPlus)

- **`restAuthProvider(node)`** reads `options.auth.provider` from the node's `options` bag (mirrors
  `shouldSummarize`). No proto/SDK type change — `options` is already an open `structpb.Value`.
- **`goplusAuthHeader()`** mints a GoPlus session token from `macros.secrets`
  (`goplus_app_key`/`goplus_app_secret`): `sign = sha1_hex(app_key + time + app_secret)` → POST
  `/api/v1/token`. Token is module-cached (RWMutex), **keyed by `app_key`** so a rotated key never
  reuses a stale token, refreshed ~60 s early. Uses an `http.Client` with a 15 s timeout, checks for a
  2xx status, and normalizes the token to a `Bearer ` prefix defensively.
- **Injection**: if the provider mints a token, it's set as the `Authorization` header in
  `processedHeaders` before the request; on `""` (keys unset or mint failed) no header is sent — GoPlus
  answers keyless (lower rate limits). A `goplusTokenProvider` seam makes the injection unit-testable.
- **Config**: `goplus_app_key`/`goplus_app_secret` added to `macros.secrets` in the example configs;
  real values via Railway env on the deployed `gateway-railway.yaml` (`${GOPLUS_APP_KEY}` /
  `${GOPLUS_APP_SECRET}`). `guardian_ruleset` (the tunable verdict knobs) is inlined as JSON (not a
  secret).

## Files

`core/taskengine/schema.go`, `vm.go`, `vm_runner_customcode.go`, `vm_runner_rest.go`, `engine.go`,
`vm_workflow_state_test.go`; `config/gateway.example.yaml`, `config/test.example.yaml`.

## Testing

- Go: state set/get/list roundtrip, cross-run persistence, simulation no-op guard; `restAuthProvider`
  parsing; Authorization-injection via `httptest` + the `goplusTokenProvider` seam.
- Live: verified against a local gateway with real GoPlus keys — the guardian scan mints an authed
  token (`attached GoPlus session token (authed)`) and the `state`-backed verdict runs; the ava-sdk-js
  guardian template test passes 14/14 with the verdict step asserting.

## Deferred / follow-ups

- `SetIfAbsent` storage primitive for **strict** exactly-once claim-once (mark-after-send is the v1
  default); `{{state.*}}` template auto-load for non-CustomCode nodes; GoPlus `code:4033` retry-keyless
  nuance; held-token (critical-token) scan (approvals-only for v1).
- Ops: republish `avs-dev:latest` so the SDK E2E guardian **live** test asserts against a gateway with
  these extensions.
- Studio (separate): the `enable_guardian_monitoring` advisor tool, web-sign enrollment, and syncing
  `guardian_ruleset` from `app/lib/goplus.ts`.
