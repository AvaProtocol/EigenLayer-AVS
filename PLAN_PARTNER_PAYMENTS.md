# PLAN: Partner Tenancy — Delegated Simulate now, Fund Authority later

**Status:** Phase 1 IMPLEMENTED 2026-06-30 — partner-delegated simulate (Ed25519 `X-Partner-Assertion`).
Decisions: v1 = direct partner assertion; authKey chainId retained as-is
**Owner:** Chris
**Created:** 2026-06-30
**Related (AVS):** `aggregator/rest/handlers_auth.go`, `aggregator/rest/middleware/jwt.go`,
`aggregator/rest/context.go`, `core/auth/{protocol,user}.go`, `aggregator/key.go`,
`aggregator/rest/{handlers_workflows,handlers_nodes,handlers_triggers}.go`,
`core/taskengine/{engine,executor,fee_ledger,fee_estimator}.go`, `core/config/config.go`,
`pkg/erc4337/preset/builder.go`, `model/workflow.go`, `protobuf/avs.proto`
**Related (Studio):** `PLAN_SOCIAL_IDENTITY.md` (§6 / Q7 — AVS authKey is wallet-scoped, survives unchanged),
`PLAN_SOCIAL_IDENTITY_REGRESSIONS.md`
**Related (AVS):** `PLAN_CHAIN_DECOUPLING.md` (G5 — task is chain-agnostic; authKey chain is auth scope only)

---

## 0. TL;DR

We are adding a **partner (tenant) layer** so third-party apps — Studio today, DeFi projects later — can
let *their own* end users drive AVS, the way Stripe lets a business act for its customers. The design is
organized by **stakes**, and that line is the whole architecture:

| Operation class | What it does | Trust required | Vouchable by partner alone? |
| --- | --- | --- | --- |
| **simulate / runTrigger / runNodeWithInputs** | preview; **no chain effect, no funds move** | partner-level | **Yes** |
| **createTask / execute** | schedules/moves funds via the smart wallet | **wallet/fund authority** | **No** |

Two facts from the code make this clean:

