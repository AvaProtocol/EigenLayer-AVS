# PLAN: On-Demand Single-Node Actions — EigenLayer-AVS backend gaps

- **Date**: 2026-07-09
- **Owner**: Chris
- **Status**: Proposed
- **Anchor repo**: `EigenLayer-AVS` (this doc's focus). `studio` owns the chat UX **and** constructs the `contractWrite` call (intent → node JSON, quotes, slippage, the market-order helper). `ava-sdk-js` only needs to surface the richer response this plan adds.
- **Motivating use case**: the studio LLM agent watches a user's portfolio and, on-demand through chat, **previews then executes a one-time Uniswap market order** by calling a **single node** (`nodes:run`), not by deploying a workflow.

---

## 0. Scope & decisions (read first)

**What's decided (so this plan can be concrete):**

1. **Custody model: delegated execution.** Reuse the existing model — the runner smart wallet is controlled by the aggregator controller key; `nodes:run` with `is_simulated=false` executes for real with **no per-transaction user signature**. We are *not* building a non-custodial "return an unsigned tx for the user to sign" path.
2. **Defer the server-side spend/policy gate.** "Make the function work first." The confirmation gate is the studio UX (user clicks Confirm) plus the existing ownership check; a server-side spend-cap/fund-authorization policy is a **later** hardening pass, explicitly out of scope here. (Tracked in the old §policy notes; not v1.)
3. **Studio owns call construction and the market-order helper.** The backend does **not** grow a Uniswap/market-order helper or a new `uniswapSwap` node type. Studio assembles the `contractWrite` node (router address, `exactInputSingle` tuple, `amountOutMinimum`, approve) and drives preview→confirm→execute. The backend's job is to **run a single `contractWrite` node correctly in both modes and return a usable result.**

**What this plan is, therefore:** a precise list of the **EigenLayer-AVS backend gaps** that stand between "studio can build the call" and "preview faithfully predicts execute, and execute returns something studio can act on." Everything below is verified against the code.

---

## 1. The single-node path as it exists today (verified)

`POST /api/v1/nodes:run` → `Server.RunNode` ([aggregator/rest/handlers_nodes.go:23](aggregator/rest/handlers_nodes.go#L23)) → `Engine.RunNodeImmediatelyRPCWithContext` ([core/taskengine/run_node_immediately.go:2996](core/taskengine/run_node_immediately.go#L2996)) → `runProcessingNodeWithInputs` → `VM.RunNodeWithInputs` ([core/taskengine/vm.go:2477](core/taskengine/vm.go#L2477)) → `ContractWriteProcessor.Execute` ([core/taskengine/vm_runner_contract_write.go:1450](core/taskengine/vm_runner_contract_write.go#L1450)).

Confirmed behaviors that make this usable **today**:

- **Real execution is wired and reachable.** `useSimulation` defaults to `true` but honors `is_simulated=false` from node config ([run_node_immediately.go:3076-3083](core/taskengine/run_node_immediately.go#L3076)); the REST mapping passes `isSimulated`/`value` through untouched (`jsonRetargetProto`, [aggregator/rest/mapping/node.go:54](aggregator/rest/mapping/node.go#L54)). There is **no gateway-level force-simulate** — the SDK's "always Tenderly" is only a test convention.
- **Preview is faithful for token-only calls.** The simulate branch ([vm_runner_contract_write.go:403-622](core/taskengine/vm_runner_contract_write.go#L403)) runs Tenderly with `senderAddress` = the runner smart wallet, honors `value` via `extractTransactionValue(node)` ([:482](core/taskengine/vm_runner_contract_write.go#L482)), seeds `erc20Overrides`, and **injects the ERC-20 allowance slot after an `approve()` sim** ([:519-548](core/taskengine/vm_runner_contract_write.go#L519)) so a following `swap` sees the approval — the approve→swap preview primitive.
- **Auth is correct for fund-moving.** `needsAASender` (true for `contractWrite`) requires a non-zero authenticated `TaskOwner` and a `settings.runner` the owner actually owns (DB list, else salt 0–4 derivation, [run_node_immediately.go:2618-2748](core/taskengine/run_node_immediately.go#L2618)). A partner-assertion-only caller (zero address) is rejected — real execution needs the user's Bearer JWT.
- **Overall step success is all-or-nothing across method calls** — `computeWriteStepSuccess` ([core/taskengine/node_utils.go:257](core/taskengine/node_utils.go#L257)) sets success=false if any method result fails or has a receipt failure.

---

## 2. "preview→confirm→execute contract" — what it is, and where it's broken

**The contract.** Studio drives one node through two backend calls with the **same node JSON**:

```
  preview  = nodes:run(node, is_simulated=true)   → show the user the outcome
  (user confirms in studio)
  execute  = nodes:run(node, is_simulated=false)  → broadcast, return a result to reconcile
```

For this to be trustworthy, two properties must hold — and **each is where the backend gap lives**:

- **(A) Fidelity** — the preview must predict the execute. It breaks wherever the *simulate* path and the *real* path diverge on the same node config.
- **(B) Reconcilable result** — the execute must return a result studio can render and match against the preview (tx identity + what the user got).

The "gap of the preview→confirm→execute contract" is exactly the set of divergences (A) and result-shape holes (B) below. It is **not** a missing endpoint — the endpoint exists and studio already calls it twice.

---

## 3. The backend gaps (verified, ranked)

| # | Gap | Property | Severity | Where |
| --- | --- | --- | --- | --- |
| G1 | Native ETH `value` honored in preview, **hardcoded to 0 on execute** | Fidelity (A) | **P0 — breaks the example** | [vm_runner_contract_write.go:687](core/taskengine/vm_runner_contract_write.go#L687) |
| G2 | Real execute returns **no typed tx identity**, and **no return value** (`Value: nil`) | Reconcilable (B) | **P0** | [vm_runner_contract_write.go:1112](core/taskengine/vm_runner_contract_write.go#L1112), [handlers_nodes.go:139-148](aggregator/rest/handlers_nodes.go#L139) |
| G3 | Pending UserOp reports **success=false** (indistinguishable from failure); `nodes:run` is stateless (no re-poll) | Reconcilable (B) | **P0** | [vm_runner_contract_write.go:1022-1038, 1087](core/taskengine/vm_runner_contract_write.go#L1022) |
| G4 | approve+swap = **two non-atomic UserOps**; partial on-chain side effects on failure | Fidelity (A) | **P1 — reliability** | [vm_runner_contract_write.go:1526, 643](core/taskengine/vm_runner_contract_write.go#L1526) |
| G5 | Execute is **not idempotent** — a retried Confirm re-broadcasts | Safety | **P1** | `nodes:run` stateless (no key) |
| G6 | Runner **salt not propagated** on the `nodes:run` real path (validates salt 0–4, sets only `aa_sender`) | Correctness (edge) | **P2** | [run_node_immediately.go:2731](core/taskengine/run_node_immediately.go#L2731) |
| G7 | **No execution fee** wired on `nodes:run` (nil `executionFeeWei`) | Product/pricing | **P2 — decision** | (fee set only in `executor.go`) |

### Implementation status (2026-07-12, branch `feat/ondemand-single-node-execution`)

| Gap | Status | Commit |
| --- | --- | --- |
| G1 native value passthrough | ✅ **done + tested** | `fix(taskengine): correct single-node contractWrite real execution` |
| G2 typed identity (userOpHash + normalized status) | ✅ **done + tested** (via receipt fields, no proto change) | same |
| G3 pending ≠ failure | ✅ **done + tested** | same |
| G6 runner salt propagation | ✅ **done** | same |
| G5 idempotency | ✅ **done + tested** (Idempotency-Key header, no proto change) | `feat(taskengine): idempotent nodes:run via Idempotency-Key header` |
| G4 atomic batching | ✅ **done + tested** — on-demand (nodes:run) multi-call real execution now submits one `executeBatch` UserOp; composes with the reimbursement wrapper; gated on the run-node route so deployed workflows are unchanged | `feat(taskengine): atomic approve+swap batching for on-demand execution` |
| G7 fee posture | ✅ **decided: fee-free for v1** (confirmed 2026-07-12); monetization revisited separately (analysis below) | — |

### G1 — Native `value` is dropped on execute (P0, breaks the example)

The real path packs the smart-wallet call with a **hardcoded zero value**:

```go
smartWalletCallData, err := aa.PackExecute(
    contractAddress,
    big.NewInt(0),   // ETH value (0 for contract calls)  ← always 0
    callDataBytes,
)
```
([vm_runner_contract_write.go:685-689](core/taskengine/vm_runner_contract_write.go#L685))

The simulate path, by contrast, passes `r.extractTransactionValue(node)` to Tenderly ([:482](core/taskengine/vm_runner_contract_write.go#L482)). So a **payable** call — wrapping ETH, or an **ETH-in** Uniswap swap — previews correctly (value applied) and then **executes with 0 wei** → reverts or no-ops. This directly breaks the "sell my ETH" market-order example on the real path.
**Fix**: replace `big.NewInt(0)` with the parsed `extractTransactionValue(node)` (guard parse errors; it already returns a decimal-wei string). Small, self-contained. Reconcile with the paymaster-reimbursement batch wrapper, which is the only place native value flows today.

### G2 — Execute returns no typed tx identity and no return value (P0)

A mined real result carries a `Receipt` **map** with `transactionHash` and the standard fields, but:
- there is **no first-class typed field** for `txHash` / `userOpHash` / status — studio must dig through the generic `output` bag that `runNodeRespToOpenAPI` flattens ([handlers_nodes.go:139-148](aggregator/rest/handlers_nodes.go#L139)); and
- **`Value` is nil for real transactions** — `createRealTransactionResult` sets `Value: nil // Real transactions don't return values directly` ([vm_runner_contract_write.go:1112](core/taskengine/vm_runner_contract_write.go#L1112)). So the **preview returns the swap `amountOut`** (decoded return value), but the **execute does not** — studio can't confirm "you received X USDC" from the execute response without decoding logs itself.

`userOpHash` is only populated in the *pending* branch ([:1025](core/taskengine/vm_runner_contract_write.go#L1025)); once a receipt exists, only `transactionHash` is kept.
**Fix**: add a typed execution result on `RunNodeWithInputsResp` (or the `contractWrite` output) — `{ userOpHash, txHash, status, blockNumber, gasUsed }` populated in both mined and pending branches — and decode/attach the primary return value (e.g. Swap `amountOut` from the receipt logs) for real executes so preview and execute expose the same shape.

### G3 — Pending ≠ failure (P0)

`SendUserOp` blocks up to ~2 minutes polling for the receipt and can return a **UserOp with no receipt** (still pending). In that branch the result is `status:"pending"`, `transactionHash:"pending"` ([vm_runner_contract_write.go:1022-1030](core/taskengine/vm_runner_contract_write.go#L1022)), and success is computed as `receipt != nil && receipt.Status == 1 && userOpInnerSuccess` ([:1087](core/taskengine/vm_runner_contract_write.go#L1087)) → **`success=false`**. So a swap that *will* land but hasn't yet is reported identically to a swap that *failed*. And because `nodes:run` is stateless, there is **no way to re-poll** that UserOp's status afterward.
**Fix**: (a) surface a distinct `pending` status in the typed result (G2) so studio doesn't render pending as failure; (b) provide a way to resolve a pending UserOp — either a lightweight status lookup keyed by `userOpHash`, or have studio poll chain/bundler with the returned `userOpHash`. Minimum viable: make `pending` unambiguous in the response.

### G4 — Non-atomic approve+swap (P1, reliability) — SHIPPED

> **Status: shipped.** On-demand (`nodes:run`) real execution of a multi-call node now submits a
> single atomic `executeBatch` UserOp instead of one UserOp per method, so approve+swap is
> all-or-nothing (no dangling allowance from an approve that lands while its swap reverts). Three
> pieces made the deferred blast radius tractable:
> 1. **Reimbursement composition.** `wrapWithReimbursement` now decodes the smart-wallet calldata
>    through a shared ABI-aware unpacker (`aa.UnpackExecuteCalldata`) and *appends* its
>    reimbursement/fee entries onto whatever app calls are present — a single `execute` (unchanged,
>    byte-identical for deployed workflows) **or** an `executeBatch`. The batch composes instead of
>    colliding.
> 2. **Route gating.** The batch path is gated on the run-node route (`vm.task == nil`); deployed
>    workflows keep the exact sequential per-method semantics — the shared-loop blast radius is
>    contained.
> 3. **Heterogeneous targets.** A new optional `contract_address` on `MethodCall` lets one node
>    express approve@token + swap@router; the batch and the per-call event decoder both honor it.
>    All-zero-value batches pack as `executeBatch` (bundler-estimatable); a value-bearing call falls
>    back to `executeBatchWithValues`. The single receipt fans out into one result per sub-call,
>    sharing tx identity.

`Execute` loops over `methodCalls` ([vm_runner_contract_write.go:1526](core/taskengine/vm_runner_contract_write.go#L1526)); each real method calls `executeRealUserOpTransaction` independently ([:643](core/taskengine/vm_runner_contract_write.go#L643)) → **one UserOp per method**, and the loop **does not stop on a failed method** ("Don't fail the entire execution for individual method failures", [:1729](core/taskengine/vm_runner_contract_write.go#L1729)). So approve+swap = two sequential UserOps; if approve lands and swap fails, `computeWriteStepSuccess` reports the *step* as failed while the **approve is already on-chain** — a partial side effect the preview (single atomic Tenderly sim with allowance injection) never showed. This is both a fidelity gap and a footgun.
**Fix**: when a node has multiple `methodCalls` and `is_simulated=false`, batch them into **one `executeBatch` UserOp** via the existing `aa.PackExecuteBatch` / `PackExecuteBatchWithValues` ([core/chainio/aa/aa.go:206](core/chainio/aa/aa.go#L206)) — atomic, one gas estimate, one signature. Keep per-method sequential as a fallback. Until this lands, restrict chat execution to single-call actions (or accept the approve-then-swap risk with a guard).

### G5 — Idempotency (P1, safety even without spend caps)

`nodes:run` execute has no idempotency key; a retried Confirm (network retry, double-click, studio re-send) **re-broadcasts** a second UserOp. Since we're moving real funds, add an **idempotency key** on the execute request that the aggregator dedupes within a short window (distinct from the on-chain nonce, which doesn't protect the API layer). This is the one "safety" item worth doing in v1 even though the broader policy gate is deferred.

### G6 — Runner salt not propagated (P2, edge correctness)

The run-node path validates the runner across **salts 0–4** and computes `matchedSalt`, but then sets only `aa_sender` and **discards the salt** ([run_node_immediately.go:2731](core/taskengine/run_node_immediately.go#L2731)) — `aa_salt` is never set on this path (it *is* set on the deployed-task path, [executor.go:491](core/taskengine/executor.go#L491), and in `runContractWrite` from `vm.task`, [vm.go:1658](core/taskengine/vm.go#L1658), but `vm.task` is nil for `nodes:run`). The real send reads `aa_salt` ([vm_runner_contract_write.go:756](core/taskengine/vm_runner_contract_write.go#L756)) and, absent it, defaults to salt 0. For the overwhelmingly common **salt:0** runner this is fine; for a runner at salt 1–4 the sender override and the auto-deploy salt/initCode diverge → wrong/failed deploy.
**Fix**: carry `matchedSalt` into `aa_salt` on the `nodes:run` path (mirror `executor.go`). Cheap; removes a latent multi-wallet bug.

### G7 — Execution fee posture (P2, decision — resolved: fee-free for v1)

The deployed-task executor adds a platform `executionFeeWei` batch transfer ([executor.go:356-368]); the `nodes:run` path leaves it nil, so **on-demand actions are gas-sponsored but fee-free** today.

**Decision for v1: keep on-demand executes fee-free.** Ship the capability; don't couple monetization to correctness. Monetization is a separate, deliberate design. The analysis that informs that later design:

**Does batching a fee into the UserOp add significant gas?** No — *if batched*, which is already how `wrapWithReimbursement` works (it appends `executionFeeWei` as an extra `executeBatchWithValues` entry, `builder.go:499-503`). The marginal cost of a fee is one extra batch op = a value-transfer CALL to an existing recipient (~9,000 gas) + the extra batch entry's calldata (~3 words ≈ 1,500 gas) + minor loop/decode overhead ≈ **~10–15k gas**. Against a Uniswap `exactInputSingle` (~120–180k) inside a full UserOp (~250–400k with EntryPoint verification), that's **~3–6%**. The expensive alternative is a *separate* fee UserOp — a whole extra EntryPoint verification + preVerificationGas + base ≈ 60–100k+ gas. So **batching is the efficient path**; a separate fee tx is not.

**Would charging on every tx raise user cost?** Yes, two components: (1) the fee amount itself (revenue, a direct cost), and (2) the ~10–15k marginal gas — borne by the user because the paymaster sponsors then the wallet reimburses. For **micro trades** a *flat* per-tx fee (+ the fixed gas overhead) is a large % of a small swap; a **bps fee** scales with size and is the better "charge on every tx" shape. Batched into the existing reimbursement op, a bps fee is the lowest-friction option and needs no new signature.

**Coinbase x402 as a micro-fee rail?** x402 revives HTTP 402: the client hits an endpoint, gets payment requirements, pays with stablecoin (USDC, typically Base) by signing an EIP-3009 `transferWithAuthorization`, retries with an `X-PAYMENT` header, and a *facilitator* verifies + settles on-chain. The **payer pays no gas directly** (the facilitator submits settlement; USDC transfer gas on Base is sub-cent), so it's effectively gasless-for-payer and purpose-built for **agent/API micropayments** — conceptually a good fit for "charge $X per chat action," stable-denominated and decoupled from the swap's gas.
- **Friction against our model:** x402 reintroduces a **user signature** (the EIP-3009 authorization), which conflicts with the delegated model where the aggregator signs and the user doesn't sign per-tx — unless the fund-authorization/session-key layer also authorizes the x402 payment. It's also Base/USDC-centric today and adds a facilitator dependency.
- **Recommendation:** for a wallet/ETH-denominated fee, **batch a bps fee into the existing reimbursement UserOp** (cheap, no new signature, works today). Treat **x402 as a separate, deliberate rail** specifically for the agent/chat surface if/when per-action USDC micropayments are desired — its own project, not part of this backend correctness work.

---

## 4. What is explicitly NOT a backend gap

- **Building the swap call** — router/quoter addresses, `exactInputSingle` tuple, fee tier, `amountOutMinimum`/slippage, the approve — studio + `@avaprotocol/protocols` own this. The backend takes a fully-formed `contractWrite` node.
- **The confirm gate / spend policy** — deferred (studio Confirm + ownership check suffice for v1).
- **A `uniswapSwap` node type** — unnecessary; a `contractWrite` with two method calls (after G4) covers it.
- **Chain resolution** — already correct: `chain_id` is required per-node post-G5 ([vm.go:354](core/taskengine/vm.go#L354)); `nodes:run` stamps the request chain onto the node if unset.

---

## 5. SDK & studio touchpoints (high-level)

- **`ava-sdk-js`**: consume the typed execution result from G2/G3 (add `RunNodeExecuteResult` fields: `userOpHash`, `txHash`, `status`, `amountOut`/return value); make `is_simulated=false` an explicit, obviously-fund-moving call that requires a Bearer JWT (not the partner-assertion simulate path). Add a `nodes:run` command to `examples/example.ts` for agent verification (none exists today). The market-order helper lives here or in `@avaprotocol/protocols`, not in EigenLayer-AVS.
- **`studio`** (kept high-level): a preview→confirm→execute action card (preview via `is_simulated=true` + `erc20Overrides`; execute via `is_simulated=false` bound to the previewed `amountOutMinimum`); a curated `uniswap-swap` verified action so the confidence scorer treats it as reviewed-not-blocked; and (later) the portfolio-watch → suggested-action loop. This is the execution capability the advisor plan and the idle-funds gap note deliberately deferred.

---

## 6. Backend sequencing (status)

1. ✅ **G1 + G2 + G3 + G6** — shipped: a single real execute is correct (value forwarded), reconcilable (userOpHash + normalized `confirmed`/`pending`/`failed`), pending is not failure, and non-zero-salt runners resolve. (commit `fix(taskengine): correct single-node contractWrite real execution`)
2. ✅ **G5 (idempotency)** — shipped: `Idempotency-Key` header dedupes retries/double-clicks. (commit `feat(taskengine): idempotent nodes:run via Idempotency-Key header`)
3. ✅ **G4 (atomic batch)** — shipped: on-demand multi-call real execution submits one `executeBatch` UserOp, composes with the reimbursement wrapper, gated on the run-node route. Heterogeneous targets via a per-call `contract_address`.
4. ✅ **G7 (fee)** — decided fee-free for v1 (confirmed); monetization is a separate design (see G7 analysis).

Deferred (later hardening): server-side spend/fund-authorization policy; fee monetization (G7).

---

## 7. Risks

- **Non-atomic partial fills (pre-G4).** Mitigation: land G4 before enabling multi-call chat execution; until then restrict to single-call or add an approve-state guard.
- **`value=0` silent wrong execution (pre-G1).** Mitigation: G1 is a small early fix; gate native-value actions off until it lands.
- **Pending read as failure (pre-G3).** Mitigation: G3 status field; studio must not render `pending` as failed.
- **Double-execute on retry (pre-G5).** Mitigation: idempotency key.
- **Scope creep into autonomous trading.** This is *one-time, user-confirmed* actions; recurring/autonomous strategies remain workflows with their own review.

---

## Appendix — verified source references

- Single-node REST handler — [aggregator/rest/handlers_nodes.go:23](aggregator/rest/handlers_nodes.go#L23); response flattening [:117](aggregator/rest/handlers_nodes.go#L117)
- Engine single-node RPC (`is_simulated` default) — [run_node_immediately.go:2996](core/taskengine/run_node_immediately.go#L2996), [:3076](core/taskengine/run_node_immediately.go#L3076)
- `needsAASender` runner ownership + salt scan — [run_node_immediately.go:2618-2748](core/taskengine/run_node_immediately.go#L2618); `aa_sender` set (no salt) [:2731](core/taskengine/run_node_immediately.go#L2731)
- ContractWrite `Execute` loop (per-method) — [vm_runner_contract_write.go:1450](core/taskengine/vm_runner_contract_write.go#L1450), method loop [:1526](core/taskengine/vm_runner_contract_write.go#L1526)
- Simulate branch (value + allowance injection) — [vm_runner_contract_write.go:403-622](core/taskengine/vm_runner_contract_write.go#L403)
- Real path (`value=0` hardcode) — [vm_runner_contract_write.go:685-689](core/taskengine/vm_runner_contract_write.go#L685)
- Real result build (`Value: nil`, pending branch) — [vm_runner_contract_write.go:952-1113](core/taskengine/vm_runner_contract_write.go#L952)
- Step success (all-or-nothing) — [core/taskengine/node_utils.go:257](core/taskengine/node_utils.go#L257)
- Batch pack helpers (for G4) — [core/chainio/aa/aa.go:206](core/chainio/aa/aa.go#L206)
- `aa_salt` set on deployed path (for G6) — [core/taskengine/executor.go:491](core/taskengine/executor.go#L491)
- REST mapping passes config through — [aggregator/rest/mapping/node.go:54](aggregator/rest/mapping/node.go#L54)
</content>
