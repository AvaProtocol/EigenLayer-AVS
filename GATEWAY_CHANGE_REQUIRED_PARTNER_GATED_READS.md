# Gateway change required: partner-gated reads & permission levels

**Status:** **Implemented (gateway)** — `permission.go` / `ensurePermission` +  
OpenAPI security overrides on PR #739.  
**Remaining (external):** Studio + ava-sdk-js e2e mint/send partner `scope: read`  
for token metadata; deploy configs with `scopes: [read]`; **per-partner rate  
limits deferred** (see §4.4 — still JWT-subject / shared `anonymous` today).  
**Repo:** EigenLayer-AVS (gateway / aggregator REST)  
**Date:** 2026-08-08  
**Related (Studio):** deposit / fund_wallet UX; `getTokenMetadataAction` fails with  
`No valid auth key … wallet must sign in again` when the user has a live wallet  
socket (AutoConnect) but no fresh **AVS user JWT** for the connected chain  
(Position D).  
**Related (code):** `aggregator/rest/permission.go`, `aggregator/rest/partner.go`,  
`api/openapi.yaml`, `PLAN_PARTNER_PAYMENTS.md` (Phase 1 partner-only simulate  
**superseded**).

---

## 1. Problem

### What Studio needs

Studio’s Deposit modal and token picker need **public-ish on-chain facts**:

| Data | Today’s Studio path | Auth today |
|------|---------------------|------------|
| Token balances | Moralis (`getWalletTokenBalancesAction`) | NextAuth `userId` only — **no AVS JWT** |
| Token metadata (name / symbol / decimals) | Gateway `GET /api/v1/tokens/{address}` via `getAuthedClient` | **Requires wallet-signed AVS user JWT** |

Balances already work without an AVS JWT. Metadata does not:  
`executionController.getTokenMetadata` → `getAuthedClient` → gateway  
`GetToken` → `requireUser`.

So after refresh, AutoConnect can restore Rabby while the modal still errors on  
metadata because **AVS JWT is missing/expired for that EOA + chain**. The data  
itself is not user-private (whitelist + `eth_call` name/symbol/decimals).

The same cold-JWT gap hits **preview wallet resolve** (list + derive/create for  
`$SMART_WALLET$`) if those paths ever required a user JWT without a partner  
path — today they already allow partner OR JWT via `requireWalletDeriveAuth`;  
this doc **keeps** that class partner-sufficient and groups list + create  
together.

### What we must not do

**Do not open true public (anonymous) endpoints.**

Unauthenticated `GET /tokens/*`, wallet list, etc. would:

- Invite DDoS / scraper load against gateway + RPC
- Bypass the tenant model (any client could hammer enrichment)
- Diverge from “gateway is not a full public service”

---

## 2. Auth model (required)

### Principle: minimal permission per operation

The gateway is **not** intended as a fully public service. Most product traffic  
sits behind a gate. Each operation gets the **weakest** permission that still  
makes it safe and attributable — not “stack every gate by default.”

**Two gates, different jobs. Applied by stakes, not always AND.**

| Gate | Who | Purpose |
|------|-----|---------|
| **1. Partner app** | Registered partner in gateway config (`partners[]`; Studio only for now) | Outer door: only known apps may call. Attribution, per-partner rate limits, kill switch. **Sufficient alone** for low-stake read / preview-resolve. |
| **2. User JWT** | End user’s wallet-signed gateway JWT (`Authorization: Bearer` via `auth:exchange`) | Binds the call to a concrete wallet subject and authority. **Required** for simulate / runNode and all user-owned or fund-moving ops. |

There is **no anonymous / fully open** product API for these surfaces.

```
  Request
      │
      │  almost never anonymous
      ▼
  ┌─────────────────────────────┐
  │ Gate 1: Partner             │  registered app (config whitelist)
  │ (when op needs tenancy /    │
  │  partner-sufficient class)  │
  └─────────────┬───────────────┘
                │
     partner-sufficient ops          JWT-required ops
     (read, preview wallet)          (simulate, execute, …)
                │                              │
                ▼                              ▼
         handler                         Gate 2: User JWT
                                         then handler
```

Gates do **not** fight each other: partner answers “which app,” JWT answers  
“which wallet / with what authority.” Conflict only appears if a low-stake  
read is forced through Gate 2 when Gate 1 alone is enough.

### Partner registration (ops only)

- Registration is **manual** (gateway YAML `partners[]` + public keys + scopes).
- **Only Studio** is registered for now; design for N, operate with one.
- **No partner registration / management API** in this change (or near term).
- Suspend / rotate keys = config change + redeploy/reload as ops practice.

