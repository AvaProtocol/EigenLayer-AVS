# SDK Hand-off — Durable Execution (Await + Signal)

**Audience:** `ava-sdk-js` maintainers. **Status:** as-built, merged to `staging` (PRs #643–#647).
**Source of truth for shapes:** [`api/openapi.yaml`](api/openapi.yaml) on `staging` — regenerate from it, don't hand-transcribe.
**Why (design rationale):** [`PLAN_DURABLE_EXECUTION.md`](PLAN_DURABLE_EXECUTION.md). That doc is design intent and predates the final API; **this doc is what actually shipped** — where they disagree, this wins.

---

## What shipped (one paragraph)

A workflow can now **pause mid-execution** and resume later, exactly-once, surviving aggregator restarts. The pause point is a new node type, **`await`**, with two mutually-exclusive flavors:

- **External-signal (human approval):** pauses until someone POSTs an approve/reject (e.g. a Telegram bot or an API client). The decision becomes the node's output.
- **Chain-event (cross-chain):** pauses until an operator observes an on-chain event (e.g. a bridge arrival on another chain). The matched log becomes the node's output.

While paused, the execution has status **`EXECUTION_STATUS_WAITING`** (non-terminal). It resumes via `POST /executions/{id}:signal` (external-signal) or automatically when the operator sees the chain event.

---

## Step 0 — regenerate types (required, mechanical)

The SDK already pulls types from this repo's `staging` OpenAPI:

```bash
yarn openapi-download   # curl staging api/openapi.yaml -> packages/types/openapi/openapi.yaml
yarn types-gen          # openapi-typescript -> packages/types/src/openapi.gen.ts
```

After regen, `openapi.gen.ts` contains: `AwaitNode` / `AwaitNodeConfig` (incl. `chainEvent`), `"await"` in the Node discriminator + `NodeType` enum, `SignalExecutionRequest`, the `signalExecution` operation, and `EXECUTION_STATUS_WAITING`. Everything below is then type-checked, not stringly-typed.

---

## The `await` node contract

`AwaitNodeConfig` (camelCase, as the SDK sees it):

| field | type | flavor | notes |
|---|---|---|---|
| `channel` | string | external-signal | `"telegram"` \| `"api"` |
| `approvers` | string[] | external-signal | authorized parties; empty ⇒ the workflow owner |
| `prompt` | string | external-signal | shown to the approver |
| `chainEvent` | `EventTriggerConfig` | chain-event | the on-chain event to wait for (same shape as an Event trigger's config: `chainId` + `queries[]`) |
| `timeoutSeconds` | int64 | both | safety bound; `0` ⇒ server default (24h). **Never unbounded.** |

**Mutual exclusivity (enforced server-side):** set **either** `channel` (+ optional `approvers`/`prompt`) **or** `chainEvent` — never both. A config with both is **rejected** (the node fails; it does not suspend). A config with neither is rejected at create. The OpenAPI schema can't express the XOR, so the SDK should guard it in the builder (below) for a good error before the round-trip.

Node discriminator value is `"await"` (like `"contractWrite"`, `"ethTransfer"`).

---

## The signal endpoint (resumes an external-signal await)

```
POST /executions/{id}:signal?workflowId={workflowId}
Authorization: Bearer <JWT>          # standard auth; caller must OWN the workflow
Content-Type: application/json

{ "decision": "approve" | "reject", "payload": { ... }?  }
```

- `id` (path) = the execution id; `workflowId` (query, **required**) = its workflow (executions are workflow-scoped, same as `getExecution`).
- `decision` is **required**, enum `approve | reject`. `payload` is optional structured data delivered as the await node's output (readable downstream as `{{<awaitNodeName>.data...}}`).
- **Returns** the `Execution` — resumed to a terminal status, or still `WAITING` if the workflow hit a *second* await.
- **Errors:** `400` (bad decision / no matching pending wait / already timed out — mapped from `InvalidArgument`/`FailedPrecondition`), `401` (auth), `404` (workflow not owned / not found).

Chain-event awaits are **not** resumed through this endpoint — an operator fires them automatically; the SDK only needs to poll/stream the execution to observe the resume.

---

## The `waiting` lifecycle (important for polling/streaming)

Over REST the status is the lowercase wire value **`"waiting"`** (the generated `ExecutionStatus` union member; proto enum `EXECUTION_STATUS_WAITING`). It is **non-terminal**. Any SDK helper that "waits for an execution to finish" must treat `"waiting"` as *keep waiting*, not done:

- `executions.get` / the SSE stream report `"waiting"` while paused, then a terminal status (`"success"` / `"failed"` / `"error"`) once resumed (or `"failed"` with "await timed out…" if the timeout sweep fires).
- A `waitForExecution`-style util that currently stops on "any status set" or "not `pending`" will return early on `"waiting"` — update its terminal-status predicate to exclude `"waiting"`.

> Note: `"waiting"` was added to the OpenAPI `ExecutionStatus` enum and the REST wire mapping together with this hand-off — before that fix a paused execution surfaced as `"pending"` over REST. Make sure your `openapi-download` is current.

---

## Ergonomic wrappers to add (recommended, matches existing DX)

**1. `Nodes.await(...)` builder** — `packages/sdk-js/src/v4/builders/nodes.ts`, alongside `ethTransfer`/`contractWrite`:

```ts
await(opts: {
  id: string;
  name: string;
  timeoutSeconds?: number;
} & (
  | { channel: "telegram" | "api"; approvers?: string[]; prompt?: string }   // external-signal
  | { chainEvent: v4.EventTriggerConfig }                                      // chain-event
)): v4.Node {
  // guard the XOR for a friendly error before the round-trip
  return {
    id: opts.id,
    name: opts.name,
    type: "await",
    config: { ...opts, id: undefined, name: undefined },  // pass through the flavor fields + timeoutSeconds
  } as v4.Node;
},
```

**2. `executions.signal(...)` method** — `packages/sdk-js/src/v4/resources/executions.ts` (mirror `workflows.resume` / the `getExecution` query pattern):

```ts
/** POST /executions/{id}:signal — resume a WAITING execution (human approval). */
signal(
  id: string,
  params: { workflowId: string; decision: "approve" | "reject"; payload?: Record<string, unknown> },
): Promise<v4.Execution> {
  return this.transport.request<v4.Execution>({
    path: `/executions/${encodeURIComponent(id)}:signal`,
    method: "POST",
    query: { workflowId: params.workflowId },
    body: { decision: params.decision, payload: params.payload },
  });
}
```

**3. WAITING-aware wait/stream** — update the terminal-status predicate in any execution-completion helper to exclude `"waiting"`.

---

## NOT built yet (don't assume these exist)

These appear in `PLAN_DURABLE_EXECUTION.md` as design/intent but are **not in v1** — building against them will break:

- **Telegram binding & bot→gateway trusted channel.** v1 authorizes the signal endpoint by **workflow owner** (the JWT). There is no `telegram_user_id ↔ owner` binding check and no bot→gateway shared-secret transport yet — that's studio/bot-side work. The `approvers` field is carried but not yet enforced as a delegated-approver check.
- **`on_timeout` fallback edge.** Timeout ⇒ the execution is failed (`FAILED`, "await timed out…"). There is no "take a fallback edge on timeout" routing.
- **Per-workflow value cap / recipient allow-list** (the "envelope tightening") and the **high-value fresh-EOA-signature tier** — design recommendations, not implemented.
- A separate `HumanApproval` node — there isn't one; it's the external-signal flavor of `await`.

---

## Quick reference

- Node contract & signal op: `api/openapi.yaml` (`AwaitNodeConfig`, `SignalExecutionRequest`, `/executions/{id}:signal`).
- Behavior proven by: `core/taskengine/vm_resume_test.go`, `core/taskengine/durable_test.go`, `aggregator/rest/mapping/node_test.go`.
- Design/why: `PLAN_DURABLE_EXECUTION.md`. Cross-chain specifics: `PLAN_CROSS_CHAIN_SEQUENCING.md`.
