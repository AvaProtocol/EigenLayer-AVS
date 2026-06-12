# Delegate chain RPC reads to workers; drop per-chain RPC config from the gateway

## Why

The gateway today holds direct `*ethclient.Client` connections to every
chain (`SEPOLIA_RPC`, `BASE_RPC`, `ETHEREUM_RPC`, `BASE_SEPOLIA_RPC`,
`BNB_RPC`) so it can run `TokenEnrichmentService.GetTokenMetadata` for
ERC-20 lookups during execution. This duplicates state already
maintained by the chain workers (each worker owns one chain RPC), and
forces the gateway to track 5 RPC URLs + 5 sets of credentials when it
should logically only need its own AVS-chain RPC.

Pain that surfaced operationally:

- A dead Dwellir BNB host took the gateway through a noisy boot warn
  on every restart, even though BNB workflows weren't running on the
  gateway — the gateway was just trying to init its own BNB token
  service.
- A rotated key requires updating 6 services' env vars; really only
  one (the worker for that chain) needs to change.
- Gateway config (`gateway-railway.yaml`) maintains 5 `chains:`
  entries — half of which (RPC URL, bundler URL) duplicate what each
  worker already has in its own `*-railway.yaml`.

## Today's state

Worker-side gRPC surface (`protobuf/worker.proto`) — already exposes
chain-state methods:

```
service ChainWorker {
  rpc WorkerHealthCheck(...)
  rpc ExecuteUserOp(...)
  rpc GetNonce(...)
  rpc GetSmartWalletAddress(...)
  rpc GetTokenMetadata(WorkerGetTokenMetadataReq) returns (WorkerGetTokenMetadataResp);
}
```

`worker/server.go:143-159` implements `GetTokenMetadata` by calling
the worker's local `tokenService.GetTokenMetadata(addr)`. So the
worker can already answer token-metadata queries — but the gateway
doesn't call it.

Gateway-side direct chain reads we want to eliminate:

| File:line | Call | Replacement |
|---|---|---|
| `core/taskengine/engine.go:4964` | `Engine.GetTokenMetadata(user, payload)` → `n.tokenEnrichmentService.GetTokenMetadata(addr)` | Route to worker for `payload.ChainId`, call `ChainWorker.GetTokenMetadata` |
| `core/taskengine/shared_event_enrichment.go:237` | `tokenService.GetTokenMetadata(contractAddr)` | Same — chain comes from event log context |
| `core/taskengine/run_node_immediately.go:1626,2167` | `n.tokenEnrichmentService.GetTokenMetadata(addr)` | Same — chain from task |
| `core/taskengine/summarizer_context_memory.go:733` | `tokenService.GetTokenMetadata(address)` | Same — chain from execution context |

