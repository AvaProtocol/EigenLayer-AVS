# PLAN — Per-Workflow State + Native Guardian-Scan (gateway design)

> **Audience.** EigenLayer-AVS gateway/backend. This is the gateway-side response to the studio
> hand-off `PLAN_GUARDIAN_MONITORING_ARCHITECTURE.md` (in the `studio` repo). It specifies the one
> capability the guardian actually needs and doesn't have — **durable, cross-run, per-workflow
> state** — as a *generic* primitive, with the continuous guardian monitor as its first consumer.
>
> **Framing decision.** State is scoped to the **workflow (`taskId`)**, never to a user. There is
> **no "one monitoring workflow per user"** rule. A user may create workflow A to watch one EOA
> today, then workflow B to watch two other EOAs next week — two independent tasks, two independent
> state namespaces, zero user-level coupling. **The client (studio) owns setting each workflow up
> correctly** (which wallets, which cadence); the gateway just gives every workflow a private,
> cascade-scoped scratchpad and a native scan step.

---

## 0. Why this doc exists

The guardian needs to alert on **new** security flags **exactly once**. That requires remembering, run
to run, which flags a workflow has already seen and already alerted on. Today the gateway has **no
cross-run workflow state**: node runners get `vm.db` (Go side) but user-authored CustomCode JS gets no
DB handle, execution history is RPC-read-only (not readable mid-run), and the only "state-like"
things are a monotonic contract-write counter (`ct:cw:`), a 15-min idempotency TTL cache
(`idem:noderun:`), and per-execution checkpoints (`ckpt:<execID>`, deleted at terminal state). None
can hold arbitrary per-workflow values across runs.

Everything else the guardian needs already exists: `CronTrigger` (per-task gocron jobs,
`core/taskengine/trigger/time.go`), `BranchNode`, `RestAPINode` + `CustomCodeNode`, durable execution
logs (`history:` keys), and `{{apContext.configVars.*}}` credential injection (`core/taskengine/vm.go`,
sourced from `macros.secrets`). So this doc scopes only the missing core.

---

## 1. The generic capability: per-workflow state (`wfstate`)

A workflow-scoped key/value store that survives across executions and is cleaned up with the workflow.

### 1.1 Key schema (matches `core/taskengine/schema.go` conventions)

```
wfstate:<taskId>:seen:<stateKey>   → JSON record   (the diff-set; prefix-scanned each run)
wfstate:<taskId>:ntfy:<stateKey>   → unix-ms       (claim marker; presence = "already alerted")
```

- `taskId` — the ULID of the workflow. **This is the entire scoping key. No `owner`, no `wallet` in
  the namespace** — the workflow already knows what it monitors (its config / input variables), and
  two workflows of the same user get different `taskId`s and thus fully isolated state.
- `stateKey` — an opaque, consumer-defined string. The guardian uses `<chainId>:<wallet>:<flagKey>`
  so one workflow can monitor several EOAs on several chains without collision.
- `seen:` vs `ntfy:` split keeps the diff-set scan clean and lets the claim be a pure key-existence
  CAS (no JSON coupling in the storage layer).

### 1.2 Storage interface additions (`storage/db.go`)

`Set`, `GetKey`, `Delete`, `BatchWrite`, `Exist`, and prefix scan (used today by `ListExecutions`)
already exist. Add two small primitives:

```go
// SetIfAbsent atomically writes value iff key does not exist. Returns claimed=true only for the
// caller that performed the write. Reuses the serializable db.Update(txn) pattern already used by
// IncCounter (storage/db.go:~486): txn.Get(key) → ErrKeyNotFound ? txn.Set : no-op.
SetIfAbsent(key []byte, value []byte) (claimed bool, err error)

// DeleteByPrefix removes every key under a prefix (workflow-teardown cascade).
DeleteByPrefix(prefix []byte) (int, error)
```

