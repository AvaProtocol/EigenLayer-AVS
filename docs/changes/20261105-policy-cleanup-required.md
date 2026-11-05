# Persist the on-chain cleanup requirement in policy reads

Every `SessionPolicy` response now includes `onChainCleanupRequired`, including
list, single-policy and inherited submit responses. It is true when the policy
is revoked and its grant's install was applied without a recorded verified
teardown. Active and pending policies, unapplied grants and recorded teardowns
return false.

The flag is independent of payload construction. A missing or corrupt
`InstallCall` can prevent `onChainCleanup` from being built while removal is
still required. Such a policy returns true with no payload instead of silently
looking clear. A payload is only returned with true. The existing revoke
responses, including `POLICIES_REVOKED_CLEANUP_UNAVAILABLE`, do not change.

Payload build or registration-check failures log the policy id, chain, wallet,
stage and cause once per policy/stage in the running REST server. Suppression is
concurrency-safe and only affects logging: every read still derives the state
and tries building the
payload again. When the record is repaired, later reads can return the payload;
when verified teardown is recorded, both the flag and payload clear.

List and detail reads now check the account's registration for revoked applied
policies whose teardown is not yet recorded. This detects owner removals made
outside Ava, without waiting for a later grant or relying on a browser latch.
The check uses the policy's chain and the existing account-registration verifier,
not residual module storage. An unserved chain never falls back to another chain.
A confirmed clear records teardown under the authority lock, reloading the stored
row so concurrent grant updates survive. Subsequent reads skip the RPC entirely.

RPC/malformed replies and storage failures retain the cleanup requirement.
Registration checks have a ten-second timeout, shared across the list request.
The RPC runs outside the authority lock; a busy wallet defers the storage update
to the next read rather than waiting behind a grant operation.

This uses existing stored grant metadata and the existing teardown marker. It
adds no storage fields, migration or permission changes. OpenAPI marks the response
boolean as required and documents that a required cleanup may lack a payload.

Offline HTTP tests cover all cleanup states on list and detail reads, a failed
revoke followed by a new server/read and payload recovery, external removal
reported by a mocked registration RPC and durable teardown, conservative error
handling, concurrent warning suppression, submit serialization and unchanged
revoke responses. The original read/recovery regressions fail on staging because
the required field is absent. The RPC reconciliation regressions also fail
against the original #894 handlers at `5eba9e90`, which never rechecked removal.
The full REST suite and relevant engine cleanup/revoke tests pass with the race
detector. Vet, changed-code lint, formatting and reproducible REST generation
also pass.

After the gateway PR merges, the SDK follow-up should sync that pinned spec,
regenerate types, cover list/detail pass-through and ship the requested 5.2.3
patch. Studio can then keep its warning across reloads. This gateway change does
not update those clients or restart the local stack.
