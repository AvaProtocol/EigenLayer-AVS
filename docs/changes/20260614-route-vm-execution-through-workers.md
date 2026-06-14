# Route the gateway's VM execution + wallet derivation through workers

## Why

`docs/changes/20260612-delegate-chain-rpc-to-workers.md` sketched a
"Phase 4b" that would delete the per-chain `*_RPC` env vars from the
gateway once withdraw balance reads were migrated (Phase 4a, PR #584).
**That plan was based on an incomplete inventory.** Phase 4a freed the
`smartWalletRpcByChain` map, but that map was never the gateway's only
per-chain RPC dependency.

The gateway's **taskengine VM** and **wallet-derivation** layer dial each
chain's `eth_rpc_url` directly and on-demand — from the same config field
the env vars feed — entirely separate from `smartWalletRpcByChain`.
Deleting the env vars would break these live gateway endpoints in prod:

- `getWallet` (CREATE2 address derivation) — hard error on empty RPC URL
- `runNodeWithInputs` / `simulateTask` for contractRead, contractWrite,
  ethTransfer nodes
- event-trigger replay (`runTrigger` on event nodes) and event enrichment

So "the gateway talks to exactly one chain" requires routing the whole VM
read/execute surface through workers — not a config strip. This doc
inventories that surface and lays out the phased migration.

## Complete inventory of gateway direct chain-RPC

Every site below runs in the **gateway** process (aggregator REST/gRPC
handlers → engine → VM), against a per-chain `swCfg.EthRpcUrl` or the
package-level `rpcConn`. Grouped by the chain operation performed.

### A. Already migrated (Phases A–4a)

| Operation | Worker RPC | Status |
|---|---|---|
| Token metadata | `GetTokenMetadata` | #579 |
| Gas price / estimate / code | `SuggestGasPrice` / `EstimateGas` / `GetCode` | #580 |
| EntryPoint nonce | `GetNonce` / `GetNonceByAddress` | #580 |
| Native + ERC-20 balance | `GetBalance` / `GetTokenBalance` | #584 |
| UserOp send | `ExecuteUserOp` | pre-existing |

### B. CREATE2 wallet derivation — **worker RPC already exists**

| Site | Call | Target |
|---|---|---|
| `engine.go:1275` (`GetWallet`) | `aa.GetSenderAddressForFactory(chainRPC, owner, factory, salt)` | `GetSmartWalletAddress` |
| `vm_runner_contract_write.go:715` | `aa.GetSenderAddress(client, owner, salt=0)` | `GetSmartWalletAddress` |

The worker's `GetSmartWalletAddress` (`worker/server.go:148`) already does
exactly this via `aa.GetSenderAddressForFactory` against the worker's own
`smartWalletCfg.FactoryAddress`. The only gap is the gateway passing a
per-chain factory override; in practice the per-chain factory == the
worker's configured factory, so routing `GetWallet` to the existing RPC
is the **lowest-effort, highest-traffic** win. Worker RPC may need an
optional `salt` (it takes one) and to surface the factory it used so the
gateway can assert agreement.

### C. Generic `eth_call` (contractRead + decimals + sanity) — **new RPC**

| Site | Call |
|---|---|
| `vm_runner_contract_read.go:166,295,950` | `r.client.CallContract(ctx, msg, nil)` |
| `run_node_immediately.go:2311` | `rpcConn.CallContract(ctx, msg, nil)` |

Needs a new `CallContract(to, data, [from], [block]) -> bytes` worker RPC
(wraps `ethclient.CallContract`). This is the single biggest surface —
contractRead is a core node type and `runNodeWithInputs`/`simulate` lean
on it heavily.

### D. Block context — **new RPC**

| Site | Call |
|---|---|
| `run_node_immediately.go:110,2330` | `rpcConn.BlockNumber(ctx)` |
| `run_node_immediately.go:130,2252` | `rpcConn.HeaderByNumber(ctx, n)` |
| `vm_runner_contract_write.go:554` | `r.client.HeaderByNumber(ctx, nil)` |

Needs `GetBlockNumber()` and `GetBlockHeader(number)` worker RPCs
(timestamp + hash + number are what callers read).

### E. Event log queries — **new RPC, payload-sensitive**

| Site | Call |
|---|---|
| `run_node_immediately.go:2654,2720,2764` | `rpcConn.FilterLogs(ctx, query)` |

Needs `FilterLogs(addresses, topics, fromBlock, toBlock) -> []Log`. The
gateway already chunks ranges (`:2764`); the worker RPC should keep the
chunking on the worker side and stream/paginate, since a wide range can
return large payloads over gRPC. This is the riskiest one for message
size and is also the operator's domain (operators already watch events) —
confirm we want the gateway replaying events at all, or whether
`runTrigger` on event nodes can be redesigned.

### F. UserOp gas estimation / receipts — **partly new**

| Site | Call |
|---|---|
| `vm_runner_contract_write.go:784` | dial + bundler gas estimation for logs |
| `executor.go:896,938` | dial for execution-path userop build |
| `run_node_immediately.go:3059` | dial for contractWrite immediate run |

ContractWrite already sends via `ExecuteUserOp`; the residual dials are
for **pre-send gas-estimation logging** and **balance checks**. Most can
be folded into `ExecuteUserOp` (have the worker return the gas estimate
it computed) or dropped (log-only). `TransactionReceipt` waiting may need
a `GetTransactionReceipt(hash)` worker RPC if any gateway path polls
receipts directly.

### G. Simulation state overrides (Tenderly) — **special case**

| Site | Call |
|---|---|
| `simulation_state.go:331+` | `ProbeERC20BalanceSlot` + `eth_getStorageAt` via `swCfg.EthRpcUrl` |

`runNodeWithInputs`/`simulate` uses storage-slot probing for ERC-20
balance overrides. Needs either a `GetStorageAt(addr, slot)` worker RPC +
keep slot-probing on the gateway, or move the whole probe to the worker
(`ProbeBalanceSlot` RPC). Tenderly HTTP calls themselves are not chain
RPC and stay on the gateway.

## Recommended approach: interface extraction, not call-site surgery

The VM passes a concrete `*ethclient.Client` (`vm.go:1490,1557`;
`NewContractReadProcessor`, `NewContractWriteProcessor`) and uses a
package-level `rpcConn`. Migrating each `.CallContract` / `.HeaderByNumber`
call individually would be error-prone.

Instead, extend the **`ChainStateReader` pattern already established in
`core/taskengine/chain_state_reader.go`** into a fuller `ChainReader`
interface covering the read surface (CallContract, BlockNumber,
HeaderByNumber, FilterLogs, GetStorageAt, TransactionReceipt, plus the
existing gas/code/balance/nonce methods), with:

1. `directChainReader` — wraps `*ethclient.Client` (single-chain mode,
   tests, operator), behaviour-identical to today.
2. `workerChainReader` — routes each method to the chain worker over the
   gRPC client already held in `ChainRegistry`.

Then change the VM/processors to accept a `ChainReader` instead of
`*ethclient.Client`, resolved per-chain via the existing
`GetChainStateReaderForChain` registry. The write path (userops) keeps
flowing through `ExecuteUserOp`. This reuses the exact registry +
fallback machinery Phases 3–4a built.

## Phased PR breakdown

Each phase is independently shippable and prod-verifiable before the
next — same migrate-then-verify discipline as Phases 2→4a.

1. **PR 1 — `GetWallet` → `GetSmartWalletAddress` (delivered).**
   Highest-traffic, lowest-risk. Routed `engine.go:GetWallet`'s factory
   derivation through the chain worker; the direct dial stays as the
   no-reader fallback. The existing `GetSmartWalletAddress` worker RPC had
   **zero callers**, so it was redefined (no back-compat needed): salt is
   now a base-10 `string` (was `int64`, which couldn't carry a 256-bit
   salt) and a `factory_address` override was added so worker-derived
   addresses match the gateway's per-request factory exactly. Added
   `GetSmartWalletAddress` to the `ChainStateReader` interface + both
   implementations. Removes the single most-hit per-chain dial.
2. **PR 2 — `CallContract` worker RPC + contractRead (delivered).** Added
   the `CallContract` worker RPC (from/to/value/data/block) + handler, and
   `CallContract` to `ChainStateReader` (direct + worker-routed). Switched
   `ContractReadProcessor`'s field from `*ethclient.Client` to
   `ChainStateReader` — it used `CallContract`, `ChainID`, and `CodeAt`,
   all now on the interface — and `runContractRead` resolves the per-chain
   reader (worker-routed in gateway mode, direct-dial fallback otherwise).
   Deferred: `callContractMethod` (`run_node_immediately.go`) reads
   `decimals()` via the package-level `rpcConn` (the AVS-chain client, no
   chainID param) — chain-ambiguous, so it moves with the event-enrichment
   work (PR 3/5) where chain context is threaded through.
3. **PR 3 — block context (`GetBlockNumber` / `GetBlockHeader`).** Migrate
   the trigger/enrichment block reads.
4. **PR 4 — contractWrite / ethTransfer residual dials.** Fold gas
   estimation into `ExecuteUserOp`'s response; drop log-only dials; add
   `GetTransactionReceipt` if a polling path needs it.
5. **PR 5 — event log queries (`FilterLogs`).** Worker-side chunking +
   pagination. Gate behind a design review on whether the gateway should
   replay events at all (operator overlap).
6. **PR 6 — simulation storage probing (`GetStorageAt`).** Migrate
   `simulation_state` balance-slot overrides.
7. **PR 7 — the strip.** Only once PRs 1–6 are in prod and Sentry is
   quiet: remove `smartWalletRpcByChain`, `ChainEntry.GetRPC`, the
   per-chain `ethclient.Dial` in the VM, the direct-reader fallbacks, the
   per-chain `eth_rpc_url`/`bundler_url` from `gateway-railway.yaml`, and
   the `<CHAIN>_RPC` / `<CHAIN>_BUNDLER_URL` env vars from the gateway
   Railway service. Keep the top-level `eth_rpc_url` (`SEPOLIA_RPC`) — the
   gateway's own AVS chain.

End state: the gateway holds exactly one chain connection (its AVS chain)
and proxies all execution-chain reads + writes through workers.

## Risks

- **Latency.** Each migrated call adds one gRPC hop on Railway's private
  network (single-digit ms). contractRead-heavy workflows multiply this;
  consider a short-lived per-request read cache on the gateway.
- **`FilterLogs` payload size.** Wide block ranges can return large log
  sets; the worker RPC must paginate/stream, not return one giant message.
- **Block-number consistency.** A single logical operation that reads
  `BlockNumber` then `CallContract(at block)` across two gRPC calls can
  straddle a new head. Where atomicity matters, add an explicit block
  param so both reads pin the same height.
- **Operator overlap (events).** Operators already own event monitoring;
  the gateway's `FilterLogs` replay may be redundant — resolve before
  building PR 5.
- **Worker availability.** A dead worker fails reads for its chain. Same
  failure mode as every other worker-routed call today; the gateway
  already degrades gracefully. The direct-dial fallback stays until PR 7.

## Out of scope

- Operator-side RPC. Operators legitimately own chain connections for
  trigger monitoring — this is strictly about the gateway.
- The AVS-chain RPC on the gateway (`SEPOLIA_RPC` / top-level
  `eth_rpc_url`). That's the gateway's own chain and stays.
