# PLAN — Cross-Chain Sequencing (Phase 4)

Status: **design, not started.** Successor to `PLAN_CHAIN_DECOUPLING.md` Phase 4. This doc is the
design of record; nothing here is implemented yet.

## What this is

Today a workflow can **watch chain X and act on chain Y independently** (per-part chains, shipped in the
chain-decoupling work). What it *cannot* do is **sequence across a bridge**: run some nodes, initiate a
bridge transaction on chain A, **wait** until the bridged funds/message arrive on chain B (minutes to
hours later), then **continue the same workflow** acting on chain B with the earlier nodes' context still
available.

That requires the executor to **suspend mid-workflow, persist its state, and be woken later** — a
re-entrant / durable executor. Today the executor is synchronous and single-pass.

## Why not "two workflows"? (decision 5)

A user can already approximate this with two separate workflows — workflow 1 bridges on A; a separate
EventTrigger workflow on B does the follow-up. **The fatal flaw: the two executions do not share
context.** Workflow 2 has none of workflow 1's variables, outputs, or execution identity — no
`{{bridge.data.*}}`, no single execution record, no continuity. Cross-chain sequencing is therefore a
**single workflow** whose `vars` map (the shared context) is carried across the suspend boundary. That
carried context is the entire point of the feature.

---

## Current architecture — what we're working with