### Target permission matrix

| Operation class | Endpoints (today) | Minimal auth | Partner alone? | User JWT? |
|-----------------|-------------------|--------------|----------------|-----------|
| **Token / public chain reads** | `GET /api/v1/tokens/{address}` | Partner `scope: read` | **Yes** | No |
| **Preview wallet resolve** | `GET /api/v1/wallets` (list), `POST /api/v1/wallets` (create/derive) | Partner (assertion `sub` = owner EOA) | **Yes** | No |
| **Simulate / runNode / runTrigger** | `POST …/workflows:simulate`, `/nodes:run`, `/triggers:run` | **User JWT** | **No** | **Yes** |
| **User-owned / fund authority** | workflows CRUD, executions, secrets, policies, withdraw, etc. | User JWT | **No** (policies already refuse partner) | **Yes** |

**Preview wallet resolve** is one class: **list + create (derive)** share the same  
partner-sufficient gate. Both are no-fund (CREATE2 / owner-scoped list).  
Update / withdraw / nonce remain JWT-only.

Optional later: require partner **in addition** on JWT routes (outer tenancy  
AND). That is product lockdown, not the **minimal** requirement for those ops.  
Minimal for simulate is JWT; minimal for token metadata and preview wallet  
resolve is partner.

### Relation to today’s code / `PLAN_PARTNER_PAYMENTS.md`

| Surface | Today | This contract |
|---------|--------|----------------|
| Token metadata | `requireUser` only | **Partner `read` only** (JWT not required) |
| List / create wallet | `requireWalletDeriveAuth` = partner **or** JWT | **Partner-sufficient** (JWT still accepted if present); keep `sub` = EOA for partner path |
| Simulate / runNode / runTrigger | `requireSimulateAuth` = partner **or** JWT | **User JWT required** — partner alone **rejected** |
| Fund / policies | User JWT; partner refused on policies | Unchanged |

Phase 1 “partner-delegated simulate” (partner alone for no-fund run) is  
**superseded** for policy: partner does **not** authorize simulate/runNode.  
Partner remains for **reads + preview wallet resolve**.

---

## 3. Scope of APIs

### In scope (must change)

| Method | Path | Today | Required |
|--------|------|-------|----------|
| `GET` | `/api/v1/tokens/{address}` | `requireUser` | **Partner `read`** (no user JWT) |
| `GET` | `/api/v1/wallets` | `requireWalletDeriveAuth` (partner **or** JWT) | **Preview wallet resolve** — partner OK without JWT; JWT OK if present |
| `POST` | `/api/v1/wallets` | same | same class as list (derive/ensure) |
| `POST` | `/api/v1/workflows:simulate` | `requireSimulateAuth` (partner **or** JWT) | **`requireUser` (JWT only)** — drop partner-only path |
| `POST` | `/api/v1/nodes:run` | same | **JWT only** |
| `POST` | `/api/v1/triggers:run` | same | **JWT only** |

Handlers:

- Tokens: `handlers_tokens.go` → `GetToken`
- Wallets: `handlers_wallets.go` → `ListWallets`, `CreateWallet` (`requireWalletDeriveAuth`)
- Simulate family: `handlers_workflows.go` / `handlers_nodes.go` / `handlers_triggers.go`

Engine paths (whitelist, CREATE2, simulate) can stay; **auth helpers and  
OpenAPI security** change.

### Explicitly out of scope

| Concern | Notes |
|---------|--------|
| Wallet **balances** for Deposit UI | Stay on Studio → Moralis + NextAuth until a gateway portfolio read is productized (then partner `read`, same as metadata). |
| Partner registration API | Manual config only. |
| Fund-moving / policies via partner | Never. |
| Long-lived `X-Partner-API-Key` | Optional later; v1 reuses Ed25519 `X-Partner-Assertion`. |

### “Public APIs” (product language)

In Studio discussion, “public” meant **non-secret chain / resolve data**, not  
unauthenticated HTTP:

1. **Token metadata** — partner-gated.  
2. **Preview wallet resolve** (list + create/derive) — partner-gated.  
3. **Future gateway balances** — same partner `read` class if introduced.

---

## 4. Implementation sketch

### 4.1 Partner verification (Gate 1)

Reuse config registry (`partners[]`, `partner_assertion_audience`), Ed25519  
`X-Partner-Assertion`:

