# Session grant entity reuse inherits stale on-chain hooks

- **Date**: 2026-08-21
- **Status**: Implemented
- **Branch**: `fix/763-entity-reuse-drift`
- **Related**: [#763](https://github.com/AvaProtocol/EigenLayer-AVS/issues/763), [#761](https://github.com/AvaProtocol/EigenLayer-AVS/issues/761), `cc19bec4` (`SESSION_POLICY_EXPIRED`)

## Problem

`NextSessionEntityID` derives the next validation entity from stored records
only. `RevokeSessionGrant` deleted pending-without-InstallCall rows, which
made those entities invisible to the allocator. Installing a new grant into
an entity the account still held kept the **old** TimeRangeModule window.

Observed on the Sepolia e2e fixture: storage said entity 2 was valid until
2027-08-21; the account enforced 2026-08-13. Every UserOp failed as
"User Operation expired or has an invalid time range" with nothing pointing
at the drifted record. Sentry `EIGENLAYER-AVS-2C`. This is not the
storage-known expiry `cc19bec4` already refuses.

## Decision

Sequenced **B → A → reader → C + sweep**. A and B are not substitutes.

- **B.** `RevokeSessionGrant` never deletes. Revoked records stay so
  `NextSessionEntityID` keeps the entity reserved. Incomplete revoked
  rows are allowed by `Validate()`.
- **A.** Prepare/submit allocate with `NextFreeSessionEntityID`: storage
  max+1, then a bounded on-chain occupancy probe (signer **or** deferred
  nonce sequence, fail-closed on read error, cap 8, then
  `SESSION_POLICY_ENTITY_OCCUPIED`). Wired on the Engine only after
  `InstallSessionResolver`, so unit tests stay offline.
- **Reader.** `aa.EntityTimeRangeOnChain` reads
  `TimeRangeModule.timeRanges(uint32,address) → (uint48, uint48)`.
- **C.** The send path, for applied grants not yet cached, compares the
  installed window to stored `ValidUntil`. Mismatch is
  `SESSION_POLICY_CHAIN_WINDOW_MISMATCH` (never reaches the bundler).
  Match sets `Grant.ChainWindowChecked`.
- **Sweep.** `FindDriftedSessionGrants` plus
  `scripts/sweep_session_windows` list already-stored drift. Re-grant is
  the owner's — this process cannot produce the signature.

## Alternatives considered

- **C alone.** Types the failure but still lets a colliding grant be
  signed and stored; every send fails until someone re-grants — the loop
  #761 exists to stop. The allocator invariant stays broken.
- **Folding expiry into `Usable()`.** Rejected in `cc19bec4`;
  `TestUsableIgnoresExpiryOnPurpose` pins it. An expired record still
  occupies its entity.
- **Closing on B alone.** The named delete path is one way to free an
  entity; a wiped DB or an install that never got a record still needs A.
  A+B also cannot heal a grant that is already stored wrong — that is C
  and the sweep.

## Verification

- Unit tests in `session_entity_drift_test.go` (occupancy skip / fail-closed /
  probe cap, revoke-without-delete, window mismatch, cache, sweep).
- `EntityTimeRangeOnChain` encoding tests in `ma_v2_entity_state_test.go`.
- `make storage-check` against the branch point: `ChainWindowChecked` is
  additive `omitempty`; `sp:%d:` is an additive scan prefix.
- Live Sepolia (optional, funded MA v2): grant, revoke without on-chain
  cleanup, grant again — second grant must take a new entity and not
  inherit the first's time range. A alone would have prevented the
  original incident at 08:02 (entity 2 occupied → allocate 3).