Grounded in the code (file:line refs in [§ Reference map](#reference-map)).

- **Execution is synchronous, single-pass.** `RunTask → vm.Compile → vm.Run` (a Kahn scheduler runs all
  ready nodes to completion) → one terminal status. No pause/resume, no checkpoint.
- **`ExecutionStatus` has only terminal states** beyond start: `PENDING(1) → SUCCESS(2) / FAILED(3) /
  ERROR(5)`. Slot **`4` is unused** — the natural home for `WAITING`.
- **Node outputs live only in `vm.vars` in memory** during a run and are discarded at the end; nothing
  persists them mid-execution.
- **The existing "wait"** (`waitForUserOpConfirmation`) is a synchronous ≤5-min poll that **blocks a
  worker** and dies on restart — unusable for bridge-scale waits.
- **The trigger → wake → execute pipeline already exists and is reusable.** Operators watch each chain
  via per-chain `EventTrigger`/`BlockTrigger` `AddCheck`; a detected condition flows
  `NotifyTriggers → AggregateChecksResultWithState → apqueue → executor`. `apqueue` has **no retry limit**
  (can hold a job indefinitely) and `QueueExecutionData` is an extensible payload that already carries an
  `ExecutionID`.
- **The smart-wallet runner address is chain-invariant** (CREATE2 of factory + owner + salt). Acting on
  chain B uses the **same address**; `resolveSmartWalletForNode` routes per-chain config, and #637 made
  the cross-chain salt lookup work. **No new wallet work is needed to act on B.**
- **No native bridge integration exists.** A bridge call is just a `contractWrite` to a bridge contract;
  bridging is external and user-configured.

---

## Locked design decisions

1. **Wake = event-on-B only** (v1). The wake condition is an event watch on chain B. Block-count and
   timer wake modes are deferred.
2. **Restart durability is a first-class requirement.** A WAITING execution must survive an aggregator
   restart and re-arm its wake sensor.
3. **Create-time validation:** an `Await` node's chain B must be operator-covered, or creation is rejected.
4. **Fee model is trivial:** a structure that exists but defaults to `0`. No billing complexity in v1.
5. **Single workflow, shared context** (see above).

---

## Components to leverage vs. build

The wake-up half already exists (operator trigger-watching); the new engine is durable execution in
`core/taskengine`. This respects the aggregator-vs-operator split: operators watch chains, the aggregator
orchestrates.

| Role in Phase 4 | Leverage (exists) | Build (new) |
|---|---|---|
| **"Wait for arrival on B" sensor** | Operator `EventTrigger.AddCheck` per chain | Accept engine-created **internal** triggers (same shape as user triggers) |
| **Wake → resume delivery** | `NotifyTriggers → AggregateChecksResultWithState → apqueue` (dedup + indefinite hold already present) | A **resume job** carrying `executionId` + resume context |
| **Suspend / checkpoint / resume brain** | `core/taskengine` lifecycle + BadgerDB storage | The **re-entrant executor**, checkpoint store, internal-trigger registry |
| **Act on chain B** | erc4337 smart wallet (chain-invariant address); `resolveSmartWalletForNode`; #637 cross-chain salt | **Nothing** — a `contractWrite(chainId=B)` just works |
| **Initiate the bridge** | A normal `contractWrite` to the bridge contract on A | Templating to forward the bridge tx hash / messageId |

---

## The `Await` node (a mid-workflow EventTrigger on chain B)

Because wake is event-only, the `Await` node's config **is** an embedded event-watch on chain B — it
behaves like an `EventTrigger` dropped into the middle of a workflow. "Bridge wait" is a *usage pattern*;
`Await` is a reusable "pause until event X on chain Y" primitive.

```
Await.Config {
  chain_id            // chain B — required, must be a configured + operator-covered chain
  address             // bridge / destination contract on B
  topics / event_sig  // the arrival event (e.g. CCIP MessageReceived, Across FilledRelay, Stargate Received)
  filter              // templated, bound to THIS execution's arrival
                      //   e.g. messageId == {{bridge.data.messageId}}
  timeout_seconds     // safety bound for a stuck bridge — NOT a wake mode
  on_timeout          // FAIL (v1) | take a fallback edge (deferred)
}
Await.Output = the matched log (received amount, sender, …) → readable by downstream B-leg nodes
```

`Await.chain_id` is a chain-aware part: subject to the required-`chainId` rule and the create-time
operator-coverage check.

---

## Persisted structures (decision 2 + 4)

All proto additions are **additive** → `make storage-check` stays clean. New storage keys are additive.

```
ExecutionCheckpoint        key: ckpt:<taskId>:<execId>        (survives restart)
  task_id, execution_id
  vars: map(JSON)                  // the SHARED CONTEXT carried across suspend (decision 5)
  completed_node_ids: []string     // completed nodes are skipped on resume (goja/custom-code NOT re-run)
  execution_logs: []Execution_Step // steps so far, for the final execution record
  resume_node_id                   // the Await node; resume runs its successors
  await { chain_id, address, topics, filter, timeout_at }
  created_at / updated_at

InternalTrigger            key: itrigger:<taskId>:<execId>    (persisted registry, re-synced on boot)
  task_id, execution_id, chain_id(B)
  event_config { address, topics, filter }
  expires_at, status

Proto additions
  ExecutionStatus: + EXECUTION_STATUS_WAITING = 4      // slot 4 currently unused
  Execution:       + resume_node_id, + wait_reason
  Execution:       + resume_fee_wei (default 0)        // the fee structure (decision 4)
```

**Fee model (decision 4).** One logical execution = leg-A value fee (the existing #637 credit check at
start) **+ `resume_fee_wei`, default `0`**. No separate credit gate on resume. The field exists only so
per-leg billing can be added later without a schema change.

---

## End-to-end flow

```
[contractWrite chainId=A]  →  [Await chainId=B]  →  [contractWrite chainId=B]
   initiate bridge,             wake on B-event       act on bridged funds
   emits txHash/messageId       (this execution)      (same smart wallet)
```

1. Executor runs the A-leg up to `Await`. The bridge `contractWrite` output feeds `Await`'s chain-B filter.
2. `Await` **checkpoints** (`vars` + `completed_node_ids` + `resume_node_id`), sets execution status
   **WAITING**, and the engine **persists + registers an internal EventTrigger** on chain B (filter bound
   to this execution).
3. An operator covering B watches it via the existing `AddCheck` path → fires `NotifyTriggers`.
4. `AggregateChecksResultWithState` (dedup via `TriggerRequestId`, already exactly-once) enqueues a
   **resume job** carrying the `executionId`.
5. The **re-entrant executor** loads the checkpoint, rebuilds the VM, **restores `vars`**, marks completed
   nodes done, seeds the scheduler with `Await`'s successors → runs the B-leg against the **same
   chain-invariant smart wallet**.
6. Terminal status → deregister the internal trigger.

---

## Restart durability (decision 2 — the make-or-break path)

- A WAITING execution persists **both** the checkpoint and the `InternalTrigger` registry entry.
- On aggregator boot — and on every operator (re)connect — WAITING executions' internal triggers are
  **included in the set synced to operators covering chain B**, exactly as active task triggers and pending
  executions are resynced today (`StreamCheckToOperator`). A mid-wait restart re-arms the sensor; nothing
  is lost.
- Timeouts are persisted (`expires_at`). A periodic sweep (reuse the existing cleanup ticker) fails/expires
  waits whose `timeout_at` passed while no operator was up.

---

## Create-time validation (decision 3)

At `CreateWorkflow`, every `Await` node's `chain_id` is validated like any chain-aware part (required,
`>0`, configured) **and** additionally that `operatorsCoveringChain(chainB)` is non-empty — so a wait can
never be registered that no operator could ever fire.

---

## Implementation sub-phases (incremental, each shippable)

- **P4.0 — contract & schema.** Add `EXECUTION_STATUS_WAITING`, `resume_node_id`, `wait_reason`,
  `resume_fee_wei` (default 0); define the checkpoint + internal-trigger schemas. No behavior change.
- **P4.1 — checkpoint persistence.** Persist node outputs after each node (`ckpt:<taskId>:<execId>`). Also
  a general crash-resilience win.
- **P4.2 — re-entrant executor (riskiest).** Resume-from-checkpoint entrypoint; teach the Kahn scheduler a
  "treat completed nodes as done, start from `resume_node_id`'s successors" mode. Restore `vars`; skip
  completed (goja/custom-code outputs restored, not re-run). Tested via a synthetic resume job — no
  triggers yet.
- **P4.3 — `Await` node + internal-trigger registry.** New node type (7 touch-points: proto enum + Config
  + Output + `TaskNode` oneof; REST mapping; `CreateNodeFromType`; `ExtractNodeConfiguration`; VM runner;
  dispatch; loop-runner support). Engine registers/deregisters mid-execution triggers and syncs them to
  operators. Wire wake → resume.
- **P4.4 — durability hardening.** Boot/reconnect re-registration; timeout sweep; exactly-once resume;
  cancel/pause of WAITING executions; internal-trigger GC.
- **P4.5 — bridge UX.** Forward bridge tx hash / messageId templating; chain-B filter helpers; docs +
  one worked example (e.g. CCIP `MessageReceived`).

---

## Explicitly deferred (out of v1)

- Block-count / timer wake modes (event-on-B only for now)
- `on_timeout` fallback edges (v1 = FAIL on timeout)
- Per-leg fee billing (`resume_fee_wei` stays 0)
- Multi-hop bridges (A→B→C)
- Non-event arrival detection (balance polling on B)

---

## Open questions / risks

- **Scheduler resume semantics (P4.2)** is the load-bearing change — needs a precise spec of how the Kahn
  ready-queue is seeded from a partial-completion checkpoint, and how Loop/Branch state mid-graph is
  represented. Drill-down pending.
- **Exactly-once across restart** — the existing `TriggerRequestId` dedup is a 5-min window; a resume
  signal that arrives around a restart must still resume exactly once. Needs an idempotency key tied to
  `executionId` rather than (or in addition to) the time-windowed dedup.
- **Operator coverage drift** — chain B is validated as covered at create time, but coverage can change
  before the wait fires. Behavior if the only covering operator drops while a wait is pending?
- **Fee credit at resume** — v1 charges `0`; confirm that's acceptable for the B-leg gas/value even though
  the A-leg credit check already ran.

---

## Reference map

Execution / VM:
- `core/taskengine/executor.go:212` `RunTask`/`RunTaskWithContext` (single blocking call); `:164` `Perform`
  (job processor); `:108` `QueueExecutionData` (extensible payload, has `ExecutionID`); `:748` atomic
  end-of-execution persistence.
- `core/taskengine/vm.go:896` `Run`; `:945` `runKahnScheduler` (the resume seam for P4.2); `:653` `Compile`;
  `:99` `SetOutputVarForStep`/`vars` (the shared context); `:339` `resolveSmartWalletForNode`.
- `ExecutionStatus` enum (proto): `UNSPECIFIED(0) PENDING(1) SUCCESS(2) FAILED(3) ERROR(5)` — `4` free.
- Existing synchronous wait: `core/taskengine/vm_contract_write_waiting.go:109` `waitForUserOpConfirmation`.

Trigger → wake → execute pipeline:
- `aggregator/rpc_server.go:559` `NotifyTriggers`; `core/taskengine/engine.go:2627`
  `AggregateChecksResultWithState` (dedup `:2706`, enqueue `:2874`); `core/apqueue/queue.go:109` `Enqueue`.
- `core/taskengine/engine.go:1818` `StreamCheckToOperator` (task sync — extend to sync internal triggers);
  `:702` `triggerMonitoringChainID`; `:718` `operatorsCoveringChain`; `:5073` `supportsTaskChain`.
- `operator/worker_loop.go:1081` `EventTrigger.AddCheck`; `:263` per-chain trigger sets; `:701` event-fire
  `NotifyTriggers`.

Smart wallet / per-chain execution:
- `core/chainio/aa/aa.go:99` `computeSmartWalletAddress` (CREATE2 — chain-invariant).
- `core/taskengine/vm_runner_contract_write.go:807` send UserOp via per-chain bundler; output receipt with
  `transactionHash` (→ `{{node.data.transactionHash}}`).
- `core/config/config.go` `SmartWalletConfig` (per-chain RPC/bundler/factory); `ChainConfigRaw` (gateway
  multi-chain registry).

Adding a node type (P4.3 touch-points):
- `protobuf/avs.proto` (`NodeType` enum + `Config`/`Output` messages + `TaskNode` oneof);
  `aggregator/rest/mapping/node.go:24` `OpenAPIToProtoNode`; `core/taskengine/vm.go:2542`
  `CreateNodeFromType`; `:3502` `ExtractNodeConfiguration`; a new `vm_runner_await.go`; VM dispatch; loop
  runner support.

---

## Appendix A — P4.2 re-entrant executor (deep-dive)

The load-bearing change. Grounded in `runKahnScheduler` (`core/taskengine/vm.go:947`) and the `VM` struct.

### A.1 What to checkpoint
Only three `VM` fields carry irrecoverable state; everything else is rebuilt by `Compile()` or re-injected
by the executor.

| VM field | Checkpoint? | Why |
|---|---|---|
| `vars map[string]any` | ✅ | shared context + every node output; JSON-serializable |
| `ExecutionLogs []*Execution_Step` | ✅ | A-leg steps for the final record |
| `plans`, `entrypoint`, `mu`, `Status`, `instructionCount` | ❌ | rebuilt by `Compile()` |
| `smartWalletConfig`, `chainConfigResolver`, `db`, `logger`, `executionFeeWei` | ❌ | re-injected on resume |

`completed_node_ids` is **derived**: `[step.NodeID for step in ExecutionLogs]`. Lean checkpoint:

```
ckpt:<taskId>:<execId>
  vars            JSON(v.vars)
  execution_logs  []Execution_Step            // also yields completed_node_ids
  resume_node_id  (the Await node)
  await           { chain_id, address, topics, filter, timeout_at }
  branch_selections  map[branchNodeId]selectedConditionNodeId   // empty in v1, forward-compat
```

### A.2 Suspend signal
`executeNode` returns `(*Step, error)`, but errors become warnings (`vm.go:1069`) and a returned `Step`
only *adds* branch scheduling — neither halts the run. Add a VM-level flag the `Await` runner sets:

```go
// VM, guarded by v.mu:
suspend *SuspendRequest   // nil normally
// SuspendRequest { awaitNodeID, wake{chain,address,topics,filter}, timeoutAt }
```

In the worker loop right after `executeNode` (`vm.go:1077`):

```go
mu.Lock()
if v.suspendRequested() {
    suspended = true
    closeOnce.Do(func() { close(ready) })   // no new work; in-flight drain & exit
    processed++
    mu.Unlock()
    continue                                // skip scheduling Await's successors
}
// ... existing decrement-successors / branch-selection ...
```

`runKahnScheduler` returns a `suspended` bool → executor writes checkpoint, sets **WAITING**, registers the
internal trigger.

**v1 constraint:** `Await` must be on a **linear path** (nothing else in-flight when it runs — the
bridge→Await→act pattern guarantees this). Draining N parallel in-flight nodes / partial checkpoint is
deferred. Reject at create-time (if cheap) a graph where `Await` can run concurrently with siblings.

### A.3 Resume seam (replaces only the initial-ready seed, `vm.go:1037-1046`)
`runKahnScheduler` gains a `completed map[string]bool` param (empty ⇒ today's behavior, byte-identical):

```go
queue := []string{}
for id, c := range predCount {
    if c == 0 && !branchTargets[id] { queue = append(queue, id) }
}
visited := map[string]bool{}
for len(queue) > 0 {
    id := queue[0]; queue = queue[1:]
    if visited[id] { continue }
    visited[id] = true
    if completed[id] {
        scheduled[id] = true                 // counts as done; NOT added to scheduledCount
        for _, succ := range adj[id] {        // fast-forward = the decrement a finished worker does
            if _, ok := predCount[succ]; ok {
                predCount[succ]--
                if predCount[succ] == 0 && !branchTargets[succ] { queue = append(queue, succ) }
            }
        }
        // branch nodes in the prefix replay via branch_selections — deferred (v1 linear)
    } else {
        ready <- id; scheduled[id] = true; scheduledCount++   // the resume frontier
    }
}
```

Fresh run: `completed` empty → every node hits `else` → identical to today. Resume: the completed prefix
decrements down; frontier = `Await`'s successors (the B-leg). `scheduledCount`/`processed` count only
executed nodes, so the existing termination (`processed == scheduledCount → close(ready)`) holds.

### A.4 Resume sequence (executor)
1. Resume job arrives (`executionId` + wake-event payload).
2. Load checkpoint → new VM; inject restored `vars`, `ExecutionLogs`, per-chain config/resolver/db.
3. `vars[awaitNodeName].data = <wake event log>` so the B-leg reads `{{await.data.*}}`.
4. `Compile()`; `runKahnScheduler(completed = set(completed_node_ids))` → run B-leg; append steps.
5. Terminal status; deregister internal trigger.

### A.5 Why goja never needs serializing
Completed nodes are **never re-executed** on resume — their outputs are already in restored `vars`. A
custom-code node from the A-leg is just `vars["myCode"].data`; resume skips it.

### A.6 P4.2 risks
- **`scheduledCount` accounting** — fast-forwarded nodes set `scheduled[id]=true` but must NOT bump
  `scheduledCount`, or termination never fires. (Needs a "resumed run terminates" test.)
- **Branch in the completed prefix** — deferred; needs `branch_selections` replay. v1 Await is post-bridge
  linear.
- **Checkpoint atomicity** — checkpoint write + status→WAITING + internal-trigger register = one
  `BatchWrite` (reuse the atomic end-of-run write at `executor.go:748`).
- **Idempotent resume** — a resume job delivered twice (around a restart) must no-op; guard on current
  status before resuming.

---

## Appendix B — Outcomes & e2e test scenarios (build toward these)

The acceptance criteria, expressed as observable outcomes and the scenarios that prove them. We build the
feature *toward* this list. **Test layers:** most scenarios are **Go integration** tests (engine + executor
+ VM, with the operator's `NotifyTriggers` and chain events **simulated/injected** — deterministic, fast).
A small subset (the real bridge) are **testnet SDK e2e** (`ava-sdk-js`, Sepolia ↔ Base Sepolia, gated/slow).
Per repo convention, integration tests **hard-fail** on missing prereqs (`require`, not `t.Skip`).

### Outcomes (definition of done)
- **O1 — Single execution across the bridge.** One workflow bridges on A, waits, and acts on B as **one
  execution with one execution record** spanning both legs.
- **O2 — Shared context.** The B-leg sees the A-leg's full `vars`, including the bridge output and any
  earlier node's output.
- **O3 — Restart durability.** A WAITING execution survives an aggregator restart and still completes when
  the B event fires.
- **O4 — Exactly-once node execution.** Completed (A-leg) nodes are **not** re-run on resume — no duplicate
  side effects.
- **O5 — Correct wake.** Only the matching B event resumes the right execution; concurrent waits don't
  cross-talk; a duplicate signal resumes once.
- **O6 — Clean timeout.** A stuck bridge times out → execution FAILED, internal trigger released.
- **O7 — Create-time validation.** Creation is rejected if chain B is unconfigured/`0` or not
  operator-covered.
- **O8 — No resume fee gate.** Resume incurs no extra credit check; `resume_fee_wei = 0`.
- **O9 — Chain-invariant wallet on B.** The B-leg acts via the same smart-wallet address; auto-deploys on B
  if needed (salt via #637).
- **O10 — Cancellable wait.** A WAITING execution can be cancelled/paused, deregistering its wait.

### E2E scenarios

**E1 — Happy path: bridge → wait → act (O1, O2, O9).** *Integration.*
Build a task: `contractWrite(chainId=A, bridge)` → `Await(chainId=B, event=Arrival, filter=messageId=={{bridge.data.messageId}})` → `contractWrite(chainId=B, act)`. Run it.
- *Then:* A-leg executes; status becomes **WAITING**; an internal trigger is registered on B with the
  bound filter. Inject the matching B `Arrival` event. Status becomes **SUCCESS**.
- *Assert:* one execution record; `ExecutionLogs` = A-leg steps + Await + B-leg steps, in order; the B-leg
  `contractWrite` resolved `{{bridge.data.transactionHash}}` and `{{await.data.amount}}`; the B UserOp used
  the same smart-wallet address as the A leg.

**E2 — Shared context proof (O2).** *Integration.*
Put a `customCode` node in the A-leg that emits a value, and have a B-leg node consume it.
- *Assert:* the B-leg received the exact A-leg value across the suspend boundary (impossible with two
  separate workflows — this is the feature's reason for existing).

**E3 — Restart durability (O3).** *Integration — the make-or-break test.*
Drive E1 to **WAITING**, then **tear down and re-create the engine from the same BadgerDB** (simulated
aggregator restart). Reconnect a fake operator covering B.
- *Assert:* the internal trigger is re-registered on boot/reconnect; injecting the B event after restart
  still resumes the execution to **SUCCESS**.

**E4 — Exactly-once across suspend (O4).** *Integration.*
A-leg node has an observable side effect (counter / mock tx). Drive to WAITING and resume.
- *Assert:* the A-leg side effect fired **exactly once** total; resume executed **only** the B-leg.

**E5 — Resumed run terminates (P4.2 A.6).** *Integration.*
Resume a multi-node B-leg (incl. a fan-out/fan-in).
- *Assert:* the resumed scheduler reaches a terminal status and does not hang (guards the
  `scheduledCount`/termination edge case).

**E6 — Correct-filter wake (O5).** *Integration.*
WAITING execution with `filter=messageId==M1`. Inject a **non-matching** B event (`messageId==M2`).
- *Assert:* no resume. Then inject the matching `M1` event → resumes once.

**E7 — Concurrent independent waits (O5).** *Integration.*
Two WAITING executions on the same chain B with distinct filters (M1, M2). Fire M2 then M1.
- *Assert:* each resumes exactly its own execution; no cross-talk; order-independent.

**E8 — Duplicate resume signal (O5).** *Integration.*
Deliver the same matching B event **twice** (e.g., around a restart).
- *Assert:* the execution resumes **once**; the second delivery is a no-op (status guard).

**E9 — Timeout on stuck bridge (O6).** *Integration.*
WAITING execution, no B event before `timeout_seconds`.
- *Assert:* the timeout sweep transitions the execution to **FAILED** with a timeout reason; the internal
  trigger is GC'd; a late B event no longer resumes.

**E10 — B-leg failure after resume (O1 terminal).** *Integration.*
Resume into a B-leg `contractWrite` that reverts.
- *Assert:* execution ends **FAILED** with the B-leg error; single record; no dangling WAITING/trigger.

**E11 — Create-time validation (O7).** *Integration.*
(a) `Await.chainId` not in the configured set / `0` → `CreateWorkflow` rejected (`InvalidArgument`).
(b) `Await.chainId` configured but **no operator covers it** → rejected.
(c) Valid + covered → accepted.

**E12 — No resume fee gate (O8).** *Integration.*
Owner with an outstanding fee balance that would block a fresh task. Drive E1 to resume.
- *Assert:* the resume leg is **not** blocked; `resume_fee_wei == 0`; total fee = A-leg fee + 0.

**E13 — Cancel a WAITING execution (O10).** *Integration.*
Drive to WAITING, then cancel/pause the workflow.
- *Assert:* the internal trigger is deregistered; a subsequent matching B event does **not** resume.

**E14 — Auto-deploy wallet on B (O9).** *Integration.*
Resume where the smart wallet is **not yet deployed** on B.
- *Assert:* the B UserOp deploys it (initCode + salt via #637 cross-chain salt lookup) at the same address;
  the act succeeds.

**E15 — Real bridge on testnet (O1, O9 end-to-end).** *Testnet SDK e2e — gated/slow.*
Via `ava-sdk-js` against a local gateway + Sepolia/Base-Sepolia workers + operator: bridge a small amount
Sepolia→Base Sepolia through a real bridge, Await the destination event, act on Base Sepolia.
- *Assert:* one workflow completes across both real chains; the destination action lands at the same smart
  wallet. (The single full-stack proof; everything else is deterministic integration.)

### Build order vs. scenarios
- P4.1 (checkpoint persistence) → unblocks **E4** state restoration assertions.
- P4.2 (re-entrant executor) → **E1, E2, E4, E5** (resume runs, no re-exec, terminates) via a synthetic
  resume job (no triggers yet).
- P4.3 (Await + internal trigger) → **E1 (full), E6, E7, E11**.
- P4.4 (durability hardening) → **E3, E8, E9, E13**.
- P4.5 (bridge UX) → **E15**.
- Cross-cutting: **E10, E12, E14** land alongside their nearest phase.

---

## Appendix C — Storage contract (the atomic suspend/resume write)

Grounded in the existing schema (`core/taskengine/schema.go`), the executor's atomic end-of-run write
(`executor.go:748`), and the `storage.Storage` interface (`storage/db.go`).

### C.1 New keys (chain-agnostic, matching `history:`/`u:`)
Post-decoupling, task/execution keys are chain-agnostic (`history:<taskId>:<execId>`,
`u:<owner>:<wallet>:<key>`). The new keys follow suit — the watched chain is **data in the value**, not in
the key (the checkpoint/wait belong to a task+execution, which are chain-agnostic; only the watch *target*
is per-chain, and that lives inside).

```
CheckpointKey(taskId, execId)       →  ckpt:<taskId>:<execId>
InternalTriggerKey(taskId, execId)  →  itrigger:<taskId>:<execId>     // chain_id(B) is in the value
Scan prefixes for boot recovery:       ckpt:        itrigger:
```

Both key templates are **new ⇒ additive**: `make storage-check` stays clean, **no migration**.

### C.2 Serialization (proto, protojson — like executions)
```proto
message ExecutionCheckpoint {
  string                  task_id = 1;
  string                  execution_id = 2;
  string                  vars_json = 3;            // JSON of v.vars (lossless for the map[string]any)
  repeated Execution_Step execution_logs = 4;       // A-leg steps (also yields completed_node_ids)
  string                  resume_node_id = 5;        // the Await node
  AwaitConfig             await = 6;                 // { chain_id, address, topics, filter, timeout_at }
  map<string,string>      branch_selections = 7;     // empty in v1, forward-compat
}
message InternalTrigger {
  string              task_id = 1;
  string              execution_id = 2;
  int64               chain_id = 3;                  // chain B (the watch target)
  EventTrigger.Config event_config = 4;              // reuse the existing event-watch config
  int64               expires_at = 5;                // unix; timeout sweep
}
// Execution (additive): + EXECUTION_STATUS_WAITING=4, + resume_node_id, + wait_reason, + resume_fee_wei(0)
```
All additive proto changes; `vars` as a JSON string keeps big-int/typed values lossless (a `Struct` would
coerce them).

### C.3 Atomic suspend write (crash-safe by ordering)
`BatchWrite` is a single Badger txn **only while it fits**; it splits on `ErrTxnTooBig` (`db.go:125`) and
**ignores commit errors** (always returns nil). So we don't put the (potentially large) checkpoint in the
same batch as the small "armed" markers, and we don't trust the return value — the boot scan (C.5) is the
source of truth.

```
1. db.Set(CheckpointKey, checkpointBlob)                       // resume DATA first (single txn)
2. db.BatchWrite({ TaskExecutionKey: execution{WAITING,…},     // ARMED markers — 2 small keys,
                   InternalTriggerKey: internalTrigger })      //   always one atomic txn
```
**Invariant:** armed markers exist ⟹ the checkpoint exists (write order). A crash between 1 and 2 leaves an
orphan checkpoint that never fires (no trigger, no WAITING execution) → swept by an orphan-checkpoint GC.
The task's own status row (`WorkflowStorageKey`/`TaskUserKey`) is **untouched** — the task stays Enabled;
only this *execution* is WAITING, so there's no status-move/`Delete` to coordinate.

### C.4 Resume-completion write (durable terminal first, then cleanup)
```
1. db.BatchWrite({ TaskExecutionKey: execution{SUCCESS|FAILED, full steps},
                   WorkflowStorageKey/TaskUserKey: task status })   // terminal result, durable
2. db.Delete(CheckpointKey); db.Delete(InternalTriggerKey)          // cleanup (post-commit)
```
A crash between 1 and 2 leaves a **terminal** execution plus a stale checkpoint/trigger → reconciled on boot
(C.5) and by the idempotency guard (a late wake sees a terminal execution → no-op, satisfying **E8**).

### C.5 Boot re-arm + GC (durability O3 / E3, timeout O6 / E9)
On aggregator start (and the source of truth for the armed set):
```
IterateKeysOnly("itrigger:") → for each (taskId, execId):
   exec = GetKey(TaskExecutionKey)
   if exec missing OR exec.status != WAITING:        // stale (completed/cancelled or crash-after-terminal)
       Delete(InternalTriggerKey); Delete(CheckpointKey)        // GC
   else if now > expires_at:
       run timeout path → mark exec FAILED; Delete(InternalTriggerKey); Delete(CheckpointKey)   // E9
   else:
       re-register the internal trigger into the set synced to operators covering chain_id(B)    // E3
```
This makes the persisted `itrigger:` set the durable registry: a restart re-arms every live wait, GCs every
dead one, and expires the timed-out ones — no in-memory state required to survive the restart.
`IterateKeysOnly` is constant-memory (safe even with many waits).

### C.6 What this buys / costs
- **Atomicity:** the armed-markers batch is always a single txn (2 small keys); the large checkpoint is a
  separate single `Set`; ordering + boot reconciliation cover the seams. No reliance on `BatchWrite`'s
  (swallowed) error.
- **storage-check:** two additive key templates + additive proto fields ⇒ **no migration**.
- **Pre-existing gotchas surfaced** (not fixed here): `BatchWrite` swallows commit errors and splits on
  `ErrTxnTooBig`. Worth a follow-up to make `BatchWrite` return commit errors, but the design above is
  correct without it.

---

## Appendix D — Internal-trigger sync path (wake routing)

The last conceptually-unproven piece. Grounded in the sync protocol (`protobuf/node.proto`),
`StreamCheckToOperator` (`engine.go:1818`), `AggregateChecksResultWithState` (`engine.go:2627`), and the
operator watch (`operator/worker_loop.go:1081`).

### D.1 The gap
The existing pipeline is **`task_id`-keyed**: `SyncMessagesResp.TaskMetadata{ task_id, trigger, chain_id }`
goes *down* to operators; `NotifyTriggersReq{ task_id, trigger_output, trigger_request_id }` comes *up* when
it fires; the aggregator starts a **fresh** run. An internal trigger is **execution-scoped** (a specific
WAITING execution wants a specific event on chain B, and the fire must *resume that execution*, not start a
new run). So we thread an `execution_id` through both messages and branch on it.

### D.2 Proto additions (additive)
```proto
// node.proto — SyncMessagesResp.TaskMetadata
+ string execution_id = 7;   // set for internal/await triggers; "" for a task's own trigger
// node.proto — NotifyTriggersReq
+ string execution_id = 11;  // operator echoes it back when an internal trigger fires
```
That's the entire wire change. An internal trigger is just a `TaskMetadata` with `chain_id = B`,
`trigger =` the internal EventTrigger (built from `Await.Config`), and `execution_id` set. The operator
needs no new *watch* logic — only to carry the id through.

### D.3 Registration (suspend → operators covering B)
- The set the sync loop pushes to operator **O** becomes: *active task triggers* (today) **+** *WAITING
  executions' internal triggers whose `chain_id ∈ O.supported_chain_ids`*. The internal-trigger set is an
  in-memory mirror, **rebuilt on boot from the `itrigger:` scan** (Appendix C.5) and updated on
  suspend/resume.
- On suspend, don't wait for the next sync tick: **immediately** push `MonitorTaskTrigger(TaskMetadata{…,
  execution_id})` to operators covering B (reuse the immediate/batched notify path at `engine.go:2341`,
  same mechanism as `ImmediateTrigger`).

### D.4 Operator side (reuse the per-chain watch; `worker_loop.go`)
- `EventTrigger.AddCheck` keys the check by **`(task_id, execution_id)`** so a task's own trigger
  (`execution_id=""`) and its internal triggers (`execution_id` set) coexist without collision.
- On fire: echo `execution_id` in `NotifyTriggersReq`; the `trigger_request_id` becomes
  `task_id:exec_id:txHash:logIndex` (per-execution-per-event).
- A `DeleteTask`/`DisableTask` message scoped by `execution_id` removes one internal check.
- **No new watch logic** — it's the existing per-chain `EventTrigger` machinery with an extra id passed through.

### D.5 Wake → resume routing (`AggregateChecksResultWithState`)
Branch right after the dedup (`engine.go:2706`), before the fresh-run path:
```go
if payload.ExecutionId != "" {                 // an internal/await trigger fired
    return n.resumeWaitingExecution(payload)
}
// ... existing runnable check + QueueExecutionData + enqueue (fresh run) ...
```
`resumeWaitingExecution`:
1. Load the execution. **If status != WAITING → no-op** (already resumed / cancelled / timed-out). This is
   the durable exactly-once guard, beyond the 5-min dedup window (**E8**).
2. (If the bound filter is a data field, post-match it here — see D.7.)
3. Build `QueueExecutionData{ ExecutionID: payload.ExecutionId, IsResume: true,
   TriggerOutput: ExtractTriggerOutput(payload.TriggerOutput) }` and enqueue `JobTypeExecuteTask`.
4. `executor.Perform` sees `IsResume` → the resume entrypoint (Appendix A.4): load checkpoint, set
   `vars[awaitNode].data = <wake event>`, run the B-leg.

`QueueExecutionData` (Go struct) gains one field: `IsResume bool` (it already carries `ExecutionID`).

### D.6 Dedup & idempotency (two layers)
- **Short window:** `trigger_request_id` (now per-execution-per-event) → the existing 5-min dedup
  (`engine.go:2706`) drops immediate duplicates.
- **Durable:** the **execution-status guard** in D.5 step 1 makes resume exactly-once across a restart
  (when a wake arrives hours later or twice around a reboot) — the dedup map doesn't need to survive.

### D.7 Filter precision (E6)
- **Preferred:** the bridge arrival event indexes the discriminator (e.g. `messageId`) as a **topic** — the
  operator's event check fires *only* on the matching log; no post-match needed.
- **Fallback:** if the discriminator is in event *data* (not indexed), the operator fires on every arrival
  event for that address/topic; `resumeWaitingExecution` **post-matches** the bound value against the event
  data and no-ops on mismatch. The `Await.Config.filter` records which mode applies.

### D.8 Deregistration
On resolve (resume success/fail, timeout, or cancel), the engine sends a `Delete` scoped by
`(task_id, execution_id)` to operators covering B, and deletes `itrigger:`/`ckpt:` (Appendix C.4). Boot GC
(C.5) is the backstop if a deregister is missed.

### D.9 What this satisfies
- **E3 (restart):** boot `itrigger:` scan → rebuild the internal-trigger mirror → the sync loop re-pushes it
  to operators covering B on (re)connect. No in-memory state needs to survive.
- **E6 (correct filter):** topic-indexed precision at the operator, or aggregator post-match (D.7).
- **E7 (concurrent isolated):** distinct `(task_id, execution_id)` checks + distinct filters; each fire
  carries its own `execution_id` → resumes exactly its own execution.
- **Open risk — coverage drift:** chain B is validated as covered at create time (O7/E11), but the only
  covering operator could drop *while* a wait is pending. Behavior then (re-route on reconnect? alert?) is
  unresolved and called out in the main Open-questions list.