```json
{
  "iss": "studio",
  "sub": "<owner EOA for wallet resolve; optional/empty for pure metadata>",
  "scope": "read",
  "aud": "<partner_assertion_audience>",
  "exp": ...,
  "iat": ...,
  "jti": "..."
}
```

Suggested scopes (config `scopes: [...]`):

| Scope | Authorizes |
|-------|------------|
| `read` | Token metadata (and later portfolio reads). May also cover preview wallet resolve if product prefers one scope. |
| `wallet_preview` (optional split) | List + create/derive only — use if `read` should not include wallet endpoints. |

**v1 recommendation:** one scope `read` covering token metadata **and** preview  
wallet resolve (list + create), to keep Studio minting simple. Split later if  
a partner needs metadata without wallet resolve.

- **Token metadata:** partner `read`; `sub` optional (logging / rate-limit key).  
- **Preview wallet resolve:** partner `read` (or `wallet_preview`); **`sub` must  
  be a real 0x EOA** (same rule as today’s `requireWalletDeriveAuth`).  
- **`scope: simulate` on partner:** no longer authorizes simulate/runNode;  
  remove from Studio mints for those calls; config may drop `simulate` or leave  
  unused.

Partner registration remains YAML only, e.g.:

```yaml
partners:
  - id: studio
    public_keys: ["ed25519:..."]
    scopes: ["read"]
    status: active
partner_assertion_audience: avs-gateway-staging   # env-specific
```

### 4.2 Auth helpers

```text
requirePartner(ctx, requiredScope) → partnerPrincipal
  - verify X-Partner-Assertion (or absent → 401 for partner-only routes)
  - check registry scope + token scope
  - return partner_id + sub

requirePartnerRead(ctx):
  - requirePartner(ctx, "read")
  - for GetToken: build *model.User from sub if hex address, else zero / omit
  - engine.GetTokenMetadata only uses user for logs today

requirePreviewWalletResolve(ctx) → *model.User
  - if user JWT present → requireUser (existing path)
  - else requirePartner(ctx, "read") with sub = concrete EOA
  - same shape as today’s requireWalletDeriveAuth, scopes updated

requireUser(ctx) for simulate / runNode / runTrigger
  - JWT required
  - if X-Partner-Assertion present: either ignore for auth (JWT wins) or
    refuse partner on these routes — prefer clear error if partner-only
    credentials are sent without JWT so Studio fails loudly
```

`GetToken` becomes partner-only (no `requireUser`):

```go
_, err := s.requirePartner(ctx, scopeRead) // or requirePartnerRead
// ... unchanged GetTokenMetadata; pass a User for logging if useful
```

Simulate family:

```go
user, err := s.requireUser(ctx) // not requireSimulateAuth
```

### 4.3 OpenAPI / SDK

- Token metadata: security = partner assertion (`X-Partner-Assertion`), not  
  bearer-only.  
- Preview wallets: partner **or** bearer (document both).  
- Simulate / runNode / runTrigger: bearer JWT **required**; document that  
  partner alone is insufficient.  
- SDK-js / Studio: mint `scope: "read"` for metadata + wallet resolve; stop  
  relying on partner for simulate (send user JWT).  
- Error codes: keep `PARTNER_*` vs `AUTH_REQUIRED` distinct.

### 4.4 Rate limiting

| Layer | Suggestion | Status |
|-------|------------|--------|
| Per partner | QPS / daily on partner-sufficient routes (`/tokens/*`, preview wallets) | **Deferred** (follow-up) |
| Per subject | When JWT present, or partner `sub` when set | Partial (JWT subject only) |
| Global | Existing gateway limits | Live |

