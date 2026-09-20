# SDK / Studio hand-off — Native ETH session grants

**Audience:** `ava-sdk-js` and Studio maintainers. **Status:** as-built on `staging` (Track A PRs #789–#794).
**Source of truth for shapes:** [`api/openapi.yaml`](api/openapi.yaml) on `staging` — regenerate from it, don't hand-transcribe.
**Why (design rationale):** [`docs/changes/20260917-native-eth-and-eoa-permissions.md`](docs/changes/20260917-native-eth-and-eoa-permissions.md). That spec is design intent; **this doc is what shipped**. Where they disagree, this wins.

No Go behavior in this PR. Gateway packing, preflight, and REST fields are already on `staging`.

---

## What shipped (one paragraph)

An owner can now grant the agent **empty-calldata native ETH send** (`ethTransfer` / REST withdraw) to a short list of EOAs, capped in wei, on the derived MA v2 smart wallet. That is a **separate purpose** from Uniswap. Uniswap is complete without native-send: router swap + cap-token approve + WETH approve/deposit/withdraw, and payable ETH-in on those selectors. There is still **one usable grant per `(chainId, runner)`**. Studio/SDK **compile** purposes into that one grant; the gateway **enforces** the signed rows and never invents any at execute.

`SESSION_POLICY_NATIVE_NOT_ALLOWED` now means “this grant cannot send ETH to that address — re-grant with `nativeRecipients`.” Stop mapping it to “do not re-grant / use the owner key.”

---

## Step 0 — regenerate types (required, mechanical)

```bash
yarn openapi-download   # curl staging api/openapi.yaml -> packages/types/openapi/openapi.yaml
yarn types-gen          # openapi-typescript -> packages/types/src/openapi.gen.ts
```

After regen, `PreparePolicyRequest` / `SubmitPolicyRequest` / `SessionPolicy` contain optional:

| field | type | notes |
|---|---|---|
| `nativeRecipients` | `EthereumAddress[]` | omit when unused. Present `[]` is **400**. `maxItems: 5` |
| `nativeSpendCap` | `{ amount: string }` | wei, `^[0-9]+$`, no reset |
| `allowContractRecipient` | boolean | default false; logged when true |
| `erc20SpendCaps` | `{ token, amount }[]` | A7, already on staging; singular `erc20SpendCap` is the alias |

`allowedActions` and `erc20SpendCap` are **no longer OpenAPI-required**. Native-only omits them. Present `allowedActions: []` is **400**. `merge()` must not emit empty arrays.

---

## Purposes (compile these, not raw selectors)

The grant screen is “allow this purpose.” Completeness is the **compiler’s** job (K0). Least privilege applies to the purpose (Uniswap, not “send ETH anywhere”), not to omitting steps the purpose needs.

| Purpose | Compile | Do **not** compile |
|---|---|---|
| **Uniswap** (Auto / Uniswap `contractWrite`) | Router swap selector(s) the node emits (at least `exactInputSingle` `0x04e45aaf`). `approve` (`0x095ea7b3`) for **cap token and catalog WETH**. `WETH.deposit` (`0xd0e30db0`) + payable `value` on deposit and on the router. `WETH.withdraw` (`0x2e1a7d4d`) if the node can unwrap. ERC-20 cap on the **cap token**. | `nativeRecipients`. `nativeSpendCap`. `NativeTokenLimitModule`. Arbitrary EOA sends. |
| **Send ETH** (`ethTransfer` or withdraw) | `nativeRecipients` + `nativeSpendCap` | Uniswap router. Token approve. Wrap. |
| **Both** | **One** merged grant: Uniswap rows ∪ native-send rows. Recipients union, native cap = **max** of native builders. | Two grants. Uniswap-only that drops send. Native-only that drops Uniswap. |
| **Payable write without send** (Lido `submit`, studio#1674) | `nativeSpendCap` **without** `nativeRecipients` (`nativeValueCap`) | `ethTransfer` / withdraw |

Studio already compiles cap-token **+ WETH `approve`** (2026-08-06). **New:** `deposit` / `withdraw` and treating payable ETH-in as Uniswap, not as send-ETH.

Addresses come from **that chain’s catalog** (Base USDC ≠ Ethereum USDC). Same purpose on another chain = another Enable.

---

## SDK builders (`SessionPolicyActions`)

Suggested home: `packages/sdk-js/src/v4/builders/sessionPolicy.ts` (existing).

```ts
SessionPolicyActions.uniswapV3Capability(chainId, {
  capToken,
  wrappedNative?, // default: catalog WETH for chainId
})
// emits allowedActions: router + capToken.approve + WETH.approve/deposit/withdraw
// emits erc20SpendCap / erc20SpendCaps on capToken
// does NOT set nativeRecipients or nativeSpendCap

SessionPolicyActions.nativeTransfer({
  recipients: Address[];              // required, non-empty, no zero address
  capWei: bigint;                     // required, > 0n
  allowContractRecipient?: boolean;   // default false
})

SessionPolicyActions.nativeValueCap({ capWei: bigint }) // payable-write cap, no ethTransfer

SessionPolicyActions.merge([
  SessionPolicyActions.uniswapV3Capability(chainId, { capToken }),
  // only if the workflow also sends ETH to an address:
  SessionPolicyActions.nativeTransfer({ recipients: [treasury], capWei: parseEther("0.05") }),
])
```

### `merge` rules

- **Uniswap builders never set a native cap.**
- Two `nativeTransfer`s: **union of recipients** (dedupe, **max 5** — `MaxNativeRecipients`), **max of caps**. Not min (silently shrinks). Not sum (invents budget).
- Different ERC-20 tokens: **union**. Same token: **max** (same rule as native).
- Do not emit `allowedActions: []` or `nativeRecipients: []`.
- Coverage: `ethTransfer` to `to` is covered iff `nativeRecipients` contains `to` (case-insensitive) **and** `nativeSpendCap` is present. Payable Uniswap is covered by Uniswap selectors, **not** by native recipients.

`MaxNativeRecipients = 5` (OpenAPI `maxItems` and `session_permissions.go`). 20-row deferred replace AA23s; install of 20 rows is not the gate.

### Wrap vs WETH cap (singleton)

AllowlistModule can cap a token **only** if that token’s **unioned** selectors ⊆ `{transfer, approve}`. Uniswap wrap adds WETH `deposit`/`withdraw`, so **a WETH spend cap and Uniswap wrap cannot coexist**. Gateway `Validate` 400s that shape.

**Wrap wins:** compile selector union first, then **omit** any spend cap whose token is not transfer/approve-only. Keep deposit/withdraw. Copy: “WETH can’t be spend-capped while Uniswap wrap is on (deposit/withdraw). USDC is still capped.” Do not drop wrap to attach a WETH amount.

---

## Studio UI contract (§A.0.1)

This is **not** Alchemy Gas Manager (one policy, many networks, gas sponsorship). Session grants are **per chain and per runner**. Enabling on Base does not enable Ethereum.

```
permission card = (chainId, smartWalletAddress)
```

Every `list` / `prepare` / `submit` / `DELETE` must send the **card’s** `chainId`, never JWT `aud` as a substitute. EIP-712 domain `chainId` + `verifyingContract` = **this** runner.

**On/Off** is not an on-chain read:

```
On  = list items on this (chainId, runner) with status pending or active
      whose compiled purposes cover the row
Off = no such usable grant
```

`pending` = signed, install may not have mined → still **On**. First UserOp installs. Do not wait. A JWT / “Sign in” is **not** a grant.

Toggles that look independent (Uniswap / Send ERC-20 / Send ETH) are **purposes compiled into one grant**, not three gateway rows. Turning a purpose **on** while others are On: compile **union**. Turning one **off**: recompile remaining and Enable (replace), or `DELETE` if none remain. **Do not DELETE** on modal close.

### Copy to pin

- Card subtitle: “Permissions apply only on {network name}.”
- Uniswap Enable: “Allow Uniswap swaps on {network}, including wrapping ETH to WETH if needed.” One consent. No second toggle for wrap / ETH-in.
- Send ETH: “Allow this agent to send ETH.” Body: “Only to the addresses you list, up to the cap, until expiry. If this wallet pays its own gas, gas counts against the cap. Sponsored runs only count ETH sent. This is not Uniswap and not wrapping to WETH — wrapping is part of Allow Uniswap.”
- Recipients are EOAs. If `allowContractRecipient`: **mandatory** copy — “This agent may call **any function** on this address. ERC-20 transfers from it are not capped by the token spend limit.” A toast is not a control. Safe/treasury: `contractWrite`, not a native recipient.
- After submit: “Saved. The agent can use this on the next action on {network}. It is not enabled on other networks.”
- JWT-only: “Signed in. This wallet has no agent permission on {network} yet.”

---

## Error mapping (REST `code`)

Stop mapping native failures to “do not re-grant / send with the owner key only.”

| Gateway `code` | Studio copy |
|---|---|
| `SESSION_POLICY_NATIVE_NOT_ALLOWED` | This agent cannot send ETH to an address. Re-authorize send-ETH (this is not a Uniswap permission). |
| `SESSION_POLICY_RECIPIENT_NOT_ALLOWED` | This recipient is not on the native allow-list. Re-authorize and add it. |
| `SESSION_POLICY_RECIPIENT_NOT_EOA` | This address is a contract. Send via a contract write, or re-authorize with the contract-recipient exception (any function, ERC-20 uncapped). |
| `SESSION_POLICY_NATIVE_CAP_EXCEEDED` | This send exceeds the remaining ETH cap. Re-authorize with a higher cap, or send less. |
| `SESSION_POLICY_TARGET_NOT_ALLOWED` on a Uniswap node | This swap needs a token that was not in the Uniswap permission (often WETH after wrapping). Re-authorize Uniswap. |

---

## Self-funded gas vs sponsored

`NativeTokenLimitModule` is installed **only** when `nativeSpendCap` is present. Then **every** self-funded UserOp burns gas from that cap, including zero-value ERC-20 on a `nativeValueCap` grant. Sponsored UserOps (Alchemy Gas Manager) decrement **value only**. Uniswap-only grants have **no** NT module; payable ETH-in is unbounded on-chain (accepted: trust the signed Uniswap grant).

Once NT is installed (because send-ETH is on), Uniswap payable `value` **also** decrements that cap — the module cannot tell swap-value from send-value. That is why Uniswap builders must not set a native cap.

---

## Out of scope

- Alchemy Gas Manager network checklist (`alchemy_paymaster_policy_id`) — ops/config, not the grant screen.
- EIP-7702 EOA session keys (Track B) — same vocabulary later; not this card’s first ship.
- Gateway packing / preflight — already on `staging` (A1–A4).
- Putting `NativeTokenLimitModule` on Uniswap-only grants (A6 cancelled).

---

## Do not

- Add `nativeTransfer` / `nativeRecipients` to `uniswapV3Capability`.
- Ship `merge(swap, approve(USDC only))` — that is the AA23 demote-ETH→WETH bug.
- Emit present empty arrays.
- Present Uniswap as USDC-approve-only.
- Treat catalog rows as separate grants (singleton; `supersededPolicyIds` means the previous grant is gone).
- Cap WETH while wrap selectors are on the same grant.
- Use JWT `aud` as `chainId`.
- Wait for on-chain install before painting On.
