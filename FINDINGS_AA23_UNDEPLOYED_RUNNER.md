# Findings — AA23 on Auto swap: userOp for an undeployed (counterfactual) smart wallet

**Date:** 2026-08-05
**Found during:** Studio live test of the Auto-mode consent flow
(`studio/PLAN_AUTO_MODE_GRANT_CONSENT.md` §C, chat conversation
`45994417-a890-4b83-8bfb-47cafa9acd55` on local dev)
**Status:** Fixed on staging (2026-08-05) — root cause was deferred-action encoding, not missing initCode

## Summary

An on-demand Auto Uniswap swap (`swapUniswapAuto` → taskengine contract-write →
bundler) failed at **gas estimation** with `AA23 reverted` on a **counterfactual
(never-deployed) smart wallet** that also had a **pending session grant** (first
use = deploy + deferred install). Initial read of the symptom pointed at
missing initCode; deploy-on-first-use was already present. The actual bug was
the session resolver attaching raw `InstallCall` instead of the encoded
deferred-action payload, so account validation always reverted AA23 on first
use of any pending grant. The client surfaced `invalid request: AA23` —
meaningless to an end user.

Funding the address is NOT sufficient for deployment (USDC ≠ code), but
funding alone would not have unblocked this path: the deferred-action framing
was wrong regardless of on-chain code.

## Evidence (local Sepolia gateway, 2026-08-05)

`gateway.log` (two identical attempts, 13:25:07 and 13:25:34 PDT):

```
INFO  taskengine/vm_runner_contract_write.go:735  🔍 DEPLOYED WORKFLOW: UserOp sender configuration
      {"owner_eoaAddress": "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
       "senderOverride_smartWallet": "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd",
       "aa_sender_var": "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"}
INFO  taskengine/vm_runner_contract_write.go:719  Using paymaster for sponsored transaction
      {"paymaster": "0xd856f532F7C032e6b30d76F19187F25A068D6d92", ...}
ERROR preset/bundler_error.go:42  bundler: UserOp transaction failed, workflow execution FAILED
      {"bundler_error": "estimating gas: eth_estimateUserOperationGas:
        validation reverted: [reason]: AA23 reverted",
       "method": "atomicBatch[approve,exactInputSingle]",
       "contract": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
       "sender_smart_wallet": "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd",
       "owner_eoa": "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788"}
```

On-chain state at the time (Sepolia `eth_getCode`, latest):

| Smart wallet | Code | Note |
|---|---|---|
| `0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd` | `0x` (**not deployed**) | The failing sender. Held 20 USDC (funded ≠ deployed). |
| `0x5d814Cc9e94b2656f59Ee439D44aa1b6CA21434f` | 1692 hex chars (deployed) | Same owner's other runner; past AAVE ops deployed it. |

Both wallets derive from owner EOA `0xc60e71bd…c788`.

Additional context: the gateway holds **two session policies** for the failing
runner `0x46aD` (an expired July grant and an active Aug-5 grant, 500-USDC cap,
`validUntil` ≈ Sep 4). If the policy-install model is "deferred action applied
on first use," note that first use can ALSO be first deployment — the install
and the account deployment must compose in one userOp for this to ever work on
a fresh runner.

## Repro

1. Owner EOA with a **counterfactual** (never-deployed) smart wallet on
   Sepolia; fund it with USDC so balance checks pass.
2. From Studio chat: Auto-mode Uniswap swap using that wallet as runner
   (`swapUniswapAuto` with `senderOverride` = the counterfactual address).
3. Gateway builds the batch → bundler `eth_estimateUserOperationGas` →
   `AA23 reverted`, before anything is submitted on-chain.

## Root cause (confirmed in code)

Deploy-on-first-use already existed in `SendUserOpMAv2` (`isDeployed` →
`Factory`/`FactoryData`). The AA23 was **not** "missing initCode".

The session resolver attached the grant's raw `InstallCall` (installValidation
calldata) as `SessionAuthorization.DeferredData`. The account expects the
**encoded** deferred-action payload:

```
locator(21) || deadline uint48(6) || installCall
```

produced by `userop.EncodeDeferredActionData`. `PrepareSessionGrant` already
builds that encoding (as `PreparedSessionGrant.DeferredData`) but never
persists it — only `InstallCall` + `Deadline` are stored. On first use the
resolver re-fed the raw install calldata into
`WrapSignatureMAv2Deferred`, so validation reverted as **AA23** with no useful
reason. That hits every **first use of a pending grant**, including (and
especially visible for) first-use that also deploys a counterfactual runner.

On-chain address check for the failing runner:

| Address | Factory | Salt | Deployed? |
|---|---|---|---|
| `0x46aD…` | MA v2 semi-modular | 0 | no (counterfactual) |
| `0x5d814…` | SimpleAccount v0.6 | 0 | yes (legacy AAVE runner) |

## Fix applied (staging)

1. **Resolver encodes deferred data** from stored `Deadline` + `InstallCall`
   via `EncodeDeferredActionData(FallbackSignerLocator(), …)` in
   `NewSessionResolver`.
2. **Verification gas** adds the deploying seed when `Factory` is set on a
   deferred/module-entity operation (deploy + install share one validation
   frame).
3. **Gas Manager sponsorship** uses `op.Signature` as `dummySignature` when
   already set (deferred estimation grant must not be replaced by a plain
   dummy).
4. **Deploy safety:** refuse initCode when `sender` does not match
   `derive(owner, factory, salt)` instead of sending a mismatched UserOp.
5. **Client-facing AA23 / not-deployed** messages are more readable than the
   raw bundler string.

## Residual / config notes

- Local `config/gateway.yaml` had **no** `gas_manager_policy_id` /
  `ALCHEMY_GAS_POLICY_ID`. MA v2 ignores the v0.6 `paymaster_address`; without
  Gas Manager the account must hold ETH for gas. That is a separate local-dev
  config gap, not the AA23 cause.
- Granting on an undeployed runner remains valid: first use deploys + installs
  in one UserOp (spike-proven path, now wired correctly).

## Studio-side follow-ups (tracked in the studio repo, not this one)

- Translate gateway AA23 / `SMART_WALLET_NOT_DEPLOYED` into user-readable chat
  copy with a recovery action.
- (Fixed 2026-08-05) coverage check compared `validUntil` (unix **ms**) against
  epoch seconds, letting the expired July grant "cover" the swap — the on-chain
  expiry would have rejected it regardless; chain enforcement was correct.
