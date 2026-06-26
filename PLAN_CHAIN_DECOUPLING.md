# PLAN: Decouple chainId from the workflow — per-trigger / per-node chains

**Owner:** AVS backend (this repo leads). **Status:** proposal / pre-implementation.
**Date:** 2026-06-26.

Today a workflow (AVS task) is bound to **one** `chainId`, fixed at creation. The goal is to make
chainId a property of the **parts that need it** — an event trigger watching a contract on chain X, a
contract-write/transfer/read acting on chain Y — so that:

- a workflow is no longer a single-chain object, and
- a workflow can eventually **bridge across chains mid-run** (act on B after bridging from A).

We lead this **bottom-up from the AVS backend**. The studio client
(the `studio` repo's `PLAN_WALLET_CHAIN_DECOUPLING.md`, Phase 3/4) and the v4 SDK
(`ava-sdk-js`) follow the model defined here — they must not ship a per-node chain the backend would
flatten.

---

## TL;DR — per-node chains already work; the task-level chain is being removed

The proto, the REST mapping for nodes, the v4 SDK, and the **deployed-execution** path already support
per-node chains. The studio plan's premise that *"triggers/nodes carry no chainId of their own"* and that
Phase 3 is *"blocked on an AVS model change"* is **out of date**. Two tiers of work remain:
**(1) narrow plumbing** (G1–G4) to make per-node/trigger chains flow on every path; and **(2) the decided
structural change** (G5) — make `chain_id` **required** on every chain-aware part and **remove the
task-level chain entirely** (no `Task.chain_id`, chain-agnostic task storage). G5 is a real refactor
(~30 readers + storage-key change + wipe migration), shipped after the plumbing.

### What already works (verified)

| Layer | Status | Evidence |
|---|---|---|
| Proto: `Task.chain_id` (task-level default) | ✅ | `protobuf/avs.proto:817`, `CreateTaskReq.chain_id` `:858` |
| Proto: per-trigger/per-node `chain_id`, `0 = inherit task` | ✅ | EventTrigger `:222`, BlockTrigger `:152`, ContractWrite `:410`, ContractRead `:455`, ETHTransfer `:384` |
| REST → proto: per-**node** chainId carried (protojson round-trip) | ✅ | `aggregator/rest/mapping/node.go:203` (`jsonRetargetProto` → `protojson.Unmarshal`); proto json name `chainId` maps onto `Config.ChainId` automatically |
| REST → proto: **BlockTrigger** config chainId | ✅ | `aggregator/rest/mapping/trigger.go:119-120` |
| Deployed execution honors per-node chainId | ✅ | `core/taskengine/vm.go:1475,1518,1743` read `node.Config.GetChainId()` → `resolveSmartWalletForNode` (`vm.go:347-361`) |
| Multi-chain RPC/smart-wallet pool in the engine | ✅ | `Engine.chainConfigs` + `ResolveSmartWalletConfig(chainID)` `core/taskengine/engine.go:426-433`; `chainConfigResolver` wired to VMs at simulation/executor/run-node sites |
| Inherit logic: node-chain → task-chain → default | ✅ | `resolveSmartWalletForNode` `vm.go:347-361`, tested `vm_resolve_smart_wallet_test.go` (regression for Sentry EIGENLAYER-AVS-1N/1M) |
| Gateway mode requires explicit task `chain_id` | ✅ | `engine.go:1637-1660` |
| v4 SDK builders + types expose per-node/per-trigger `chainId` | ✅ | `ava-sdk-js` `builders/nodes.ts`, `builders/triggers.ts`; `packages/types/src/openapi.gen.ts` (`*NodeConfig.chainId`, `*TriggerConfig.chainId`) |
| OpenAPI spec documents per-node/trigger chainId + inherit | ✅ | `api/openapi.yaml:402,492,592,618,634` |

### The actual gaps (Phases 1–2 are plumbing; G5/Phase 3 is the decided model change)

| # | Gap | Location | Effect today |
|---|---|---|---|
| **G1** | ~~EventTrigger.Config.chainId dropped at REST→proto.~~ **FIXED** — `openAPIEventToProto`/`protoEventToOpenAPI` now map `chainId` both directions (mirrors BlockTrigger). Test: `trigger_test.go` event case. | `aggregator/rest/mapping/trigger.go` | Event-trigger chain now reaches the proto. |
| **G2** | ~~Operator monitors triggers by task chain, not the trigger's own chain.~~ **FIXED** — the aggregator now dispatches `TaskMetadata.ChainId` as the trigger's monitoring chain (`triggerMonitoringChainID`, event/block config chain → task-chain fallback), and computes operator coverage + orphan-scan from it; the operator routes on that value. Test: `chain_per_part_test.go` (`TestTriggerMonitoringChainID`). | `core/taskengine/engine.go`, `operator/worker_loop.go` | An event trigger on chain X attached to a task whose default chain is Y is now watched on X. |
| **G3** | ~~`ExtractNodeConfiguration` omits `chainId`.~~ **FIXED** — emits `chainId` for contract/transfer nodes; `CreateNodeFromType` reads it back onto the proto; `requireChainIDFromConfig` accepts the camelCase key. Test: `chain_per_part_test.go`. | `core/taskengine/vm.go`, `run_node_immediately.go` | Simulate/preview/`runNodeImmediately`/loop-nested now honor the per-node chain. |
| **G4** | ~~Unknown explicit chain silently fell back / wasn't validated at create.~~ **FIXED** — `resolveSmartWalletForNode` errors on an explicit unresolvable chain (`vm.go:347`), and `validateExplicitPartChains` rejects an unconfigured explicit part chain at create (gateway mode). Tests: `vm_resolve_smart_wallet_test.go`, `chain_per_part_test.go`. `0`-inherit still allowed until G5. | `core/taskengine/engine.go`, `vm.go:347` | Explicit wrong-chain execution closed at both create and run. |
| **G5** | **Make `chain_id` required per chain-aware part AND remove the task-level chain entirely** (the decided model). Per the resolution rule, `chain_id` becomes REQUIRED (`>0`, configured) on event/block triggers and contract/transfer nodes, all modes; `0` rejected. `Task.chain_id` / `CreateTaskReq.chain_id` are **removed** and task **storage becomes chain-agnostic** (chain drops out of `t:`/`u:`/execution keys — Decision 1). Requires: drop "0 = inherit" from proto + OpenAPI; remove audit-class-**A** fallback sites; fix the Loop runner; update ~30 `task.ChainId` readers; **wipe-all migration** (storage schema changed; legacy parts unfixable). | proto `avs.proto` (remove fields 16/10 + the 151/222/383/410/455 comments), `api/openapi.yaml`, `storage/schema/workflow.go` + `core/taskengine/schema.go` (key builders), `vm.go:347`/`:1583`, `loop_helpers.go:205`, `executor.go`, `engine.go` (~30 readers), `ChainRegistry.*`, `aggregator/key.go`, `migrations/` | Today chain is baked into the task (field + storage key) and a `0`-chain part silently runs on it. After G5, chain lives **only** on the parts — explicit-or-error — and a task belongs to no chain at all; per-chain views derive from the parts. Only the (B) chain-agnostic operator-routing sentinel for cron/manual survives. |

G1–G4 turn the existing infrastructure into working per-node/per-trigger multi-chain execution. **G5 is
the model change** the team decided on (2026-06-26): make `chain_id` required on chain-aware parts and
delete inheritance. No backfill — a one-shot delete migration clears pre-existing `0`-chain tasks at boot
before the strict code runs; owners re-create them with explicit chains.

---

## Backend answers to studio's Phase-3 gate questions

(Responses to `studio/PLAN_WALLET_CHAIN_DECOUPLING.md` → "Open questions to confirm with the AVS backend
before studio Phase 3". All verified in code.)

1. **Auth granularity — one authKey authorizes the whole task; single-sign, no per-chain user signature.**
   On-chain execution is signed by the **backend controller key** (`pkg/erc4337/preset/builder.go:1064`,
   `smartWalletConfig.ControllerPrivateKey`), resolved per chain — **not** by the user. The user authKey/JWT
   (`aud` = chain) is checked **only at the create-API boundary** (`aggregator/rest/middleware/jwt.go`);
   the executor never re-validates it. The authKey's chain is just which gateway endpoint the create
   request used — it does **not** have to match the task's per-node chains. So one create-time authKey
   authorizes a task whose nodes/triggers span several chains. Per-step user auth is only the bridge case
   (Phase 4). **Studio's read confirmed.**
2. **Create-path acceptance — explicit-per-part required; the task-level chain field is removed.**
   Per-node/trigger chain flows through the protojson round-trip already. Under the decided model (G5),
   the create path **requires** every chain-aware trigger/node to carry an explicit `chain_id` in the
   configured set, and **rejects** `chain_id == 0` or an unconfigured chain with `InvalidArgument`. There
   is **no workflow-level chain at all** — `Task.chain_id` / `CreateTaskReq.chain_id` are deleted and task
   storage is chain-agnostic (Decision 1). So: studio sends an explicit chain on every event/block trigger
   and contract/transfer node, and **stops sending / reading any workflow-level `chainId`**. A request that
   omits a chain-aware part's chain is rejected, not flattened.
3. **Runner provisioning/funding — automatic deploy, per-chain paymaster, no pre-check.** The wallet
   deploys counterfactually on first UserOp (initCode injected when code is empty,
   `pkg/erc4337/preset/builder.go:1331`). Paymaster + bundler are the node-chain's via
   `ResolveSmartWalletConfig(nodeChainID)`. One CREATE2 address is the runner on all 4 core chains
   (Decision 2). The backend does **not** pre-check balance; it surfaces `AA21` (prefund) at execution.
   **Studio must surface per-node-chain readiness** ("needs gas / paymaster on chain Y"); the backend
   won't block create.
4. **Cleared chain set — the 4 core chains, enforced.** The usable set = the aggregator's configured
   `chainConfigs` (bundler + paymaster + controller present). Per `docs/Contract.md` that's **Ethereum,
   Base, Sepolia, Base Sepolia**. The per-node chain selector **must** restrict to those four — once G5
   lands, a node naming any other chain is rejected at create. Soneium/Minato out of scope.
5. **Landing order — align studio with G5, not just G1/G3.** Backend G1 (event-trigger mapping) + G3
   (extract config) make per-part chains flow; G5 makes them required and removes task-level inheritance.
   Studio should send **explicit per-part chains from day one** (works after G1+G3) and **never** depend
   on a workflow-level `chainId` cascading — that contract is exactly what G5 enforces. Net: studio
   Phase 3 ships after backend G1+G3; the data model it adopts must match G5 (per-part required, no
   workflow-level inheritance).

---

## Design model (the contract every layer follows)

### chainId is a property of *conditions/actions*, not of the task

A task has **exactly one trigger** (`Task.trigger` is a single field, not `repeated`) and N nodes.
chainId is intrinsic to the parts that touch a chain, and irrelevant to the rest:

| Part | What it does | chainId relevant? |
|---|---|---|
| Manual trigger | fired by API call | no |
| Cron / FixedTime trigger | watches wall-clock time | no |
| **Block trigger** | watches block height on a chain | **yes** |
| **Event trigger** | watches logs on a chain | **yes** |
| **ContractWrite / ContractRead / ETHTransfer node** | acts on a chain | **yes** |
| CustomCode / REST / GraphQL / Branch / Filter / Loop node | pure compute / off-chain | no |

So a workflow's multi-chain behavior has **two independent axes**, and the operator only owns the first:

1. **The trigger** decides which chain (or none) to *watch* for the firing condition. The operator
   monitors exactly this. Because there is one trigger per task, the operator never reconciles multiple
   chains — it routes the single trigger by *the trigger's own chain* (event/block) or treats it as
   chain-agnostic (cron/manual). There is no `Task.chain_id` to consult — the event/block trigger carries
   its own required chain, and the operator routes by it.
2. **Each node** decides which chain to *act on* during execution. This is the executor/aggregator's
   concern (already wired via `resolveSmartWalletForNode`), invisible to the operator.

Example: watch a price event on Ethereum → execute a swap on Base. The operator only ever opens the
Ethereum subscription; Base enters the picture solely at node execution. This is exactly why G2's fix is
"route by the trigger's chain," not "thread the task chain through."

### Resolution rule — `chain_id` is REQUIRED on chain-aware parts; there is no inheritance

**Decided (2026-06-26):** chain lives **only** on the part that touches a chain. `0` is the protobuf
zero value — it means "unset", never a real chain — so on any chain-aware trigger/node it is **invalid**
and rejected. There is **no fallback to the task chain, and no aggregator-default fallback, in any mode
(gateway or single-chain).**

```
chain-aware trigger/node Config.chain_id:
   > 0 and in the configured set   → use it
   anything else (0 / unconfigured) → ERROR (reject at create AND at execution)
```

- Applies to: event & block triggers; contractRead / contractWrite / ethTransfer nodes; the Loop runner
  when it wraps one of those.
- Does **not** apply to parts that never touch a chain: manual/cron triggers and off-chain nodes
  (customCode, REST, GraphQL, branch, filter, loop-over-data). Those carry no chain — a task built
  only from them is genuinely chain-agnostic.
- **Single-chain dev mode is not exempt** (per decision): even with one configured chain, a chain-aware
  node must name its `chain_id` explicitly and it must equal the configured chain. No "the one chain
  stands in for 0" shortcut — we want zero `0`-special-cases.

Why no inheritance: `0`-means-inherit is exactly the coupling that produced the wrong-chain paymaster
failures (Sentry EIGENLAYER-AVS-1N/1M) and the silent-wrong-chain footgun (G4). Making `chain_id`
required everywhere collapses ~6 "magic fallback to a default chain" sites (see audit class **A** below)
into one uniform "explicit-or-error" rule — the pattern the strict guards already use
(`requireChainIDFromConfig`, worker-routed readers/token services).

> Audit context: `chain_id == 0` is special-cased in ~17 places, in three roles. **(A)** magic fallback
> to a real default chain (`resolveSmartWalletForNode` node→task→`chains[0]`, `executor.go:256`,
> `ChainRegistry.ResolveChainID/GetWorker/GetChainConfig`, `operator.triggersForChain`,
> `aggregator/key.go`, and `chainScopedTaskKey`) — **all removed**; per Decision 1 the task storage key
> drops the chain entirely (chain-agnostic addressing), so `chainScopedTaskKey` goes away rather than
> losing only its `0`-default. **(B)** legitimate "chain-agnostic / not applicable" sentinel
> (`operatorsCoveringChain(0)`, cron/manual tasks, `chainHasOperator[0]`) — **kept**, that's how a
> chain-free task routes to any operator; **(C)** guards that already hard-error on 0 — **the target
> pattern**.

### Decision 1 — Remove the task-level chain entirely; task storage is chain-agnostic

**Decided (2026-06-26):** there is **no chain tied to a task** — not for execution, not for storage.
`Task.chain_id` and `CreateTaskReq.chain_id` are **removed**. A task is addressed by id (+ owner index),
not by chain. Chain lives **only** on the chain-aware parts (G5); any "by chain" view is *derived* from
the trigger/node chains, never stored on the task. This is the full structural decoupling — a workflow
does not belong to a chain in any sense.

What this changes:

- **Storage keys drop the chain.** `t:<chain>:<status>:<id>` → `t:<status>:<id>`; the user index
  `u:<chain>:<owner>:<wallet>:<id>` → `u:<owner>:<wallet>:<id>`; execution keys
  (`ChainTaskExecutionKey`) drop the chain too (an execution can span chains, so keying it by one is the
  same coupling). **Breaking storage-key change** — `make storage-check` will flag it; lands with the
  migration below.
- **Proto.** Remove `Task.chain_id` (16) and `CreateTaskReq.chain_id` (10); reserve the field numbers.
  Update mappers (`aggregator/rest/mapping/workflow.go`), drop the REST handler's authKey-aud backfill
  (`handlers_workflows.go:56`), run `make protoc-gen`. The JWT `aud` chain stays — it's **API auth scope**,
  no longer a task field.
- **Listing/CRUD goes chain-agnostic.** `get(id)` / `cancel` / `pause` key on id alone; `list(owner)`
  returns all of an owner's tasks. A "tasks on chain Y" filter is computed server-side from the parts'
  chains, not a key prefix. Studio's per-chain client (`getClientWithToken(chainId)`) moves to a
  chain-agnostic list; the dashboard (their #4) shows all chains with an optional derived per-chain filter.
- **Execution has no task-default chain.** `NewExecutor`/`NewVMWithData` no longer pass
  `ResolveSmartWalletConfig(task.ChainId)`; the VM default smart-wallet config is **nil**, so every
  chain-aware node must resolve its *own* chain or error (exactly G5's rule). The ~30 `task.ChainId`
  readers (engine.go, vm.go, summarizer, executor) are updated to derive chain from the relevant part:
  operator coverage at create → the **trigger's** chain; summarizer enrichment → the node's chain.
- **Wallet-salt keys stay per-chain (separate concern).** `wsalt:%d:…` and `GetWallet(db, chainID, …)`
  (`vm.go:1583`) are about wallet derivation, not task identity. The runner address is chain-invariant
  across the 4 core chains (Decision 2), so these can pass *any* configured chain (e.g. the node's). Not
  part of this decoupling — leave the wallet keys as-is for now; flag for a follow-up if desired.

This dissolves studio's "which chain is `CreateTaskReq.chain_id`?" loose end entirely: **there is no such
field.** Studio sends explicit per-part chains and nothing else; the create endpoint's authKey is API auth
only. Their dashboard lists across chains and derives any per-chain view from the parts.

**Migration: wipe all tasks (decided 2026-06-26).** Two facts make a selective migration pointless:
(1) the storage-key schema changes (`t:<chain>:…` → `t:…`), so *every* existing row is on the old layout;
(2) legacy tasks carry `chain_id == 0` on their parts and can't be made executable by re-keying — they'd
fail G5's required-chain rule anyway. Re-keying buys nothing. So the one-shot migration **deletes all
existing tasks** (and their user-index / execution rows) at boot — taking a full DB backup, recording
completion so it runs once, *before* the engine serves — and owners re-create with explicit per-part
chains. The deployed set is tiny and we've accepted breaking it; cleanliness over preservation.

Follows the established delete-task pattern (`DeleteAutoDisabledInvalidTasks`,
`docs/historical-migrations/2026-completed/`): constant-memory `IterateKeysOnly` scan of the old `t:`
prefix, delete every key each task owns. Undecodable rows are deleted too (the whole old namespace is
being retired). *(If preserving chain-agnostic cron/off-chain-only tasks later matters, those — and only
those — could be re-keyed instead of deleted, since they have no chain-aware part to fix. Out of scope
unless asked.)*

### Decision 2 — One `smart_wallet_address` (runner) stays task-level; chain varies per step

The studio plan assumes per-node chains require *"multiple runners (a smart wallet per chain)."* **They
do not.** `model.SmartWallet` is keyed by `(owner, factory, salt)` with **no chainId**
(`model/user.go`); the CREATE2 address is deterministic in the **deployer** (Factory Proxy) and the
**wallet init-code hash** (which embeds the Wallet Implementation). So:

- The task carries **one** `smart_wallet_address`; the same address is the runner on every chain where
  those two inputs match.
- What is per-chain is **deployment + funding + paymaster**, which the engine already resolves per chain
  via `ResolveSmartWalletConfig(chainID)`.

**Audit result (`docs/Contract.md`, 2026-06-26) — scoped to the supported chains (Ethereum, Base,
Sepolia, Base Sepolia):**

| Input to the CREATE2 address | Value | Across the 4 supported chains |
|---|---|---|
| Factory **Proxy** (the deployer) | `0xB99BC2E399e06CddCF5E725c0ea341E8f0322834` | identical ✅ |
| Wallet **Implementation** (in wallet init code) | `0xf5d0c65516f0724242343c4eAA5D9de3ee4291fB` | identical ✅ |
| Factory **Implementation** | varies | irrelevant — the Proxy is the deployer; the impl runs via delegatecall in the proxy context |

**Decision 2 holds.** Same deployer + same wallet implementation ⇒ identical CREATE2 address across all
four supported chains. One runner address serves all of them; safe to ship per-node multi-chain now.

(Soneium/Minato are out of scope — not currently supported — so their differing wallet implementation is
not a concern for this work.)

### Decision 3 — `chain_id` required on every chain-aware part, all modes

Supersedes the old "gateway requires task chain_id" carve-out: the requirement is now uniform and lives
on the **part**, not the task. Gateway and single-chain mode behave identically — a chain-aware
trigger/node with `chain_id ≤ 0` or an unconfigured chain is rejected at create and at execution. The
existing gateway task-chain check (`engine.go:1637-1660`) is **deleted** along with `Task.chain_id`
(Decision 1) — there is no task chain left to validate.

---

## Implementation — bottom-up phases

Phases 1–2 are **pure plumbing on the existing model** (per-node/trigger chains already representable)
and unblock studio Phase 3. Phase 3 is the **decided model change** — make `chain_id` required, delete
inheritance (a one-shot delete migration clears stale `0`-chain tasks at boot; no backfill). Phase 4 is
the genuinely new protocol work (cross-chain sequencing) and maps to studio Phase 4.

### Phase 0 — Lock the contract & prove the gaps (no behavior change)

- Add a backend integration test that builds a task whose **node** chain ≠ **task** chain and asserts the
  deployed execution dials the node's chain (this should already pass — it pins the working path so the
  later fixes don't regress it).
- Add tests that assert G1/G3 currently drop the chain (red), to be flipped green by Phases 1–2.
- Audit `SmartWalletConfig.FactoryAddress` equality across all configured chains (Decision 2 risk).
- `make storage-check` baseline.

### Phase 1 — Close the ingestion + simulation gaps (G1, G3, G4) — **IMPLEMENTED**

1. **G1 — EventTrigger chain mapping. ✅** `openAPIEventToProto`/`protoEventToOpenAPI`
   (`aggregator/rest/mapping/trigger.go`) map `chainId` both directions, mirroring BlockTrigger. Covered
   by the `event` case in `trigger_test.go`.
2. **G3 — `ExtractNodeConfiguration` chain. ✅** Emits `chainId` for ContractWrite/ContractRead/ETHTransfer;
   `CreateNodeFromType` reads it back onto the proto Config; `requireChainIDFromConfig` now accepts the
   camelCase `chainId` key (node maps) alongside snake `chain_id` (trigger maps). Covered by
   `chain_per_part_test.go`.
3. **G4 — reject unknown explicit chains at create. ✅** `validateExplicitPartChains`
   (`core/taskengine/engine.go`), called in `CreateTask`, rejects a chain-aware trigger/node naming an
   unconfigured explicit chain with `InvalidArgument` (gateway mode; checks against `knownChainIDs()`,
   walks Loop runners). Pairs with the runtime guard in `resolveSmartWalletForNode`. `chain_id == 0`
   (inherit) is still allowed — G5 tightens that. Covered by `chain_per_part_test.go` +
   `vm_resolve_smart_wallet_test.go`.

**Outcome:** per-node and per-event-trigger chains are honored on **every** execution path *except* the
operator's event subscription (G2). Independent, low-risk, no storage change. `chain_id == 0` still
inherits the task chain (legacy path) — tightened in Phase 3. Build + targeted tests green.

### Phase 2 — Operator routes the trigger by the trigger's own chain (G2) — **IMPLEMENTED**

Done at the **aggregator** (single source of truth) rather than only the operator: a new
`triggerMonitoringChainID(trigger, fallback)` returns the event/block trigger's own configured chain
(falling back to the task chain when the trigger left it 0 — legacy, until G5). It's applied at all three
`TaskMetadata.ChainId` dispatch sites, the create-time coverage check (`operatorsCoveringChain`), and the
orphan-scan coverage map. The operator keeps routing on `TaskMetadata.GetChainId()` — now the trigger's
monitoring chain — and its variable was renamed `monitorChainID` for clarity. Because there is **one
trigger per task**, there's no union-of-chains problem. Cron/manual/fixed-time stay chain-agnostic (the
helper carries the fallback through; the operator's TimeTrigger ignores it).

**Outcome:** event triggers fire on their own chain. A workflow can now *watch* chain X and *act* on
chain Y within one task, with independent (non-sequential) steps. This fully satisfies studio Phase 3.
Build + targeted tests (`TestTriggerMonitoringChainID`, operator + engine coverage suites) green.

### Phase 3 — `chain_id` required per part; remove the task-level chain; chain-agnostic storage (G5)

The decided model change — the big structural one. Ships as one release with a **wipe-all migration** at
boot (the storage schema changes, so all old rows go; owners re-create with explicit per-part chains).
The migrator runs before the engine serves, so the new schema is clean from first request.

1. **Wipe-all migration.** Delete every task on the old `t:<chain>:…` schema (status rows, `u:` user
   index, execution rows). Pattern: `DeleteAutoDisabledInvalidTasks` (constant-memory `IterateKeysOnly`,
   full DB backup, recorded once) — but scanning the whole old `t:` namespace, not a subset. Register in
   `migrations/migrations.go`.
2. **Chain-agnostic storage.** Drop the chain from the key builders — `storage/schema/workflow.go` +
   `core/taskengine/schema.go` (`ChainWorkflowStorageKey`→`WorkflowStorageKey`, `ChainTaskUserKey`,
   `ChainTaskExecutionKey`). Update list/get/cancel/pause to key on id; the per-chain filter is derived
   server-side from the parts. `make storage-check` will flag the template change — that's expected, the
   migration covers it.
3. **Remove the task chain field.** Delete `Task.chain_id` (16) + `CreateTaskReq.chain_id` (10) from the
   proto (reserve the numbers), update mappers, drop the authKey-aud backfill (`handlers_workflows.go:56`),
   `make protoc-gen`. Update the ~30 `task.ChainId` readers to derive chain from the relevant part
   (operator coverage at create → trigger chain; summarizer → node chain; executor → nil VM default).
4. **Flip to explicit-or-error.** Make `resolveSmartWalletForNode` reject `chain_id <= 0` (no fallbacks);
   the create validator rejects `0`/unconfigured on chain-aware parts in **all** modes; remove the
   remaining audit-class-**A** sites (`executor.go`, `ChainRegistry.ResolveChainID/GetWorker/GetChainConfig`,
   `operator.triggersForChain`, `aggregator/key.go`).
5. **Fix the Loop runner.** `createNestedNodeFromLoop` (`loop_helpers.go:205`) must carry an explicit
   chain on the nested node — from the Loop runner's own config. (Once nodes require a chain, the inner
   node already has it; verify the nested-node path preserves it.)
6. **Drop the docs/comments.** Remove "0 = inherit task's chain_id" from `protobuf/avs.proto` (151/222/
   383/410/455) and the `ChainId` schema note in `api/openapi.yaml:117-124`.
7. **Keep the (B) sentinel.** Do **not** touch `operatorsCoveringChain(0)` / cron-manual chain-agnostic
   routing — a chain-free task legitimately has no chain.
8. **SDK + tests.** SDK always writes explicit per-part chains and stops sending a workflow-level chain;
   update tests that assumed `0`-inherit or a task chain.

**Outcome:** chain lives **only** on the parts — explicit-or-error — and a task belongs to no chain in
any sense (not execution, not storage, not listing). Per-chain views are derived. This is the full
structural decoupling.

### Phase 4 — True cross-chain sequencing (bridge mid-workflow)

This is the genuinely unbuilt protocol work (studio Phase 4). Per-node chains give *independent*
multi-chain steps; a real bridge needs **sequential cross-chain settlement**: act on B only after the
bridge from A confirms. Requires, at minimum:

- a node/primitive that submits a bridge and **waits for finality on the destination chain** before
  downstream nodes run;
- execution state that survives a cross-chain wait (re-entrant executor, not a single synchronous pass);
- per-step authKey/paymaster minting on the destination chain (the engine already resolves per-chain
  configs; the executor must request the right one per step — already true for per-node chains, but the
  **wait/resume** is new).

Scope this separately once Phases 1–3 land; do not block them on it.

---

## Coordination & sequencing

- **AVS backend (this repo): Phases 0→1→2→3** — self-contained. Phases 1–2 are non-breaking plumbing;
  Phase 3 (G5) is the breaking model change, shipped with a one-shot delete migration that clears stale
  `0`-chain tasks at boot (no backfill).
- **v4 SDK (`ava-sdk-js`):** already exposes the fields; add a **multi-chain template test** (per-part
  explicit chains) once Phase 1 lands, to lock the wire contract. Ensure it always writes explicit chains
  (G5 makes that mandatory). No type changes needed.
- **Studio:** its Phase 1/2 (identity/chain decoupling from the live wallet) are independent and can
  proceed now. Its Phase 3 (per-node chain in the canvas data model) is unblocked the moment backend
  Phase 1–2 ship — and must adopt the **G5 contract**: explicit chain per chain-aware part, no
  workflow-level `chainId` inheritance.
- Every change touching `model/` or `protobuf/` must pass `make storage-check` against `origin/main`
  before staging→main.

## Correction to send to the studio plan

`PLAN_WALLET_CHAIN_DECOUPLING.md` should be amended:

- Line ~59 *"Triggers/nodes carry no chainId of their own"* — **false** at the AVS/proto/SDK layer; the
  fields exist and (for nodes) are already honored end-to-end. The gap is studio's DTO not populating
  them, plus backend G1/G2/G3.
- Phase 3 *"Blocked on AVS: confirm the task schema can represent a multi-chain task"* — **unblocked**;
  it can. Reframe Phase 3 as "populate per-node/per-trigger chainId; reconcile deploy-time runner."
- Line ~106 *"multiple runners (a smart wallet per chain)"* — **not required**; one CREATE2 address
  serves all chains (per the Decision 2 audit, for the 4 core chains).
- **New (decided model):** chain is **required per chain-aware part**, and the **workflow-level `chainId`
  is removed entirely** — no `CreateTaskReq.chain_id`, no `Task.chain_id`, and task storage is
  chain-agnostic. Studio must drop the workflow-level `chainId` from both the request it sends *and* the
  record it reads: set chain explicitly on each event/block trigger and contract/transfer node (restricted
  to the 4 configured chains); a part left without a chain is rejected at create. The dashboard lists
  across chains and derives any per-chain view from the parts; the create endpoint's authKey remains
  API-auth scope only, not a task chain.

---

## References (code, exact locations)

- Proto: `protobuf/avs.proto` — task `:817`, CreateTaskReq `:858`, EventTrigger.Config `:222`,
  BlockTrigger `:152`, ContractWrite `:410`, ContractRead `:455`, ETHTransfer `:384`.
- REST mapping: `aggregator/rest/mapping/node.go:203` (protojson round-trip),
  `aggregator/rest/mapping/trigger.go:117-135` (BlockTrigger maps chainId), `:190-205` (EventTrigger
  **does not** — G1).
- Execution: `core/taskengine/vm.go:347-361` (`resolveSmartWalletForNode`), `:1475/1518/1743`
  (deployed nodes read proto chain), `:3493` (`ExtractNodeConfiguration` — G3).
- Engine multi-chain pool: `core/taskengine/engine.go:426-433` (`ResolveSmartWalletConfig`),
  `:649-682` (`operatorsCoveringChain`), `:1637-1660` (task chain resolution / gateway requirement).
- Operator: `operator/worker_loop.go:1055-1090` (event routing by task chain — G2).
- Model: `model/user.go` (`SmartWallet` has no chainId; `User.ChainID` from JWT aud),
  `model/workflow.go` (`Workflow` wraps `*avsproto.Task`).
- OpenAPI: `api/openapi.yaml:402,492,592,618,634`.
- SDK (the `ava-sdk-js` repo): `packages/sdk-js/src/v4/builders/{nodes,triggers}.ts`,
  `packages/types/src/openapi.gen.ts` (per-node/trigger `chainId`).
