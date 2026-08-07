# Session grants replace instead of stacking

**Date:** 2026-08-06
**Status:** Implemented
**Branch:** `feat/session-policy-atomic-supersede`
**Related:** [#715](https://github.com/AvaProtocol/EigenLayer-AVS/issues/715), Studio [#1458](https://github.com/AvaProtocol/studio/pull/1458)

## Summary

Submitting a session grant now revokes every other usable grant on the same
runner, so a wallet carries at most one. Replacement is unconditional — there is
no flag — and the submit response reports what it replaced in
`supersededPolicyIds`.

## Problem

A smart wallet could accumulate usable grants, and the send path refuses to
execute when it finds more than one (`ActiveSessionPolicyForWallet`). So a
second grant did not extend the user's permission; it bricked the wallet until
someone revoked one by hand. That is what blocked Auto on Sepolia after a
second successful grant sign.

The issue framed this as a race between concurrent clients. It is mostly not.
Two grants in plain sequence were enough:

1. First grant takes entity 1 and is stored.
2. Later, `NextSessionEntityID` returns 2, the owner signs for entity 2, and
   submit's entity re-check passes because 2 *is* the next free one.
3. Two usable grants. Execute refuses.

The pre-existing `ErrSessionEntityTaken` guard does not catch this. It protects
entity uniqueness, not policy singleton-ness: it only fires when two grants were
prepared against the same free entity, so any two grants separated by a
completed submit sail through it.

## Decision

**Replace, always, scoped to the runner.**

- `SubmitSessionPolicy` holds a write lock on `(chainID, owner, runner)` across
  the entity re-check, the store, and the supersede. `RevokeSessionPolicyByID`
  takes the same lock; the resolver and the contract-write preflight take it
  shared.
- An in-process sharded `RWMutex` is sufficient, and that is a property of the
  deployment rather than an assumption: the store is an embedded BadgerDB opened
  once per aggregator, and Badger holds an exclusive lock on its directory, so a
  second writer process cannot exist. Nothing distributed is needed unless that
  changes.
- Order is store-then-supersede. On a crash that can leave two usable grants,
  which fails closed and is repaired by granting again; superseding first could
  leave zero — working authority destroyed for a replacement that never landed.
- A failed supersede returns `ErrSessionPolicySupersedeFailed` (HTTP 500,
  `POLICIES_SUPERSEDE_FAILED`) rather than a 201. The grant *did* land, so the
  client must not retry the submit — the entity is spent — only clear the
  named leftovers.

Because replace supersedes *all* other usable grants, the operation is
idempotent and self-healing: **a wallet already stuck with dual rows is repaired
by the next successful grant.** No migration, no cleanup endpoint.

### Scoped to the runner, not the capability

The issue asked for ≤1 usable grant per `(runner, chainId, capability)`. The
send path already enforces something stricter — ≤1 per runner, whatever the
capability — so capability-scoped supersede would deliberately permit a pair
that execute then rejects.

v1 is therefore runner-scoped, which removes the whole capability prerequisite:
no `capabilityId` on `SessionPolicy`, no server-side allowlist fingerprint, no
mirroring Studio's `exactInputSingle` (`0x04e45aaf`) heuristic in the gateway.
Capability becomes real only alongside concurrent multi-class grants and
execute-time selection, which changes the execute guard too.

### Superseding revokes, never deletes

`RevokeSessionGrant` deletes a *pending* policy outright, on the grounds that
nothing was installed. Supersede must not reuse that path.
`NextSessionEntityID` derives the next entity by scanning stored records and
counts revoked ones deliberately, so a deleted record frees its entity for
reuse. Superseding a pending grant whose install was already in flight would
hand entity N to the next grant, whose `installValidation` would land on an
entity the account already has. Keeping the record keeps the entity spoken for.

## Alternatives

- **`replaceExisting: true` (issue Option A) / reject-unless-replace (Option
  C).** Both make correct behavior opt-in, i.e. a flag whose default value is
  "break." They also leave the clients the issue worried about — non-Studio
  writers that never learned the flag — still broken. Unconditional replace
  fixes them with no change on their side, and needs no request-shape change.
- **Dedicated `policies.supersede` (Option B).** Its only job is cleaning up
  legacy dual rows, which a plain re-grant already does.
- **Pointer record** (`sp:active:<chain>:<owner>:<runner>` → policy id). Makes
  authority a single key, so the flip is atomic in one write and needs no lock.
  Rejected for v1: it adds a second source of truth that can drift, needs
  legacy-fallback logic, and leaves already-broken wallets inert-but-dirty
  rather than repaired. Reconsider if the gateway ever becomes multi-writer.
- **Collapsing the key to one record per runner.** History is load-bearing:
  revoked records rebuild the uninstall payload and back entity allocation.
- **Deterministic selection at execute** (newest entity wins). Removes the
  fail-closed backstop and lets an accidental grant silently take over while the
  previous spend cap stops applying.

## Client contract

- **Nothing to opt into.** Do not revoke the previous grant before submitting;
  submit does it. There is no `replace` flag.
- **`supersededPolicyIds`** is always present on the 201, empty on a first
  grant. Non-empty means the user's earlier permission is gone — worth
  reflecting in the UI. The superseded records' `status` reads `revoked`; the
  array is what distinguishes a gateway-performed replacement from a
  user-requested `DELETE .../policies/{policyId}`.
- **`SESSION_POLICY_AMBIGUOUS`** now prefixes the >1-usable refusal from the
  send path, so clients branch on the code instead of the prose. Reaching it
  needs records that predate this change or a supersede that failed partway;
  both are cleared by granting again.
- **`POLICIES_SUPERSEDE_FAILED`** (500) means the new grant is stored but a
  previous one was not revoked. Do not retry the submit. Revoke the older usable
  policies, or grant again.

## Off-chain only

Superseding marks a grant unusable **by this gateway**. The validation entity
and its ERC-20 spend cap stay installed on the account until the owner signs
`uninstallValidation`. After three re-grants the wallet carries three live
entities and three caps, of which the gateway will use one — so "update
permission" does not reduce what the account *could* authorize on chain.

Closing that half means making the deferred action a batch that uninstalls the
prior entity as it installs the new one, so both sides agree under a single
owner signature. That is real work on payload shape and signature semantics and
is deliberately out of scope here; it needs its own issue.

## Verification

`core/taskengine/session_policy_supersede_test.go`, all under `-race`:

- a second grant replaces the first and reports it (the sequential regression)
- a superseded grant is retained as `revoked` and its entity is never recycled
- replacement does not touch the owner's other wallets
- a wallet seeded with two usable grants is repaired by granting again
- the ambiguity refusal carries `SESSION_POLICY_AMBIGUOUS`
- six concurrent submits: exactly one is accepted, one usable grant remains
- 25 sequential re-grants against a spinning resolver never expose the
  store-then-supersede window (fails reliably without the resolver's read lock)
- a supersede failure surfaces as an error, not a 201

`aggregator/rest/handlers_policies_test.go` covers the HTTP round trip and pins
`SubmitPolicyResponse` against `SessionPolicy` so the hand-written converter for
the flattened `allOf` cannot silently drop a field.

`make storage-check`: no key or model changes.
