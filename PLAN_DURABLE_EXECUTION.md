# PLAN — Durable Execution Engine

Status: **design, not started.** Supersedes the bolt-on approach in `PLAN_CROSS_CHAIN_SEQUENCING.md`
(retained for its test scenarios and the file:line reference map). This is the engine-level plan; cross-chain
and human-approval are the first **features** that ride it.

## Intent

Reshape the task executor into a **durable execution engine**: an execution is a *persisted state machine*,
and the executor is a re-entrant function that advances it. This makes any step able to **suspend** (wait for
something) and the whole execution able to **resume** later or **recover** after a crash — uniformly, for
every workflow, not as a special case.

Driven by two concrete near-term features that the current synchronous, single-pass executor cannot
support:

- **Cross-chain sequencing** — initiate a bridge on chain A, wait for arrival on chain B (minutes–hours),
  continue on B. Wake source: a **chain event** (operator-watched).
- **Human-approval gates** — a money-moving `contractWrite` must wait for a human green-light (e.g. a
  Telegram approve/reject) before sending. Wake source: an **external signal** (gateway-received).

Both are the same shape: *run some steps → suspend on a condition → resume on a signal → continue.* Build the
engine once; both features become thin `Suspendable` steps on top.

## Why a homegrown engine (not Temporal/Cadence)

We use neither today and adopt neither. They're the reference architectures for this pattern; the wrong fit
here because:

1. **Replay is wrong for irreversible on-chain steps.** Temporal/Cadence re-run workflow code from event
   history and require *deterministic* code. You cannot replay a sent UserOp — re-running a completed
   `contractWrite` double-spends. Our engine must be **checkpoint-based**: persist each step's *result*,
   advance from the snapshot, and **never re-run a completed side-effecting step.** (This also *frees* node
   code from any determinism constraint.)
2. **Heavy separate infra** vs. our existing BadgerDB + apqueue.
3. **Deep integration** — execution is already fused with the gateway, operators, chain-watching, ERC-4337
   wallets, and fees. A focused engine on the existing stack beats bending an external one.

## The model

```
Execution = persisted state machine:
  { step_outputs: map<nodeId, result>,    // durable; the resumable state
    completed: set<nodeId>,
    status: RUNNING | WAITING | SUCCESS | FAILED,
    pending_signal: WakeSubscription?,     // set while WAITING
    … identity, fees, logs … }

Executor = advance(executionId, signal?):
  1. load state
  2. if signal: apply it (the wake event/approval becomes the suspended step's output)
  3. run every ready, not-yet-completed step (predecessors all completed)
       - persist each step's result as it finishes   ← checkpoint-forward
       - a step may return Continue | Suspend(wake) | Fail
  4. on Suspend → persist WAITING + register the wake subscription; return
     on all-done → SUCCESS;  on fatal → FAILED
  Re-entrant by construction: a fresh run is advance() from empty; a resume is advance() with a signal;
  a crash-recovery is advance() over whatever was persisted.
```

The 10 existing node runners (ContractWrite, ContractRead, ETHTransfer, REST, GraphQL, CustomCode, Branch,
Filter, Loop, Balance) are **reused as step bodies** — we replace the orchestration shell, not the steps.

## The signal model (the generalization)

A `Suspendable` step suspends with a **WakeSubscription**; a matching **Signal** delivered later advances the
execution. Three sources, one intake:

| Source | Detected by | Subscription | Signal example |
|---|---|---|---|
| **Chain event** | operator (chain watch) | event filter on chain B | bridge arrival log |
| **External signal** | gateway (inbound API/webhook) | `await signal S for exec E from authorized party` | Telegram approve/reject |
| **Timer** | scheduler (due-time sweep) | `wake at T` | delay elapse, approval timeout |

All converge on `advance(executionId, signal)`. The chain-event path reuses the operator trigger machinery;
the external-signal path is a new authenticated gateway endpoint; the timer path is a due-time sweep over
persisted subscriptions. **Idempotency** is uniform: a signal only advances an execution that is still
`WAITING` on a *matching* subscription — durable exactly-once across restarts, independent of any in-memory
dedup window.

## Suspendable steps — the interface

```go
type StepResult interface{}  // Continue(output) | Suspend(WakeSubscription) | Fail(err)
// A Suspendable step computes its wake condition from vars and yields Suspend(...).
// On resume, the delivered signal becomes the step's output; the step is NOT re-run.
```

The first two implementations:

- **`Await` (cross-chain).** `Config` embeds an event-watch on chain B (chain B = `event.chain_id`); on
  resume the matched log is its output (`{{await.data.amount}}`). Wake source: chain event.
- **`HumanApproval`.** `Config { prompt, approvers, channel (telegram|api), timeout_seconds, on_timeout }`.
  Suspends with an external-signal subscription scoped to the execution and the authorized approver(s); the
  gateway's approve/reject endpoint (hit by the Telegram bot callback or an API client) delivers the signal;
  the decision becomes the step's output, and a downstream `Branch` routes on approve/reject. Timeout →
  auto-reject (or escalate). **Security:** the signal must be authenticated and authorized against
  `Config.approvers` — a money-moving gate is only as safe as who can sign the green-light.