`SetIfAbsent` is the exactly-once primitive; nothing generic like it exists today (grep for
`CompareAndSet|SetIfAbsent|SetIfNotExist` is empty), but BadgerDB's `Update` gives the required
serializable isolation and the exact txn shape is copy-paste from `IncCounter`.

### 1.3 Helper key builders (`core/taskengine/schema.go`)

```go
func WorkflowStateSeenKey(taskID, stateKey string) []byte // "wfstate:%s:seen:%s"
func WorkflowStateSeenPrefix(taskID string) []byte         // "wfstate:%s:seen:"   (diff scan)
func WorkflowStateNtfyKey(taskID, stateKey string) []byte  // "wfstate:%s:ntfy:%s"
func WorkflowStatePrefix(taskID string) []byte             // "wfstate:%s:"        (cascade delete)
```

`wfstate` is a fresh namespace (no existing `wfstate:` literal collides — verified against the
namespace list in `core/taskengine/doc.go`).

### 1.4 Lifecycle

- **Create:** implicit — the first `Set`/`SetIfAbsent` under a `taskId` creates its state.
- **Cascade delete:** the task-teardown path (engine `DeleteWorkflow` / task deletion) calls
  `DeleteByPrefix(WorkflowStatePrefix(taskId))`. Delete the workflow → its state is gone. **No orphan
  bookkeeping, no per-user cleanup.**
- **Re-deploy = fresh state (the one tradeoff).** A new workflow has a new `taskId`, so its state
  starts empty and its first run re-alerts on all currently-active flags **once**. For the guardian
  this is benign (surfacing real current risk to someone who just re-opted-in), and mitigable by
  **keeping `taskId` stable across re-enroll** (update-in-place instead of delete+create) or seeding
  new-workflow state at migration. Documented, not a blocker.

### 1.5 Access boundary (v1: engine-internal, not CustomCode JS)

Node **runners** (Go, `vm_runner_*.go`) already receive `vm.db` (`core/taskengine/vm.go`, `WithDb`)
and the executing task context (so they know `taskId`). The guardian-scan runner (§3) reads/writes
`wfstate` through `vm.db` directly. We **do not** expose `wfstate` to user-authored CustomCode JS in
v1 (goja gets no DB handle by design) — that would turn every workflow stateful and pull in size
limits, isolation, and abuse concerns. A future `state.get/set` JS binding can layer on this same
primitive if we decide to make it a public building block.

---

## 2. The user scenario, modeled

> User creates **Workflow A** to monitor **EOA1**; two weeks later creates **Workflow B** to monitor
> **EOA2 + EOA3**.

```
Workflow A (taskId = 01A…)                     Workflow B (taskId = 01B…)
  wfstate:01A…:seen:1:0xEOA1:approval:0x…:0x…    wfstate:01B…:seen:1:0xEOA2:token:0x…
  wfstate:01A…:seen:1:0xEOA1:token:0x…           wfstate:01B…:seen:8453:0xEOA3:approval:0x…:0x…
```

Two tasks, two `wfstate:<taskId>:…` namespaces, no shared "user" record. B monitoring two EOAs is
just two `stateKey` prefixes inside B's own namespace. Deleting A wipes `wfstate:01A…:*` and leaves B
untouched. This is exactly the "workflow does not attach a user" model.

---

## 3. The native `guardian-scan` node (first consumer)

A new engine-side node type — because the scan must (a) reach `wfstate` via `vm.db`, (b) hold the
GoPlus/Moralis credentials without leaking them into client-visible workflow JSON, and (c) mint the
GoPlus signed token — none of which a CustomCode JS node can do.

### 3.1 Node shape (protobuf `avs.proto` → `make protoc-gen`)

Add `NODE_TYPE_GUARDIAN_SCAN` to the node enum and a message to the `TaskNode` oneof:

```proto
message GuardianScanNode {
  message Config {
    repeated string wallets   = 1;   // EOAs to scan (from the workflow's inputVariables)
    repeated int64  chain_ids = 2;   // mainnets only: [1, 8453]
  }
  message Output { google.protobuf.Value data = 1; }  // { newFindings: [ … ] }
  Config config = 1;
}
```

