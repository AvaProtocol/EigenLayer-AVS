# Findings — Auto swap AA23 on ETH/WETH sell (session allowlist / Gas Manager)

**Date:** 2026-08-06  
**Owner for fix:** User **re-grant** (new allowlist) + AVS typed errors / Gas Manager ops  
**Related:** `FINDINGS_AA23_FACTORY_MISMATCH.md` (factory mismatch = red herring; packing/`contractAddress` fixed in #706; USDC buy path proven)  
**Status:** **Studio grant path landed (2026-08-06)** — compile USDC+WETH, coverage = approve(tokenIn)+router, `SESSION_POLICY_TARGET_NOT_ALLOWED` preflight + card copy. On-chain still needs re-authorize once; AVS optional typed prefix.

---

## TL;DR

| Item | Detail |
|------|--------|
| **Symptom (UI)** | Studio Auto card: “Smart-wallet validation failed (AA23)…” on **sell 0.0001 ETH → USDC** (Sepolia) |
| **Studio residual** | **OK** — demote + grant compile (cap USDC + WETH approve) + demote coverage + typed fail-closed |
| **Where it fails** | **Prod** `nodes:run` → bundler/session validation → AA23 when grant lacks WETH approve (preflight now blocks with re-authorize copy) |
| **Likely root cause** | Session grant allowlist was **router + approve(USDC only)** — WETH `approve` off-allowlist |
| **Not the problem** | Wrong runner (using MA v2 `0x46aD…`), missing WETH balance (~0.00043), Studio demote, factory mismatch (already closed) |
| **Local gateway** | Idle for this failure (`GATEWAY_URL` points at prod). Older local run failed for a **different** reason (Gas Manager webhook **404**) |

---

## Identities (test account)

| Role | Address |
|------|---------|
| Owner EOA (session) | `0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788` |
| MA v2 Auto runner (salt 0) | `0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd` |
| Legacy v0.6 SimpleAccount (must not be Auto runner) | `0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f` |
| Studio userId | `f203353b-0331-4213-81ae-8c0dd27d6af7` |
| Chain | Sepolia `11155111` |
| Alchemy Gas Manager policy | `bf905871-55d7-4197-a020-605302f4bc87` |
| Session policy id (local prepare log / earlier dig) | `01kz8ebkzgv2th9r32w9qrcw79` |
| Sepolia USDC | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` |
| Sepolia WETH | `0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14` |
| SwapRouter02 | `0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E` |

---

## What works vs what fails

### Works — USDC → ETH (buy) on prod

Live Auto swap succeeded earlier the same session:

| | |
|--|--|
| Direction | USDC in → ETH/WETH out |
| Tx | [`0x0c34930ea89dbd80de13b718eb50eb4f37e3f6e2fe4519f22c57709368274459`](https://sepolia.etherscan.io/tx/0x0c34930ea89dbd80de13b718eb50eb4f37e3f6e2fe4519f22c57709368274459) |
| UserOp | `0xb48ba67f5e89015902456f51fd3555e54c56dcdd2c432511fa0a0930555af0f1` |
| Batch shape | `approve(USDC)` + `exactInputSingle` (USDC allowlisted) |

Also proven historically under Gas Manager in `FINDINGS_AA23_FACTORY_MISMATCH.md` (first-use install + USDC batch).

### Fails — ETH → USDC (sell) after WETH demote on prod

**When:** 2026-08-06 ~**03:52 PDT** / `2026-08-06T10:52:30Z`  
**Utterance:** `auto swap 0.0001 ETH for USDC on Sepolia`  
**Studio log:** `/tmp/studio-yarn-dev-manual.log`

```text
[swapUniswapAutoAction] demoted ETH-in to WETH (native insufficient)
  runner=0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd
  needWei=100000000000000          # 0.0001 ETH
  nativeWei=0
  wethWei=434668218823063          # ~0.000434 WETH — enough for the sell

executeNodeWithInputs.server response:
  error: 'invalid request: Smart wallet validation failed (AA23)'
  errorCode: 'INVALID_REQUEST'
  executionContext: { chain_id: 11155111, is_simulated: false, provider: 'bundler' }
  metadata.results:
    - methodName: approve            → Smart wallet validation failed (AA23)
    - methodName: exactInputSingle   → Smart wallet validation failed (AA23)
```

UI maps this to the friendly AA23 card (not a balance shortfall).

### Intermediate failures (same session, before clean demote/AA23)

| Time (approx UTC) | Error surface | Notes |
|-------------------|---------------|--------|
| ~10:23–10:26 | `gas manager declined … policy bf905871-…: execution reverted` | ETH-in Auto attempts; Gas Manager sponsorship simulation failed (may mask AA23 or true sim revert) |
| ~10:26 | Same Gas Manager `execution reverted` on `exactInputSingle` | Smaller 0.0001 ETH attempt before demote path was confirmed in logs |
| ~10:52 | **AA23** on approve + exactInputSingle | After demote; clearest failure mode |

### Local gateway (not this AA23)

Studio `.env.development`:

```text
GATEWAY_URL=https://api.avaprotocol.org/api/v1
GATEWAY_PARTNER_AUDIENCE=avs-gateway-prod
```

Local `ap` listens on `:8080` but **received no** `/api/v1/nodes:run` for the 10:52 AA23.

Last local chat UserOp in `gateway.log` was **01:24 PDT** — USDC batch with correct per-call targets, then:

```text
bundler_error: gas manager declined to sponsor:
  alchemy_requestGasAndPaymasterAndData (policy bf905871-…):
  Unexpected webhook response code: 404
```

That is **ops/config**: Alchemy Gas Manager custom-rules webhook expects **prod** `https://api.avaprotocol.org/webhooks/gas-manager` (or equivalent). Local gateway cannot complete sponsored UserOps until webhook routing or a non-webhook policy is fixed. Separate from AA23.

---

## Grant / allowlist model (why WETH sell should AA23)

Studio compiles Uniswap Auto grants as (**default cap token = USDC**):

```ts
// studio/app/lib/policies/grantScope.ts
SessionPolicyActions.merge([
  SessionPolicyActions.uniswapV3Swap(chainId),           // router exactInputSingle
  SessionPolicyActions.erc20Approve(capTokenAddress),    // ONLY the cap token
]);
// DEFAULT_CAP_TOKEN_SYMBOL = "USDC"
```

Gateway dig on the same runner (from prior findings) recorded:

| Field | Value |
|-------|--------|
| allowlist | router `exactInputSingle` + **USDC** `approve` |
| spend cap | **500 USDC** |
| entity | 1, hooks / `requires_execute_user_op` |

| Batch | approve target | On allowlist? | Expected |
|-------|----------------|---------------|----------|
| USDC → ETH | USDC | Yes | Mine under Gas Manager |
| WETH → USDC (demoted sell) | **WETH** | **No** (unless user granted cap=WETH or multi-token approve) | **AA23** |
| Native ETH → USDC with `msg.value` | (no WETH approve; value path) | Router may be OK; native balance was **0** so path not viable | Demote → WETH path |

So AA23 on the demoted sell is **consistent with correct hooks enforcement**, not a random bundler flake.

**Product gap:** Auto “sell ETH” after demote needs either:

1. **Grant scope expansion** — allowlist `approve` for the actual `tokenIn` (WETH and/or wrapped native), not only the spend-cap token; and/or multi-token approve rows for Uniswap capability; and  
2. **Spend-cap semantics** — cap is USDC-denominated; selling WETH must map to cap currency (USD notional) without requiring USDC balance as the sold asset; and  
3. **UX / preflight** — if grant covers only USDC-in, Studio/gateway should fail **before** bundler with a clear “permission covers USDC swaps only; re-grant for WETH” instead of generic AA23.

---

## Studio side (already fixed residual — not for AVS)

| Fix | Location | Behavior |
|-----|----------|----------|
| ETH spend mode | `studio/app/lib/chat/resolveEthInSpendMode.ts` | Prefer native if `nativeWei >= need`; else demote to WETH if `wethWei >= need` |
| Auto action demote | `studio/app/actions/uniswapSwapActions.ts` | Logs `demoted ETH-in to WETH (native insufficient)` then packs ERC-20 WETH path |
| AA23 UI copy | existing friendly mapper | “authorization/setup issue”, not balance |

Studio correctly builds a **WETH ERC-20** atomic batch when native is zero. Failure is **after** handoff to gateway validation.

---

## Environment matrix

| Layer | Config used for failing attempt |
|-------|----------------------------------|
| Studio dev | `GATEWAY_URL=https://api.avaprotocol.org/api/v1` |
| Partner audience | `avs-gateway-prod` |
| Local AVS | Running (`./out/ap aggregator --config=config/gateway.yaml`) but **unused** by Studio for this run |
| Local log path | `/Users/mikasa/Code/EigenLayer-AVS/gateway.log` |
| Studio log path | `/tmp/studio-yarn-dev-manual.log` |

To capture **local** AA23_DEBUG for the WETH path:

1. Set Studio `GATEWAY_URL=http://localhost:8080/api/v1` (and matching partner audience/key).  
2. Fix Gas Manager webhook → local **or** use a policy that does not require the custom webhook (else you only see **404**, not AA23).  
3. Re-run sell; grep gateway log for `AA23_DEBUG`, `per-call-targets`, `bundler_error`, `policy_id`, `deferred`, `entity`.

---

## Reproduction (prod)

1. Owner EOA `0xc60e71…` with MA v2 runner `0x46aD59…` on Sepolia.  
2. Active Uniswap session grant with **USDC cap** (default Studio grant).  
3. Runner balances similar to failure: **0 native ETH**, **~0.0004 WETH**, small USDC OK.  
4. Studio Auto: “swap 0.0001 ETH for USDC on Sepolia”.  
5. Expect: demote log → `nodes:run` → **AA23** on `approve` (+ often `exactInputSingle` in the same UserOp).  
6. Control: “swap 0.5 USDC for ETH” should still succeed under the same grant (if USDC balance + pool OK).

---

## Hypotheses (ranked)

| # | Hypothesis | Evidence | Verdict so far |
|---|------------|----------|----------------|
| **H1** | Session allowlist lacks **WETH `approve`**; demoted sell is off-policy | Grant compiles only `erc20Approve(capToken)`; dig said USDC approve only; AA23 on approve after demote | **Primary** |
| **H2** | Spend-cap / hooks reject WETH notional even if approve were allowlisted | Cap is 500 USDC; selling WETH may still fail validation until cap accounting covers tokenIn | Secondary; check after H1 |
| **H3** | Gas Manager / bundler sim flake | Earlier `execution reverted` without AA23 string; USDC buy succeeded same session | Possible for intermediate errors; **not** needed to explain clean AA23 after demote |
| **H4** | Missing/wrong `methodCalls[].contractAddress` (old packing bug) | Fixed in #706; local 01:24 AA23_DEBUG showed approve target USDC correctly | Unlikely for this case |
| **H5** | Wrong runner / factory / SimpleAccount | Logs use `0x46aD…`; USDC buy mined | Ruled out |
| **H6** | Insufficient WETH | `wethWei > needWei` | Ruled out |
| **H7** | Local gateway bug on this request | Studio pointed at **prod**; local log has no 10:52 `nodes:run` | Ruled out for this AA23 |

---

## Suggested AVS / product fix plan

### P0 — Correctness + diagnosis

1. **Confirm on-chain session policy rows** for `policy_id` on runner `0x46aD…` (AllowlistModule targets/selectors). Expect: USDC + router only.  
2. On next failure, log (prod) full UserOp: targets, values, entity, deferred, policy_id, paymaster — surface **which call** failed validation if possible (not only generic AA23 on both methods).  
3. **Preflight grant coverage** in gateway (or Studio pre-`nodes:run`): if any batch target/selector is outside active grant, return a **typed** error e.g. `SESSION_POLICY_TARGET_NOT_ALLOWED` with the missing `(token, approve)` row — never only AA23.

### P1 — Product behavior for ETH/WETH sell

Pick one product design (product + AVS):

| Option | Description |
|--------|-------------|
| **A. Multi-token Uniswap grant** | Compile allowlist: router + `approve` for **cap token and** catalog WETH (and optionally other tradeable bases). Cap remains USDC notional. |
| **B. Cap-token = spend token** | When Auto demotes to WETH-in, require or mint a grant with `tokenSymbol=WETH` (or dynamic tokenIn) before execute. |
| **C. Native-only sell** | Refuse demote under USDC-only grant; require user wrap/fund native and use value path if native is on-policy (may still need native value allowlisting). |

Recommended default: **A** or **B** with clear grant UX (“this permission can spend USDC on Uniswap” vs “ETH/WETH too”).

### P2 — Gas Manager ops

| Issue | Action |
|-------|--------|
| Local `Unexpected webhook response code: 404` | Point Alchemy policy custom webhook at reachable gateway **or** document that local sponsored Auto requires prod webhook + tunnel |
| Prod `execution reverted` via Gas Manager | After H1 fixed, re-test; if still present, capture Alchemy sponsorship response body + `eth_estimateUserOperationGas` without paymaster |

### P3 — Cleanup

- Temporary Studio/AVS `AA23_DEBUG` / `actingWallet TEMP` / `policyActions TEMP` logs — strip after soak (already noted in prior findings).  
- Cross-link this doc from Studio handoff (`docs/changes/`).

---

## Acceptance criteria for “fixed”

1. Same account/grant **or** intentionally expanded grant: Auto **sell** 0.0001 ETH (demoted to WETH) → USDC on Sepolia **mines** (tx hash, status 1).  
2. USDC → ETH still works under the same policy design.  
3. Off-allowlist batch returns **typed** policy error in Studio (not raw AA23-only) when intentionally mis-scoped.  
4. Optional: local gateway can complete one sponsored Auto swap without webhook 404 (ops).

---

## Evidence pointers

| Source | Path / ref |
|--------|------------|
| Studio demote + AA23 | `/tmp/studio-yarn-dev-manual.log` ~10:52Z (`demoted ETH-in`, `Smart wallet validation failed (AA23)`) |
| Successful USDC buy | same log, tx `0x0c34930…` |
| Local Gas Manager 404 | `EigenLayer-AVS/gateway.log` ~01:24 PDT `bundler_error` … `404` |
| Prior AA23 closed items | `FINDINGS_AA23_FACTORY_MISMATCH.md` |
| Studio grant compile | `studio/app/lib/policies/grantScope.ts` |
| Studio demote | `studio/app/lib/chat/resolveEthInSpendMode.ts`, `studio/app/actions/uniswapSwapActions.ts` |
| SDK builders | `ava-sdk-js` `SessionPolicyActions.uniswapV3Swap` / `erc20Approve` |

---

## One-line handoff for ticket title

**Auto Uniswap sell ETH/WETH AA23 on prod: session grant allowlists USDC `approve` only; demoted WETH `approve` fails validation — expand grant scope or preflight; Gas Manager 404 is separate local ops issue.**

---

## Complete fix plan (ownership split)

**Architecture invariant (do not break):** grant scope is **client-defined and owner-signed** via `policies:prepare` / `submit` (or `grant`). The gateway **enforces** installed allowlist rows; it never invents or silently widens permissions at execute time. Studio demote (ETH→WETH) must either (a) already be covered by the active grant or (b) force re-consent with a wider compile — not rely on opaque AA23.

### Design decision (recommended)

| Choice | Decision |
|--------|----------|
| Product default | **A — Multi-token Uniswap grant** for the “Uniswap Auto” capability: router `exactInputSingle` + `approve` for **cap token (USDC) and catalog WETH** (and any other tradeable `tokenIn` the product explicitly lists). Cap remains USDC-denominated notional. |
| Cap-token = spend token (B) | Optional later path for “WETH-capped” grants; not required if A covers sell demote. |
| Native-only sell (C) | Reject as primary: runner often has 0 native after demote. |

### Layer ownership

| Layer | Owner | Work | Status |
|-------|--------|------|--------|
| **Studio grant compile** | Studio | Expand `resolveGrantScope` / Uniswap capability so `allowedActions` include WETH `approve` (and document that demote needs it). Keep prepare-time `verifyPreparedScope` in lockstep. | **Handoff** |
| **Studio coverage / consent** | Studio | After demote, required rows must be **actual batch** rows (WETH approve + router), not only default USDC capability compile. If uncovered → grant card as consent, not `nodes:run`. Reuse/extend `grantCoverage.ts`. | **Handoff** |
| **Studio UX copy** | Studio | Map gateway typed error `SESSION_POLICY_TARGET_NOT_ALLOWED` to friendly “permission covers USDC swaps only; re-authorize for WETH”. | **Handoff** |
| **SDK builders** | AVS / SDK | `SessionPolicyActions.uniswapV3Capability(chainId, { approveTokens })` + `actionsCover` / `missingActions`. | **Done** (ava-sdk-js) |
| **Gateway preflight** | AVS | Before MA v2 UserOp send (atomic batch + single contract write), compare planned `(target, selector)` to active grant `AllowedActions`; fail with `SESSION_POLICY_TARGET_NOT_ALLOWED`. | **Done** (this repo) |
| **Gateway AA23 mapping** | AVS | Keep generic AA23 fallback; preflight path surfaces typed string as method error. | **Done** |
| **Ops** | Ops | Gas Manager webhook URL for local = prod or tunnel (404 is separate). | Separate |

### Implementation phases

#### Phase 0 — Diagnosis (done / continuous)

- [x] Confirm grant shape: USDC approve + router only (this doc).  
- [x] Confirm demote produces WETH approve batch.  
- [x] Confirm USDC buy mines under same grant.  
- [ ] Optional: on-chain AllowlistModule read for entity on `0x46aD…` after fix soak.

#### Phase 1 — Gateway preflight (blocks opaque AA23) — **IMPLEMENTED**

**Goal:** Any off-allowlist batch fails **before** bundler/Gas Manager with a stable machine-readable reason.

| Piece | Location |
|-------|----------|
| Pure coverage | `core/taskengine/session_grant_coverage.go` (`MissingGrantCalls`, `SelectorFromCalldata`, `FormatSessionPolicyTargetNotAllowed`) |
| Atomic batch | `executeAtomicBatch` preflight before pack/send |
| Single call | `executeRealUserOpTransaction` preflight before pack/send |
| Error prefix | `SESSION_POLICY_TARGET_NOT_ALLOWED: …` (Studio error map) |
| Skip when | not MA v2, no db, no `aa_sender`, no policy, empty `AllowedActions` |

**Out of scope for Phase 1:** changing what Studio grants; native value allowlisting; spend-cap notional for WETH (H2).

#### Phase 2 — SDK grant builders + coverage (Studio dependency) — **IMPLEMENTED**

| Piece | Location |
|-------|----------|
| `SessionPolicyActions.uniswapV3Capability(chainId, { approveTokens, router? })` | `packages/sdk-js/src/v4/builders/sessionPolicy.ts` |
| `actionsCover` / `missingActions` | same module, re-exported from `@avaprotocol/sdk-js` |
| Tests | `tests/v4/core/sessionPolicyActions.test.ts` |

Studio handoff: call `uniswapV3Capability(chainId, { approveTokens: [capToken, weth] })` from `grantScope.ts` instead of `merge(swap, approve(USDC only))`.
#### Phase 3 — Studio (handoff checklist)

1. **Compile:** Uniswap Auto capability → `uniswapV3Capability(chainId, { approveTokens: [capToken, weth] })` (catalog WETH for chain).  
2. **Coverage after demote:** required actions = approve(tokenIn) + swap(router), where `tokenIn` is WETH after demote.  
3. **Consent:** uncovered → grant card; covered → execute.  
4. **Error map:** `SESSION_POLICY_TARGET_NOT_ALLOWED` → friendly card (not generic AA23).  
5. **Regression:** USDC→ETH still one consent; ETH demote sell works after re-grant once; existing USDC-only grants **fail closed with clear copy** until user re-grants (intentional).

#### Phase 4 — Acceptance / soak

Matches earlier “Acceptance criteria for fixed” plus:

1. Prod: demoted 0.0001 ETH→USDC mines with expanded grant.  
2. Prod: USDC-only legacy grant → typed preflight error (not AA23-only).  
3. Gateway unit tests green; SDK tests green.  
4. Optional: local Gas Manager webhook ops note remaining.

### Sequence diagram (happy path after fix)

```text
Studio compile ──uniswapV3Capability(USDC,WETH)──► policies:grant (owner signs)
Auto demote ETH→WETH ──coverage(WETH approve+router)──► covered?
    no  → grant card
    yes → nodes:run atomic batch
            gateway preflight AllowedActions ──ok──► Gas Manager + bundler ──mine──►
```

### Explicit non-goals

- Gateway auto-installing extra allowlist rows without owner signature.  
- Treating AA23 as “retry with different packing” for this case (packing fixed in #706).  
- Changing Alchemy policy caps as a substitute for session allowlist.

### Ticket titles (split)

1. **[AVS]** Gateway: session grant allowlist preflight → `SESSION_POLICY_TARGET_NOT_ALLOWED`  
2. **[SDK]** `SessionPolicyActions.uniswapV3Capability` + coverage helpers  
3. **[Studio]** Expand Uniswap grant + demote coverage + error map (this handoff)
