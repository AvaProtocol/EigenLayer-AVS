# SDK / Studio handoff — EOA 7702 session grants

Track B: session keys on the **user's EOA** via EIP-7702 → Alchemy `SemiModularAccount7702`. Complementary to the derived MA v2 runner (Track A). Blast radius is **everything at the EOA**.

This AVS PR is **B2+B3+B4**: wallet record, delegation API, and `policies:*` against `runner == EOA`. **Execute is off.** `eoa_7702_execute: true` still fails gateway boot until B5. Do not send UserOps with `sender = EOA` through production `SendUserOpMAv2` yet.

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

## Consent copy (Studio)

Blast radius is the EOA itself. Scoped, expiring, revocable; never root. Two-step UX (delegate vs grant) is Studio-owned; this repo does not quote avs-infra private docs.
