# Operator capability hardening: soft-fail dials, liveness watermark, gateway coverage guards

## Why

The previous architecture audit (`docs/changes/20260612-delegate-chain-rpc-to-workers.md`)
established that chain-trigger monitoring lives in the operator, that
`SupportedChainIds` advertisement is the routing primitive the gateway
already uses, and that the right long-term shape is one operator binary
with per-network deployments (`operator-mainnet`, `operator-testnet`).

Three failure modes blocked that direction in production:

1. **Bad endpoint = dead operator.** `operator/worker_loop.go`'s startup
   path returned an error from `ethclient.Dial` and friends, so a single
   misconfigured chain crashed the whole operator — even though the
   other chains were healthy and the gateway's per-chain routing was
   already designed to handle a partial set.
2. **Stalled subscription = silent black hole.** `supportedChainIDs()`
   was a boot-time snapshot of `chainOrder`. If a WebSocket subscription
   stalled mid-run (Alchemy / Dwellir hosts dropping heads silently),
   the operator kept advertising the chain and the gateway kept routing
   tasks at it. Triggers never fired; nothing surfaced this in logs or
   metrics.
3. **Orphaned tasks.** A `CreateTask` request for a chain no operator
   advertised was accepted, written to storage, and then sat there
   forever — the task was `enabled` but no operator would ever pick it
   up. Existing tasks could become orphaned the same way when an
   operator's coverage shrank.

## What shipped

### 1. Soft-fail per-chain dial (`operator/worker_loop.go`)

Per-chain RPC dial / `ChainID` detect / chain_id-mismatch all moved
from `return err` to `log.Warn + continue`. The chain is excluded from
`chainTriggers` and therefore from `supportedChainIDs()`. Duplicate
chain_id stays fatal — that's a deterministic config bug, not a
transient one. If *every* configured chain fails, the operator still
hard-fails at the end of the loop (advertising no chains would make
the operator advertising-but-useless).

A skipped-chain summary line is logged once at startup so the operator
owner sees "chain excluded from advertised capability: name=base
reason=dial failed".

### 2. Liveness-driven capability (`operator/operator.go`, `protobuf/node.proto`)

`ChainTriggerSet` gained `lastHeadSeenAt time.Time` guarded by a small
RWMutex. The per-chain fan-in goroutine in `worker_loop.go` calls
`markHeadSeen()` on every block event. `supportedChainIDs()` filters
chains whose head is older than `chainCapabilityStaleThreshold`
(90s — comfortable headroom over 2× block time for every supported
chain).

`Checkin` proto gained `supported_chain_ids` (field 10). The operator
re-advertises the live set on every `Ping` (5s cadence). The aggregator
exposes `Engine.UpdateOperatorSupportedChains` which the
`RpcServer.Ping` handler calls to refresh the per-operator snapshot in
`trackSyncedTasks` — so a stalled subscription drops out of routing
within one Ping interval without forcing a `SyncMessages` reconnect.

Logs surface the narrowing (`operator advertising fewer chains than
configured`) on each Ping where the live set differs from the configured
set.

### 3. Observability gauges (`metrics/metrics.go`)

Two new operator-side Prometheus gauges:

- `ap_operator_advertised_chains{operator, version, chain_id}` —
  1 when advertising, 0 when dropped. Trivial alert: "any chain at 0
  for >2 minutes".
- `ap_operator_chain_head_lag_seconds{operator, version, chain_id}` —
  seconds since the last head observed. Rising values are a leading
  indicator before the chain crosses the staleness threshold.

Both are set on every Ping in `worker_loop.go`. Gauges are exposed via
the operator's existing `/metrics` endpoint — no scrape-config change.

The gateway side has no Prometheus infrastructure yet (the aggregator
binary has never bound a metrics endpoint), so gateway coverage
observability is structured logs only — covered by the orphan scan
below.

### 4. Gateway coverage guards (`core/taskengine/engine.go`)

`CreateWorkflow` now rejects (gateway mode only) when no connected
operator advertises the task's chain for block/event triggers:

> `FailedPrecondition: no connected operator currently monitors chain_id=N for X triggers; task cannot fire until coverage is restored`

A periodic `scanOrphanedTasks` loop (5-minute interval) walks every
loaded task and logs a warning per orphan. Degradation (operator drops
chain mid-run) is the common case; create-time validation alone catches
only the first half of that.

`chainNeedsOperatorMonitoring` classifies which trigger types depend on
operator coverage — block and event do, cron / fixed-time / manual
don't.

## Out of scope

- **`operator-mainnet` / `operator-testnet` config split**. Pure
  avs-infra YAML change — staged separately so the code-side primitives
  land first and can be exercised before the topology flip.
- **Gateway Prometheus endpoint**. Would unlock the
  `gateway_operators_per_chain{chain_id}` gauge the design doc called
  for; reasonable follow-up but not blocked on this PR.
- **Signed `NotifyTriggers` as a slashing hook**. The design doc was
  explicit about not shipping this — `keccak256(taskId || chainId ||
  blockNumber || triggerHash)` is an authenticated claim, not an
  objectively verifiable attestation, and a fraud-proof oracle would be
  needed to make slashing real.

## Backward compatibility

- Pre-multi-chain operators (empty `SupportedChainIds`) still count as
  "covers everything" — same back-compat semantics as before. External
  operators on legacy binaries are unaffected.
- `Checkin.supported_chain_ids` is a new optional field; old operators
  that don't send it will have the aggregator log "Operator advertised
  chain set changed via Ping" once on first contact (the engine still
  reflects the prior `SyncMessages`-snapshot set), then steady-state
  no-op.

## Testing

- `operator/chain_liveness_test.go` — markHeadSeen / isHeadFresh,
  stalled-chain filter, missing-trigger-set defense.
- `operator/multi_chain_test.go` — pre-existing FiltersZero test updated
  to seed `lastHeadSeenAt` so the new liveness filter is neutral.
- `core/taskengine/operator_capability_test.go` —
  `UpdateOperatorSupportedChains` live update + unknown-operator no-op,
  `operatorsCoveringChain` legacy/multi-chain/zero/uncovered branches,
  `chainNeedsOperatorMonitoring` classification, `scanOrphanedTasks`
  no-deadlock smoke.