Runner `core/taskengine/vm_runner_guardian_scan.go`. Per run, for each `(wallet, chainId)`:

1. **Scan** (port studio §5–§6 verbatim — this doc does not restate the verdict rules):
   - approvals via Moralis `GET /wallets/{address}/approvals` → GoPlus
     `GET /v2/token_approval_security` join on `(token, spender)`; `flagged` needs a strong signal
     and a non-trust-listed spender (weak signals `honeypot_related_address`/`blacklist_doubt` never
     flag alone).
   - held tokens via balances → GoPlus `GET /v1/token_security` (top 25 by USD); `critical` per the
     critical-flag set or `max(tax) ≥ 0.5`; `trust_list=="1"` overrides.
   - alert set = **flagged approvals + critical tokens only** (never warnings/spam/unknown).
2. **Diff:** prefix-scan `WorkflowStateSeenPrefix(taskId)` → the set of already-seen `stateKey`s.
   `new = currentFindings − seen`. For each current finding, upsert its `seen:` record
   (`detail`, `firstSeenAt`, refresh `lastSeenAt`).
3. **Emit:** the node output is the **new findings** (`detail` payloads) — the downstream `BranchNode`
   gates "any new findings? → notify", and the `RestAPINode` sends the Telegram DM.
4. **Degraded scans contribute nothing:** if a provider/chain errors, emit no finding for it and
   **never delete** `seen:` rows — absence of data ≠ "flag cleared."

### 3.2 Claim-once (exactly-once alert)

The notify node (or the guardian-scan node, if it also delivers) claims per finding:

```
claimed, _ := db.SetIfAbsent(WorkflowStateNtfyKey(taskId, stateKey), nowMillisBytes)
if claimed { send alert; if send fails → db.Delete(ntfyKey)  // release, retry next run }
else       { already alerted — skip }
```

This is the `notifiedAt: null→now` claim from studio §7, implemented as key-existence CAS.

### 3.3 Credentials (studio §3.3 / §5.1)

- **Config only for the secrets:** add `goplus_app_key` / `goplus_app_secret` to `macros.secrets` in
  `config/gateway.yaml` (+ `gateway.example.yaml`, `test.yaml`, `test.example.yaml`). `moralis_api_key`
  is **already present**. No Go change to expose them — the loader reads `Macros["secrets"]` generically.
- **Server-side token mint (net-new helper):** GoPlus uses `sign = sha1_hex(app_key + time + app_secret)`
  → `POST /api/v1/token` → `Bearer` token (~2h). The runner reads the secrets via
  `GetMacroSecret("goplus_app_key"/"goplus_app_secret")` (engine.go), mints + module-caches the token
  (refresh ~60s early), and calls GoPlus itself. **The `app_secret` is never templated into a node
  and never leaves the gateway.** Keyless fallback: on app-level `code:4033`, retry once with no auth
  header; accept `code ∈ {1,2}`. A first cut may run keyless entirely and add minting for the paid
  CU tier at volume.

---

## 4. The workflow shape studio emits

Unchanged from the existing alert templates, with the scan step swapped to the native node:

```
[CronTrigger "0 * * * *"]  →  [GuardianScanNode wallets=[…] chains=[1,8453]]
   →  [BranchNode: newFindings.length > 0 ? "if" : "else"]
       if →  [RestAPINode → /api/notify  OR  api.telegram.org/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage]
       else → (stop)
```

Cadence is cron (studio converts "hourly/6h" → cron; no separate `TimeInterval` type exists). The
scan `wallets`/`chains` come from the workflow's own config/`inputVariables` — **the client decides
what each workflow watches.** No signature is needed per run; deploy needs the owner's session JWT
(see §6.2).

---

## 5. Worked state timeline (one workflow, one wallet)

`notifiedAt` = presence of the `ntfy:` key.

