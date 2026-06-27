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
