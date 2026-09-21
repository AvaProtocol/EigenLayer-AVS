# SDK / Studio handoff — EOA 7702 session grants

Track B: session keys on the **user's EOA** via EIP-7702 → Alchemy `SemiModularAccount7702`. Complementary to the derived MA v2 runner (Track A). Blast radius is **everything at the EOA**.

B2–B5: wallet record, delegation API, `policies:*` against `runner == EOA`, and production execute when `eoa_7702_execute: true` (default **false**; Sepolia/Base + canonical pin only). Workflows must name the EOA runner; the gateway does not fall back from an empty derived SW to the EOA.

## Two approvals

1. **Delegate** — `POST /api/v1/wallets/{eoa}/delegation:prepare` then `:submit`. User signs an EIP-7702 authorization (not a session grant). Partner assertions are refused (`LevelUserRefusePartner`).
2. **Grant** — same `policies:prepare` / `policies:submit` as Track A, with `{address}` = the EOA. `SessionPolicyActions.*` are wallet-kind agnostic.

Do not overload `policies:prepare` to mean 7702 delegate.

## Delegation payload

Prepare returns `{ chainId, delegate, nonce, digest }`.

- `delegate` is always `0x69007702764179f14F51cdce752f4f775d74E139` (`alchemy.sma-7702.1.0.0`). Never propose another implementation.
- `digest` is `SetCodeAuthorization.SigHash()`: `keccak256(0x05 ‖ rlp([chainId, address, nonce]))`.
- `nonce` is the **EOA's** nonce. Aggregator broadcasts the type-4, so the EOA is the authority, not the tx sender — **not** `nonce+1` (that is only for self-sponsored type-4).
- `chainId=0` is refused. First chains: Sepolia `11155111` and Base `8453`.

Submit body: `{ chainId, nonce, signature }` (65-byte ECDSA, v 0/1 or 27/28). Gateway recovers the authority (must be the path EOA), **refuses if `nonce` ≠ the EOA's current pending nonce** (`DELEGATION_STALE_NONCE` — a signed-but-unusable nonce must not spend gas), broadcasts a type-4 paid by the **per-chain `controller_private_key`** (not the AVS EigenLayer identity key), then **K13**. Receipt status is not evidence.

- `200` + `status: delegated` — code matches the pin; wallet row upserted.
- `202` + `status: pending` — type-4 was sent; designation not yet visible. **Poll GET. Do not resubmit** (EOA nonce is unchanged; a second broadcast spends controller gas again).
- `409` `EOA_DELEGATION_MISSING` — code is not the pin.

GET `/wallets/{eoa}/delegation` reads code, not tx history (`missing` or `delegated`). When it returns `delegated`, it also **upserts** the `eoa_7702` wallet row (idempotent, owner-gated). That is how a 202 becomes grantable without a second submit.

## Wallet record

After submit, list/get include:

| Field | Value |
| --- | --- |
| `kind` | `eoa_7702` |
| `address` / owner | the EOA |
| `salt` | empty |
| `factoryAddress` | omitted |
| `delegate` | SMA-7702 |

Do not create this via `POST /wallets` (CREATE2 salt). A user may have **both** a derived runner and an `eoa_7702` runner. Workflows name a runner; do not fall back from empty derived SW to the EOA.

## Session grants

`policies:*` against the EOA requires the stored `kind=eoa_7702` row **and** a live K13 code check. Same Track A permission JSON (`nativeRecipients`, `nativeSpendCap`, `allowedActions`, `erc20SpendCaps`). Controller `isSignatureValidation` stays false.

## Execute (B5)

`SendUserOpMAv2` accepts `sender = EOA` only when all of: `eoa_7702_execute: true`, stored `kind=eoa_7702`, `sender == owner`, K13. `initCode` is never attached. Session resolver is unchanged (grant keyed by the EOA). Rollback: set the flag false.

A workflow whose runner is the derived CREATE2 wallet is unaffected. Do not set `aa_sender` to the owner EOA unless that workflow is meant to spend from the EOA.

If `alchemy_paymaster_policy_id` is set, sponsorship is requested with `sender = EOA`. Gas Manager simulated the 7702 EOA the same as a deployed MA v2 runner; **AA23 was missing-grant, not sender-type rejection** (MA v2 takes the entity from the nonce key, so with no grant validation reverts `ValidationFunctionMissing` instead of returning `SIG_VALIDATION_FAILED`; undeployed CREATE2 is **AA20**). `dummySignatureV07` was already correctly framed. Full K7 is `TestEOA7702SponsorshipK7_Sepolia` (in-process `EOA7702Execute=true`, live grant via deferred install, production `SendUserOpMAv2`): Paymaster non-zero, EOA Δ = 1 wei, `success` + `actualGasCost > 0`, NT `limits` Δ = value only, then the same op self-funded (value+gas). That run needs a Sepolia policy **without** a production custom-rules webhook. Sponsorship refusal is fatal (no silent self-fund). Flag stays false until that run is green. Do not cite an AA23-only simulation as "sponsorship was tested."

## Native recipients (Track A interaction)

A 7702-delegated EOA has 23 bytes of designation, so `eth_getCode` is non-empty. Listing it in `nativeRecipients` without `allowContractRecipient` is still refused (K4: unscoped empty-calldata on an account that can `execute`). The error is `native recipient is a 7702-delegated EOA; set allowContractRecipient`, not "send via contractWrite". Already-installed grants are unaffected; re-prepare and new grants hit this.

## Consent copy (Studio)

Blast radius is the EOA itself. Scoped, expiring, revocable; never root. Two-step UX (delegate vs grant) is Studio-owned; this repo does not quote avs-infra private docs.