**Deferred note (PR #739 / Copilot):** rate-limit middleware still runs  
*before* `ensurePermission` and keys only on JWT subject or the shared  
`anonymous` bucket. Partner-only token/wallet traffic therefore shares  
`anonymous` with unauthenticated noise. Fix requires verifying (or  
peeking) partner identity before the limiter — separate change; not  
blocking the permission-map merge. Partner Gate 1 still rejects unknown  
apps at the handler.

### 4.5 Studio follow-up

1. Mint partner assertion `scope: "read"` for token metadata and preview  
   wallet list/create.  
2. Call those APIs with **partner only** when AVS JWT is cold (AutoConnect  
   without re-exchange).  
3. **Catalog-first** metadata fallback for known tokens (USDC/WETH) as extra  
   UX hardening.  
4. Simulate / runNode: **always** user JWT after auth exchange; do not send  
   partner-only for those.  
5. Deposit / preview resolve should not map “missing AVS JWT” to “wallet not  
   connected” when only partner-gated reads were needed.

---

## 5. Security properties

| Threat | Mitigation |
|--------|------------|
| Open internet scrapes `/tokens` or lists wallets | Gate 1: unknown partners rejected |
| Leaked partner private key | Short TTL assertions; key rotation; scope limited to `read` / preview resolve — **not** simulate, execute, or policies |
| Partner-only scrape of metadata | Per-partner rate limits + TTL (accepted blast radius: RPC cost, not funds) |
| Partner-only simulate abuse | **Rejected** — JWT required for simulate/runNode |
| Partner used for fund moves / policies | Existing refuse / requireUser; unchanged |
| Leaked user JWT | Same as today for JWT routes; partner-sufficient routes do not need JWT |

**Blast radius of partner `read` + preview wallet resolve:** metadata RPC,  
owner-scoped wallet list/derive for assertion `sub` — not transfers, not  
task create, not full simulate.

---

## 6. Acceptance criteria

### Token metadata

- [ ] `GET /api/v1/tokens/{address}` with valid partner `read` assertion,  
      **no** user JWT → **200** (same body shape: `found`, symbol, decimals, …).  
- [ ] Same without partner credential → **401** (even with valid user JWT,  
      if product requires partner on this route; if JWT-only is still allowed  
      as transition, document it — **target is partner-gated**).  
- [ ] Partner without `read` scope → scope error.

### Preview wallet resolve (list + create/derive)

- [ ] `GET /api/v1/wallets` and `POST /api/v1/wallets` with partner assertion  
      + `sub` = owner EOA, **no** user JWT → success (list / derived wallet).  
- [ ] Partner with empty/non-address `sub` → **400** `PARTNER_SUBJECT_REQUIRED`  
      (or equivalent).  
- [ ] User JWT alone still works (optional compatibility).  
- [ ] Update/withdraw/nonce still **JWT only**.

### Simulate / runNode / runTrigger

- [ ] Valid partner assertion alone (any scope) → **401** (JWT required).  
- [ ] Valid user JWT → existing success behavior.  
- [ ] Partner assertion cannot authorize createTask / execute / policies.

### Ops / docs / Studio

- [ ] Config example: Studio `scopes: ["read"]` (no registration API).  
- [ ] OpenAPI + tests updated for the matrix.  
- [ ] Studio: metadata + wallet resolve with partner when JWT cold;  
      simulate only with JWT; catalog-first optional.

---

## 7. Non-goals

- Fully public unauthenticated token, balance, or wallet HTTP APIs.  
- Partner registration / multi-tenant admin API.  
- Replacing Moralis balances in Studio in this change.  
- Partner-only simulate / runNode (explicitly **not** allowed).  
- Expanding partner scope to fund-moving operations.  
- Dual-gate **AND** (partner + JWT) as the default for cheap reads.

---

## 8. Suggested implementation order

1. Add `scopeRead` (+ helpers `requirePartner` / partner-only read path) in  
   `aggregator/rest/partner.go`.  
2. Switch `GetToken` to partner `read`; tests for partner-only success.  
3. Keep/adjust `requireWalletDeriveAuth` as **preview wallet resolve** under  
   `read` (list + create); confirm create stays partner-ok.  
4. Switch simulate / runNode / runTrigger from `requireSimulateAuth` to  
   `requireUser`; tests that partner-only is rejected.  
5. Config examples: Studio `scopes: [read]`; deprecate partner `simulate` for  
   auth.  
6. SDK + Studio: `read` assertion for metadata + wallets; JWT for simulate;  
   catalog-first as UX polish.

---

## 9. Summary

| Question | Answer |
|----------|--------|
| Is token metadata user-secret? | No |
| True public HTTP API? | **No** — partner gate |
| Minimal gate for reads / preview wallet? | **Partner only** |
| List + create (derive) one class? | **Yes** — preview wallet resolve under partner |
| Simulate / runNode partner-only? | **No** — **user JWT required** |
| Partner registration API? | **No** — manual config; Studio only for now |
| Dual-gate AND on metadata? | **No** — that over-gates cold JWT UX |
| Balances today | Moralis + NextAuth; partner `read` if moved to gateway later |

This file is the **gateway-side auth contract**: partner as outer/minimal gate  
for read + preview resolve; user JWT for simulate and restrictive ops; no  
anonymous product APIs. Studio catalog/Moralis fallbacks remain complementary  
UX, not a substitute for partner-bound gateway traffic.
