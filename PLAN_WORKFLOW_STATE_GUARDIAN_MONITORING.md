# PLAN — Composable Workflow State + REST Auth Providers (guardian as a pure composition)

> **Audience.** EigenLayer-AVS gateway/backend. Gateway-side response to the studio hand-off
> `PLAN_GUARDIAN_MONITORING_ARCHITECTURE.md` (in the `studio` repo).
>
> **Design decision (revised).** **No bespoke `guardian-scan` node.** Instead, add **two small,
> generic, reusable extensions** to existing primitives, and let the guardian be an ordinary workflow
> composed from them:
> 1. **REST node server-side auth providers** — the RestAPI node can mint/attach a provider's auth
>    (GoPlus signed session token first) from gateway-held secrets, so a workflow calls GoPlus with a
>    plain REST node and the secret never enters the (client-visible) workflow JSON.
> 2. **Client-defined per-workflow state** — a generic, cross-run key/value store scoped to the
>    workflow (`taskId`), exposed to CustomCode as a `state.*` binding so the **client defines the JSON
>    structure** and the diff/verdict logic. Useful far beyond the guardian.
>
> **Framing (unchanged).** State is scoped to the **workflow (`taskId`)**, never to a user. No "one
> workflow per user" rule — a user can create workflow A to watch one EOA today and workflow B to
> watch two other EOAs later; two tasks, two isolated state namespaces. **The client owns composing
> each workflow correctly.**

---

## 0. Why this shape (composition, not a monolith)

A bespoke node buries the guardian's logic in Go and helps nothing else. The two extensions below are
each independently useful (any workflow can call a signed-auth API; any workflow can be stateful), and
together they let the guardian be a **normal node graph** whose verdict/diff logic is **the studio JS
ported almost verbatim** (`app/lib/goplus.ts`, `app/lib/guardian/rescan.ts`). Everything else the
guardian needs already exists: `CronTrigger`, `BranchNode`, `RestAPINode`, `CustomCodeNode`, durable
`history:` logs, and `{{apContext.configVars.*}}` injection.

---

## 1. Extension 1 — REST node server-side auth providers (GoPlus first)

Let the RestAPI node attach a provider-minted credential at execution time, resolved entirely
server-side. **Feasibility: EASY** (every primitive has a working precedent in the same file).

### 1.1 How the client asks for it

Reuse the already-generic `options` bag (`RestAPINode.Config.Options`, a `google.protobuf.Value`;
`options.summarize` is read today at `vm_runner_rest.go:832`). No proto change:

```jsonc
// A GoPlus call in a plain REST node — no secret in the JSON:
{ "type": "restApi",
  "config": {
    "method": "GET",
    "url": "https://api.gopluslabs.io/api/v1/token_security/1?contract_addresses=0x…",
    "options": { "auth": { "provider": "goplus" } }
  } }
```

Optionally also **URL auto-detection** (`api.gopluslabs.io` → goplus), mirroring the existing
`detectNotificationProvider` (`vm_runner_rest.go:848`). Explicit `options.auth` is preferred (clear intent).

### 1.2 What the gateway does (server-side, secret never leaves)

Precedents this reuses directly:
- **Read a gateway secret in the runner, not from client JSON:** `vm_runner_balance.go:272` reads
  `macroSecrets["moralis_api_key"]`; accessor `GetMacroSecret(...)` at `engine.go:90`. GoPlus keys go
  in `macros.secrets` beside Moralis (config-only; §4).
- **Runner makes its own outbound call with a gateway credential:** the `/api/notify` summarizer
  (`vm_runner_rest.go:649-682`, its own `http.Client`).
- **Inject the header pre-flight:** `processedHeaders` is built at `vm_runner_rest.go:554-572` and
  consumed by `ExecuteRequest` at `:732` — insert one block in between.

Flow for `provider:"goplus"`:
```
token := goplusToken()                       // module-cached; mint if missing/expiring
processedHeaders["Authorization"] = token    // GoPlus token already includes "Bearer "
```
`goplusToken()` (net-new, ~40 lines): `sign = sha1_hex(app_key + time + app_secret)` →
`POST /api/v1/token` → cache `{value, expiresAt}` behind an `sync.RWMutex`, refresh ~60s early. On
GoPlus app-level `code:4033`, drop the header and retry keyless (studio §5.1). New import `crypto/sha1`.

