# Findings — AA23 on Auto swap: factory mismatch was a red herring

**Date:** 2026-08-05  
**Follow-up to:** deferred-action AA23 fix on staging; initial hand-off titled
“factory_address / account_provider mismatch”.  
**Found during:** Studio live test of Auto-mode Uniswap swap, owner EOA
`0xc60e71bd…c788`, Sepolia.  
**Status:** **Resolved** (merged to staging as PR #706). Root cause of Studio
Auto AA23 was `methodCalls[].contractAddress` dropped on the `nodes:run`
extract→recreate round-trip (approve fell back to the router). Packing fix
verified live on Sepolia under Gas Manager.

**New open issue (2026-08-06):** Auto **ETH/WETH sell** still AA23s on prod after
Studio demote — session grant allowlists **USDC approve only**. See
**`FINDINGS_AA23_WETH_SELL_SESSION_SCOPE.md`** (handoff for fix).

**AVS follow-ups (this branch):** strip temporary `AA23_DEBUG` logs; refuse
session grant prepare/submit for non–MA v2 runners (SimpleAccount).

**Not AVS:** Studio Fix C / consent Deny / false-Confirmed hydrate — stay in
Studio plans. Gas Manager custom-rules webhook still optional ops wiring.

---

## TL;DR (current)

| # | Issue | Verdict |
|---|--------|---------|
| 1 | Factory mismatch (`0xB99BC2` vs MA v2) | **Red herring** — raw config field; send path uses `EffectiveFactory` → `0x0000…17c6…` |
| 2 | Studio Auto runner / picker | **Fixed (Studio)** — Auto on `0x46aD…`; v0.6 chip filtered |
| 3 | Missing Alchemy paymaster policy | **Fixed (config)** — `alchemy_paymaster_policy_id` / `ALCHEMY_PAYMASTER_POLICY_ID` |
| 4 | Zero balance without sponsorship | **Fixed as guard** — fail-fast + clear error; sponsorship is the product path |
| 5 | Misleading boot logs / v0.6 paymaster probe | **Fixed (code)** — effective factory/EP/policy logged; MA v2 skips v0.6 probe |
| 6 | Moralis “no API key” WARN | **Fixed (config)** — top-level `moralis_api_key` (fee USD only; not UserOps) |
| 7 | AA23 on first-use / Auto batch under Gas Manager | **Fixed (#706)** — preserve per-call `contractAddress`; live Sepolia mine |
| 8 | Temporary `AA23_DEBUG` instrumentation | **Stripped** (cleanup after #706 soak) |
| 9 | Session grant on SimpleAccount runner | **Fixed** — prepare/submit refuse non–MA v2 |

---

## Resolution (2026-08-05 evening)

### What failed in Studio (19:31 PDT)

```
v0.7 user operation prepared { sender: 0x46aD…, deferred: true, deploying: true,
  wrap_execute_user_op: true, verification_gas_seed: 900000, paymaster_policy_set: true }
→ gas manager declined to sponsor: alchemy_requestGasAndPaymasterAndData … AA23 reverted
```

Prepare log was correct. Failure was only at Alchemy’s sponsorship simulation.

### What the investigation showed

Replayed the **same grant + same approve/swap batch** against live Alchemy Sepolia
with the real app key and policy `bf905871-…`:

| Probe | Result |
|-------|--------|
| `eth_estimateUserOperationGas` (prod-like: 900k vgas, zero fees, deferred est sig) | **OK** |
| `alchemy_requestGasAndPaymasterAndData` (same shape) | **OK** → paymaster `0x2cc0…B633` |
| Fee seeding / higher vgas / body-sig cleared | All **OK** (not required) |
| Off-allowlist simple `execute` | **AA23** (expected under hooks grant) |
| Single on-allowlist `approve` | **OK** |

Then ran the **real** gateway path `preset.SendUserOpMAv2` with the live
controller key, MA v2 factory, deferred install + hooks, atomic batch, and
Gas Manager policy:

| Step | Result |
|------|--------|
| First-use send (deploy + deferred install + approve + swap) | **Mined** `status=1` |
| Tx | [`0xb24e3a73…2755`](https://sepolia.etherscan.io/tx/0xb24e3a73a2d7ad7a243fcfdea167b8d7503d0823a1992cd090d90c261b4d2755) |
| Account | [`0x46aD59…7BDd`](https://sepolia.etherscan.io/address/0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd) (81-byte proxy) |
| Paymaster | `0x2cc0c7981D846b9F2a16276556f6e8cb52BfB633` |
| Follow-up plain entity op (no deferred, still sponsored) | **Mined** `0xc51061a2…6a74` |

On-chain logs for the first tx include factory create, session install / hooks,
USDC `Approval`, USDC/`WETH` transfers, and EntryPoint `UserOperationEvent`
success — i.e. the full Auto swap frame, not just deploy.

### Why Studio still showed AA23 at 19:31

The send path + grant + sponsorship request shape are **not** the durable
bug. That single Studio failure did not reproduce under the same inputs once
the live Alchemy key/policy were used in a direct probe. Most likely causes
for the one-off AA23:

1. **Transient Gas Manager / bundler simulation flake** (or brief rate-limit
   surface as AA23).
2. **Earlier misconfiguration windows** (policy unset, wrong runner) already
   fixed before 19:31, leaving a residual flaky attempt in the same session.

**Not** root causes for case B (ruled out by the successful replay):

- Factory mismatch / wrong initCode  
- Missing / mis-encoded deferred grant  
- Allowlist mismatch on the production batch  
- Zero `maxFeePerGas` before sponsorship (Gas Manager fills fees)  
- Verification seed 900k under-seed for this install size  

### Local Badger note after the probe

The diagnostic `SendUserOpMAv2` **did not** call `OnApplied`, so the live
gateway DB may still show policy `01kz8ebk…` as `pending` even though the
install is on-chain. The send path already heals this: on the next op it sees
the deferred carrier sequence consumed, records the grant applied, and
continues as a plain entity operation. **No manual DB edit required** — retry
Studio Auto on `0x46aD…`.

---

## Case split (historical, still useful)

| # | Runner | Type | On-chain | Outcome |
|---|--------|------|----------|---------|
| A | `0x5d814Cc9…434f` | v0.6 SimpleAccount (salt 0) | **deployed** | Always AA23 under MA v2 path. Studio no longer offers this in Auto. Gateway **fails fast** with derive mismatch before bundler. |
| B | `0x46aD59…7BDd` | MA v2 (salt 0) | **deployed (after probe)** | Correct factory/runner/grant. First-use + sponsorship **works**. |

---

## Evidence timeline (local Sepolia, PDT)

### Early runs (15:36–15:41) — factory red herring + no Gas Manager

- Debug logs printed raw `factory_address: 0xB99BC2…` while `entrypoint` was already v0.7.
- Case A used v0.6 runner `0x5d81…` → AA23 (account-type mismatch).
- Case B used MA v2 `0x46aD…` → AA23 at `eth_estimateUserOperationGas` with
  `paymaster_policy_set: false` and zero wallet balance.

### Live re-probe (same grant + production batch)

| Step | Result |
|------|--------|
| Factory / derive / deferred / hooks | OK |
| `eth_estimateUserOperationGas` (unsponsored) | **Succeeded** for approve+swap batch |
| `eth_sendUserOperation` | Prefund fail (balance+deposit = 0) |

So the durable pre-policy blocker was **sponsorship / prefund**, not factory
mismatch.

### After gateway fix + paymaster policy (19:05–19:31 PDT)

| | Before | After policy |
|---|--------|--------------|
| Path | `eth_estimateUserOperationGas` | `alchemy_requestGasAndPaymasterAndData` |
| Studio 19:31 | — | One AA23 at Gas Manager (did not reproduce) |

### End-to-end success (post-19:31 investigation)

| | Result |
|---|--------|
| Direct `RequestSponsorshipV07` / estimate matrix | All on-allowlist variants OK |
| `SendUserOpMAv2` first-use | Tx `0xb24e3a73…` status 1 |
| `SendUserOpMAv2` follow-up (installed entity) | Tx `0xc51061a2…` status 1 |

---

## Why “factory mismatch” is wrong

1. **Config** fills `SmartWalletConfig.FactoryAddress` with the v0.6 default when
   YAML omits it (`0xB99BC2…`).
2. **Send path** uses `aa.EffectiveFactory` → MA v2 constant under
   `modular_account_v2`.
3. **Deploy-safety** refuses `sender ≠ derive(owner, factory, salt)` before the
   bundler (covers case A and wrong salt).
4. Wallet DB records already store the correct factory per runner
   (`w:` / `wsalt:`).

---

## Session grant dig (case B is not “missing auth”)

| Field | Value (MA v2 runner) |
|-------|----------------------|
| id | `01kz8ebkzgv2th9r32w9qrcw79` |
| status | was `pending`; install applied on-chain by probe |
| entity_id | 1 |
| session_signer | gateway controller `0x82F2…` |
| carrier_nonce | entity 1 + deferred + seq 0 (now consumed) |
| requires_execute_user_op | true |
| install | `installValidation` (`0x1bbf564c`) |
| allowlist | router `exactInputSingle` + USDC `approve` |
| spend cap | 500 USDC |

Owner signature recovers as **raw EIP-712** to `0xc60e…` (correct for MA v2).

Off-allowlist calldata (e.g. bare `execute` to the owner) **does** AA23 under a
hooks grant; the Auto swap batch targets are on-allowlist.

---

## Implemented fixes (gateway / config)

### Send-path guards & logging

| Fix | Status |
|-----|--------|
| Log effective factory / provider / deferred / entity / policy / seed | ✅ |
| Fail-fast derive mismatch (v0.6 runner, wrong salt) | ✅ |
| Fail-fast missing session grant on MA v2 | ✅ |
| Assert deferred `CarrierNonce == op.Nonce` | ✅ |
| Fail-fast zero balance + no paymaster policy | ✅ |
| Prefund error annotation → `ALCHEMY_PAYMASTER_POLICY_ID` | ✅ |
| MA v2 does not claim v0.6 verifying paymaster is “in use” | ✅ |
| Deferred est signature on sponsorship request | ✅ (required; plain dummy → AA23) |
| Heal consumed carrier nonce → mark grant applied | ✅ |

### Config / naming

| Item | Canonical name |
|------|----------------|
| YAML | `alchemy_paymaster_policy_id` |
| Env | `ALCHEMY_PAYMASTER_POLICY_ID` |
| Go | `AlchemyPaymasterPolicyID` |

Legacy names removed. Local + avs-infra use the canonical names; production
Railway env: `ALCHEMY_PAYMASTER_POLICY_ID=bf905871-55d7-4197-a020-605302f4bc87`.

### Boot / observability

| Fix | Status |
|-----|--------|
| Effective factory / v0.7 EP / provider / policy on engine start | ✅ |
| Skip v0.6 verifying-paymaster probe on MA v2 | ✅ |
| Top-level `moralis_api_key` for fee USD | ✅ |

### E2E / unit tests

```bash
go test -tags=integration -count=1 -v \
  -run 'TestMAv2SendRejects|TestMAv2EffectiveFactoryOnSepolia' \
  ./core/taskengine/
```

| Test | Proves |
|------|--------|
| `TestMAv2SendRejectsMissingSessionGrant_Sepolia` | No grant → hard error, not AA23 |
| `TestMAv2SendRejectsSimpleAccountRunner_Sepolia` | Live v0.6 runner refused (derive) |
| `TestMAv2SendRejectsUndeployedWrongSalt_Sepolia` | Wrong salt refused |
| `TestMAv2SendRejectsZeroBalanceWithoutGasManager_Sepolia` | Zero balance + no policy → clear prefund error |
| `TestMAv2EffectiveFactoryOnSepoliaConfig_IgnoresRawV06Factory` | Raw v0.6 factory ignored under MA v2 |

Unit: `pkg/erc4337/preset/send_v07_guards_test.go`.  
Full happy-path (keys + funded salt): `TestMAv2SessionGrantEndToEnd`.

---

## Studio (cross-repo)

- Auto routes to MA v2 `0x46aD…`; consent / grant active (e.g. 500 USDC).  
- Picker filter: v0.6 salt-0 chip removed/filtered.  
- **Retry Auto** on this wallet — account is deployed, grant installed on-chain,
  sponsorship works. Gateway will mark the stored grant applied on the next op
  if DB still says `pending`.

---

## Reproduction / verify checklist

1. Runner `0x46aD…` (not `0x5d81…`).
2. Gateway boot: `paymaster_policy_set: true`, MA v2 skip of v0.6 paymaster probe.
3. Prepare log: `effective_factory` MA v2, `deferred: true|false`, `paymaster_policy_set: true`.
4. Expect success at Gas Manager + mined UserOp (sponsored by `0x2cc0…`).
5. Explorer: first-use tx `0xb24e3a73…`, follow-up `0xc51061a2…`.

---

## Appendix — key addresses (Sepolia)

| Role | Address |
|------|---------|
| Owner EOA | `0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788` |
| Gateway controller / session signer | `0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB` |
| MA v2 AccountFactory | `0x00000000000017c61b5bEe81050EC8eFc9c6fecd` |
| v0.6 SimpleAccount factory (legacy default) | `0xB99BC2E399e06CddCF5E725c0ea341E8f0322834` |
| EntryPoint v0.7 | `0x0000000071727De22E5E9d8BAf0edAc6f37da032` |
| MA v2 runner (Auto) | `0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd` |
| v0.6 runner (do not use in Auto) | `0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f` |
| Uniswap router | `0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E` |
| USDC | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` |
| Alchemy paymaster | `0x2cc0c7981D846b9F2a16276556f6e8cb52BfB633` |
| Alchemy paymaster policy | `bf905871-55d7-4197-a020-605302f4bc87` |
| First-use success tx | `0xb24e3a73a2d7ad7a243fcfdea167b8d7503d0823a1992cd090d90c261b4d2755` |
| Follow-up success tx | `0xc51061a22bfb73015cc13e049cdf867127b108c49dc1fb8e6d105f70d6486a74` |

---

## Supersedes

1. Claim that AA23 on `0x46aD` was caused by building initCode with the v0.6
   `factory_address` — **false** (misleading raw config logs).  
2. Claim that the only remaining issue after deferred-action fix is factory
   config — **false** (paymaster policy was required; first-use now proven).  
3. Claim that unsponsored estimate always AA23s for this batch — **false**
   (estimate can succeed; prefund blocks send without policy).  
4. Claim that first-use under Gas Manager is a durable AA23 bug — **false**
   (full path mined; Studio 19:31 did not reproduce).