1. **AVS auth is wallet-centric, and the authKey proves EOA ownership only — chain is cosmetic.** The
   JWT `aud` chainId is "API auth scope only; not stamped onto the task"
   ([handlers_workflows.go:46](aggregator/rest/handlers_workflows.go#L46)); request-body chain always
   overrides it ([handlers_nodes.go:40](aggregator/rest/handlers_nodes.go#L40)); no code rejects on a
   chain mismatch. So an authKey is a chain-independent proof of address control.
2. **Simulate already skips wallet-ownership** — "SimulateTask intentionally skips ValidWalletOwner …
   so users can test workflows before deploying smart wallets"
   ([engine.go:3595](core/taskengine/engine.go#L3595)). The *only* thing gating simulate today is "you
   hold a wallet-signed JWT." Nothing about simulate actually needs that wallet to have signed.

Therefore:

- **URGENT (this plan's deliverable): partner-delegated simulate.** Give Studio a **scoped partner
  credential** and let it call the simulate family **on behalf of its socially-authenticated users
  without a per-user wallet signature.** AVS trusts the partner for no-fund operations. This is a pure
  backend change and lets a social-login user preview a workflow before they ever have/sign a wallet.
- **FUTURE (architecture only, do not build now): fund authority + billing.** When funds must move, the
  user grants permission by **signing with their EOA via Uniswap Calibur** — no custody, no mandate
  module of our own. Plus per-partner billing. We **lock the seam** for these now and defer the build.

The architectural invariant we commit to today: **operations are gated by scope, and scope is gated by
stakes.** Simulate-scope = partner trust. Execute-scope = on-chain fund authority (controller key today,
Calibur tomorrow), *never* partner trust alone.

---

## 1. Current model (grounded in code)

| Concern | Today | Citation |
| --- | --- | --- |
| **Auth** | JWT `sub` = owner EOA, `aud` = chainId (**cosmetic default, not a gate**), optional `roles`. 48h. | [handlers_auth.go:39](aggregator/rest/handlers_auth.go#L39), [context.go:22](aggregator/rest/context.go#L22) |
| **Admin key** | CLI mints a 10-year `roles:["admin"]` JWT that can act on **any** wallet — all-or-nothing, unscoped. | [aggregator/key.go:24](aggregator/key.go#L24), [core/auth/user.go:96](core/auth/user.go#L96) |
| **Simulate gate** | Requires a valid JWT; **skips wallet-ownership** (any address allowed). | [engine.go:3595](core/taskengine/engine.go#L3595) |
| **Execute authority** | Single aggregator `ControllerPrivateKey` signs **every** UserOp for every wallet; no on-chain spend limit, no session keys. | [config.go:190](core/config/config.go#L190), [builder.go:1062](pkg/erc4337/preset/builder.go#L1062) |
| **Fees** | Real per-`(chainId, owner)` `FeeLedger` (outstanding/accrued, idempotent record, optional credit limit). | [fee_ledger.go](core/taskengine/fee_ledger.go), [fee_estimator.go:52](core/taskengine/fee_estimator.go#L52) |
| **Task storage** | `avsproto.Task`: `Owner`, `SmartWalletAddress`, trigger/nodes/edges; **chain-agnostic** post-G5; no tenant field. | [model/workflow.go:211](model/workflow.go#L211) |

**The only thing standing between Studio's social users and simulate is the "wallet-signed JWT"
requirement — which simulate doesn't actually need.** That is precisely what the urgent work removes,
safely, by substituting partner trust for the no-fund class.

---

## 2. Architecture: scope-by-stakes + a partner credential

**Partner credential (server-to-server).** A partner authenticates as *itself* with a **signed client
assertion** — a short-lived JWT the partner signs with its own private key, which AVS verifies against
registered public keys. Per-partner, rotatable, nothing secret stored server-side, and **scoped**. This
replaces the temptation to hand Studio the all-or-nothing 10-year admin key (which can also create and
execute on any wallet — far too much authority for a partner).

**Scoped delegation — decided v1: direct partner assertion.** Studio presents its **partner assertion
directly** on simulate calls; there is **no separate token-exchange endpoint** in v1. The assertion (or
a thin per-call wrapper) conceptually carries:

```json
{ "iss": "studio",                        // partner_id → selects registered keys
  "sub": "<end-user address or partner-user-id>",
  "scope": "simulate",                    // simulate | (later) execute
  "aud": "avs-gateway-staging",           // binds to this gateway (anti-replay)
  "exp": "<now + minutes>",               // required, short-lived (≤1h)
  "jti": "<nonce>" }                       // RESERVED for replay cache (future)
```

- **`scope: simulate`** → AVS honors it for the simulate family on partner trust alone. No wallet
  signature, no ownership check (simulate already skips it). `sub` is used only for attribution and
  rate-limiting; because chain is cosmetic and ownership is skipped, it need not be a wallet that signed.
- **`scope: execute`** (future) → AVS additionally requires real fund authority for `sub` (the
  controller-signed path today; a **Calibur** permission tomorrow). Partner trust alone is never enough.

We keep the claim shape above (`sub` + `act.partner_id` + `scope`) as the stable contract so that adding
a real RFC 8693 token-exchange endpoint later — and carrying `sub = user EOA` into the execute path — is
an **additive change, not a rewrite**: Calibur slots into the authority check, not into a new auth system.

---

## 3. URGENT — partner-delegated simulate (build this)

**Goal:** Studio's socially-authenticated user can simulate a workflow with **no wallet signature**,
because simulate moves nothing and AVS trusts Studio's authentication of its own user.

**3.1 Partner registry (minimal).** New `model/partner.go` + `p:<partner_id>` key:

```go
type Partner struct {
    PartnerID        string   `json:"partner_id"`              // "studio"
    DisplayName      string   `json:"display_name"`
    AssertionPubKeys []string `json:"assertion_pub_keys"`      // client-assertion verify keys (rotatable)
    Scopes           []string `json:"scopes"`                  // e.g. ["simulate"]
    RateLimit        int      `json:"rate_limit,omitempty"`
    Status           string   `json:"status"`                  // active | suspended
}
```

Register **Studio** as the first (and, for now, only) partner with `scopes: ["simulate"]`. Build for N,
exercise with one — no multi-partner UX until a second partner is real.

**3.2 Partner auth middleware.** Alongside the existing `JWT` middleware
([middleware/jwt.go](aggregator/rest/middleware/jwt.go)), add partner-assertion verification: validate
the client assertion against `AssertionPubKeys`, load the `Partner`, attach it to the request context.
The simulate-family handlers (`SimulateWorkflow` [handlers_workflows.go:278](aggregator/rest/handlers_workflows.go#L278),
`RunNode` [handlers_nodes.go:23](aggregator/rest/handlers_nodes.go#L23),
`RunTrigger` [handlers_triggers.go:22](aggregator/rest/handlers_triggers.go#L22)) accept **either**:
- a wallet-signed user JWT (today's path, unchanged), **or**
- a partner token with `scope: simulate`.

`requireUser` ([context.go:23](aggregator/rest/context.go#L23)) gains a sibling, e.g.
`requireSimulateAuth`, that returns a principal from *either* source. The engine simulate path is
untouched — it already accepts an arbitrary address and skips ownership.

**3.3 What AVS trusts.** For `scope: simulate`, AVS trusts the partner credential, full stop — the same
way it would trust an admin key, but **scoped to no-fund operations only**. The blast radius of a leaked
partner simulate-token is "someone can run free previews," not "someone can touch funds."

**3.3a Runner resolution (the second gate).** Studio's simulate flow resolves the `$SMART_WALLET$` runner
placeholder at simulate time via `getWallets` → the wallet derive/list endpoints
([ListWallets](aggregator/rest/handlers_wallets.go#L27), [CreateWallet](aggregator/rest/handlers_wallets.go#L64)),
which were also `requireUser`-gated — so partner-delegated simulate alone would die at runner resolution.
Deriving a smart wallet is deterministic from `(owner, salt, factory)`, moves no funds, and needs no
ownership check — the **same no-fund class as simulate**. So these two wallet endpoints accept the partner
assertion too (`requireWalletDeriveAuth`), with the extra requirement that `sub` be a real EOA (you can't
derive a wallet without an owner). Listing is owner-scoped, so it never exposes another user's wallets.
Fund-moving wallet ops (`:withdraw`, `:getNonce`, `UpdateWallet`) stay user-JWT-only.

**3.4 Rate-limit & attribution (REQUIRED).** Meter calls per `partner_id` **and per `sub`** so one abusive
end-user can't exhaust the whole partner's quota (noisy-neighbor within a tenant). No billing yet —
simulate is free/near-free; this is abuse control. *(Implementation note: the partner path currently logs
`partner_id`/`sub` for attribution; the per-`(partner_id, sub)` limiter is the immediate fast-follow.)*

**That is the entire urgent deliverable.** No proto change, no storage-key change, no contracts work — a
registry namespace, an auth middleware, and an `either/or` gate on three handlers.

---

## 4. FUTURE — fund authority + billing (architecture only; do NOT build now)

Captured so the urgent work leaves the right seams. None of this is in the urgent scope.

**4.1 Execute authority via Uniswap Calibur (replaces any mandate-module idea).** When funds must move,
the user **signs with their own EOA via Calibur** to grant AVS permission to move funds — **no custody**.
This becomes the authority check for `scope: execute`: AVS may execute for `sub` iff a valid Calibur
permission exists (bounded/revocable per Calibur's terms). Until Calibur lands, execute keeps using the
controller-signed authKey path. **Seam to preserve now:** the execute authorization decision must be a
single, swappable check (today: "controller can sign for this wallet"; tomorrow: "Calibur permission
exists") so dropping Calibur in does not touch the partner/scope layer.

**Why Calibur: it automates a different wallet than MA v2 does.** MA v2 operates the **smart wallet we
derive from the user's EOA** — a separate contract at a factory address, which the user must fund before
we can automate anything in it. Calibur automates the **user's EOA itself**, via EIP-7702 delegation: the
account is the address the user already has, holding the assets they already hold, with no second address
and no funding step. These are complementary products, not competing implementations of one product. The
seam above stays the same either way — only the authority check swaps.

**Superseded rationale — the EntryPoint v0.6 argument no longer applies.** An earlier revision of this
section argued for Calibur on the grounds that we ran EntryPoint v0.6 while every 7702 candidate targeted
v0.7+, making a bundler-routed 7702 account a forced migration. **We have since completed that cutover.**
MA v2 on EntryPoint v0.7 is the only account provider we allow
([config.go:58](core/config/config.go#L58) pins `EntryPointV07AddressHex`,
[config.go:385](core/config/config.go#L385) returns v0.7 for MA v2 chains,
[config.go:407](core/config/config.go#L407) rejects anything but `modular_account_v2`); see
[docs/changes/20260807-retire-v06-send-path.md](docs/changes/20260807-retire-v06-send-path.md). The v0.6
constant survives only as legacy in [bundler/client.go:25](pkg/erc4337/bundler/client.go#L25). Do not
cite the migration cost as a reason for anything.

**The comparison that does remain: Calibur vs MA v2's own 7702 mode.** MA v2 also ships a 7702 flavor
that delegates the user's EOA to the MA v2 implementation — same wallet type Calibur targets, so the
choice is genuine and lives entirely in the EOA lane. Trade-off, now that the migration argument is gone:
Calibur's relayer-native model lets the aggregator sign and submit `execute()` directly (**no bundler, no
EntryPoint**), while MA v2 7702 routes every op as a UserOp through EntryPoint v0.7 and a bundler but
keeps us on **one account system with first-class validation modules** instead of a second, parallel one
whose scoping we would own. Verify MA v2 7702 module semantics against current Alchemy docs before
deciding — that is the least-settled input.

**De-risking step: DONE.** The Sepolia PoC landed and was independently verified —
[calibur-7702-poc](https://github.com/Antrikshgwal/calibur-7702-poc), verification branch and write-up at
[chrisli30/calibur-7702-poc @ verify/ava-protocol-standalone](https://github.com/chrisli30/calibur-7702-poc/tree/verify/ava-protocol-standalone).
Direct-transaction path works (aggregator-relayed scoped `execute()`, 128,296 gas, no bundler); revocation
and expiry are total. **Three findings that must shape any production build:**

1. **Scoping does not cover the signature path.** The policy hook is address-flag dispatched, and the
   execution flags (`0x18`) leave `AFTER_IS_VALID_SIGNATURE_FLAG` (`1 << 2`) clear. Calibur's
   `isValidSignature` admits any registered, unexpired key with no target or value scoping, so a
   "scoped" key can mint arbitrary ERC-1271 account signatures — Permit2 approvals, Seaport orders — a
   token drain that never reaches `beforeExecute`. **This is worse here than it would be on a derived
   smart wallet**, precisely because of the wallet-type distinction above: the EOA is the user's primary
   asset store, not a purpose-funded automation wallet. A production hook must mine for `0x1f` and
   explicitly deny the validation callbacks.
2. **Hook scoping is fail-open.** `KeyManagement.update` accepts any hook with code and any nonzero flag
   bit, so a mis-flagged hook is accepted, *looks* attached in `getKeySettings`, and silently enforces
   nothing. Assert the hook's address bits at registration, not just at deploy.
3. **Sponsored delegation fails silently on forge ≥ 1.2.** `vm.signAndAttachDelegation` signs
   `accountNonce + 1`, valid only for a self-send; with the aggregator as sender the authorization is
   *skipped* rather than rejected, the type-4 tx lands status 1, and nothing is delegated. Sponsored
   relay is exactly our model — confirm delegation by reading `code(account)`, never by tx status.

Common shape across all three: the bound is absent and the system reports success. Any Calibur
integration needs positive assertions that each bound is live.

**4.2 Partner attribution on tasks.** Add `string partner_id = NN [json_name="partnerId"]` to
`protobuf/avs.proto` (additive/`omitempty`; old tasks load via the existing `DiscardUnknown` path,
[executor.go:138](core/taskengine/executor.go#L138)); run `make protoc-gen` + `make storage-check`).
Resolve it from a `(wallet,chainId)→partner` registry written only by a partner-authenticated call —
never self-asserted by a user token (attribution drives billing, so it must be trustworthy).

**4.3 Per-partner billing (Stripe model: user → partner → AVS).** Extend the fee ledger key from
`(chainId, owner)` to `(chainId, partner_id, owner)` plus a partner rollup; add `FeeRecord.PartnerID`;
source `execution_fee`/tiers from a per-partner fee schedule (default = today's global
[fee_estimator.go:52](core/taskengine/fee_estimator.go#L52) so Studio's numbers don't move); add a
partner-level `CheckCreditLimit`. **Seam to preserve now:** when we add `partner_id` to the task, also
thread it (optional, ignored) into the existing `RecordValueFee`
([fee_ledger.go:101](core/taskengine/fee_ledger.go#L101)) call site so wiring billing later is a fill-in,
not a refactor. **Storage caution:** the ledger key change must be **add-alongside, never reshape**, or it
orphans stored balances.

---

## 5. Storage-safety summary

| Change | Phase | Additive? | Notes |
| --- | --- | --- | --- |
| `Partner` model + `p:<id>` key | URGENT | ✅ | brand-new namespace |
| Partner auth middleware / `requireSimulateAuth` | URGENT | n/a | no storage |
| `Task.partner_id` (proto) | FUTURE | ✅ | optional; `DiscardUnknown` load path; `make protoc-gen` |
| `(wallet,chainId)→partner` registry key | FUTURE | ✅ | brand-new namespace |
| `FeeRecord.PartnerID` | FUTURE | ✅ | new `omitempty` field |
| `FeeLedgerKey(chainId, partner_id, owner)` | FUTURE | ⚠️ **add-alongside** | never reshape the existing `(chainId, owner)` key — write partner-keyed entries alongside |

Run `make storage-check` vs `origin/main` before any merge to `main`.

---

## 6. Phasing

- **Phase 1 — URGENT: partner-delegated simulate. ✅ IMPLEMENTED 2026-06-30.**
  Built as a **config-based** partner registry (`config.PartnerConfig`, `partners:` YAML block) rather
  than BadgerDB — partners are a small, trusted, operator-curated set, so no storage migration / CRUD API
  is needed for v1. Partners authenticate with a short-lived **Ed25519-signed assertion** (private_key_jwt
  style) in the **`X-Partner-Assertion`** header, kept separate from the user `Authorization: Bearer` path.
  `requireSimulateAuth` ([aggregator/rest/partner.go](aggregator/rest/partner.go)) is the either/or gate
  wired into the three simulate-family handlers **and** the two no-fund wallet-derivation handlers
  (`ListWallets`/`CreateWallet`, via `requireWalletDeriveAuth`, so Studio's `$SMART_WALLET$` runner
  resolution works end-to-end — §3.3a). It verifies signature against the partner's registered key(s)
  (rotation-friendly), enforces partner `status: active`, requires the scope to be both registry-granted
  and token-declared, requires a short-lived `exp` (TTL ≤1h), and — when `partner_assertion_audience` is
  set — binds `aud` to this gateway (anti-replay). Studio is the first partner (`scopes: ["simulate"]`).
  No proto/storage-key change; no change to the user-JWT path. **Deferred fast-follows:** per-`(partner,
  sub)` rate-limiting (currently logged/attributed) and a `jti` replay cache.
- **Phase 2 — FUTURE: attribution seam.** `Task.partner_id` proto field + `(wallet,chainId)→partner`
  registry + stamp on `CreateTask`; thread an ignored `partner_id` through the fee record call site.
- **Phase 3 — FUTURE: per-partner billing.** Partner-keyed ledger + per-partner fee schedule + credit
  limit (default schedule preserves Studio's numbers).
- **Phase 4 — FUTURE: Calibur execute authority.** Swap the execute authorization check to "valid Calibur
  permission for `sub`"; enable `scope: execute` for partners; recurring-payment workflow template. This
  is the gate before a third-party DeFi partner moves real user funds.

---

## 7. Open decisions

1. **Delegation token shape for v1** — ✅ **RESOLVED: direct partner assertion now.** Studio presents its
   partner assertion directly on simulate calls; no separate token-exchange endpoint in v1. The claim
   shape (`sub` + `act.partner_id` + `scope`) is fixed as the stable contract so a token-exchange endpoint
   is a later additive change, not a rewrite (§2).
2. **authKey chainId** — ✅ **RESOLVED: keep as-is.** It is already non-binding (cosmetic default, §1) and
   removing it is a breaking signed-message/SDK change orthogonal to this work. If we ever simplify the
   signed message, do it as a deliberate SDK-coordinated breaking change, not bundled here.
3. **`sub` for simulate** — end-user wallet address vs an opaque partner-user-id. Address is convenient and
   harmless (chain cosmetic, ownership skipped); opaque id avoids leaking a maybe-not-yet-real wallet.
   Defer to implementation.
4. **Calibur permission shape** — defer; it defines the Phase 4 authority check, not the urgent work.
5. **Settlement** — how partners actually pay AVS (on-chain deposit / off-chain invoice); Phase 3+.

---

## 8. Risk register

- **Over-trusting the partner token** — mitigated by **scope**: a simulate-token can only run free
  previews and derive wallet addresses; it can never create or execute. The execute class always requires
  on-chain fund authority, never partner trust (the §0 invariant).
- **Data exposure (non-issue, stated to preempt review)** — partner-delegated simulate does **not** widen
  data exposure. Arbitrary-address preview is already possible today with a user JWT (the engine skips
  ownership), and wallet listing is owner-scoped. The partner token only removes the *signature*
  requirement; it exposes nothing about other users' wallets that a user JWT couldn't already.
- **Replay** — assertions are bound to this gateway via `aud` (`partner_assertion_audience`) so a captured
  token can't be replayed across environments, and `exp` is required and capped (≤1h). A `jti`/nonce
  replay cache (true single-use) is reserved as a follow-up — the claim carries `jti` now so the cache is
  a drop-in later.
- **Leaked partner assertion key** — rotatable via multiple `public_keys`; short-lived assertions; scoped
  to simulate. Blast radius = preview spam, bounded by the per-`(partner, sub)` rate-limit.
- **Scope creep into execute on partner trust** — guard in code: the execute authorization check must be
  independent of the partner layer and require fund authority (controller/Calibur). Never let
  `scope: execute` be satisfied by a partner credential alone.
- **Fee-ledger key reshape (future)** — add-alongside only; never reshape `(chainId, owner)`.
- **Attribution spoofing (future)** — `partner_id` derives from the partner-authenticated registry, not a
  user-held claim.