### 1.3 Generic framing

Model it as a tiny **auth-provider registry** (`provider → mintFn`) so future signed-token APIs plug in
without touching the runner core. GoPlus is provider #1. **Change surface: `vm_runner_rest.go` only**
(a `restAuthProvider(node)` reader like `shouldSummarize`, a `mintGoPlusToken()` + cache, one injection
block). No proto, no SDK type change.

---

## 2. Extension 2 — client-defined per-workflow state (`{{state.*}}`)

A generic cross-run key/value store scoped to `taskId`, with the **client defining the JSON** via a
CustomCode binding. **Feasibility: EASY→MODERATE.**

### 2.0 Namespace — `state`, parallel to `settings`

The client already configures workflows with `{{settings.*}}` (immutable config: `runner`,
`chain_id`, `address_list`, …) and reads platform runtime via `{{context.*}}` (read-only). Neither is
mutable + persistent + client-defined. Add one namespace — **`state`** — for exactly that:

| Namespace | Role | Mutability | Set by |
|---|---|---|---|
| `{{apContext.configVars.*}}` | platform secrets | immutable, read-only | gateway config |
| `{{settings.*}}` | workflow config | immutable, read-only | client at create |
| `{{context.*}}` | runtime metadata | read-only | gateway |
| **`{{state.*}}`** | **workflow memory** | **read + write, persists across runs** | **client (CustomCode)** |

Rule of thumb: **`settings` in, `state` in/out, `context` read-only.** `state` is chosen (over
`store`/`storage`/`memory`) because it's the same word as the CustomCode binding and consistent with
the existing single-word lowercase namespaces. Verified: `state` is unused as a var namespace today.
**Register `state` in `reservedSystemVarNames` (`durable.go:204`)** alongside `settings`/`context` so
it's re-injected fresh each run (never snapshotted as a node output) and can't be clobbered by a node
named `state`. Client `state.<key>` maps to storage `wfstate:<taskId>:<key>`.

Sub-key conventions (mirror `settings` snake_case): scalars/objects as top-level keys
(`{{state.last_processed_block}}`, `{{state.cursor}}`, template-readable anywhere); iterated
collections as prefixed keys via the JS binding (`state.set("seen:"+id, …)` / `state.list("seen:")`).

### 2.1 Storage layer (`storage/db.go` + `core/taskengine/schema.go`)

```
wfstate:<taskId>:<stateKey>   → client-defined JSON blob
```
- `taskId` (ULID) is the whole scope — **no owner/wallet in the key**; two workflows of one user get
  isolated namespaces. `stateKey` is opaque/client-chosen.
