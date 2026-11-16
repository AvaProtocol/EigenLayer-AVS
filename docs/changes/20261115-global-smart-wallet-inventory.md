# Global MA v2 smart wallet inventory

Status: implemented locally; not deployed. Design: [PLAN_ACTIVE_SMART_WALLETS.md](../../PLAN_ACTIVE_SMART_WALLETS.md).

MA v2 identities, hidden state, salt allocation, and the active quota now belong to the owner EOA across supported networks. Three active salts `0,1,2` block a new identity. After hiding `0`, salt `4` can be created and used on another supported network; restoring `0` then requires another free slot. Existing hidden identities remain readable and recoverable at capacity. Archiving does not pause automation, revoke spending permissions, move funds, or remove account history.

## Implementation

- The `core/walletinventory` registry stores pinned profile identities, salt and reverse-owner indexes, owner revisions, monotonic next-salt state, and durable owner/key allocation results. All writers use dedicated inventory locks and atomic Badger commits. Derivation runs outside the locks. Runtime materialization never consumes a slot or restores visibility.
- The startup migration imports the union of valid MA v2 network records, preserves source/recovery rows, votes active when any valid network row was visible, reports mixed flags and conflicting addresses, and blocks activation for unresolved conflicts. Conflicting addresses retain distinct record IDs so downstream mirrors preserve both. It seeds allocation above stale/retired salt history too. An owner import and marker commit atomically, and reruns preserve newer global visibility. Existing inventories above the quota are preserved.
- REST lists include `walletId`, `deploymentProfile`, and owner-wide `usage` with a decimal-string revision. `POST /wallets:allocate` requires an owner JWT and `Idempotency-Key`; a network hint is excluded from replay inputs. Explicit-salt ensure and address-based archive/restore use the same registry. Partner-only callers cannot allocate.
- Ownership, policy/withdrawal lookups, sponsorship reverse lookup, and per-node VM runtime materialization resolve the exact registered salt without scanning an expanding salt range. Execution locks, balances, deployment state, grants, budgets and nonces keep their network scope.
- Companion SDK changes add `wallets.createNext` and additive types. Studio adds a revisioned Prisma mirror, one inventory read per linked EOA, global Add/archive/restore controls, and shared consumer refresh. Stale network projections cannot resurrect hidden identities. The allocation HTTP adapter works with the currently published SDK while its companion release is pending.
- The gateway configuration is `smart_wallet_inventory.max_active_wallets_per_owner`. Consistent legacy settings map to one value; conflicting settings require an explicit global limit. The infra source config sets three once.

## Verification

- Read-only factory calls for a public owner test vector and salts `0` and `4` returned matching MA v2 addresses on all 11 networks in the infra source config. No transactions, signatures, account deployments, grant changes or live database migrations were performed.
- Go registry race tests and the targeted registry, migration, configuration, engine, REST and aggregator suites cover admission, cross-network races, atomic failures, durable replay after reopening storage, global restore capacity, history and uint256 exhaustion. Runtime tests retain high non-contiguous salts and archived flags on first network use while all execution shards are held. Sponsorship resolves a global identity before any local network row exists.
- SDK transport tests cover allocation headers and retries, additive usage, and problem-code propagation; both packages build.
- Studio's 23 targeted suites (350 tests) pass. They cover the revisioned mirror, stale-response rejection, unknown capacity after refresh failure, owner changes, retry keys, global consumer reads, the published-SDK adapter, and the actual wallet cards' hide/create/network-switch/blocked-restore/successful-restore flow. Changed-line type checking and changed-source/test lint pass; 348 pre-existing type errors elsewhere remain outside this change. The Prisma schema and infra YAML validate without changing a database or live configuration.

Backend checks: `go test -race ./core/walletinventory`, `go test -short ./core/walletinventory ./migrations ./core/config ./core/taskengine ./aggregator/rest ./aggregator`, and `make storage-check REF=origin/staging`. SDK checks: `yarn build` and `walletInventoryResource.test.ts` transport cases. Studio checks: the targeted Jest suites, `yarn typecheck:changed`, ESLint on changed TypeScript sources/tests, and `yarn prisma validate`.

## Audit follow-up

- Migration now merges any identities registered after its runtime scan, preserves their latest flags, and atomically removes an ambiguous canonical salt index. A failed import preserves the old index. The compatibility writer cannot admit new derived identities or restore archived state; fixtures use explicit registry admission.
- Ownership-only probes read the global registry without creating runtime rows. Execution paths still materialize on the requested network before policy and sponsorship checks. The earlier audit's claim that each probe writes every network was overstated: the old loop returned on its first match, but even that write was unnecessary.
- SDK and gateway validate portable, unambiguous ASCII idempotency headers. SDK validation rejects the promise. The OpenAPI snapshots now describe global listing, archived visibility, ensure-at-capacity, bearer-only allocation, quota/conflict failures and retry-safe server errors.
- Studio preserves inventory problem codes before its generic error normalizer, serializes actionable quota/conflict messages, explains unavailable usage, and returns a newer mirror snapshot instead of a rejected stale response on first load. Adapter tests use the real normalizer. The 23 Studio suites now contain 350 passing tests; SDK transport coverage contains 12 passing cases.

Signed live flows remain unverified. Invalid or incomplete authoritative identity metadata still fails closed; silently accepting malformed global snapshots would weaken the cutover contract.

## Rollout still required

1. Back up and audit the gateway database's global union, including over-limit owners, mixed flags, and canonical conflicts. Stop old chain-only writers before migrating; do not run old and new inventory writers together.
2. Deploy the AVS registry/API and migration with the explicit global infra setting. Confirm the migration reports before enabling Studio changes. No destructive cleanup is part of this migration.
3. Apply Studio's additive Prisma migration before deploying its mirror consumers. Snapshots are imported from AVS on refresh; legacy Prisma rows do not establish global visibility. Publish the SDK companion release separately; Studio's adapter avoids coupling this deployment to an unpublished version.
4. Run signed integration verification using an owner whose salt `4` and a higher non-contiguous salt lack runtime rows on the target networks. Exercise policy setup, sponsorship, simulation, execution, withdrawal, and independent nonces. Include a Sepolia-triggered workflow with Base Sepolia and Sepolia write nodes. The local suites and read-only derivation checks do not establish that complete live flow.

A rollback to chain-only writers after any global mutation requires data reconciliation, rather than a binary-only rollback. Never auto-hide funded wallets to force the migrated union under three.
