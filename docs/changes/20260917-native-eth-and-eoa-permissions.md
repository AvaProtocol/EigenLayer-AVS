# Native ETH session permission (derived MA v2) and EOA 7702 permissions

**Date:** 2026-09-17
**Status:** Proposed
**Branch:** docs/native-eth-and-eoa-permissions
**Related:** [discussion #658](https://github.com/AvaProtocol/EigenLayer-AVS/discussions/658), `PLAN_PARTNER_PAYMENTS.md` §4.1 / Phase 4, `FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`, `docs/changes/20260806-session-grant-replace-on-submit.md`


| Field | Value |
| --- | --- |
| **Author** | TBD |
| **Date** | 2026-09-17 |
| **Status** | Proposed |
| **Repo** | EigenLayer-AVS (Ava Protocol Automation AVS) |
| **Feature branch** | `docs/native-eth-and-eoa-permissions` |
| **Branch policy** | PRs target `staging`. Conventional Commits. `make storage-check` before anything that touches persisted models/keys. |
| **Related** | [discussion #658](https://github.com/AvaProtocol/EigenLayer-AVS/discussions/658), `PLAN_PARTNER_PAYMENTS.md` §4.1 / Phase 4, `FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`, `docs/changes/20260806-session-grant-replace-on-submit.md`, `core/taskengine/session_grant_native_test.go` |

This is **one design covering two independently-shippable tracks**. They are complementary products, not competing implementations of one product.

- **Track A** can ship without Track B.
- **Track B** must not require Track A, but **must reuse Track A's permission vocabulary** where the idea is the same (recipient allow-list, native spend cap, expiry, revoke).

---

## Overview

Today an owner can grant the gateway a session key on the derived Modular Account v2 smart wallet that is selector-scoped and ERC-20-capped. That shape cannot authorize `execute(to, value, 0x)` — empty inner calldata — so `ethTransfer` and native `ExecuteWithdraw` are **blanket-refused** with `SESSION_POLICY_NATIVE_NOT_ALLOWED`, and the message tells the caller **not** to re-grant. Payable contract writes with a native `value` already pass the allowlist (they carry a selector) and have **no native-value cap**. Separately, session keys exist only on the derived smart wallet; automating the user's EOA itself is a written future in `PLAN_PARTNER_PAYMENTS.md` §4.1, not implemented, not tracked under `docs/changes/`.

**Track A** adds an opt-in native-ETH **send** purpose (`ethTransfer` / withdraw to listed EOAs). Uniswap is a **separate purpose**: the compiler emits every row a swap needs (router, cap token, WETH approve/deposit/withdraw, payable ETH-in) so the swap is not gated after the user said yes (K0). Native-send is only compiled when that purpose is present, into the **same singleton grant**. `HasSelectorAllowlist=false` is used **only** on native-recipient rows (it is any-function on that address, not “ETH send”); ERC-20/router rows stay selector-scoped. Every grant, including native-only, still installs `AllowlistExecHook` as the self-admin latch. Preflight stops being a chain-level blanket for empty-calldata sends and starts reading the actual grant.

**Track B** lets the same permission vocabulary apply to the **user's EOA** via EIP-7702. **Vendor is Alchemy Modular Account v2 for both tracks:** derived smart wallet (SemiModularAccountBytecode / factory — Track A) and EOA (`SemiModularAccount7702` — Track B). Same permission modules (Allowlist, NativeTokenLimit, TimeRange, SingleSigner) on both account types. **Calibur is not used.** Track B starts with a spike that does not enable production fund movement. First production chains: **Sepolia and Base**.

---

## Background & Motivation

### Current grant shape (verified)

REST grants (`PreparePolicyRequest` / `SubmitPolicyRequest` in `api/openapi.yaml`) require:

- `allowedActions[]` — each: contract `target` + `selectors` (`minItems: 1`)
- `erc20SpendCap` — token must appear as an `allowedActions` target
- `expiresInSeconds` (≥ 60)

`SessionPermissions.Validate()` (`core/taskengine/session_permissions.go`) refuses empty selectors, zero targets, and a missing ERC-20 cap. `allowlistInputs()` **always** sets `HasSelectorAllowlist: true`. Comments and `TestHooksForAlwaysScopesSelectors` explain why: Alchemy `AllowlistModule` skips `data.length < 4` only when that flag is false.

Production packing (`core/taskengine/session_grant.go`): `SessionGrant.Global = (len(Selectors) == 0)`. REST never passes `Selectors`, so production grants are **global validations** whose *where/how-much/how-long* live in hooks, not in the validation's selector list. `aa.SessionGrant.Validate()` (`core/chainio/aa/ma_v2_install.go`) refuses a global grant with no execution hook (self-administration / escalation). The ERC-20 cap's `AllowlistExecHook` is that hook today.

Authority is a **singleton per runner** (`docs/changes/20260806-session-grant-replace-on-submit.md`). `SubmitSessionPolicy` supersedes every other usable grant on `(chainID, owner, runner)`. `ActiveSessionPolicyForWallet` refuses >1 usable grant (`SESSION_POLICY_AMBIGUOUS`). Changing that singleton rule is a bigger change than Track A needs.

Storage: `sp:%d:%s:%s` (`SessionPolicyKey` in `core/taskengine/schema.go`) — chain, owner, policy id. Runner lives on the record. Grant material (`InstallCall`, signature, carrier nonce) is secret-grade and never returned on list.

Architecture invariant (`FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`): grant scope is **client-defined and owner-signed** via `policies:prepare` / `submit`. The gateway **enforces** installed allowlist rows; it never invents or silently widens permissions at execute time.

### Why native ETH is refused today

`ETHTransferProcessor` packs `aa.PackExecute(destination, amount, []byte{})` (`core/taskengine/vm_runner_eth_transfer.go`). `ExecuteWithdraw` of token `"ETH"` builds the same empty-calldata execute (`aggregator/rpc_server.go`).

Alchemy `AllowlistModule._checkCallPermission` (v2.0.x, deployed v2.0.1 at `0x00000000003e826473a313e600b5b9b791f5a59a` — the address we already pack in `core/chainio/aa/ma_v2_hooks.go`):

```solidity
if (hasSelectorAllowlist) {
    if (data.length < 4) {
        revert NoSelectorSpecified();
    }
    if (!selectorAllowlist[entityId][selector][target][account]) {
        revert SelectorNotAllowed();
    }
}
```

Listing the recipient with selector `0x00000000` does **not** help: the length check runs first. The zero-address wildcard would skip that branch (`if target == address(0)` installs selector wildcards for any address) but `SessionPermissions.Validate` refuses a zero target, correctly — that would be "send anywhere."

`FormatSessionPolicyNativeNotAllowed` (`core/taskengine/session_grant_coverage.go`) is therefore a **blanket MA v2 refusal**. Tests in `session_grant_native_test.go` pin: the message must not say "re-grant", and `TestHooksForAlwaysScopesSelectors` says that if a grant shape ever sets `HasSelectorAllowlist=false`, the blanket must be **narrowed to the actual grant**, not loosened blindly.

### Payable contract writes are a different case

`ContractWriteProcessor` resolves `node.Config.Value` and packs `execute(target, value, calldata)` with a real selector (`vm_runner_contract_write.go`). `preflightSessionGrantCoverage` checks target+selector only — **not value**. So:

| Inner call | Allowlist today | Native-value cap today |
| --- | --- | --- |
| `ethTransfer` / withdraw ETH: `execute(to, value, 0x)` | Always reject (`NoSelectorSpecified`) | n/a (never reaches exec) |
| Payable `contractWrite`: `execute(router, value, exactInputSingle…)` | Allowed if target+selector listed | **None** — unbounded ETH as `value` |
| ERC-20 `transfer`/`approve` | Allowed + `AllowlistModule` ERC-20 exec cap | n/a |

A Uniswap grant that allowlists the router can attach `value` to `exactInputSingle`. **Trust that signed grant.** Uniswap ETH-in is a payable `contractWrite` on an allowlisted selector — **not** an `ethTransfer`, and it does **not** need a native-send grant. Do not install `NativeTokenLimitModule` on ERC-20/Uniswap-only grants (not even exec-only limit=0). Gateway preflight of payable `value` on an **already-allowlisted** target+selector **passes** without `nativeSpendCap`. Native-send permission (`nativeRecipients` + `nativeSpendCap` + NT module) is only for empty-calldata sends (`ethTransfer` / withdraw ETH). NativeTokenLimitModule is installed only when the owner explicitly adds that native-send permission.

You never put two grants on one wallet. One usable grant per runner (singleton). If the user wants **both** Uniswap and ETH-send, Studio compiles **one** `PreparePolicyRequest` (merge). Adding native-send to a Uniswap user replaces the previous grant; it does not stack.

### Track B current state

MA v2 automates the **derived** smart wallet (CREATE2 from owner EOA + factory). The user must fund that second address. Session keys live on that contract.

MA v2 7702 automates the **user's EOA itself** via EIP-7702: same address, existing assets, no funding step. Complementary product to the derived smart wallet. Written as Phase 4 in `PLAN_PARTNER_PAYMENTS.md` (that plan still names Calibur; **this spec does not use Calibur** — vendor is Alchemy MA v2 for both EOA and smart-wallet permissions). Partner-layer invariant: `scope: execute` must **never** be satisfied by a partner credential alone (`aggregator/rest/permission.go` `LevelUserRefusePartner` for policies; `PLAN_PARTNER_PAYMENTS.md` §0 / §8).

Discussion #658 called scoped / expiring / user-revocable (never root-equivalent) **non-negotiable** on a delegated EOA, because the blast radius is everything the user owns.

---

## Goals & Non-Goals

### Track A goals

1. An owner can sign a session grant that **explicitly** authorizes native ETH sends, bounded by **recipient allow-list AND native spend cap AND expiry**.
2. Studio/SDK can compile that grant; the gateway installs it; `ethTransfer` and native withdraw **succeed when covered** and **fail closed when not**.
3. Uniswap/ERC-20-only grants work **alone**: payable `contractWrite` `value` on an allowlisted target+selector is trusted (no native-send grant, no NT module). Empty-calldata `ethTransfer` / native withdraw still require explicit native-send permission. Native cap on-chain exists only when that permission is present.
4. `SESSION_POLICY_NATIVE_NOT_ALLOWED` is no longer a blanket MA v2 refusal. It becomes "this grant has no native permission" (with a re-grant path that converges), plus distinct coverage/cap codes.
5. Grants are **purpose-matched** (K0): compile every permission the stated intent needs so the action works; do not add unrelated capabilities. Native-send is its own purpose (`ethTransfer` / withdraw), not a Uniswap toggle.

### Track B goals

1. A concrete, implementable plan for a user to grant AVS a scoped, expiring, revocable permission to move funds **from their EOA** (native + ERC-20).
2. Alchemy MA v2 for **both** the derived smart wallet and the EOA (7702 mode), with a threat model that does not fail open. **Yes: Alchemy MA v2 works for both EOA and smart-wallet permissions.** Calibur is not used.
3. AVS API / storage / execute-path design; Studio/SDK consent sequence as a **handoff** (do not invent avs-infra private-doc contents).
4. A PR plan that **starts with de-risking work that does not ship fund movement**.

### Non-goals (both tracks)

- Do **not** enable native ETH by turning grants global-without-hooks, by using the zero-address wildcard as hidden "send anywhere", or by setting `HasSelectorAllowlist=false` on ERC-20/router targets as a side effect.
- Do **not** treat `NativeTokenLimitExecHook` as the self-admin latch. That hook does not revert on `installValidation` / `updateLimits`.
- Do **not** list a contract as a `nativeRecipient` unless the owner set `allowContractRecipient` (off by default). Safe/treasury sends use `contractWrite`.
- Do **not** split Track A into two usable grants per runner (that changes the execute-path singleton rule).
- Do **not** implement code in this document's PR; this is the spec.
- Do **not** ship Track B production execute in the same milestone as Track A.
- Do **not** satisfy `scope: execute` with a partner credential.
- Do **not** grant `isSignatureValidation` / ERC-1271 to the controller on either wallet type.
- Do **not** reshape existing `allowedActions` / `erc20SpendCap` JSON field names (breaking API). Additive fields only.
- Do **not** silently install native-send (`nativeRecipients` / `ethTransfer`) onto a Uniswap grant at execute time.
- Do **not** install `NativeTokenLimitModule` on Uniswap/ERC-20-only grants (not even exec-only limit=0). Trust the signed Uniswap grant.
- Do **not** make the user toggle WETH approve / wrap / payable ETH as extra gates when they already consented to Uniswap. That is under-authorizing the stated purpose (`FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`).
- Do **not** use Calibur. No Phase 2 Calibur timeline.
- Do **not** ship per-user controller keys in Track B v1 (shared controller as today).

---

## Key Decisions

| # | Decision | Rationale |
| --- | --- | --- |
| K0 | **Purpose-matched grants.** Compile the permission set that makes the user's stated intent succeed. Prefer a slightly wider *capability* over a grant that AA23s / preflights after they said yes. The gateway still never invents rows at execute time — completeness is the **compiler's** job (Studio/SDK), from the purpose (workflow nodes + explicit capabilities), not a menu of raw selectors the user must get right. | Anti-pattern: Uniswap Auto granted router + **USDC approve only**; demote ETH→WETH then AA23'd on WETH approve (`FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`). The user intended a Uniswap swap. Missing WETH was a gate on that intention, not least-privilege. Least privilege applies **to the purpose** (Uniswap, not “send ETH anywhere”), not to omitting steps the purpose requires. |
| K1 | **On-chain native send = AllowlistModule row with `HasSelectorAllowlist=false` on each explicit recipient + `NativeTokenLimitModule` cap + existing `TimeRangeModule`.** `NativeTokenLimitModule` is installed **only** when `nativeSpendCap` is present (both validation + execution hooks). ERC-20/Uniswap-only grants do **not** get that module — not even exec-only limit=0. Uniswap ETH-in does **not** require native-send permission. | The AllowlistModule length check makes selector `0x00000000` useless. The only way to authorize `data.length < 4` is wildcard-selectors on a **specific** address — and that is **any-function** on that address, not ETH-send. Alchemy `NativeTokenLimitModule` (`0x00000000000001e541f0D090868FBe24b59Fbe06`). **Trust the signed Uniswap grant:** payable `value` on allowlisted router/WETH selectors is authorized by those selectors. Gateway preflight must not demand `nativeSpendCap` for that. Empty-calldata send is a different capability. |
| K2 | **REST: additive `nativeRecipients` + `nativeSpendCap` (+ optional `allowContractRecipient`). Do not break `allowedActions` / `erc20SpendCap`.** Native-only **omits** `allowedActions` (present `[]` is 400). | Relax `required` (server-side) so native-only grants can omit ERC-20 fields. Old clients that still send them keep working. Not a JSON rename. OpenAPI `minItems: 1` applies only when the array is **present**. |
| K3 | **One composable grant class, not two grants on the wallet.** Uniswap works with the Uniswap grant **alone**. Native-send is a second *capability*, compiled into the **same** `SessionPolicy` only when the user also wants `ethTransfer` / withdraw. SDK `merge`: Uniswap builders **never** set a native cap. Two `nativeTransfer` builders → **union of recipients** (dedupe, max 20) and **max of caps**. NativeTokenLimitModule is one scalar, so per-recipient caps cannot be preserved; max is the honest “more generous of the two send permissions.” Not min (silently shrinks), not sum (invents budget neither builder stated). | Two grants would recreate the dual-grant brick `20260806-session-grant-replace-on-submit.md` fixed. Uniswap+nativeTransfer has exactly one native cap (from the native builder). Side effect: once NT is installed, Uniswap payable `value` **also** decrements that cap on-chain — the module cannot tell swap-value from send-value. |
| K4 | **`HasSelectorAllowlist=false` is legal only on `nativeRecipients` rows, and those recipients must be EOAs.** ERC-20/router `allowedActions` stay `true` with non-empty selectors. Overlap with an allowed-action target is refused. Gateway `Validate` and native-send preflight require `eth_getCode(recipient)==0` unless `allowContractRecipient` is **true** (default **false**, logged). | `_checkCallPermission` with `hasSelectorAllowlist=false` skips the length check **and** does not consult selectors — `transfer`/`approve`/`withdraw` on that address are authorized, and NativeTokenLimit only subtracts `execute` **value** (a `value=0` ERC-20 transfer does not touch the native cap; native rows set `HasERC20SpendLimit=false`). Safe/treasury contracts use `contractWrite`, not `nativeRecipients`. Studio copy is not a control. |
| K5 | **Always install `AllowlistExecHook` on every REST grant, including native-only with no ERC-20 cap.** `NativeTokenLimitExecHook` is value accounting only — never the self-admin latch. | `aa.SessionGrant.Validate()` only checks that **some** exec-hook bit is set. On chain, AllowlistModule `preExecutionHook` reverts `SpendingRequestNotAllowed` for every selector that is not `execute`/`executeBatch`. NativeTokenLimit `preExecutionHook` does **not**: for any other selector `value` stays 0 and the hook returns success. REST grants are already global; Allowlist **validation** also no-ops on non-execute selectors. Without `AllowlistExecHook`, a native-only session key can `installValidation` / `uninstallValidation` / `NativeTokenLimitModule.updateLimits`. Native recipient rows have `HasERC20SpendLimit=false`, so Allowlist exec is a no-op on `execute` and still rejects self-admin. |
| K6 | **Preflight reads the actual grant.** Blanket MA v2 refusal is deleted. Simulation (`ethTransfer` **and** payable `contractWrite`) and `nodes:run` run the same preflight when a policy is resolvable. Formatter rewrite and grant-aware **withdraw** land in the **same PR**. | `TestHooksForAlwaysScopesSelectors` predicted this. Shipping the new “re-grant” copy on the old blanket withdraw path recreates the non-converging loop. |
| K7 | **Native cap is NativeTokenLimitModule's unified limit: self-funded = `value + gas`; sponsored = value only.** Preflight uses **GrantedCap** plus that inequality. **Do not** read on-chain remaining cap on every send in v1. | Module: no paymaster ⇒ validation hook subtracts gas, exec subtracts `value`. `SendUserOpMAv2` seeds `MaxFeePerGas=0` until bundler pricing. Map on-chain `ExceededNativeTokenLimit`. |
| K8 | **Vendor is Alchemy Modular Account v2 for both tracks.** Derived SW: SemiModularAccountBytecode / factory (Track A). EOA: `SemiModularAccount7702` (Track B). Same modules (Allowlist, NativeTokenLimit, TimeRange, SingleSigner). **Calibur is not used** — no Phase 2, no dual-run. Track B first production chains: **Sepolia and Base**. Shared controller as today (no per-user controller keys in v1). | We already run MA v2 + EntryPoint v0.7 + Alchemy bundler/Gas Manager. Audited modules; `isSignatureValidation` stays false. **Yes, Alchemy MA v2 works for both EOA (7702 mode) and smart-wallet permissions.** Calibur’s fail-opens (ERC-1271 admit-any-key, mis-flagged hook, sponsored delegation success-without-code) are why it is rejected, not deferred. |
| K9 | **Track B execute path is 4337 UserOp with `sender = EOA`, signed by the same shared controller session key, under the same session-grant hooks.** A user may hold both a derived SW and a 7702 EOA; the runner address distinguishes them. **B5 must add a derivation-check exception** — today's `SendUserOpMAv2` refuses `sender != DeriveSenderAddressAuto(owner, factory, salt)`. | Same SessionResolver / bundler stack. `senderOverride` does **not** bypass the factory match (`pkg/erc4337/preset/send_v07.go`). |
| K10 | **Revocation/expiry/replace reuse today's machinery, but uninstall hook data is not a flat reverse of the install array.** Teardown is: **validation hooks in reverse-install (stored) order, then execution hooks in reverse-install order**, one slot per hook. NativeTokenLimit val slot = `abi.encode(uint32 entityId)`; exec slot empty. | Account `_uninstallValidation` applies val-then-exec (`ma_v2_uninstall.go` package comment). Today's 3-hook flat reverse happens to work because the swapped slots are the **same AllowlistModule**. Mixed `[AL-val, AL-exec, NT-val, NT-exec, TR-val]` is different modules: flat reverse routes NT install data onto Allowlist and empty slots onto NativeTokenLimit. The account **catches** `onUninstall` reverts and mines `success=true` while stranding `limits[entity][account]` — the #717 class of bug. |
| K11 | **Controller never gets ERC-1271 / `isSignatureValidation` on either wallet type.** | #658 non-negotiable. Alchemy `PermissionBuilder` already hardcodes `isSignatureValidation: false` even for `root`. |
| K12 | **Hook entity ID equals the session validation entity** for every module we install (Allowlist, TimeRange, NativeTokenLimit), on derived SW and 7702 EOA. **Do not use Alchemy's example `hookEntityId: 0`.** | This repo already packs `PackHookConfig(..., entityID, ...)` with the grant entity (`MinSessionEntityID` ≥ 1). Entity IDs are per-module, so session entity 1 on Allowlist is independent of SingleSigner entity 1. Copying Alchemy's `sessionKeyEntityId: 1` / `hookEntityId: 0` would pack NT at 0 while Allowlist/TimeRange stay at 1, split teardown keys, and collide with leftover `limits[0][account]`. Derived-SW entity 1 vs EOA entity 1 do not collide (different `account` keys); that does **not** license a hook/validation ID split. |
| K13 | **EIP-7702 delegation check is exact:** `len(code) >= 23 && code[0:3]==0xef0100 && code[3:23]==canonical SMA-7702`, plus bytecode hash of that implementation. Never assert on tx status. | EIP-7702 designated code is `0xef0100 \|\| address`. The sloppy `code == 0xef0100 \|\| sma7702` notation is not a comparison. |

---

## Proposed Design

### Track A — Native ETH send permission on the derived MA v2 smart wallet

#### A.0 Purpose-matched grant compilation (K0)

The grant screen is **not** “pick selectors.” It is “allow this purpose.” Studio/SDK compiles the rows. The gateway **enforces** the signed rows and never adds any at execute (`FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md` architecture invariant).

**Rule:** if the user consents to a purpose, the compiled grant must include every call that purpose is allowed to make so the happy path and the product’s own fallbacks work. Do not ship a Uniswap grant that only works for USDC-in.

| Purpose (what the user said) | Compile (what the grant must contain) | Do **not** compile |
| --- | --- | --- |
| **Uniswap swap** (Auto / a `contractWrite` Uniswap node) | Router swap selector(s) the node actually emits (at least `exactInputSingle`). `approve` for the **cap token and catalog WETH** (demote ETH→WETH). `WETH.deposit` + payable `value` on deposit and on the router (ETH-in). `WETH.withdraw` if the node can unwrap. ERC-20 spend cap on the cap token. | `ethTransfer` / withdraw to arbitrary EOAs. `nativeRecipients`. `NativeTokenLimitModule`. Other protocols. |
| **Send ETH** (`ethTransfer` node or withdraw) | `nativeRecipients` (destinations from the node if known, else the grant-screen list) + `nativeSpendCap` + NT module. | Uniswap router. Token approve. |
| **Both** (workflow has Uniswap **and** `ethTransfer`) | **One** merged grant: Uniswap rows ∪ native-send rows. Recipients union, native cap = max of native builders. | Two grants on the wallet (singleton). Native-only that drops Uniswap. |

**Uniswap capability is complete.** `SessionPolicyActions.uniswapV3Capability(chainId, { capToken, wrappedNative? })` — default `wrappedNative` = catalog WETH for that chain — **must** emit:

```
allowedActions:
  - router:  exactInputSingle (and any other swap selector the node packs)
  - capToken: approve
  - WETH:     approve, deposit, withdraw
erc20SpendCap: capToken
# omit nativeRecipients, nativeSpendCap, NativeTokenLimitModule
```

Payable `value` on those allowlisted WETH/router calls is authorized by the Uniswap purpose (K1). ETH-in and wrap/unwrap must not require a second “allow ETH” toggle.

This is the Studio handoff that `FINDINGS_AA23` already named: call `uniswapV3Capability({ approveTokens: [capToken, weth] })`, not `merge(swap, approve(USDC only))`. Existing USDC-only grants fail closed with `SESSION_POLICY_TARGET_NOT_ALLOWED` until the user re-grants the complete capability — that is intentional, not a new gate on first consent.

**Compiler input** (Studio, in order):

1. Walk the workflow graph. Union the purposes of the nodes (`uniswap` / `contractWrite` to a known router, `ethTransfer`, ERC-20 transfer, …).
2. Add explicit capabilities the user turned on (e.g. they also want withdraw).
3. `merge` once into a single `PreparePolicyRequest`.
4. Show the purpose in plain language on the grant screen (“Uniswap swaps, including wrapping ETH to WETH”; “Send ETH to these addresses”), not the raw selector list as the primary UI. Advanced view may show rows.

If a planned call is still missing at execute, preflight `SESSION_POLICY_TARGET_NOT_ALLOWED` / native codes — **re-grant with the complete purpose**, never silent widening.

#### A.1 Exact on-chain module configuration

**AllowlistModule** (already packed in `core/chainio/aa/ma_v2_hooks.go`):

| Row kind | `Target` | `HasSelectorAllowlist` | `Selectors` | `HasERC20SpendLimit` |
| --- | --- | --- | --- | --- |
| ERC-20 / router `allowedActions` | contract | **true** | 4-byte list, min 1 | true iff this target is `erc20SpendCap.token` |
| Native recipient | **EOA** (`eth_getCode == 0`) | **false** | empty | **false** |

`HasSelectorAllowlist=false` is "specific address + **any selector**" in the module's check order. Empty inner calldata then passes `_checkCallPermission` because the `data.length < 4` branch is skipped — **and so does every other selector** on that address (`transfer`, `approve`, `withdraw`, …). NativeTokenLimitModule only subtracts `execute`/`executeBatch`/`performCreate` **value**; a `value=0` token transfer does not decrement it. That is why native recipients are EOAs by default (K4).

Zero-address wildcard stays refused (`Validate` already refuses a zero target). `address(0)` + `hasSelectorAllowlist=false` would be send-anywhere + call-anything.

**NativeTokenLimitModule** (`alchemy.native-token-limit-module.1.0.0`):

- Address (v2.0.0, canonical, not re-deployed in v2.0.1): `0x00000000000001e541f0D090868FBe24b59Fbe06`
- `onInstall`: `abi.encode(uint32 entityId, uint256 spendLimit)` — packed on the **validation** hook only
- Execution hook: config-only, empty initData (same pattern as `AllowlistExecHook`)
- Validation hook `preUserOpValidationHook`: decrements `limits[entityId][account]` by `gas * maxFeePerGas` unless a paymaster is present (Alchemy Gas Manager is **not** a "special paymaster" by default, so sponsored UserOps do **not** burn the cap as gas)
- Execution hook `preExecutionHook`: sums `value` from `execute` / `executeBatch` / `performCreate`, reverts `ExceededNativeTokenLimit()` if `value > limit`, then subtracts
- `moduleId()`: `alchemy.native-token-limit-module.1.0.0`

New helpers in `core/chainio/aa/ma_v2_hooks.go` (mirroring allowlist/time-range):

```go
const NativeTokenLimitModuleAddressHex = "0x00000000000001e541f0D090868FBe24b59Fbe06"

func PackNativeTokenLimitInstallData(entityID uint32, spendLimit *big.Int) ([]byte, error)
func PackNativeTokenLimitUninstallData(entityID uint32) ([]byte, error) // abi.encode(uint32 entityId) only
func NativeTokenLimitValidationHook(entityID uint32, spendLimit *big.Int) ([]byte, error) // flag = HookFlagValidation, initData packed
func NativeTokenLimitExecHook(entityID uint32) []byte // flag = HookFlagExecHasPre, no initData
```

`entityID` on every helper is the **session validation entity**, never `0` (K12). Golden vector: `cast abi-encode "f(uint32,uint256)" <entity> <limit>` — same discipline as `TestPackAllowlistInstallDataGolden`. Uninstall golden: `cast abi-encode "f(uint32)" <entity>`.

**TimeRangeModule**: unchanged. Every grant still expires.

**Validation entity**: still `SingleSignerValidationModule`, still `Global` (empty `SessionGrant.Selectors`), still `AllowSignatureValidation=false`.

**Self-admin latch (K5):** `AllowlistExecHook` is installed on **every** grant, including native-only with no ERC-20 cap. Native recipient rows have `HasERC20SpendLimit=false`, so Allowlist `preExecutionHook` is a no-op on `execute`/`executeBatch` (returns without decrementing) and still reverts `SpendingRequestNotAllowed` for **outer** `installValidation` / `uninstallValidation` and every other non-`execute`/`executeBatch` selector. `NativeTokenLimitModule.updateLimits` is **not** in that list: it rides outer `execute`, so the revert is `AddressNotAllowed` (target not allowlisted). Spike selectors: A0 item 7 / Fail closed. `NativeTokenLimitExecHook` is **value accounting only**: for a non-execute selector it leaves `value=0` and returns success. `aa.SessionGrant.Validate()` seeing *any* exec-hook bit is packing-time hygiene, not the on-chain guard.

**Hook install order** for `SessionPermissions.HooksFor` (the array passed to `installValidation`):

```
1. AllowlistValidationHook(entity, inputs)     // always — WHERE; inputs = allowedActions ⊕ nativeRecipients
2. AllowlistExecHook(entity)                   // ALWAYS — self-admin latch; ERC-20 HOW MUCH when SpendCap set
3. NativeTokenLimitValidationHook(entity, cap) // iff nativeSpendCap present — gas accounting
4. NativeTokenLimitExecHook(entity)            // iff nativeSpendCap present — value accounting only
5. TimeRangeValidationHook(entity, until, 0)   // always — HOW LONG
```

Native-only (no ERC-20): still includes (2). Do **not** skip `AllowlistExecHook`.

ERC-20-only: (1)(2)(5) — **byte-identical** to today's `HooksFor` (golden). No NT hooks.

**`allowlistInputs` (nil-safe):**

- If `SpendCap == nil`: do **not** read `SpendCap.Amount` / `SpendCap.Token`. No row gets `HasERC20SpendLimit=true`.
- For each `allowedActions` row: `HasSelectorAllowlist=true`, parsed selectors; `HasERC20SpendLimit` only when `SpendCap != nil` and `*action.Target == *SpendCap.Token`.
- Append one row per native recipient: `HasSelectorAllowlist=false`, `Selectors` empty (not nil-vs-empty: encode as empty `bytes4[]`), `HasERC20SpendLimit=false`, `ERC20SpendLimit=0`.
- Panic/NPE on `SpendCap` is a bug, not an implicit.

**Uninstall routing (K10) — `SessionSignerUninstallFromInstall` must change.** Today's implementation reverses the **flat** install array (`ma_v2_uninstall_from_install.go`). That is **not** the account's order. Do **not** special-case 3-hook grants to keep the old test green.

**3-hook install** `[AL-val, AL-exec, TR-val]` (today's ERC-20-only `HooksFor`):

| Group | Install order | Stored / uninstall order (prepend reverse) |
| --- | --- | --- |
| Validation | AL-val, TR-val | TR-val, AL-val |
| Execution | AL-exec | AL-exec |

`hookUninstallData` **must** be `[TR-val data, AL-val data, empty]`.

Today's `TestUninstallReversesIntoStoredOrder` asserts the **flat** reverse `[TR-val, empty, allowlist tuple]` (`ma_v2_uninstall_from_install_test.go` lines 87–92). That only works on chain because AL-val and AL-exec are the **same module**. After the rewrite, **update that test** to expect val-then-exec `[TR-val data, AL-val data, empty]`. Comment: the old `[TR, empty, AL]` order was the flat reverse and only worked because both AL slots share a module. Do **not** preserve flat reverse as a 3-hook compatibility path. Live 3-hook teardown still succeeds (allowlist tuple lands on AL-val instead of AL-exec); the **unit test** is what changes.

Account order (both shapes):

> hookUninstallData ordering is the account's, not the install's: **validation hooks in STORED order (reverse of install — prepend-on-add), then execution hooks (same reversal)**, one entry per installed hook or `ArrayLengthMismatch`. An entry routed to the wrong module does NOT fail the uninstall: `onUninstall` reverts are caught, flagged only in `ValidationUninstalled`, and the module state is silently stranded. (`core/chainio/aa/ma_v2_uninstall.go`)

**5-hook mixed install** `[AL-val, AL-exec, NT-val, NT-exec, TR-val]`:

| Group | Install order | Stored / uninstall order (prepend reverse) |
| --- | --- | --- |
| Validation | AL-val, NT-val, TR-val | TR-val, NT-val, AL-val |
| Execution | AL-exec, NT-exec | NT-exec, AL-exec |

`hookUninstallData` **must** be:

```
[0] TR-val  = TimeRange install tuple  abi.encode(uint32 entityId, uint48 validUntil, uint48 validAfter)
[1] NT-val  = abi.encode(uint32 entityId)     // NativeTokenLimitModule.onUninstall — NOT the (entityId, spendLimit) install tuple
[2] AL-val  = Allowlist install tuple         abi.encode(uint32 entityId, AllowlistInput[] inputs)
[3] NT-exec = empty
[4] AL-exec = empty
```

`NativeTokenLimitModule.onUninstall` is `abi.encode(uint32 entityId)` only. Extra ABI words of the install tuple would decode as a `uint32` (first word) and *happen* to work; **do not rely on that** — pack entityId only. Index routing is the fatal bug: flat reverse would be `[TR-val, NT-exec, NT-val, AL-exec, AL-val]` and strand `limits[entity][account]`.

Implementation: split decoded install hooks by `HookFlagValidation` in the config's flag byte; reverse each group independently; concatenate val-reversed then exec-reversed. Derive payloads from the stored install bytes (Allowlist/TimeRange reuse install data; NT-val **replace** install data with `PackNativeTokenLimitUninstallData(entityID)`; exec slots empty). **One splitter for every grant shape.** `TestUninstallReversesIntoStoredOrder` is **rewritten** to `[TR-val, AL-val, empty]`. `TestUninstallMixedNativeGrantValThenExecOrder` is the 5-hook proof. There is no 3-hook compatibility branch.

A0/L7 remain release-blocking and must read `NativeTokenLimitModule.limits(entity, account)` after replace (expect 0 / empty), not the receipt. **Do not merge A2 until that vector is green.**

**PR 0 (spike, live Sepolia, not production):** prove on a throwaway entity, before any REST change:

1. Selector-scoped USDC row still rejects `execute(alice, 1 wei, 0x)` (`NoSelectorSpecified`).
2. Adding a native-recipient row with `HasSelectorAllowlist=false` **allows** that execute (recipient is an EOA).
3. Same grant **rejects** `execute(bob, 1 wei, 0x)` (`AddressNotAllowed`) — not a wildcard.
4. `NativeTokenLimitModule` with cap `X` reverts `ExceededNativeTokenLimit` for value `X+1`. **Self-funded `amount == X` also reverts** (gas burns the remainder).
5. ERC-20/router rows remain `HasSelectorAllowlist=true`; a native-recipient row does not widen them.
6. Replace/uninstall: after teardown, `NativeTokenLimitModule.limits(entity, account) == 0`, Allowlist signer/rows clear, TimeRange clear. A receipt is not evidence.
7. **Native-only session key cannot self-admin:**
   - `installValidation` / `uninstallValidation` (global selector, not wrapped in `execute`) reverts `SpendingRequestNotAllowed` — **AllowlistExecHook**, not NT exec.
   - `execute(NativeTokenLimitModule, 0, updateLimits(...))` reverts `AddressNotAllowed` (NT is not an allowlisted target). Do not expect `SpendingRequestNotAllowed` here: Allowlist exec only reverts that error for non-`execute`/`executeBatch` **outer** selectors; `updateLimits` rides `execute` and would succeed if the target were allowed.
   If either call succeeds, K5 is wrong — stop.

Until (1)–(7) are green on Sepolia, Track A does not merge hook packing into the REST path.

#### A.2 REST / OpenAPI / storage shape

Additive fields. No JSON rename of `allowedActions` / `erc20SpendCap`.

**New schemas** (`api/openapi.yaml`):

```yaml
NativeSpendCap:
  type: object
  required: [amount]
  properties:
    amount:
      type: string
      pattern: '^[0-9]+$'
      description: |
        Cumulative native-token cap in wei (decimal string, no reset).
        Enforced on-chain by NativeTokenLimitModule. Self-funded UserOps
        also decrement this cap by gas; sponsored UserOps decrement only
        the ETH value sent.
      example: '10000000000000000'  # 0.01 ETH

# nativeRecipients: array of EthereumAddress, minItems 1 when present
```

**`PreparePolicyRequest` / `SubmitPolicyRequest`:**

- `required` becomes `[chainId, agentLabel, expiresInSeconds]` (and submit's existing id/signature/deadline/validUntil/entityId).
- `allowedActions` stays in properties; `minItems: 1` **when the array is present**. Native-only **omits** the field. A present empty array (`[]`) is **400** — OpenAPI 3 rejects it under `minItems: 1`, and generated Go clients disagree on nil vs empty. SDK `merge()` must not emit `allowedActions: []`.
- `erc20SpendCap` becomes optional in the schema.
- Add optional `nativeRecipients` (array of `EthereumAddress`, `minItems: 1` when present). Payable-write-only native **omits** the field; present `[]` is 400. Mirror of `allowedActions`.
- Add optional `nativeSpendCap`.
- Add optional `allowContractRecipient` (boolean, default false). When true, logged at prepare/submit.

This is a **server relaxation**, not a breaking wire change. Clients that still send today's required fields keep working. Regenerating SDK types is required to *use* the new fields; coordinate with `ava-sdk-js` but this is not a protobuf JSON rename.

**Application validation** (`SessionPermissions.Validate`), replacing "v1 requires all three":

```
erc20Class    := len(AllowedActions) > 0
nativeSend    := len(NativeRecipients) > 0
hasNativeCap  := NativeSpendCap != nil && amount > 0

if !erc20Class && !nativeSend:
    error "a grant needs allowedActions and/or nativeRecipients"

if erc20Class:
    existing selector/target checks
    require SpendCap covering a token that is an allowed-action target

if nativeSend:
    require hasNativeCap
    each recipient is a non-zero address
    max 20 recipients (bound install size)
    no recipient equals any allowedActions.target
      (error: "list contracts as allowed actions with selectors;
       native recipients are for empty-calldata sends")
    refuse if recipient is a known module (Allowlist, NativeTokenLimit,
      TimeRange, SingleSigner, factory, EntryPoint) — even with the flag
    unless allowContractRecipient:
        eth_getCode(recipient) must be empty for every recipient
        (RPC failure = fail closed, do not prepare)
        code != 0 → error "native recipient is a contract; send via
        contractWrite, or set allowContractRecipient (any function
        on this address, ERC-20 uncapped)"

if NativeSpendCap != nil && NativeSpendCap.amount is not a positive decimal:
    error
```

`eth_getCode` at prepare needs an RPC (prepare already does occupancy/teardown chain reads). Submit re-runs Validate on the echo, including the code check — a recipient that became a contract between prepare and submit is refused.

Uniswap-only (ETH-in included): `allowedActions` + `erc20SpendCap`; **omit** `nativeRecipients` / `nativeSpendCap`. Empty-calldata sends stay refused. Payable `contractWrite` on those selectors is trusted — no native-send grant.

Optional payable-write cap without ETH-send recipients (`nativeValueCap`): `allowedActions` + `erc20SpendCap` + `nativeSpendCap`; **omit** `nativeRecipients`. Only if the owner **explicitly** wants an on-chain native budget on top of Uniswap. Not required for Uniswap to work. Empty-calldata sends stay refused.

Native-only (`ethTransfer` / withdraw ETH, no ERC-20): `nativeRecipients` + `nativeSpendCap`; **omit** `allowedActions` / `erc20SpendCap`. `AllowlistExecHook` is still installed (K5).

**A1 packing gate:** if native fields are present before A2 lands, `HooksFor` returns `"native permission packing is not implemented"` **before** calling `allowlistInputs` (which would otherwise nil-deref `SpendCap` on native-only). Validate may accept the fields; the owner must not be handed a digest.

**`SessionPolicy` response / storage model** (`model/session_policy.go`):

```go
NativeRecipients        []*common.Address `json:"native_recipients,omitempty"`
NativeSpendCap          *NativeSpendCap   `json:"native_spend_cap,omitempty"`
AllowContractRecipient  bool              `json:"allow_contract_recipient,omitempty"`

type NativeSpendCap struct {
    Amount     string `json:"amount"`      // wei, decimal
    GrantedCap string `json:"granted_cap"` // == Amount at grant time; "used X of Y"
}
```

Both `omitempty`. **No new storage key.** `make storage-check` must stay green (additive fields). `attachDeclaredPermissions` copies them the same way it copies `AllowedActions` / `ERC20SpendCap`. Display/rebuild data only — signed truth remains `Grant.InstallCall`.

`permissionsFromAPI` / `policyToAPI` / `submitPolicyToAPI` in `aggregator/rest/handlers_policies.go` grow the new fields. `TestSubmitPolicyResponseCarriesEverySessionPolicyField` will fail until the flattened `allOf` copy includes them — that test is load-bearing.

#### A.3 Preflight changes

Delete the chain-level blanket. `ETHTransferProcessor.preflightSessionGrant` and `ExecuteWithdraw`'s ETH branch become grant-coverage checks, same family as `ContractWriteProcessor.preflightSessionGrantCoverage`.

**Shared helper** (new, in `session_grant_coverage.go` or a sibling `session_grant_native.go`):

```go
type NativeIntent struct {
    Recipient    common.Address // empty-calldata destination; zero if payable write
    Amount       *big.Int
    Kind         NativeIntentKind // NativeSend (ethTransfer/withdraw) | NativeValue (payable write)
    Sponsored    bool             // SponsorshipPolicyID() != ""
    EstimatedGas *big.Int         // wei; production fills from eip1559.SuggestFee × 1_300_000 gas units; tests inject
}

func PreflightNativePermission(policy *model.SessionPolicy, intent NativeIntent) string
```

When the chain is not MA v2, or there is no config: skip (same as today).

When MA v2 and no usable policy: existing send path already fails "no session authorization" — do not invent a native code. Exception for **withdraw unit tests without db**: fail closed on MA v2 ETH (refuse with `SESSION_POLICY_NATIVE_NOT_ALLOWED`, no policy id) so a missing store cannot reach the bundler. When a policy is injected and has no native cap, same code **with** policy id.

| Condition | Code | Re-grant? |
| --- | --- | --- |
| Empty-calldata send (`ethTransfer` / withdraw) and policy has no `nativeSpendCap` | `SESSION_POLICY_NATIVE_NOT_ALLOWED` | **Yes** — "re-grant with nativeSpendCap and nativeRecipients" |
| Native send and recipient not in `nativeRecipients` | `SESSION_POLICY_RECIPIENT_NOT_ALLOWED` (new) | Yes — add this recipient |
| Native send, `allowContractRecipient=false`, `eth_getCode(recipient) != 0` | `SESSION_POLICY_RECIPIENT_NOT_EOA` (new) | Use `contractWrite`, or re-grant with the flag |
| Native send, self-funded: `amount + estimatedGas > GrantedCap`. Sponsored: `amount > GrantedCap` | `SESSION_POLICY_NATIVE_CAP_EXCEEDED` (new) | Yes — raise the cap (replace grant) or send less / sponsor |
| Payable write `value > 0`, selector **covered**, **no** `nativeSpendCap` | **pass** | Uniswap-only / trusted allowlisted call. Do not demand native-send permission. |
| Payable write `value > 0`, selector covered, **`nativeSpendCap` present** (batch: **sum** of per-call `values` vs cap) | cap check; else pass | NT module will decrement this value; preflight must match |
| Payable write target/selector missing | `SESSION_POLICY_TARGET_NOT_ALLOWED` (unchanged) | Yes — existing copy |

**Cap inequality (K7), not `amount > GrantedCap` alone.**

Named fee source — do not use the unpriced UserOp (`MaxFeePerGas=0` at `send_v07.go` ~149).

```
nativePreflightGasUnits = initialCallGasLimit             // 500_000  (builder_v07.go)
                        + initialPreVerificationGas       // 100_000
                        + seedVerificationGasDeferredHooks // 700_000
                        = 1_300_000
```

Production grants always carry `AllowlistExecHook`, so the deferred-hooks seed is the right one (not `seedVerificationGasDeployed` 60_000). If the send path has already estimated, use those numbers instead of seeds.

```
maxFeePerGas = max(eip1559.SuggestFee(client).maxFeePerGas, NativePreflightFeeFloorWei)
NativePreflightFeeFloorWei = 2_000_000_000  // 2 gwei — same as pkg/eip1559 minGweiFloor
estimatedGasWei = nativePreflightGasUnits * maxFeePerGas
```

`eip1559.SuggestFee` already computes `(2 * baseFee) + tip` with a 2 gwei floor. Reuse it; do not invent a third fee formula. **Production always has a chain RPC** (the same reader withdraw already uses for balance; the node path already has `ethClient`). If `SuggestFee` or `CodeAt` fails: **fail closed** (typed infra error or `SESSION_POLICY_LOOKUP_FAILED` / native cap code — do not skip the check, do not treat fee as 0).

- Self-funded (`SponsorshipPolicyID() == ""`): preflight `amount + estimatedGasWei <= GrantedCap`.
- Sponsored (Gas Manager policy set; paymaster not in `specialPaymasters`): preflight `amount <= GrantedCap` (value only) — still needs `CodeAt` for K4 unless `allowContractRecipient`.
- Studio copy: "If this wallet pays its own gas, the cap is ETH sent **plus** gas. Leave headroom. Sponsored runs count ETH sent only."
- Map bundler `ExceededNativeTokenLimit` → `SESSION_POLICY_NATIVE_CAP_EXCEEDED` in `pkg/erc4337/preset/bundler_error.go`. **No remaining-cap chain read in v1** (K7).

**`CodeAndFeeReader` (injected in unit tests, real RPC in production):**

```go
type CodeAndFeeReader interface {
    CodeAt(ctx context.Context, addr common.Address) ([]byte, error)
    MaxFeePerGas(ctx context.Context) (*big.Int, error) // eip1559.SuggestFee maxFee
}
```

Unit tests **inject** this. A covering-grant case mocks `CodeAt → []byte{}` and `MaxFeePerGas → 2 gwei` (or a test-controlled fee). **Do not** claim covering-grant numeric preflight returns `""` with `withdrawTestServer`'s nil RPC as-is — that server has no reader, so K4/K7 would fail closed, not pass. Uncovering-grant (no `nativeSpendCap`, or recipient not in list) still 400 **before** any RPC: those checks run first.

**Message contract change (intentional, same PR as withdraw):** today's `FormatSessionPolicyNativeNotAllowed` must **not** say "re-grant" because no REST shape could converge. After Track A, the same **code** is reused for "grant has no native permission", and the message **must** advise re-grant. `TestFormatSessionPolicyNativeNotAllowed` is rewritten, not loosened. **Do not land this formatter rewrite while `ExecuteWithdraw` is still a blanket MA v2 refusal** — that would tell Studio to re-grant a native withdraw that still cannot succeed. One PR owns both (PR A3 below). Two formatters until withdraw is grant-aware is the only acceptable split; this spec picks **one PR**.

`TestHooksForAlwaysScopesSelectors` is **split**, not deleted:

- `TestAllowlistInputs_ERC20RowsAlwaysScopeSelectors` — every `allowedActions` row still `HasSelectorAllowlist=true` with non-empty selectors.
- `TestAllowlistInputs_NativeRecipientRowsAreWildcardSelector` — native rows are `false` with empty selectors, and **only** those rows.
- The coupling comment moves: blanket refusal is gone; if native rows ever flip back to `true`, empty-calldata preflight would be wrong again.

**`ExecuteWithdraw` control flow** (replaces the block at `aggregator/rpc_server.go` ~184 that currently runs after recipient hex validation and **before** amount parse):

1. Existing required-field and **invalid recipient hex** checks. Invalid recipient still wins (`TestExecuteWithdraw_InvalidRecipientTakesPrecedence`) — no native code.
2. Parse amount: numeric positive int, or `MAX` (case-insensitive). Invalid amount here.
3. If token is ETH (case-insensitive) and `UsesModularAccountV2()`:
   - **MAX without sponsorship** (`SponsorshipPolicyID() == ""`): refuse, matching `ETHTransferProcessor` (`cannot use MAX amount without sponsorship`). No reader needed.
   - **Cheap preflight (no RPC):** no usable policy / no `nativeSpendCap` → `SESSION_POLICY_NATIVE_NOT_ALLOWED`; recipient not in `nativeRecipients` → `SESSION_POLICY_RECIPIENT_NOT_ALLOWED`. Uncovering-grant unit tests still 400 here with nil RPC.
   - **Resolve `CodeAndFeeReader`.** Production: the same chain RPC withdraw already uses for balance (move that resolution **before** covering-grant preflight). Missing reader / RPC error → fail closed, not skip. Unit tests: inject `CodeAndFeeReader`.
   - **Covering-grant preflight** (needs reader): `CodeAt` unless `allowContractRecipient`; self-funded `amount + estimatedGasWei`; sponsored value-only. Covering-grant tests mock empty code + 2 gwei fee; they **must not** assert `""` on `withdrawTestServer` as-is (nil RPC).
   - MAX **with** sponsorship: resolve balance via the same reader, then preflight that amount (value-only vs cap, plus `CodeAt`).
4. Existing chain-reader balance checks and `BuildWithdrawalCalldata` (reader already in hand).

The **node** path (`executeRealETHTransfer`) is the same: cheap checks first; then `ethClient` as `CodeAndFeeReader` (`CodeAt` + `eip1559.SuggestFee`); fail closed if `ethClient` is nil on a real MA v2 send. Simulation with a resolvable policy uses the same reader when present.

REST mapping in `handlers_wallets.go` already keys `badRequest` off `SESSION_POLICY_NATIVE_NOT_ALLOWED`; add `SESSION_POLICY_RECIPIENT_NOT_ALLOWED`, `SESSION_POLICY_RECIPIENT_NOT_EOA`, `SESSION_POLICY_NATIVE_CAP_EXCEEDED` the same way.

**Payable `contractWrite`:** after selector coverage passes:
- **No `nativeSpendCap`:** pass. Trust the allowlisted call (Uniswap ETH-in). Do **not** treat this as a native-send coverage miss.
- **`nativeSpendCap` present:** run native preflight with `Kind=NativeValue` (recipient zero) against that cap. Batch: compare **sum** of per-call `values` to the cap (`NativeTokenLimitModule` sums `executeBatch` values; node-level value is applied to the last sub-call today). Do **not** treat the contract as a native recipient.

Uniswap ETH-in therefore works on a Uniswap-only grant. It only shares the native cap when the owner also opted into native-send (NT module is then installed and cannot distinguish swap-value from send-value).

**Simulation / `nodes:run`:** when `vm.db` can resolve a policy, run selector + native-value preflight on:

- `ETHTransferProcessor` simulation path (today preflight is only in `executeRealETHTransfer`).
- `ContractWriteProcessor` Tenderly/`shouldSimulate` path (today `preflightSessionGrantCoverage` is only on real UserOp single-call ~984 and atomic batch ~1275).

Skip rules unchanged: no policy / empty permission classes. `IsSimulation` is not a license to hide a coverage miss. Studio preview of `ethTransfer` must not look green then fail deployed. Payable Uniswap on a Uniswap-only grant must look green — it is authorized.

#### A.4 SDK builders and Studio grant-screen copy

**SDK (`ava-sdk-js`, not this repo) — handoff:**

```ts
// packages/sdk-js/src/v4/builders/sessionPolicy.ts  (existing SessionPolicyActions)

SessionPolicyActions.nativeTransfer({
  recipients: Address[];   // required, non-empty, no zero address; EOAs (gateway eth_getCode)
  capWei: bigint;          // required, > 0n
  allowContractRecipient?: boolean; // default false; any-function, ERC-20 uncapped
})

// Payable-write native cap without ethTransfer recipients:
SessionPolicyActions.nativeValueCap({ capWei: bigint })

SessionPolicyActions.merge([
  SessionPolicyActions.uniswapV3Capability(chainId, {
    capToken,
    approveTokens: [capToken, weth], // purpose-complete: demote ETH→WETH must work
  }),
  // only if the workflow also sends ETH to an address:
  SessionPolicyActions.nativeTransfer({ recipients: [treasury], capWei: parseEther("0.05") }),
])
```

`uniswapV3Capability` is the **complete Uniswap purpose** (K0): router swap + cap-token approve + WETH approve/deposit/withdraw. It **does not** set `nativeSpendCap` / `nativeRecipients`. Uniswap ETH-in is payable `contractWrite` on those selectors, **not** `nativeTransfer`. Do not ship `merge(swap, approve(USDC only))` — that is the AA23 demote bug.

If two `nativeTransfer` builders are merged:

```
nativeTransfer({ recipients: [alice], capWei: 0.1e18 })
nativeTransfer({ recipients: [bob],   capWei: 0.05e18 })
→ nativeRecipients = {alice, bob}   // union, dedupe, max 20
→ nativeSpendCap   = 0.1e18         // max, not min, not sum
```

On-chain there is one `NativeTokenLimitModule` scalar, so per-recipient budgets cannot be preserved. Max is the union analog for that scalar. Min would silently shrink the 0.1 send. Sum would invent 0.15 neither builder stated.

Merging Uniswap + one `nativeTransfer` has exactly one native cap (from the native builder). Do not reopen two-grants-per-runner.

Coverage helpers (`actionsCover` / `missingActions`) grow a native analog: an `ethTransfer` to `to` is covered iff `nativeRecipients` contains `to` (case-insensitive) and `nativeSpendCap` is present. Payable Uniswap is covered by the Uniswap builder's selectors, not by native recipients.

Do not add `nativeTransfer` (or `nativeRecipients`) to `uniswapV3Capability`. That would be a **different purpose** (send ETH to people). `nativeValueCap` is **optional** and not required for Uniswap to work.

**Studio grant screen (handoff, copy to pin):**

Purpose first. The primary question is “what should this agent be allowed to do?”, compiled from the workflow.

- **Uniswap purpose** (workflow has a Uniswap / swap node): one consent, already complete. Copy: “Allow Uniswap swaps, including using ETH (wrap to WETH if needed).” Do **not** ask a second question for WETH approve, wrap, or payable ETH-in. Show an advanced row list if they want details.
- **Send ETH purpose** (workflow has `ethTransfer` / they want withdraw): separate section, only if that purpose is present. Title: "Allow this agent to send ETH." Body: "Only to the addresses you list, up to the cap, until expiry. If this wallet pays its own gas, gas counts against the cap. Sponsored runs only count ETH sent. This is not Uniswap and not wrapping to WETH — wrapping is part of Allow Uniswap."
- Recipients: address chips, required when send-ETH is on. Prefer destinations already in the `ethTransfer` node when known.
- Cap: ETH amount, converted to wei.
- Recipients are EOAs. Gateway rejects contracts unless `allowContractRecipient` is on (off by default).
- If the owner enables the exception, the screen **must** say: "This agent may call **any function** on this address. ERC-20 transfers from it are not capped by the token spend limit." That copy is mandatory; a warning-only toast is not a control.
- Safe / contract treasury: use a `contractWrite`, not a native recipient.
- Warning if a recipient overlaps the ERC-20 section: refused at Validate, not merely warned.
- After submit, `supersededPolicyIds` still means the previous grant is gone — compile the **full** current purpose set (Uniswap ∪ send-ETH) so enabling send does not drop swaps.
- Map `SESSION_POLICY_NATIVE_NOT_ALLOWED` → "This agent cannot send ETH to an address. Re-authorize send-ETH (this is not a Uniswap permission)."
- Map `SESSION_POLICY_RECIPIENT_NOT_ALLOWED` → "This recipient is not on the native allow-list. Re-authorize and add it."
- Map `SESSION_POLICY_RECIPIENT_NOT_EOA` → "This address is a contract. Send via a contract write, or re-authorize with the contract-recipient exception (any function, ERC-20 uncapped)."
- Map `SESSION_POLICY_NATIVE_CAP_EXCEEDED` → "This send exceeds the remaining ETH cap. Re-authorize with a higher cap, or send less."
- Map `SESSION_POLICY_TARGET_NOT_ALLOWED` on a Uniswap node → "This swap needs a token that was not in the Uniswap permission (often WETH after wrapping). Re-authorize Uniswap."
- **Stop** mapping native failures to "do not re-grant / send with the owner key only."
- **Stop** presenting Uniswap as USDC-approve-only.

#### A.5 One grant class (composition)

Today: one usable grant per runner. A second submit **replaces** (`supersededPolicyIds`).

Track A keeps that. Native fields are additional columns on the **same** `SessionPolicy` / same `installValidation` / same owner signature.

**Uniswap does not need native-send on the wallet.** A purpose-complete Uniswap grant is enough for swaps, including ETH-in and ETH→WETH demote. Native-send is only for `ethTransfer` / withdraw to listed EOAs.

Consequence when the workflow **also** sends ETH: adding native-send **replaces** the previous grant (singleton). Studio must compile **both purposes** into one `PreparePolicyRequest` (merge), or the user loses swaps — that would gate Uniswap after they added send. Once merged, NT is installed and Uniswap payable `value` counts against the native cap (module limitation; grant-screen copy should say swap ETH-in shares the send cap).

Rejected alternative: two grants (native class + ERC-20 class) — would require `ActiveSessionPolicyForWallet` to select by capability, a `capabilityId` on the record, and execute-time matching. That is the change `20260806-session-grant-replace-on-submit.md` explicitly deferred. Do not take it for Track A.

#### A.6 Sequence: prepare → sign → submit → first `ethTransfer`

```mermaid
sequenceDiagram
    autonumber
    actor Owner
    participant Studio
    participant GW as Gateway REST
    participant Store as Badger sp:
    participant EP as EntryPoint v0.7
    participant Acc as MA v2 runner
    participant AL as AllowlistModule
    participant NT as NativeTokenLimitModule
    participant TR as TimeRangeModule

    Studio->>GW: POST /wallets/{runner}/policies:prepare<br/>allowedActions? + erc20SpendCap?<br/>nativeRecipients + nativeSpendCap<br/>expiresInSeconds
    GW->>GW: SessionPermissions.Validate (incl. eth_getCode==0)<br/>HooksFor(entity): AL-val + AL-exec + NT + TR
    GW->>GW: PackSessionSignerInstall (Global + exec hooks)<br/>optional replace batch uninstalls prior entity
    GW-->>Studio: policyId, entityId, digest, typedData, validUntil
    Note over Store: prepare stores nothing
    Owner->>Studio: eth_signTypedData_v4(typedData)
    Studio->>GW: POST .../policies:submit (echo + signature)
    GW->>GW: recompute install from echo; recover owner
    GW->>Store: store pending; supersede other usable grants
    GW-->>Studio: 201 SessionPolicy + supersededPolicyIds

    Note over Studio,Acc: later: workflow ethTransfer
    Studio->>GW: nodes:run / deployed executor
    GW->>Store: ActiveSessionPolicyForWallet
    GW->>GW: PreflightNativePermission (recipient, amount)
    alt coverage miss
        GW-->>Studio: SESSION_POLICY_* (fail closed)
    else covered
        GW->>EP: UserOp (session key, executeUserOp wrap,<br/>deferred install on first use)
        EP->>Acc: validate + exec hooks
        Acc->>AL: preUserOpValidationHook (recipient allowed, no selector required)
        Acc->>NT: preUserOpValidationHook (gas if self-funded)
        Acc->>TR: validUntil
        Acc->>AL: preExecutionHook (self-admin latch; ERC-20 no-op on empty data)
        Acc->>NT: preExecutionHook (value <= remaining; not the latch)
        Acc->>Acc: execute(recipient, amount, 0x)
        EP-->>GW: success
        GW-->>Studio: tx hash
    end
```

---

### Track B — Permissions on the user's EOA (EIP-7702)

#### B.1 Vendor: Alchemy MA v2 for both EOA and smart wallet

**Yes — Alchemy Modular Account v2 is the permission layer for both account types.**

| | Derived smart wallet (Track A) | User's EOA (Track B) |
| --- | --- | --- |
| Account | SemiModularAccountBytecode via factory (CREATE2 runner) | `SemiModularAccount7702` at `0x69007702764179f14F51cdce752f4f775d74E139` (`alchemy.sma-7702.1.0.0`) |
| Address | Second address; user funds it | The EOA itself; existing assets |
| EntryPoint | v0.7 `0x0000000071727De22E5E9d8BAf0edAc6f37da032` | **Same** |
| Permissions | Allowlist + NativeTokenLimit + TimeRange + SingleSigner | **Same modules, same REST vocabulary** |
| ERC-1271 | Controller `isSignatureValidation` false | Same (account reverts `SignatureValidationInvalid`) |
| Execute | UserOp, `sender = runner` | UserOp, `sender = EOA` (B5 derivation-check exception) |

**Calibur is not used.** No Phase 2 timeline. See Alternatives (D).

7702-specific: after type-4 and before grant install, assert K13 (`ef0100` + SMA-7702 + bytecode hash). Typed `EOA_DELEGATION_MISSING` if not. Never tx status. Do not invent avs-infra private-doc copy. Finding 3 from the Calibur PoC (sponsored type-4 can look successful without delegating) still applies to **any** 7702 sponsor path, including MA v2 7702 — that is why the code-hash pin exists.

First production chains: **Sepolia and Base**. Spike and first `eoa_7702_execute=true` are those two. Other mainnets wait.

#### B.2 Threat model (EOA vs derived SW)

| | Derived MA v2 (Track A) | EOA 7702 (Track B) |
| --- | --- | --- |
| Assets in blast radius | What the user **funded into the runner** | **Everything at the EOA** (ETH, ERC-20, approvals, ERC-1271-gated positions) |
| Root key | Owner EOA (not the controller) | Same EOA; 7702 delegation is an overlay |
| Controller authority | Session entity + hooks | Session entity + **the same hooks** |
| ERC-1271 | Controller flag off | Controller flag off **and tested** (Permit2 digest must revert) |
| User revoke | `uninstallValidation` (owner tx) + TimeRange | Same, **plus** clear/re-point 7702 delegation |
| Fail-open risk | If gateway preflight is skipped, Uniswap-only payable `value` is unbounded **on-chain by design** (no NT module; trust signed grant) | Delegation "success" without code; re-delegate to a drainer implementation |
| Acceptable only if | Cap + allowlist + expiry live on chain | Same, **and** K13 code pin, **and** 1271 proven deny |

Invariants (code + tests, not comments):

1. Controller is never root-equivalent (`AllowSelfAdministration` remains test-only; `AllowSignatureValidation` remains false).
2. No zero-address allowlist wildcard.
3. No `HasSelectorAllowlist=false` except native-recipient rows.
4. Partner assertion **alone** cannot call policy endpoints (`LevelUserRefusePartner`) or execute as fund authority. `OpWithdrawWallet` is `LevelUser` (user JWT required; a partner assertion header sitting **alongside** a user JWT is not categorically refused — match the code, do not silently promote withdraw in this design). Track B `delegation:*` is `LevelUserRefusePartner`.
5. Per-chain 7702 authorizations only (`chain_id = 0` refused — one signature must not authorize every chain).
6. Only the canonical SMA-7702 address is ever proposed; verify `0xef0100 \|\| impl` plus bytecode hash, not a name.

#### B.3 AVS API / storage / execute path

**Wallet model** (`model/user.go` `SmartWallet`) — additive `omitempty` fields; `make storage-check`:

```go
// Kind is empty/omitted for today's derived wallets.
Kind     string          `json:"kind,omitempty"`      // "" | "derived" | "eoa_7702"
Delegate *common.Address `json:"delegate,omitempty"`  // SMA-7702 implementation
```

**Wallet-record shape for `eoa_7702` (B2):**

| Field | Value |
| --- | --- |
| `Kind` | `"eoa_7702"` |
| `Address` | owner EOA |
| `Owner` | same EOA |
| `Factory` | omitted / zero — **no CREATE2 factory** |
| `Salt` | omitted / nil |
| `Delegate` | canonical SMA-7702 |
| `StaleDerivation` | unused (no factory slot to stale) |

Row creation: `POST .../delegation:submit` (after K13 code assert) upserts this record. `ListWallets` / `GetWallet` include it. Do not go through `CreateWallet` salt allocation.

No new `sp:` key. A 7702 grant is a `SessionPolicy` whose `Runner == Owner` (the EOA). `ActiveSessionPolicyForWallet(db, chain, owner, eoa)` already resolves by runner. Entity uniqueness is per **account**; for 7702 the account *is* the EOA, so derived-SW entity 1 and EOA entity 1 do not collide (different `account` keys in the singleton modules). Hook entity IDs still **equal** the session validation entity on both (K12) — that coexistence fact does not license Alchemy's `hookEntityId: 0`.

Optional additive namespace later if we need delegation audit rows (`eoa:%d:%s` → last verified code hash / tx). Not required for v1 if `SmartWallet.Delegate` + a runtime code check suffice.

**REST:** reuse `/wallets/{address}/policies:*` with `{address}` = EOA.

`requireMAv2SessionWallet` today refuses missing factory / non-MA-v2 factory (`ErrSessionWalletNotMAv2`). Track B **branches**:

```
if rec.Kind == "eoa_7702":
    allow iff rec.Address == owner
         && rec.Factory is nil/zero
         && on-chain code passes K13 (ef0100 || SMA-7702 + hash)
else:
    existing factory == MA v2 check
```

Do not allow preparing a 7702 grant against a derived runner or vice versa.

**New endpoints (Track B, after spike)** — AVS-side consent, Studio-owned UX:

```
POST /wallets/{eoa}/delegation:prepare   // returns 7702 authorization payload, chain-bound
POST /wallets/{eoa}/delegation:submit    // aggregator broadcasts type-4; asserts code(eoa)
GET  /wallets/{eoa}/delegation           // { status, delegate, codeHash }
```

Partner-refused (`LevelUserRefusePartner`). Exact EIP-712/7702 auth bytes are an implementation spike output, not invented here. Studio two-approval UX (delegate vs grant) is a **Studio handoff**; this spec does not quote avs-infra `EOA_7702_Delegation_Consent_Model.md`.

**Execute coexistence:**

```mermaid
flowchart TD
    A[Workflow / withdraw / nodes:run] --> B{Resolve runner}
    B -->|runner is derived CREATE2 MA v2| C[Existing SendUserOp path<br/>sender = runner<br/>session grant on runner]
    B -->|runner == owner and kind eoa_7702| D{code(EOA) is SMA-7702?}
    D -->|no| E[EOA_DELEGATION_MISSING<br/>user-actionable]
    D -->|yes| F[SendUserOp path<br/>sender = EOA<br/>no factory initCode<br/>same SessionResolver]
    C --> G[Track A native + ERC-20 preflight]
    F --> G
```

Implementation sketch (B5 — production send path; **B0 does not touch this**):

- `aa.GetSenderAddressMAv2` is **not** used for 7702; sender is the EOA.
- `preset.SendUserOpMAv2` (`pkg/erc4337/preset/send_v07.go` ~113–131) **requires** `sender == DeriveSenderAddressAuto(owner, factory, salt)` and otherwise refuses (“wrong account type, salt, or factory”). `senderOverride` does **not** bypass that check. B5 adds an exception:
  - `wallet.Kind == "eoa_7702"` AND `sender == owner` AND K13 code check passes → skip factory derivation equality; `initCode` remains omitted (already the code-present path).
  - otherwise keep today's check (derived MA v2 / legacy SimpleAccount refusal).
- Session resolver is unchanged: same controller key, same entity, same deferred install on first use.
- Flag `smart_wallet.eoa_7702_execute: false` default. Spike B0 uses a throwaway script, not this function.
- Fee estimator (`core/taskengine/fee_estimator.go`) needs a 7702 UserOp cost model in a later PR; spike can log gas without changing production quotes.

**A user may have both.** Two runners, two singleton grants, two `aa_sender` values. Workflows name a runner; do not silently fall back from empty derived SW to the EOA (that would move the blast radius without a grant screen).

#### B.4 Studio / SDK consent sequence (handoff)

```mermaid
sequenceDiagram
    autonumber
    actor User
    participant Studio
    participant GW as Gateway
    participant Chain

    User->>Studio: "Automate this EOA" (not the derived SW)
    Studio->>Studio: Consent copy: blast radius is everything at this address;<br/>scoped, expiring, revocable; never root
    Studio->>GW: POST /wallets/{eoa}/delegation:prepare (chainId)
    GW-->>Studio: 7702 auth typed data (chain-bound)
    User->>Studio: Sign 7702 authorization (wallet)
    Studio->>GW: POST .../delegation:submit
    GW->>Chain: type-4 tx (aggregator broadcasts, sponsored)
    GW->>Chain: read code(eoa)
    alt not (ef0100 prefix and impl == SMA-7702 and hash pin)
        GW-->>Studio: EOA_DELEGATION_MISSING
    else delegated
        GW-->>Studio: { delegate, codeHash }
        Note over Studio: Second approval — same grant screen as Track A
        Studio->>GW: policies:prepare (runner=eoa, native+erc20 vocabulary)
        User->>Studio: Sign deferred installValidation
        Studio->>GW: policies:submit
        Note over GW,Chain: First workflow UserOp installs hooks on the EOA account
    end
```

SDK: `SessionPolicyActions.*` are **wallet-kind agnostic** — they compile permission JSON. A separate `prepareEoaDelegation({ chainId })` helper is Track B-only. Do not overload `policies:prepare` to also mean 7702 delegate.

#### B.5 Why Track B does not require Track A, but reuses the vocabulary

Track B spike may hardcode the same hook packing Track A introduces. If Track A has merged, 7702 grants call `SessionPermissions.HooksFor` as-is. If Track A has not merged, the spike copies the hook encoder and rebases. The REST field names (`nativeRecipients`, `nativeSpendCap`, `allowedActions`, `erc20SpendCap`) must match so Studio does not grow two grant compilers.

---

## API / Interface Changes

### OpenAPI (additive)

| Schema / op | Change |
| --- | --- |
| `NativeSpendCap` | New |
| `PreparePolicyRequest` | Optional `nativeRecipients`, `nativeSpendCap`, `allowContractRecipient`; `erc20SpendCap` / `allowedActions` no longer in `required`. Present empty arrays are 400 |
| `SubmitPolicyRequest` | Same additive fields; `required` keeps policyId/entityId/deadline/validUntil/signature/chainId/agentLabel |
| `SessionPolicy` / `SubmitPolicyResponse` | Echo native fields |
| Problem+json `code` | `SESSION_POLICY_NATIVE_NOT_ALLOWED` (semantics change), `SESSION_POLICY_RECIPIENT_NOT_ALLOWED`, `SESSION_POLICY_RECIPIENT_NOT_EOA`, `SESSION_POLICY_NATIVE_CAP_EXCEEDED` |
| Track B (later) | `POST /wallets/{eoa}/delegation:prepare\|submit`, `GET .../delegation` |

Regenerate: `oapi-codegen` (`aggregator/rest/generated/`).

### Go

| Package | Change |
| --- | --- |
| `model.SessionPolicy` | `NativeRecipients`, `NativeSpendCap` omitempty |
| `taskengine.SessionPermissions` | Same + `Validate` / `allowlistInputs` / `HooksFor` |
| `aa` | NativeTokenLimit address, packers, hook builders; **rewrite `SessionSignerUninstallFromInstall` val-then-exec** |
| `session_grant_coverage.go` | `PreflightNativePermission`; rewrite native formatter **in the same PR as withdraw** |
| `vm_runner_eth_transfer.go` | Grant-aware preflight; simulation path too |
| `vm_runner_contract_write.go` | Value preflight on **real and simulation** paths; batch sums values |
| `aggregator/rpc_server.go` `ExecuteWithdraw` | Grant-aware ETH branch |
| `rest/handlers_policies.go` | Map new fields |
| `rest/handlers_wallets.go` | Map new problem codes |
| `pkg/erc4337/preset/bundler_error.go` | Client-failure match for new codes + `ExceededNativeTokenLimit` |
| `pkg/erc4337/preset/send_v07.go` | Track B: derivation-check exception for `eoa_7702` (B5 only) |

### Protobuf

No proto field rename. Withdraw already uses `token == "ETH"`. No `make protoc-gen` required for Track A.

### SDK / Studio

Handoff doc (see PR plan): builders, merge semantics (recipients = union, cap = max), grant-screen copy, error maps. Not implemented in this repo.

---

## Data Model Changes

| Item | Additive? | Notes |
| --- | --- | --- |
| `SessionPolicy.NativeRecipients` / `NativeSpendCap` / `AllowContractRecipient` | Yes (`omitempty`) | Same `sp:%d:%s:%s` key |
| OpenAPI required-array relaxation | Yes (weaker server) | Old payloads still valid |
| `SmartWallet.Kind` / `Delegate` (Track B) | Yes (`omitempty`) | Empty Kind = derived, today's records |
| New `eoa:` namespace | Not in v1 | Reconsider if we need delegation history |
| `FeeLedgerKey` | Unchanged | Out of scope |

Run `make storage-check` (vs `origin/main` before merging to `main`; vs `origin/staging` on the feature PRs). Expected: no breaking key/model diffs.

`Grant.InstallCall` already stores the exact hook set; uninstall/replace must not re-derive from live structs. Adding NativeTokenLimit hooks does not require a migration of old rows — old rows keep their install bytes. **New grants need a rewritten `SessionSignerUninstallFromInstall` (val-then-exec).** Old 3-hook rows still tear down on chain (allowlist tuple lands on AL-val). The **unit test** changes: `TestUninstallReversesIntoStoredOrder` expects `[TR-val data, AL-val data, empty]`, not the old flat reverse `[TR, empty, AL]`. One splitter; no 3-hook compatibility path.

---

## Alternatives Considered

### (A) Keep refusing native; tell users to wrap-to-WETH

**Rejected.** Wrapping is a different product (WETH `deposit` + `approve` + router) and is already the Auto demote path when native is insufficient (`FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`). Users still cannot withdraw ETH from the runner or `ethTransfer` to a treasury. The refusal message currently sends them in a non-converging loop. Track A exists because this is a real hole, not a documentation issue.

### (B) `HasSelectorAllowlist=false` on recipients **without** a native cap

**Rejected.** Wildcard-selectors on an address with no value cap lets the session key drain the runner's entire ETH balance to that address (and call any function if it is a contract). NativeTokenLimitModule is the HOW MUCH; the allowlist is only WHERE. Both are required for native send.

### (C) Global grant (or zero-address wildcard) as the native mechanism

**Rejected.** `address(0)` in AllowlistModule is "any address for these selectors" — send anywhere. A global validation without hooks can `installValidation` itself (`aa.SessionGrant.Validate` already refuses this; selector-scoped spike `scripts/spike/selector_scoped` proved the other form). Production grants are already global-**with**-hooks; we are not widening the validation. We are adding constrained hook rows.

### (D) Calibur for Track B (EOA)

**Rejected / not using.** Direct-tx gas is proven (~128k) but the PoC failed open on ERC-1271, hook flags, and sponsored delegation success-without-code. We will not author a second permission system. No Phase 2. Vendor is Alchemy MA v2 for both EOA and smart wallet.

### (E) MA v2 7702-only Track B (no Calibur)

**Accepted.** This is the Track B vendor: `SemiModularAccount7702` + the same modules as Track A.

### Other alternatives (brief)

| Idea | Why not |
| --- | --- |
| Two grants per runner (native class + ERC-20 class) | Breaks singleton execute guard; recreates the dual-grant brick |
| `replaceExisting` flag | Correct behavior must not be opt-in (`20260806` already rejected this) |
| Selector `0x00000000` on a selector-scoped row | Module reverts `NoSelectorSpecified` before consulting the set |
| Install NativeTokenLimit limit=0 on ERC-20/Uniswap-only grants | **Rejected.** Trust the signed Uniswap grant. Uniswap ETH-in does not require native-send. No follow-up PR. |
| Two native-send builders merge as **min** cap | **Rejected.** Min silently shrinks. **Union of recipients, max of caps.** Not sum. |
| Uniswap grant = router + USDC approve only | **Rejected (K0).** Gates ETH→WETH demote. Capability must include WETH approve/deposit/withdraw. |
| Skip `AllowlistExecHook` on native-only (use NT exec as latch) | NT exec does not revert on `installValidation` / `updateLimits`. Fail-open self-admin |
| Per-policy controller keys | Explicitly not planned (`controllerSessionSigner`); does not shrink blast radius today |
| Kernel / Nexus / Safe {7702} | #658 already passed/eliminated; EP and ecosystem reasons unchanged except the v0.6 argument |

---

## Security & Privacy Considerations

### Blast radius

| Attack / accident | Derived SW + Track A native | EOA 7702 + same grant |
| --- | --- | --- |
| Session key stolen | Spend up to native cap to listed recipients + ERC-20 cap to listed selectors, until expiry or owner uninstall | **Same numeric caps, but sourced from the user's primary balances** |
| Grant compiled without native toggle | `ethTransfer` / withdraw ETH refused; payable `value>0` refused at preflight | Same |
| Native recipient is a contract | **Default refused** (`eth_getCode != 0` at prepare and native-send preflight). Only if `allowContractRecipient=true` (logged): any function, ERC-20 uncapped, native cap still applies to `value` only | Same, worse blast radius — flag still default off |
| Preflight skipped (bug) | On-chain: Allowlist still restricts WHERE; NativeTokenLimit still restricts HOW MUCH **if the grant opted into native**. Uniswap/ERC-20-only: payable `value` unbounded on-chain **by design** (no NT module) | Same on ERC-20-only 7702 grants |
| Silent widening at execute | Forbidden. Gateway may only pack what `HooksFor` encoded and the owner signed | Same |
| Partner token | Cannot prepare/submit/revoke grants (`LevelUserRefusePartner`). Partner assertion **alone** cannot withdraw (`LevelUser` needs a user JWT). A partner header *alongside* a user JWT on withdraw is today's `LevelUser` behavior — not changed here | Track B `delegation:*` is partner-refused |
| ERC-1271 / Permit2 | Controller cannot answer `isValidSignature` | **Must be proven on the 7702 spike** with a Permit2-shaped digest |
| Fake 7702 delegate | n/a | Pin implementation address + code hash; refuse `chain_id=0` |
| User re-delegates away | n/a | Typed `EOA_DELEGATION_MISSING`; do not keep executing |

### Fail closed

- Unknown / unreadable policy → do not sign as owner fallback (`session_policy.go` already errors rather than skip).
- >1 usable grant → `SESSION_POLICY_AMBIGUOUS`.
- Native intent without native fields → `SESSION_POLICY_NATIVE_NOT_ALLOWED`.
- NativeTokenLimit `updateLimits` is **not** exposed through the session key. Quote A0 item 7 — do not look for the wrong revert:
  - `installValidation` / `uninstallValidation` (outer selector, not wrapped in `execute`) → **`SpendingRequestNotAllowed`** (`AllowlistExecHook`).
  - `execute(NativeTokenLimitModule, 0, updateLimits(...))` → **`AddressNotAllowed`** (NT is not an allowlisted target). Allowlist exec does **not** revert `SpendingRequestNotAllowed` here: the outer selector is `execute`. `NativeTokenLimitExecHook` provides neither guarantee.
  Known module addresses stay refused as native recipients even with `allowContractRecipient`. Do not add an AVS API to raise the cap without a new owner signature (replace grant).

### Privacy

Grant material stays secret-grade (not logged, not listed). Native recipients/caps are display fields on `GET` policy (like `allowedActions` today) — they are what the manage screen shows, not the signature.

---

## Observability

| Signal | Where | Notes |
| --- | --- | --- |
| `session grant cannot authorize a native ETH transfer` | `ETHTransferProcessor` Warn today | Change to structured: `code`, `policy_id`, `recipient`, `amount`, `has_native_cap`, `recipient_listed` |
| Withdraw native refusal | `rpc_server.go` Warn | Same fields |
| Payable value refusal | contract-write Warn (new) | `value`, `target`, `policy_id` |
| `ExceededNativeTokenLimit` | bundler error mapper | Client failure, not Sentry-fan (same as today's native code in `IsClientUserOpFailure`) |
| Track B `EOA_DELEGATION_MISSING` | execute path Error/Warn | Include `code_hash`, `expected_delegate` |
| `allowContractRecipient=true` | prepare/submit Warn | `policy_id`, `recipients`, `code_hashes` — exception is auditable |
| Metrics (follow-up) | `metrics/` | counters: `session_native_preflight{code=...}`, `eoa_delegation_check{result=ok\|missing\|wrong_impl}` |

Do not log `InstallCall`, signatures, or controller keys.

Alerting: a sudden spike in `SESSION_POLICY_NATIVE_NOT_ALLOWED` after Track A ships is **expected** (Studio not yet compiling native fields) — treat as a client-adoption dashboard, not a pager, for one release. A spike in `ExceededNativeTokenLimit` **after** preflight is a preflight bug (pager).

---

## Rollout Plan

### Track A

1. **Spike PR (no REST):** Sepolia proofs A.1 **(1)–(7)** (includes self-admin L11). Feature-flag not needed; spike is `scripts/spike/` or `//go:build integration`.
2. **Types/OpenAPI PR:** additive fields, Validate, generated types. Old grants still pack identically if native fields absent — **must be byte-identical** for ERC-20-only `HooksFor` (test: golden install calldata unchanged).
3. **Hook packing PR:** NativeTokenLimit packers + **uninstall val-then-exec rewrite**. Update `TestUninstallReversesIntoStoredOrder` to `[TR-val, AL-val, empty]`. Do not merge until A0 (6)(7) and the 5-hook unit test are green.
4. **Preflight + withdraw PR (one PR):** grant-aware preflight, formatter rewrite with re-grant copy, `ExecuteWithdraw` control flow, simulation paths. Do not ship the new copy on the old blanket withdraw. Production always has `CodeAndFeeReader`; unit tests inject it.
5. **SDK/Studio handoff** after OpenAPI is on `staging`.
6. **Rollback:** stop compiling `nativeRecipients`/`nativeSpendCap` in Studio. Already-installed native grants remain until expiry/replace; gateway cannot silently drop hook enforcement. To disable the *capability*, do not ship a flag that unpacks native rows as selector-scoped — that would break opted-in users. Rollback = don't merge; after merge, owners replace with an ERC-20-only grant (supersede + deferred uninstall).

### Track B

1. Spike/PoC PR on **Sepolia and Base**: delegate a test EOA to SMA-7702, install a **non-root** session grant, prove 1271 deny, prove scoped execute, prove revoke. **Production execute remains the derived-SW path.**
2. Config pin: per-chain `sma_7702_delegate` + code hash in `config/gateway.example.yaml` (Sepolia + Base first).
3. Storage/API for `kind=eoa_7702` and delegation endpoints.
4. Production execute switch is a **later** PR, explicitly named, after spike evidence is in the change log. Feature flag: `smart_wallet.eoa_7702_execute: false` default.

### Staged exposure

- Track A: Sepolia live tests, then mainnet after Studio grant-screen review.
- Track B: spike and first execute-flag-on on **Sepolia and Base**. Other mainnets wait.
- Track B execute only after 1271/delegation assertions are CI-enforced, not checklist items.

---

## Test Plan

### Track A — unit (CI)

- `SessionPermissions.Validate`: native-only OK; ERC-20-only OK; mixed OK; overlap refused; zero recipient refused; native send without cap refused; empty grant refused; ERC-20 without cap still refused; **contract recipient refused unless `allowContractRecipient`**; present `[]` vs omit.
- `allowlistInputs`: ERC-20 rows `HasSelectorAllowlist=true`; native rows `false`; no native row on an allowed-action target; **`SpendCap == nil` does not panic**.
- `HooksFor`: ERC-20-only golden **unchanged**; native-only includes **AllowlistExec +** NT validation+exec; mixed includes all five.
- `PackNativeTokenLimitInstallData` golden vs `cast`.
- `PreflightNativePermission` table: empty-calldata no cap / wrong recipient / contract recipient / self-funded `amount+gas` vs cap / sponsored value-only / payable value **without** cap **passes** (Uniswap-only) / payable value **with** cap / batch sum / non-MA-v2 skip.
- `TestETHTransferPreflightSessionGrant`: covering grant injects `CodeAndFeeReader` (empty code + 2 gwei) then returns `""`; without native fields returns `SESSION_POLICY_NATIVE_NOT_ALLOWED` **with** re-grant copy (no reader required); simulation path also calls it when db present.
- `TestExecuteWithdraw_*`: invalid recipient still wins; uncovering grant 400 **before** RPC (no cap / bad recipient); covering-grant numeric **injects `CodeAndFeeReader`** (empty code + 2 gwei) and then asserts preflight `""` — **not** with nil RPC; MAX self-funded refused for sponsorship; lowercase `eth` still hits preflight.
- `TestUninstallReversesIntoStoredOrder`: **updated** to val-then-exec `[TR-val data, AL-val data, empty]`. Comment records the old flat reverse. No 3-hook compatibility path.
- `TestUninstallMixedNativeGrantValThenExecOrder`: 5-hook `hookUninstallData` grouping (K10).
- Contract-write **simulation** path invokes selector + value preflight.
- `preflightSessionGrantCoverage` + value: `exactInputSingle` with `value>0` and no native cap → native code, not target code.
- REST: prepare/submit round trip with native fields; `SubmitPolicyResponse` field parity test updated; old body without native fields still 201.
- `IsClientUserOpFailure` includes new codes.
- `TestHooksForAlwaysScopesSelectors` replaced by the split tests above — **failing because a native row is false is not a regression**; failing because an ERC-20 row is false **is**.

### Track A — live Sepolia (`//go:build integration`)

Must be green before calling Track A **done**. Pattern: `session_grant_replace_live_test.go` / `scripts/spike/`. Self-funded runner (no Gas Manager on laptop — existing convention).

| # | Proof | Fail closed if |
| --- | --- | --- |
| L1 | Native-only grant: `ethTransfer` of 1e12 wei to listed recipient succeeds | AA23 / `NoSelectorSpecified` |
| L2 | Same grant: `ethTransfer` to a **non-listed** recipient reverts `AddressNotAllowed` (or preflight `RECIPIENT_NOT_ALLOWED`) | Send succeeds |
| L3 | Cap `X`: send `X+1` reverts / preflight cap code. **Self-funded `amount == X` also reverts `ExceededNativeTokenLimit`** | Over-cap or exact-cap self-funded send succeeds |
| L4 | Purpose-complete Uniswap grant **without** native-send: `ethTransfer` refused; USDC **and WETH** `approve` work; payable router/`WETH.deposit` `value` **succeeds**; WETH-in swap after demote succeeds | Native send succeeds without send-ETH purpose, **or** Uniswap ETH-in / WETH demote refused |
| L5 | Mixed grant **with** native fields: both USDC approve and listed `ethTransfer` work | Either class broken by composition |
| L6 | Native withdraw REST to listed recipient works; to other recipient refused | Withdraw is a second pack path — do not only test the node |
| L7 | Replace: new grant without native fields; prior native entity **gone**. **Read `NativeTokenLimitModule.limits(entity, account)`** (expect 0), plus Allowlist + signer. Receipt is not evidence | Receipt success but leftover cap (the #717 class of bug) |
| L11 | Native-only key: `installValidation` → `SpendingRequestNotAllowed`; `execute(NT, updateLimits)` → `AddressNotAllowed` | Self-admin |
| L8 | Mixed grant **with** native cap: payable `contractWrite` value under cap succeeds; over cap refused. Uniswap-only (no native cap): payable value succeeds with no NT module | Mixed grant lets over-cap swap value through; Uniswap-only ETH-in refused |
| L9 | Controller `isValidSignature` as the account still reverts (flag off) | 1271 accidentally enabled |
| L10 | Bytecode presence of NativeTokenLimitModule at the v2.0.0 address on Sepolia | Wrong address packed |

L1–L7 and L11 are **release-blocking** for Track A. L8–L10 can land in the same integration binary.

### Track B — spike (not production)

| # | Proof |
| --- | --- |
| B1 | Type-4 delegate to SMA-7702; assert K13 (`ef0100` + impl + hash), not tx status |
| B2 | Install session grant with Track A vocabulary; `isSignatureValidation=false` |
| B3 | Permit2-shaped `isValidSignature` digest reverts `SignatureValidationInvalid` |
| B4 | Scoped native/ERC-20 UserOp from aggregator bundler succeeds |
| B5 | Disallowed target reverts; over-cap reverts |
| B6 | Owner uninstall or TimeRange expiry; subsequent UserOp fails |
| B7 | Derived SW runner on the same owner still uses the old path (coexistence) |

Production Track B execute is **not** done when B1–B7 pass; it is done when those assertions are in CI and the feature flag is explicitly turned on in a later PR.

---

## Open Questions

**None remaining.** User resolved 2026-09-17. Folded into Key Decisions.

| # | Resolution | Where |
| --- | --- | --- |
| Q1 | **No** native module on Uniswap/ERC-20-only grants. Trust the signed Uniswap grant (K0: purpose-complete, including WETH wrap/unwrap). Uniswap works **without** a native-send grant. Gateway must **not** refuse payable `value` on allowlisted Uniswap selectors. Follow-up PR A6 **cancelled**. | K0, K1 |
| Q2 | Uniswap does not need native-send. Two `nativeTransfer` builders → **union of recipients, max of caps** (not min, not sum). Max 20 recipients. | K3 |
| Q3 | Max native recipients = **20**. | K3 |
| Q4 | EOA-only native recipients; `allowContractRecipient` off by default (earlier). | K4 |
| Q5 | Track B production: **Sepolia and Base**. Other mainnets wait. | K8 |
| Q6 | **Drop Calibur.** Alchemy MA v2 for both EOA (7702) and derived smart wallet. No Phase 2. | K8, Alternatives D |
| Q7 | Per-user controller keys **not** in Track B v1. Shared controller as today. | K8, K9 |
| Q8 | Do **not** read on-chain remaining cap every send in v1. GrantedCap + K7 value+gas. | K7 |

---

## Risks (severity)

| Severity | Risk | Mitigation |
| --- | --- | --- |
| **High** | `HasSelectorAllowlist=false` on a contract recipient = any function + ERC-20 uncapped | Prepare+preflight `eth_getCode==0`; flag off by default; Safe uses `contractWrite` |
| **High** | Track B 1271 if someone sets `AllowSignatureValidation` | Keep false; spike test; `SessionGrant.Validate` / packing tests |
| **High** | Uniswap grant omits WETH / wrap (gates the swap) | K0: `uniswapV3Capability` includes cap token **and** WETH approve/deposit/withdraw; preflight `TARGET_NOT_ALLOWED` until re-grant |
| **High** | Studio adds `nativeTransfer` to every Uniswap grant (over-purpose) | Uniswap purpose must not emit `nativeRecipients`. Send-ETH is a separate purpose. |
| **Med** | Message change for `SESSION_POLICY_NATIVE_NOT_ALLOWED` breaks Studio maps that look for "do not re-grant" | Same PR as grant-aware withdraw; pin new copy in tests; Studio coordinated in the same milestone |
| **Med** | Self-funded gas consumes native cap; users think cap is "ETH sent" | Preflight `amount+estimatedGas`; grant-screen copy; MAX self-funded refused |
| **Med** | Uniswap-only payable `value` unbounded **on-chain** | **Accepted.** Trust signed Uniswap grant. Gateway preflight does not demand native-send for allowlisted payable calls. |
| **Med** | NativeTokenLimitModule address wrong on a chain | L10 bytecode check; same presence-verify pattern as AllowlistModule comments |
| **Low** | OpenAPI required-array relaxation surprises generated clients | Fields remain sendable; SDK regen in handoff |
| **Low** | Replace batch gas grows with extra hooks | Already capped by `maxOnChainTeardowns`; native adds 2 hook entries |

---

## References

- Alchemy AllowlistModule source: [modular-account `AllowlistModule.sol` v2.0.x](https://github.com/alchemyplatform/modular-account/blob/v2.0.x/src/modules/permissions/AllowlistModule.sol) — `_checkCallPermission`, `NoSelectorSpecified`
- Alchemy NativeTokenLimitModule source: [modular-account `NativeTokenLimitModule.sol` v2.0.x](https://github.com/alchemyplatform/modular-account/blob/v2.0.x/src/modules/permissions/NativeTokenLimitModule.sol)
- Deployments: [modular-account `deployments/v2/Deployments.md`](https://github.com/alchemyplatform/modular-account/blob/develop/deployments/v2/Deployments.md) — NativeTokenLimit `0x00000000000001e541f0D090868FBe24b59Fbe06`; SMA-7702 `0x69007702764179f14F51cdce752f4f775d74E139`; Allowlist v2.0.1 `0x00000000003e826473a313e600b5b9b791f5a59a`
- Alchemy docs: [Adding session keys — native token and/or gas limit](https://www.alchemy.com/docs/wallets/smart-contracts/modular-account-v2/session-keys/adding-session-keys)
- This repo: `core/taskengine/session_permissions.go`, `session_grant_coverage.go`, `session_grant_native_test.go`, `vm_runner_eth_transfer.go`, `vm_runner_contract_write.go`, `core/chainio/aa/ma_v2_hooks.go`, `ma_v2_install.go`, `ma_v2_uninstall_from_install.go`, `model/session_policy.go`, `api/openapi.yaml`, `aggregator/rpc_server.go` (`ExecuteWithdraw`), `aggregator/rest/handlers_policies.go`, `aggregator/withdraw_native_test.go`, `core/taskengine/schema.go` (`sp:` keys), `docs/changes/20260806-session-grant-replace-on-submit.md`, `PLAN_PARTNER_PAYMENTS.md` §4.1, `FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`, `scripts/spike/selector_scoped/`, `scripts/spike/permission_hooks/`
- Discussion: [EIP-7702 Delegation Contracts #658](https://github.com/AvaProtocol/EigenLayer-AVS/discussions/658)
- MA v2 7702 PoC: [chrisli30/mav2-7702-poc](https://github.com/chrisli30/mav2-7702-poc)
- Calibur PoC (rejected vendor; cited only in Alternatives D): [chrisli30/calibur-7702-poc @ verify/ava-protocol-standalone](https://github.com/chrisli30/calibur-7702-poc/tree/verify/ava-protocol-standalone)
- SDK pattern: `SDK_HANDOFF_DURABLE_EXECUTION.md`

---

## docs/changes entry outline

This spec **is** `docs/changes/20260917-native-eth-and-eoa-permissions.md` (Status: Proposed, Branch: staging). Header: Date 2026-09-17. Vendor: Alchemy MA v2 for both tracks. No Calibur. No NT module on Uniswap-only grants. Track B first chains: Sepolia and Base.

---

## PR Plan

Independently reviewable PRs, all targeting **`staging`**. Conventional Commit titles. Split Track A and Track B. Track A is several small PRs. Track B starts with a spike that does **not** enable production execute.

### Track A

#### PR A0 — `test: spike native ETH session hooks on Sepolia`

- **Files/components:** `scripts/spike/native_eth_hooks/` (new), possibly `core/chainio/aa/ma_v2_hooks.go` packers if the spike needs them (prefer packing in the spike first, promote in A2).
- **Dependencies:** none.
- **Description:** Live Sepolia script proving A.1 (1)–(7): empty calldata to a listed **EOA** recipient; unlisted reverts; NT cap `X+1` and **self-funded `amount==X`**; ERC-20 rows stay selector-scoped; teardown reads `NativeTokenLimitModule.limits`; **native-only key cannot `installValidation` or `updateLimits`**. Does not change REST or preflight. Evidence (tx hashes, revert data) in the PR body. Self-funded; no Gas Manager.

#### PR A1 — `feat: add native session-grant fields to OpenAPI and storage model`

- **Files/components:** `api/openapi.yaml`; `make` oapi-codegen outputs under `aggregator/rest/generated/`; `model/session_policy.go`; `core/taskengine/session_permissions.go` (`Validate` only; `HooksFor` errors **"native permission packing is not implemented" before `allowlistInputs`** if native fields are set); `aggregator/rest/handlers_policies.go` mapping; `aggregator/rest/handlers_policies_test.go`; `core/taskengine/engine_session_policy_test.go` (`TestSessionPermissionsValidation`).
- **Dependencies:** none (can parallel A0).
- **Description:** Additive `nativeRecipients` / `nativeSpendCap` / `allowContractRecipient`. Relax OpenAPI `required`. **Omit vs `[]`:** present empty arrays are 400. Validate rules (K2/K3/K4) including `eth_getCode==0`. `attachDeclaredPermissions` copies new fields. ERC-20-only `HooksFor` stays byte-identical. `make storage-check` green.

#### PR A2 — `feat: pack NativeTokenLimitModule and fix uninstall hook order`

- **Files/components:** `core/chainio/aa/ma_v2_hooks.go`, `ma_v2_hooks_test.go` (golden); `core/chainio/aa/ma_v2_uninstall_from_install.go` (**val-then-exec rewrite**); `ma_v2_uninstall_from_install_test.go` (`TestUninstallMixedNativeGrantValThenExecOrder`; **`TestUninstallReversesIntoStoredOrder` updated to `[TR-val, AL-val, empty]`**); `core/taskengine/session_permissions.go` (`allowlistInputs` nil-safe, `HooksFor` always includes `AllowlistExecHook`); split of `session_grant_native_test.go`.
- **Dependencies:** A0 (evidence, including (6)(7)), A1 (types).
- **Description:** Implement K1 hook order and K5 (AllowlistExec always). ERC-20-only `HooksFor` byte-identical to pre-A2 (golden). Native-only and mixed pack NT hooks. Promote spike packers into `aa`. **One uninstall splitter; no 3-hook flat-reverse compatibility path.** **Do not merge until** the 5-hook uninstall unit test is green **and** A0 reads `limits(entity,account)==0` after replace. `TestHooksForAlwaysScopesSelectors` replaced by the split tests.

#### PR A3 — `feat: grant-aware native preflight for ethTransfer and withdraw`

- **Files/components:** `core/taskengine/session_grant_coverage.go` (or `session_grant_native.go`); `session_grant_native_test.go`; `session_grant_coverage_test.go`; `core/taskengine/vm_runner_eth_transfer.go` (real **and** simulation); `core/taskengine/vm_runner_contract_write.go` (value on real **and** simulation; batch sum); `aggregator/rpc_server.go` (`ExecuteWithdraw` control flow); `aggregator/withdraw_native_test.go`; `aggregator/rest/handlers_wallets.go`; `pkg/erc4337/preset/bundler_error.go` (+ test).
- **Dependencies:** A2.
- **Description:** Delete blanket MA v2 refusal. `PreflightNativePermission` with K7 inequality (`eip1559.SuggestFee`, 1_300_000 gas units, 2 gwei floor). **Rewrite native error copy and grant-aware withdraw in this same PR.** New codes `SESSION_POLICY_RECIPIENT_NOT_ALLOWED`, `SESSION_POLICY_RECIPIENT_NOT_EOA`, `SESSION_POLICY_NATIVE_CAP_EXCEEDED`. ExecuteWithdraw: cheap no-RPC checks first; covering grant uses injected/`CodeAndFeeReader`. Payable `value>0` preflight. Simulation paths included. Studio maps `SESSION_POLICY_RECIPIENT_NOT_EOA`.

#### PR A4 — `test: live Sepolia native ETH session grant`

- **Files/components:** `core/taskengine/session_grant_native_live_test.go` (`//go:build integration`) covering L1–L11 as practical; reuse `session_grant_testhelper_test.go` patterns.
- **Dependencies:** A2–A3.
- **Description:** Release-blocking live proofs. On-demand, not per-PR CI (same as replace live test). Track A is **not done** until L1–L7 and L11 pass on Sepolia.

#### PR A5 — `docs: SDK/Studio handoff for native ETH session grants`

- **Files/components:** `SDK_HANDOFF_NATIVE_ETH_SESSION_GRANT.md` (new, this repo); pointer from `docs/changes/20260917-native-eth-and-eoa-permissions.md` once approved.
- **Dependencies:** A1 (stable OpenAPI).
- **Description:** K0 purpose-matched compile. `uniswapV3Capability` is complete (router + cap token + WETH approve/deposit/withdraw). `nativeTransfer` only when send-ETH is a purpose. Merge = **union of recipients, max of caps**. Uniswap ETH-in is not native-send. Grant-screen copy is purpose-first. Error maps including `SESSION_POLICY_RECIPIENT_NOT_EOA` and Uniswap `TARGET_NOT_ALLOWED` → re-authorize Uniswap. No Go behavior.

#### PR A6 — **cancelled / out of scope**

Would have installed NativeTokenLimit exec-only limit=0 on ERC-20/Uniswap-only grants. User decision Q1: **trust the signed Uniswap grant; do not put a native module on a Uniswap grant.** Uniswap ETH-in is authorized by the Uniswap selectors, not by native-send.

### Track B

#### PR B0 — `test: spike MA v2 7702 scoped session grant on Sepolia and Base`

- **Files/components:** `scripts/spike/mav2_7702_eoa/` (new). No production execute-path changes. Optional read-only config comments.
- **Dependencies:** none (does not require Track A; may copy hook packing or import A2 if already merged).
- **Description:** B1–B7 on **Sepolia and Base**: delegate to `SemiModularAccount7702` `0x69007702764179f14F51cdce752f4f775d74E139`; assert K13 (`ef0100` + impl + hash), **not tx status**; install non-root session key with allowlist + native cap + expiry; prove 1271 deny (Permit2-shaped digest); scoped UserOp succeeds; disallowed/over-cap fail; revoke/expiry fail closed; derived SW on the same owner still separate. **Does not enable production execute and does not patch `SendUserOpMAv2`.** PR body = verification write-up (hashes, revert selectors).

#### PR B1 — `feat: pin SMA-7702 delegate and code-hash in gateway config`

- **Files/components:** `core/config/config.go`, `config/gateway.example.yaml`, tests.
- **Dependencies:** B0 evidence.
- **Description:** Per-chain delegate address + **implementation bytecode hash** for **Sepolia and Base** first. Comparison is `code[0:3]==0xef0100 && code[3:23]==delegate && hash(impl)`. Refuse `chain_id=0` authorizations when the later API lands. Execute flag `eoa_7702_execute: false` default.

#### PR B2 — `feat: register EOA 7702 wallets (storage + list/create)`

- **Files/components:** `model/user.go` (`Kind`, `Delegate` omitempty); wallet REST handlers; `make storage-check`.
- **Dependencies:** B1.
- **Description:** Additive wallet kind. Record shape: `Address==Owner`, `Factory` omitted, `Salt` nil, `Delegate` set. Created by delegation:submit, not salt allocation. No grant execute yet. List/get include `runner == owner`.

#### PR B3 — `feat: EOA 7702 delegation prepare/submit API`

- **Files/components:** `api/openapi.yaml`; REST handlers; broadcast type-4; **mandatory `code(EOA)` assert**; problem code `EOA_DELEGATION_MISSING`.
- **Dependencies:** B2.
- **Description:** AVS-side consent API. Partner-refused (`LevelUserRefusePartner`). Mandatory K13 code assert after type-4. Does not install session hooks (that's policies:prepare). Studio handoff for two-step UX; do not invent avs-infra private copy.

#### PR B4 — `feat: session grants on eoa_7702 runners`

- **Files/components:** `engine_session_policy.go` `requireMAv2SessionWallet` extension; reuse Track A `SessionPermissions` (depends on A1–A2 **or** includes a subset if Track A slipped — prefer depending on A2).
- **Dependencies:** B3, A2 (vocabulary).
- **Description:** `policies:*` against runner=EOA. `requireMAv2SessionWallet` branches on `Kind==eoa_7702` (no factory; K13 code check) vs derived factory. Still no production workflow execute if the flag is false (prepare/submit allowed on Sepolia for testing).

#### PR B5 — `feat: execute workflows from delegated EOA when eoa_7702_execute is on`

- **Files/components:** `pkg/erc4337/preset/send_v07.go` (**derivation-check exception** for `eoa_7702` + K13; initCode stays omitted); session resolver wiring; `ETHTransferProcessor` / contract-write `aa_sender`; fee estimator follow-up if needed; flag default **false**.
- **Dependencies:** B0–B4, Track A preflight (A3) so native/ERC-20 coverage applies to EOA runners too.
- **Description:** The only PR that can move EOA funds in production. Must cite B0 1271/code-assert evidence. **Cannot use today's `sender == DeriveSenderAddressAuto(...)` as-is.** Rollback = flag off.

#### PR B6 — `docs: SDK/Studio handoff for EOA 7702 consent`

- **Files/components:** `SDK_HANDOFF_EOA_7702.md`; `docs/changes` update.
- **Dependencies:** B3 OpenAPI.
- **Description:** Delegation helpers vs policy helpers; blast-radius copy; coexistence with derived SW; never propose a non-canonical delegate.

### Suggested merge order

```
A0 ──┐
A1 ──┼── A2 ── A3 (preflight+withdraw) ── A4 (live)     (Track A done)
     └── A5 (docs, after A1)

B0  (parallel, any time; does not patch SendUserOpMAv2)
B0 + A2 ── B1 ── B2 ── B3 ── B4 ── B5 (flag off) ── B6
```

Track A user-facing capability is live after **A3 + A4 + Studio consuming A5**. **PR A6 is cancelled.** Track B production fund movement is **only B5 with the flag on** (Sepolia and Base first), which should not be in the same release train as A3.