- Fresh namespace (`wfstate:` doesn't collide — verified vs `core/taskengine/doc.go`). Key builders in
  `schema.go` (`WorkflowStateKey`, `WorkflowStatePrefix`).
- `Storage` already has `Set`, `GetKey`, `Delete`, `GetByPrefix`, `ListKeys`, `BatchWrite`
  (`storage/db.go:23`). Add two small primitives:
  - `SetIfAbsent(key, value) (claimed bool, err error)` — atomic exactly-once (reuse the
    `IncCounter` serializable `db.Update(txn)` shape at `storage/db.go:~486`). Optional; only needed
    for strict claim-once under concurrent runs (§3.3).
  - `DeleteByPrefix(prefix) (int, error)` — the workflow-teardown cascade.

### 2.2 The `state.*` CustomCode binding (client defines the structure)

goja binds Go closures as JS methods today (`console.log` at `utils.go:116`); runners hold `r.vm.db`
(`WithDb`, `vm.go:332`) and the task id (`r.vm.GetTaskId()`, `vm.go:2469`). So in `NewJSProcessor` /
`NewJSProcessorWithIsolatedVars` (`vm_runner_customcode.go:29, 80`) bind:

```js
state.get(key)               // → stored JSON (or undefined)
state.set(key, value)        // persist arbitrary JSON under wfstate:<taskId>:key
state.list(prefix?)          // → [key, …]  for diffing
state.setIfAbsent(key, val)  // → bool      (optional, strict claim-once)
```
The closures capture `r.vm.db` + `taskId`. The **client writes arbitrary JS over arbitrary JSON** —
this is the "broader use case" surface (rate-limiters, "last processed block", DCA "already-bought",
dedup, and the guardian's flag diff).

Two caveats to honor:
- **Simulation must not mutate real state.** Gate writes on `vm.IsSimulation` (`vm.go:247`) —
  `nodes:run` / `workflows:simulate` dry-runs get a scratch/no-op `state` so previews never persist.
- **Atomicity.** A direct `state.set` hits the db immediately (not atomic with the `history:` write at
  `executor.go:784`). For the guardian that's fine (mark-after-send, §3.3). If atomic-with-execution
  is wanted later, buffer writes and flush them into the executor's `updates` batch (`executor.go:754-787`),
  exactly as durable checkpoints do (`durable.go:217/295`).

### 2.3 Optional read-side sugar

One line in `NewVMWithData…` (`vm.go:424`, beside the apContext injection at `:457`):
`v.AddVar("state", loadWorkflowState(db, taskId))` — makes `{{state.*}}` template-readable in **every**
node (REST url/body, Branch expressions), not just CustomCode. Cheap, high-leverage; can follow v1.

### 2.4 Lifecycle

Cascade-delete `wfstate:<taskId>:*` in the task-teardown path (engine `DeleteWorkflow`). Re-deploy =
new `taskId` = fresh state (first run re-alerts once — benign for the guardian; mitigate by keeping
`taskId` stable across re-enroll, or seed state at migration).

---

## 3. The guardian as a pure composition (no bespoke node)

```
[CronTrigger "0 * * * *"]
  → [restApi  Moralis: approvals + balances]                         (macros: moralis_api_key)
  → [restApi  GoPlus: token_security / token_approval_security]      (options.auth: goplus  ← Ext 1)
  → [customCode  verdict + diff]                                     (state.* ← Ext 2)
  → [branch  newFindings.length > 0 ?]
      if → [restApi  notify → /api/notify | api.telegram.org/…]      ({{apContext.configVars.ap_notify_bot_token}})
  → [customCode  mark-notified on send success]                     (state.set ← Ext 2)
```

### 3.1 The verdict+diff CustomCode (ports studio JS ~verbatim)

Reads the upstream REST outputs from `vm.vars`, applies the studio verdict rules (§6 of the studio
plan — `trust_list` override, critical/warning flag sets, tax thresholds; approvals `flagged` needs a
strong signal and a non-trust-listed spender, weak signals never flag alone), then diffs against state:

```js
const seen = new Set(state.list("seen:"));                 // flags already recorded
const current = computeFindings(moralis.data, goplus.data);// studio verdict logic, verbatim
const notified = new Set(state.list("ntfy:"));
const newFindings = current.filter(f => !notified.has("ntfy:" + f.flagKey));
for (const f of current) state.set("seen:" + f.flagKey, f.detail);   // record seen (never delete)
return { newFindings };                                     // → branch + notify
```
`flagKey` = `approval:{token}:{spender}` or `token:{tokenAddress}` (lowercased). Degraded scan (a REST
node errored) → `computeFindings` yields nothing for it and we **never delete** `seen:` rows (absence ≠
cleared).

### 3.2 Notify

Branch gates on `newFindings.length > 0`; the notify REST node sends the studio-formatted plaintext DM
(read-only, counterfactual voice — studio §7.1).

### 3.3 Claim-once (exactly-once alerting)

Mark **after** a successful send: the trailing CustomCode reads the notify node's response from
`vm.vars` and, only if it succeeded, `state.set("ntfy:"+flagKey, now)` for each finding just sent. On
send failure nothing is marked → next run retries (§3.1 diffs against `ntfy:`). This is
practically-exactly-once for a sequential per-workflow cron and self-healing on transient failures.
For strict exactly-once under overlapping runs, pre-claim with `state.setIfAbsent("ntfy:"+flagKey, now)`
before sending and release (`state.set(..., null)` / delete) on failure.

---

## 4. The user scenario, modeled

> Workflow A watches EOA1; two weeks later Workflow B watches EOA2 + EOA3.

```
Workflow A (taskId 01A…)                 Workflow B (taskId 01B…)
  wfstate:01A…:seen:approval:0x…:0x…       wfstate:01B…:seen:token:0x…            (EOA2)
  wfstate:01A…:ntfy:approval:0x…:0x…       wfstate:01B…:seen:approval:0x…:0x…     (EOA3)
```
Two tasks, two `wfstate:<taskId>:*` namespaces, no shared user record. B watching two EOAs just uses
`stateKey`s that encode the wallet (`<chainId>:<wallet>:<flagKey>`). Delete A → `wfstate:01A…:*` wiped,
B untouched.

---

## 5. Worked timeline (one workflow, one wallet)

| Run | REST scan finds | state before | CustomCode + notify | state after |
|---|---|---|---|---|
| t0 | approval **A** | ∅ | A not in `ntfy:` → newFindings=[A] → send ✅ → mark `ntfy:A` | seen:{A}, ntfy:{A} |
| t1 | A again | {A} | A in `ntfy:` → newFindings=[] → no send | unchanged |
| t2 | A + critical **B** | {A} | B new → send ✅ → mark `ntfy:B` | seen:{A,B}, ntfy:{A,B} |
| t3 | *(Moralis errors)* | {A,B} | degraded → no findings, delete nothing | unchanged |
| t4 | *(A revoked, clean)* | {A,B} | alert on **new** only; no delete/alert on removal | unchanged |

Send-failure at t0: notify REST node fails → trailing CustomCode does **not** mark `ntfy:A` → next run
re-sends. Exactly-once preserved without an explicit release.

---

## 6. Answers to studio §8 open questions

1. **Per-workflow persistent state (deciding capability):** added as generic `wfstate` + a `state.*`
   CustomCode binding (§2). Client-defined JSON; reusable beyond the guardian.
2. **No-fund deploy auth:** a read-only, notify-only scheduled workflow deploys with **no per-run
   signature and no fund-authority check** (a task with no runner wallet skips the ownership gate,
   `engine.go` `CreateWorkflow`). **But not signature-less:** `CreateWorkflow` needs a Bearer session
   JWT, minted only after an EIP-191 wallet signature (per-48h, not per-run). `X-Partner-Assertion` is
   **forbidden for create** — simulate-only. Studio must design opt-in around a real session.
3. **GoPlus token-mint:** Extension 1 — creds in `macros.secrets`, token minted + cached server-side
   in the REST runner, never exposed to the node. Moralis key already configured.
4. **Cost & cadence:** cron cadence is the lever (hourly vs 6h); pair with a paid GoPlus CU tier + a
   brief per-`(chain,address)` verdict cache. Runs are durably logged (`history:`).
5. **Migration:** re-deploy starts fresh state (§2.4); seed `wfstate:<taskId>:ntfy:*` (mark current
   flags notified, no alert) at cutover, or keep `taskId` stable. One-time.

---

## 7. Build phases

1. **Extension 1 (REST auth providers)** — `restAuthProvider(node)` reader + `mintGoPlusToken()` +
   RWMutex token cache + header injection, all in `vm_runner_rest.go`; add `goplus_app_key`/
   `goplus_app_secret` to `macros.secrets` (`config/gateway.yaml` + examples/test). Keyless fallback
   on `code:4033`. Independently shippable and testable (a REST node hitting GoPlus authed).
2. **Extension 2 (workflow state)** — `SetIfAbsent`/`DeleteByPrefix` on `Storage` (reuse `IncCounter`
   txn); `wfstate` key builders; `state.*` goja binding in `vm_runner_customcode.go` (honor
   `IsSimulation`); cascade-delete hook in task teardown; persist any tracked record struct under
   `model/`. Additive storage (new namespace, no migration — `make storage-check` clean).
3. **Guardian composition** — port studio `goplus.ts`/`rescan.ts` verdict logic into the CustomCode
   node; assemble the cron→REST→REST→customCode→branch→notify template studio emits; e2e on
   mainnet-only wallets (GoPlus has no testnet coverage).
4. **Optional** — `{{state.*}}` auto-load (§2.3) for template-read in any node.

## 8. Non-goals / open choices

- Exposing `state.*` to CustomCode **is** the design (previously a non-goal). Guard it with
  `IsSimulation` and, if needed, a per-workflow state-size cap.
- Claim-once is mark-after-send (§3.3); `setIfAbsent` pre-claim is the opt-in for strict concurrency.
- Auth-provider registry starts with GoPlus; add providers as needed without touching the runner core.

---

## 9. Client-review resolutions (A–D) — gateway-side commitments

The studio client-side review of the composed workflow surfaced four items. Resolutions, and the
gateway facts behind them (all verified against current code):

- **A — Logic-freeze / central update. DECIDED (studio, 2026-07-15): accept the freeze; NO
  `UpdateWorkflow` ask.** Confirmed there is no in-place workflow update today
  (`handlers_workflows.go` exposes only create/get/delete/pause/resume; editing a node's `customCode`
  means delete+recreate → new `taskId` → lost `wfstate`). The only gateway-side need is:
  - **Expose a `guardian_ruleset` (and, generally, feature-ruleset) configVar** so a workflow reads
    volatile rules (flag sets, thresholds, trust overrides) at runtime and a single gateway-config
    change propagates to **all** deployed instances on next run — no re-deploy, no state loss. (Note:
    only `macros.secrets` currently reaches `apContext.configVars`; either place the ruleset there or
    add `macros.vars` injection.) **It must ALWAYS be set** (even to `{}`): verified live that an
    unresolved `{{...}}` inside a `customCode` `source` hard-fails the node (`could not resolve
    variable apContext.configVars.guardian_ruleset in source`), so this is required guardian setup,
    not an optional value a JS fallback can cover.

  A rare **structural** (interpreter-shape) change is handled by **re-creating** the workflow — the new
  task's first run is a **silent seed** (compute findings, write all `ntfy:` markers, send nothing) so
  the switch doesn't double-alert. This needs **no `wfstate` read API** and **no `UpdateWorkflow`** — a
  fleet re-create on rare structural change is acceptable. (An in-place `UpdateWorkflow` remains a
  possible future ergonomics win but is explicitly **not** required for the guardian.)
- **B — Read-only deploy auth. DECIDED (studio, 2026-07-15): web hand-off; NO create-auth change.**
  Confirmed: `CreateWorkflow` requires a user JWT (EIP-191 signature); partner assertion is rejected for
  create; **no** fund-moving-vs-read-only distinction exists; and a **counterfactual (undeployed) runner
  is accepted** (the ownership gate is a pure DB check). Studio enrolls via its existing
  conversation-resume-on-web hand-off — a one-time EIP-191 sign establishes the 48h session and deploys
  (one signature ever). **No gateway change required.** Relaxing create-auth for the read-only/no-fund
  class via `X-Partner-Assertion` (zero user signature) is parked as a **future lever**, not a
  dependency.
- **C — Claim-once semantics.** Confirmed: executions are **serialized per task** (single FIFO worker,
  no overlap) and **at-most-once with NO retry** (`apqueue/worker.go` marks failed and drops;
  `Recover()` is a no-op). Therefore **mark-after-send (at-least-once) is the correct design for a
  security monitor** — never miss a real flag; a rare duplicate (send ok → mark fails → re-send next
  tick) is the safe direction. Pre-claim (`setIfAbsent`) would convert a send failure into a permanent
  miss and is **not** the default. `state` binding for v1 = `get/set/list` (`setIfAbsent` optional).
  One detail to verify: the notify node sends a **single pre-built JSON body template**
  (`{{verdict.data.telegramBody}}`) — ensure `preprocessJSONWithVariableMapping` forwards it verbatim
  rather than re-parsing/re-serializing (which could corrupt the escaped payload).
- **D — Cadence / cost.** **Default cadence is 6h** (`0 */6 * * *`), not hourly. Scans/day =
  `users × wallets × chains × (24/cadenceHours)`; approvals-only = 2 external calls/scan. Pair with a
  paid GoPlus CU tier; add a brief per-`(chain,address)` verdict cache. **Scope: approvals-only for v1**;
  held-token scan is a fast-follow.

`wfstate` growth is bounded by distinct-flags-ever per workflow (small in practice); no prune on clean
scans (absence ≠ cleared, studio parity). A time-based `wfstate` TTL is a future option if needed.

---

## 10. Completion status

The two gateway extensions shipped in **PR #662** (merged to `staging`, 2026-07-16). Shipped record:
`docs/changes/20260717-workflow-state-and-rest-auth.md`.

**✅ Done — gateway (PR #662):**
- [x] **Extension 1 — REST `options.auth` providers.** `restAuthProvider` reads `options.auth.provider`;
  `goplusAuthHeader` mints + caches a GoPlus signed token (sha1 sign → `/v1/token`), cache keyed by
  `app_key`, 15 s HTTP timeout, 2xx check, `Bearer ` normalization; Authorization injected server-side
  in `vm_runner_rest.go`. Keyless fallback when keys unset. (`vm_runner_rest.go`)
- [x] **Extension 2 — `{{state.*}}` per-workflow state.** `wfstate:<taskId>:<stateKey>` namespace +
  `state.get/set/list` goja binding (VM-scoped scratch, `IsSimulation`-guarded, deterministic-sorted
  `list`); cascade-delete on task teardown. Additive storage (`make storage-check` clean).
  (`schema.go`, `vm_runner_customcode.go`, `vm.go`, `engine.go`)
- [x] **`guardian_ruleset` config wired** — `gateway.example.yaml` / `test.example.yaml`, the local
  gateway config, and avs-infra `gateway-railway.yaml` (`${GOPLUS_APP_KEY}`/`${GOPLUS_APP_SECRET}` env
  refs + inlined ruleset).
- [x] **Tests** — state roundtrip / cross-run persistence / simulation no-op; `restAuthProvider`
  parsing; Authorization-injection (mockable seam). Live-verified: authed GoPlus + `state` against a
  local gateway.

**✅ Done — SDK (ava-sdk-js `staging`):**
- [x] Guardian wallet-risk monitor test + builder/verdict fixture (offline verdict parity + builder
  shape + live deploy/trigger) — `tests/v4/templates/guardian-wallet-risk-monitor.test.ts`.

**⏭️ Deferred / optional (not blocking v1):**
- [ ] `SetIfAbsent` storage primitive for **strict** exactly-once claim-once (mark-after-send is the v1
  default, §3.3).
- [ ] `{{state.*}}` template auto-load for non-CustomCode nodes (§2.3).
- [ ] GoPlus `code:4033` retry-keyless nuance (keyless-on-unset-keys **is** implemented).
- [ ] Held-token (critical-token) scan — approvals-only for v1; held-token is a fast-follow.
- [ ] Promote the `Guardian` builder from SDK test fixture to published `packages/sdk-js` surface.

**🟨 Client-side (studio) — separate product work, tracked in the studio guardian docs:**
- [ ] Advisor tool `enable_guardian_monitoring` deploys the per-user workflow.
- [ ] Enrollment via the one-time web-sign hand-off (§9-B).
- [ ] Sync `guardian_ruleset` from `app/lib/goplus.ts`.

**🔧 Ops follow-up:**
- [x] Railway `GOPLUS_APP_KEY` / `GOPLUS_APP_SECRET` set on the gateway service.
- [ ] Republish `avs-dev:latest` dev docker so the SDK E2E guardian **live** test asserts (it soft-skips
  against the pre-merge image).

---

*Gateway-side response to the studio `PLAN_GUARDIAN_MONITORING_ARCHITECTURE.md` hand-off. The two
extensions are now implemented (PR #662); this document is retained as the design record.*