Future steps reuse the same interface for free: `Delay`, `WaitForWebhook`, retry/backoff wrappers, SLAs.

## Storage / execution-record schema (clean redesign)

Relaxing "existing executions reproduce identically" lets us redesign the execution record around the state
machine instead of bolting fields on, and **wipe old executions on cutover** (the chain-decoupling playbook:
a boot migration clears the old shape; tasks re-run fresh). Core persisted shape (chain-agnostic keys,
additive to task storage):

```
exec:<execId>            execution state machine (status, completed set, pending subscription, fees, logs)
exec_out:<execId>:<nodeId>   per-step result (checkpoint-forward; the resumable state)
wake:<execId>            the active WakeSubscription while WAITING (scanned on boot to re-arm)
```

`wake:` is the durable signal registry: boot re-arms chain-event subscriptions to operators, reloads timer
due-times, and GCs subscriptions whose execution is no longer WAITING.

## Triggers unified

The entry trigger becomes **the first condition in the graph** — there is no separate "task trigger vs.
mid-graph wait" concept. An entry chain-event/cron *creates and advances* an execution; an `Await`/`Approval`
*resumes and advances* one. All watches are execution-scoped from the start, which removes the
`task_id`-vs-`execution_id` duality the bolt-on had to thread through the sync protocol.

## Landing — 3 shippable steps

1. **Durable executor core.** Per-step persisted state, `advance()` resume-from-state, the `Suspendable`
   interface, node runners reused as step bodies. *Outcome: the engine is crash-recoverable for every
   workflow* (independent of any feature). Built and tested with a synthetic signal (no triggers/UI yet).
2. **Signal intake + unified triggers.** The three wake sources (operator chain-event, gateway
   external-signal endpoint, timer sweep) → one `advance(executionId, signal)`; entry triggers folded into
   the condition model. *Outcome: `Await` (chain) and `HumanApproval` (Telegram) both work end-to-end.*
3. **Stateless `advance()` shell + retries.** Consolidate orchestration into the re-entrant function; the
   queue carries `advance` jobs (triggers, signals, timers, crash-sweep, retry/backoff). *Outcome: the final
   durable shape — long-running, failing, resuming work handled uniformly.*

## Outcomes & test scenarios

Carry forward the cross-chain outcomes/scenarios (`PLAN_CROSS_CHAIN_SEQUENCING.md` Appendix B: O1–O10,
E1–E15) — they hold for the chain-event feature on this engine — and **add** the approval + crash-recovery
set:

- **O-A1 — Approval gate.** A `contractWrite` preceded by `HumanApproval` does **not** send until an
  authorized approve signal arrives; reject → the write never runs; the workflow takes the reject path.
- **O-A2 — Authorization.** A signal from a non-authorized party is rejected; only `Config.approvers` can
  decide. Replayed/duplicate approvals advance once.
- **O-A3 — Approval timeout.** No decision before `timeout_seconds` → auto-reject (or escalate); the gate is
  released; a late approval no-ops.
- **O-C1 — Universal crash recovery.** *Any* workflow (not just suspendable ones) killed mid-execution
  resumes from the last persisted step on restart, re-running only incomplete steps — completed
  side-effecting steps never re-fire.
- **O-C2 — Timer wake.** A `Delay`/approval-timeout fires from the due-time sweep after a restart.

E2E layering unchanged: deterministic Go integration (signals injected) for the bulk; one testnet SDK e2e for
the real bridge; a gateway-endpoint integration test for the Telegram approve/reject round-trip.

## Open questions / risks

- **Approval security model** — authentication of the approve/reject signal, the `approvers` allow-list, and
  binding a Telegram identity to an authorized party. This is the highest-stakes new surface (it gates money
  movement) and needs its own mini-design before P-step 2.
- **Migration** — relaxed same-results ⇒ wipe in-flight/old executions at cutover. Confirm no live executions
  must survive the switch (or provide a drain window).
- **Crash-recovery re-arm correctness** — the boot `wake:` re-arm + GC must be exactly-once against
  operators (chain) and not double-fire timers.
- **Per-step persistence cost** — a small write per step for all workflows; negligible for typical node
  counts (loops iterate in-memory), but worth a benchmark on the largest real graphs.
- **Scope discipline** — adopt the Level-3 *model* but land incrementally (the 3 steps); do not big-bang a
  from-scratch rewrite. Node runners are reused, not rewritten.

## Relationship to prior docs

- `PLAN_CHAIN_DECOUPLING.md` — done/shipped; the per-part chain model this builds on.
- `PLAN_CROSS_CHAIN_SEQUENCING.md` — superseded as the *engine* approach (its Level-1 bolt-on becomes native
  here); retained for Appendix B (test scenarios) and the reference map, both still valid.
