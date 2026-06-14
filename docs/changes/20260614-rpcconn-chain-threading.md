# Thread chainID through run_node/enrichment; retire the AVS-chain `rpcConn`

## Why

PRs 1–4 of `20260614-route-vm-execution-through-workers.md` migrated every
**chain-aware** gateway VM path (getWallet, contractRead, contractWrite)
onto per-chain `ChainStateReader`s. What remains is a cluster of reads
that go through the **package-level `rpcConn`** — a single
`*ethclient.Client` set once at startup from
`config.SmartWallet.EthRpcUrl` (`engine.go:SetRpc`, the gateway's **AVS
chain**, currently Sepolia).

Two problems with `rpcConn`:

1. **Wrong chain in multi-chain mode.** These reads hit the AVS chain
   regardless of the workflow's actual chain. A contractRead-style
   enrichment or event search for a Base workflow reads Sepolia. This is
   a latent correctness bug today, not just a routing concern.
2. **Last `eth_rpc_url` consumers.** `rpcConn` (and the per-chain VM dials,
   now migrated) are the reason the gateway still needs every chain's
   `eth_rpc_url`. Retiring `rpcConn` is the precondition for the env-var
   strip (PR 7 of the parent doc).

`rpcConn == nil` also doubles as the **test/CI mock signal**
(`engine.go:1261` GetWallet, etc.) — any migration must preserve that.

## Complete `rpcConn` inventory

| Site | Enclosing func | Op | chainID source |
|---|---|---|---|
| `engine.go:3089` | `instructOperatorImmediateTrigger(taskID)` | `BlockNumber` | load task → `task.ChainId` |
| `run_node_immediately.go:110,130` | `runBlockTriggerImmediately(triggerConfig, …)` | `BlockNumber`, `HeaderByNumber` | `triggerConfig["chainId"]` (block trigger is chain-scoped) |
| `run_node_immediately.go:2252` | `parseEventWithParsedABI(eventLog, abi, query)` | `HeaderByNumber` | thread param (caller `:1881` has it; `node_output_handlers.go:427` passes `nil` query) |
| `run_node_immediately.go:2311` | `callContractMethod(addr, callData)` | `CallContract` (decimals) | thread param (caller `:1958` has `eventLog` → event chain) |
| `run_node_immediately.go:2330` | `runEventTriggerWithHistoricalSearch(ctx, queries, …)` | `BlockNumber` | `EventTrigger_Config.GetChainId()` from the queries |
| `run_node_immediately.go:2654,2720,2764` | `searchEventsForQuery(ctx, addrs, topics, …)` | `FilterLogs` | thread param (caller `:2433`) |
| `utils.go:94` | `GetBlock(blockNumber)` (package func) | `HeaderByNumber` | needs a chainID param + caller audit |

Already converted: `engine.go:1261` (GetWallet) keeps `rpcConn == nil`
purely as the test signal — no chain read.

## Worker RPCs

Existing (reuse): `CallContract`, `HeaderByNumber` (PRs 2–3).

New, needed here:

- **`GetBlockNumber() → uint64`** — wraps `ethclient.BlockNumber`. Trivial.
- **`FilterLogs(addresses, topics, from_block, to_block) → []Log`** — wraps
  `ethclient.FilterLogs`. **Payload-sensitive** (see Risks). The gateway
  already chunks ranges caller-side (`:2764`); keep chunking on the
  gateway and have the worker serve one bounded range per call, OR move
  chunking worker-side and stream. Decide before building.

Add `GetBlockNumber` and `FilterLogs` to the `ChainStateReader` interface
+ both impls, mirroring the existing methods.

## Strategy: thread chainID, resolve per-chain reader

Each function above gains a `chainID int64` parameter (or extracts it from
a config/trigger already in scope), then:

```go
reader := GetChainStateReaderForChain(uint64(chainID))
if reader == nil {
    // test/CI (no registry) or unconfigured chain — preserve today's
    // behavior: skip the read (mock/degraded path), exactly as the
    // rpcConn == nil branches do now.
}
```

The `rpcConn == nil` test signal is preserved: in tests the registry is
empty, so `GetChainStateReaderForChain` returns nil and we take the same
degraded/mock branch the code takes today. Production (gateway) always has
per-chain readers registered.

The threading is mechanical but wide — it touches the run_node entry
points, event historical-search, and event-enrichment helpers. Do it in
small, independently-shippable slices, each verified before the next.

## PR breakdown

1. **PR A — `GetBlockNumber` + `GetBlock`/block-trigger reads.** Add the
   `GetBlockNumber` worker RPC + reader method. Thread chainID through
   `instructOperatorImmediateTrigger`, `runBlockTriggerImmediately`,
   `GetBlock` (+ its callers). Lowest-risk, no payload concerns.
2. **PR B — event-enrichment reads.** Thread chainID through
   `parseEventWithParsedABI` + `callContractMethod`; route the
   `HeaderByNumber` (event block timestamp) and `CallContract` (decimals)
   through the per-chain reader. Subsumes the `callContractMethod`
   deferral noted in parent PR 2.
3. **PR C — `FilterLogs` + historical search.** *Gated on the design
   question below.* Add `FilterLogs` worker RPC (with the chunking
   decision settled); thread chainID through
   `runEventTriggerWithHistoricalSearch` + `searchEventsForQuery`.
4. **PR D — retire `rpcConn` + the env-var strip.** Once no chain read
   uses `rpcConn`, replace the `rpcConn == nil` test signal with an
   explicit test flag, drop the per-chain dial fallbacks, strip the
   per-chain `eth_rpc_url`/`bundler_url` from `gateway-railway.yaml`, and
   delete the `<CHAIN>_RPC` env vars from the gateway Railway service.
   Keep the top-level `eth_rpc_url` (AVS chain). **This is PR 7 of the
   parent doc — the irreversible step, after a prod cycle.**

## Open design question — should the gateway run `FilterLogs` at all?

Operators already own event monitoring and historical replay. The
gateway's `FilterLogs` path (`runEventTriggerWithHistoricalSearch`) is the
manual-run / simulate path for event triggers. Before building PR C,
decide:

- **Route it** through the worker (per this plan), or
- **Redesign** — have the gateway ask an operator (which already holds the
  event subscription + history) rather than replaying logs itself.

Routing is the smaller change and keeps the gateway self-sufficient for
simulate/runTrigger; redesign removes duplicate chain work but couples the
gateway's simulate path to operator availability. Resolve before PR C.

## Risks

- **`FilterLogs` payload size.** A wide block range returns large log sets.
  Keep per-call ranges bounded (gateway-side chunking already exists at
  `:2764`); never let the worker return one unbounded message. Consider
  streaming if single-range payloads can still be large.
- **Block-number atomicity.** Functions that read `BlockNumber` then act
  on `HeaderByNumber`/`FilterLogs(at block)` across two gRPC calls can
  straddle a new head. Where it matters (historical search), pin the
  height explicitly across calls.
- **Test-mode signal.** The `rpcConn == nil` mock signal is load-bearing
  across many tests. Until PR D swaps it for an explicit flag, keep the
  nil-reader branch behaviorally identical to today's nil-rpcConn branch.
- **Wide blast radius.** Threading chainID touches many call sites; keep
  each PR to one function cluster and lean on the existing run_node tests.