All five call sites already have access to the chain context (the
task's `ChainId` is the canonical source), so the routing layer is
straightforward.

## Plan

### Phase 1 — introduce a `ChainTokenLookup` indirection in the gateway

New type in `core/taskengine/`:

```go
// ChainTokenLookup resolves token metadata for a given chain.
// Production implementation routes via the chain's worker gRPC;
// tests can swap in a fake.
type ChainTokenLookup interface {
    GetTokenMetadata(ctx context.Context, chainID uint64, addr string) (*TokenMetadata, error)
}
```

Two implementations:

1. **Worker-routed** (prod): looks up the worker for `chainID` from
   the `ChainRegistry` already maintained at `aggregator/chain_registry.go`,
   calls `ChainWorker.GetTokenMetadata` over the existing gRPC
   connection. The registry already knows the mapping
   (`worker-bnb-mainnet.railway.internal:50051` → chain 56 etc.) —
   we'd just expose a getter.
2. **Local-fallback** (single-binary, integration tests): falls back
   to the existing `TokenEnrichmentService` for callers that don't
   pass through the gateway.

### Phase 2 — replace the five call sites

Each of the five gateway-side callsites already has the chain ID in
scope. Convert them in one PR:

```go
// before
md, err := n.tokenEnrichmentService.GetTokenMetadata(addr)

// after
md, err := n.chainTokenLookup.GetTokenMetadata(ctx, task.ChainId, addr)
```

`TokenEnrichmentService` itself stays — the **worker** uses it
unchanged. We're only changing who *invokes* it.

### Phase 3 — drop per-chain RPC config from the gateway

Once Phase 2 is live and confirmed in production:

1. Remove the per-chain RPC + bundler entries from
   `gateway-railway.yaml`'s `chains:` block. Each entry collapses to
   just `worker_addr: worker-X.railway.internal:50051` + the static
   AVS contract addresses.
2. Delete `SEPOLIA_RPC`, `BASE_RPC`, `ETHEREUM_RPC`, `BASE_SEPOLIA_RPC`,
   `BNB_RPC` env vars from the **gateway** Railway service. (The
   single `eth_rpc_url` at the top of the config — the AVS-registered
   chain, currently Sepolia — stays.)
3. Workers keep `CHAIN_ENDPOINT` (post-rename from PR landing today).

### Phase 4 — audit other gateway direct-RPC paths

Token metadata is the easiest one because the worker gRPC is already
in place. Other direct-RPC consumers in the gateway worth auditing:

- **`aggregator/task_engine.go`** chain config bootstrap — currently
  loads paymaster, factory, entrypoint addresses for every chain.
  These are static (not RPC-read), so they don't need delegation,
  but the config layout should reflect that they're chain-static and
  not RPC-dependent.
- **`aggregator/chain_registry.go`** — already routes via worker
  for chain-specific calls. Confirm there's no fallback path that
  dials chain RPC directly from the gateway.
- **ETH transfer / contract-write nodes** that resolve sender,
  estimate gas, sign userops — likely already go through worker
  `ExecuteUserOp` + `GetNonce` + `GetSmartWalletAddress`. Trace and
  confirm.

If any direct-RPC paths surface in Phase 4 that the worker doesn't
already cover, extend the worker gRPC surface as needed before
ripping out the gateway's RPC config.

## Risks & rollback

- **Worker-routed lookup latency**: adds 1 gRPC round-trip per token
  lookup. Workers are on Railway's private network (`<svc>.railway.internal`),
  so single-digit milliseconds. The local
  `TokenEnrichmentService` cache lives on the worker side now —
  consider whether the gateway also needs a thin LRU in front to
  collapse repeated lookups per execution.
- **Worker availability**: a dead worker makes token lookups for its
  chain fail. Already true today for other worker calls (nonce,
  userop) — the gateway already handles worker-unreachable gracefully.
  No new failure mode introduced.
- **Rollback**: revert the Phase 2 PR. The
  `TokenEnrichmentService` and gateway env vars stay in place
  through Phase 2, so there's no flag-day. Phase 3 (deleting gateway
  RPC config) is the irreversible step — only do it after Phase 2
  has been stable in prod for at least one release cycle.

## Out of scope

- Refactoring `TokenEnrichmentService` itself. It's the right
  internal API; just executed at the wrong layer.
- Cross-chain token metadata (a token at the same address on two
  chains). Each lookup already carries `chainID`; no design change
  needed.
- Removing the `SEPOLIA_RPC` env var on the gateway (the AVS-RPC,
  not a chains[] entry). The gateway still needs to talk to the
  EigenLayer/AVS contracts on its registered chain.

## Concrete sequencing

1. **PR A**: introduce `ChainTokenLookup` interface + worker-routed
   implementation. No call-site changes; type sits unused except for
   tests.
2. **PR B**: migrate the five call sites to use `ChainTokenLookup`.
   Behavior-preserving — production switches to worker-routed lookups.
3. **One full release cycle on Phase 2** to confirm no regressions
   (Sentry should stay quiet on token lookups).
4. **PR C**: strip per-chain RPC entries from `gateway-railway.yaml`;
   delete corresponding env vars from the gateway Railway service.

PR A + B can land in the same release. PR C waits for the safety
cycle.
