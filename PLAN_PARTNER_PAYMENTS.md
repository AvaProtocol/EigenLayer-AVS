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
{ "sub": "<end-user address or partner-user-id>",
  "act": { "partner_id": "studio" },     // delegation / audit trail
  "scope": "simulate",                    // simulate | (later) execute
  "exp": "<now + minutes>" }
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

**3.4 Rate-limit & attribution.** Meter simulate calls per `partner_id` (+ optional `sub`) so a partner's
preview volume is bounded and observable. No billing yet — simulate is free/near-free; this is abuse
control, not revenue.

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
  wired into the three simulate-family handlers; it verifies signature against the partner's registered
  key(s) (rotation-friendly), enforces partner `status: active`, requires the scope to be both
  registry-granted and token-declared, and caps the assertion TTL (≤1h, `exp` required). Studio is the
  first partner (`scopes: ["simulate"]`). No proto/storage-key change; no change to the user-JWT path.
  Per-partner **rate-limiting is deferred** (logged/attributed for now) — fast follow.
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
  previews; it can never create or execute. The execute class always requires on-chain fund authority,
  never partner trust (the §0 invariant).
- **Leaked partner assertion key** — rotatable via `AssertionPubKeys`; short-lived derived tokens; scoped
  to simulate. Blast radius = preview spam, bounded by rate-limit.
- **Scope creep into execute on partner trust** — guard in code: the execute authorization check must be
  independent of the partner layer and require fund authority (controller/Calibur). Never let
  `scope: execute` be satisfied by a partner credential alone.
- **Fee-ledger key reshape (future)** — add-alongside only; never reshape `(chainId, owner)`.
- **Attribution spoofing (future)** — `partner_id` derives from the partner-authenticated registry, not a
  user-held claim.