| Run | Scan finds | `seen:` before | Action | After |
|---|---|---|---|---|
| t0 | approval **A** | ∅ | A new → write `seen:A` → `SetIfAbsent(ntfy:A)`=claimed → **alert** ✅ | seen:{A}, ntfy:{A} |
| t1 | A again | {A} | A already seen; `ntfy:A` exists → **no alert** | unchanged |
| t2 | A + critical **B** | {A} | B new → write `seen:B` → claim B → **alert B** ✅ | seen:{A,B}, ntfy:{A,B} |
| t3 | *(Moralis down)* | {A,B} | degraded → emit nothing, **delete nothing** | unchanged |
| t4 | *(A revoked, clean)* | {A,B} | guardian alerts on **new** only; do **not** delete/alert on removal | unchanged |

Send-failure variant at t0: `SetIfAbsent(ntfy:A)` claimed, Telegram POST fails → `Delete(ntfy:A)` →
next run re-claims and retries. Exactly-once preserved.

---

## 6. Answers to studio §8 open questions

1. **Per-workflow persistent state (the deciding capability):** **Being added** as generic `wfstate`
   (§1). Not present today; additive, low-risk.
2. **No-fund deploy auth:** A read-only, notify-only scheduled workflow deploys with **no per-run
   signature and no fund-authority check** (a task with no runner smart wallet skips the ownership
   gate, `engine.go` `CreateWorkflow`). **But it is not signature-less:** `CreateWorkflow` requires a
   Bearer **session JWT**, minted only after an EIP-191 wallet signature (per-48h session, not
   per-run). `X-Partner-Assertion` is **forbidden for create** (partner.go) — it's simulate-only. So
   enrollment needs the user to hold an AVS session, i.e. one wallet signature per 48h, not zero.
   **Studio must design opt-in around a real session, not a partner assertion.**
3. **GoPlus token-mint injector:** creds in `macros.secrets`; token minted server-side via a net-new
   sha1 helper reading `GetMacroSecret`; secret never exposed to a node (§3.3). Moralis key already
   configured.
4. **Cost & cadence:** cron cadence is the lever (hourly vs 6h); pair with a paid GoPlus CU tier and a
   brief per-`(chain,address)` verdict cache. Per-workflow executions are durably logged (`history:`)
   — the observability win studio wanted.
5. **Migration:** re-deploy starts fresh state (§1.4). Handle existing enrolled users by seeding
   `wfstate:<taskId>:seen:*` (mark current flags as already-seen, no `ntfy:` → no alert) at cutover,
   or keep `taskId` stable. One-time, not a per-run concern.

---

## 7. Build phases

1. **Storage primitive** — `SetIfAbsent` + `DeleteByPrefix` on the `Storage` interface (reuse the
   `IncCounter` serializable-txn shape); `wfstate` key builders in `schema.go`; cascade-delete hook in
   the task-teardown path. Persist the guardian record struct under `model/` so `make storage-check`
   tracks it (additive — new namespace + new struct, no migration).
2. **GoPlus client** — port studio `app/lib/goplus.ts` (auth mint + `token_security` +
   `token_approval_security`), add secrets to `macros.secrets`.
3. **`guardian-scan` node** — `avs.proto` message + enum, `make protoc-gen`, runner
   `vm_runner_guardian_scan.go` (scan → diff via `wfstate` → emit new findings), claim-once on notify.
4. **Template + wiring** — the cron→scan→branch→notify template studio emits; end-to-end test on
   mainnet-only wallets (GoPlus has no testnet coverage).

## 8. Non-goals / open choices

- **Not** exposing `wfstate` to CustomCode JS in v1 (engine-internal only). Revisit as a public
  `state.*` binding later.
- Claim-once granularity is per-`stateKey` (per flag); a coarser per-run claim is not needed.
- Whether the notify send lives in the guardian-scan node or a separate REST node is an
  implementation detail — the claim-once logic is identical either way.

---

*Planning only. No code changed by this document. Gateway-side response to the studio
`PLAN_GUARDIAN_MONITORING_ARCHITECTURE.md` hand-off.*
