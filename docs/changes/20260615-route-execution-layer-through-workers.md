# Route the gateway's execution + simulation dials through workers

## Why

PRs #584–#593 migrated the gateway's **read** surface to per-chain workers
(token metadata, balances, getWallet derivation, contractRead `eth_call`,
block reads, event enrichment) and deleted the event historical `FilterLogs`
search. But the env-var strip — deleting the per-chain `<CHAIN>_RPC` vars
from the gateway — is still blocked: a handful of **execution / simulation**
paths dial `ethclient.Dial(swConfig.EthRpcUrl)` directly.

`rpcConn` itself is NOT one of these — it's the gateway's **AVS-chain**
client (top-level `eth_rpc_url` = `SEPOLIA_RPC`), the one chain the gateway
legitimately keeps. (The dead `utils.GetBlock` that read it has been
removed.) This doc is strictly about the **per-chain** `<CHAIN>_RPC` dials.

## Inventory of remaining per-chain dials

### Live (must migrate)

| Site | Operation | Target |
|---|---|---|
| `executor.go:896` `validateWalletOwnership` | `user.LoadDefaultSmartWallet(rpc)` — derive salt:0 wallet | worker `GetSmartWalletAddress` |
| `executor.go:938` `validateDerivedWallet` | `aa.GetSenderAddressForFactory(rpc, …)` | worker `GetSmartWalletAddress` |
| `run_node_immediately.go:2568` | derive salts 0–4 via `aa.GetSenderAddress` | worker `GetSmartWalletAddress` (×5) |
| `vm_contract_write_waiting.go:90,185` | userop receipt / confirmation polling (+ bundler) | new worker `GetTransactionReceipt`, or fold into `ExecuteUserOp` |
| `simulation_state.go:331` `InjectERC20BalanceChange` | `ProbeERC20BalanceSlot` (`eth_getStorageAt`) + balance read for Tenderly state overrides | new worker `GetStorageAt` (+ existing `GetTokenBalance`), or keep Tenderly-side |

### Fallback-only (dead in production)

`vm.go:1489,1530,1751` (contractRead/Write/ethTransfer), `engine.go:1293`
(getWallet), and the analogous dials in `executor.go`, `run_node`,
`vm_contract_write_waiting`, and `simulation_state` dial `swConfig.EthRpcUrl`
**only** when `GetChainStateReaderForChain` returns nil.

There is no "single-chain" production deployment. The aggregator always runs
as the gateway: it connects to its **AVS chain** (Ethereum mainnet, or
Sepolia on testnet — the top-level `eth_rpc_url`) for the EigenLayer /
registry contracts, and to N chain workers over gRPC for execution. A
per-chain reader is registered for **every** `chains[]` entry at startup, so
the nil-reader guard is always false and these dials **never execute in
production** — they're test scaffolding / defensive no-ops, not a real
alternative runtime. They still reference the env-var-backed URL, so they're
deleted (not preserved) in the final step.

## Strategy

The reads were migrated by introducing per-chain readers; the execution
dials follow the same model, reusing existing worker RPCs where possible:

- **Wallet derivation** (`GetSenderAddress[ForFactory]`) → the existing
  `GetSmartWalletAddress` worker RPC (added in PR #585). No new proto.
- **UserOp send** already routes through `ExecuteUserOp`. The residual
  receipt-waiting is the gap; prefer returning the receipt from
  `ExecuteUserOp` over adding a polling RPC, if the worker already waits.
- **Tenderly state overrides** are the odd one — the storage-slot probe is
  an `eth_getStorageAt` scan that exists *to build a Tenderly simulation
  request*. Evaluate whether Tenderly can supply the balance override
  itself (it accepts `state_objects`) before adding a worker `GetStorageAt`.

## PR breakdown

1. **PR 1 — wallet-derivation dials (delivered).** The `validateDerivedWallet`
   salt-scan (up to `MaxWalletsPerOwner` = 2000) made per-salt routing a
   non-starter, so a **server-side scan RPC** `FindMatchingWalletSalt(owner,
   factory, target, max_salts) → (found, salt)` was added (capped at 2000):
   the worker runs the loop + comparison locally, one round-trip. Added it
   to the `ChainStateReader` interface + both impls. Migrated:
   `validateWalletOwnership` Step 1 (default wallet) → `GetSmartWalletAddress`;
   `validateDerivedWallet` and the `run_node` salt-scan → `FindMatchingWalletSalt`.
   Each keeps a direct-dial + local-loop fallback for the no-reader (test)
   path. `LoadDefaultSmartWallet` stays for the fallback; the reader
   path sets `user.SmartAccountAddress` from the worker-derived address
   (using the task's per-chain factory, fixing the latent global-factory
   inconsistency).
2. **PR 2 — userop receipt waiting (delivered).** `waitForUserOpConfirmation`
   is a real pending-receipt poll (reachable when a send returns "pending"),
   so it was migrated rather than dropped: added a `GetTransactionReceipt`
   worker RPC (gas/status/block fields; `found=false` → pending) + a
   `ChainStateReader.GetTransactionReceipt` (NotFound → `(nil, nil)`). The
   single `client.TransactionReceipt` call now goes through `v.receiptForTx`
   (per-chain reader, direct-dial fallback), removing the function's
   top-level chain dial. Deleted the dead `getReceiptByUserOpHash`
   placeholder (it dialed then returned an error without using the client).
   The bundler poll (`bundler.GetUserOperationReceipt`) stays — bundler
   routing is a separate concern from chain RPC.
3. **PR 3 — simulation state overrides (delivered).** Tenderly's
   `state_objects` can't supply an ERC-20 balance override without the
   discovered storage slot, so the probe stays — routed through a new worker
   `GetStorageAt` RPC. Added it to `ChainStateReader`; changed
   `ProbeERC20BalanceSlot` to take a `ChainStateReader` (its `balanceOf`
   `eth_call` reuses `CallContract`; its slot probing uses `GetStorageAt`)
   and `InjectERC20BalanceChange` to resolve the per-chain reader (direct
   dial fallback). The probe result is cached per token, so the worker
   round-trips are one-time. **With this, every live per-chain read / write
   / derivation is worker-routed** — the remaining `ethclient.Dial` sites in
   `core/taskengine` are all behind the nil-reader guard and so are dead in
   production (test scaffolding / defensive only; see "Fallback-only" above).
4. **PR 4 — delete the dead fallback dials + env-var strip.** The
   production gateway never dials per-chain RPC anymore, so:
   - **Delete the dead fallback dials** in `vm.go`, `engine.go`,
     `executor.go`, `run_node_immediately.go`, `vm_contract_write_waiting.go`,
     and `simulation_state.go` — making "reader required" explicit rather
     than keeping a never-taken dial. Update the tests that relied on them to
     inject a `ChainStateReader` directly (most already do).
   - Remove `smartWalletRpcByChain` + `ChainEntry.GetRPC` + the REST/withdraw
     direct-reader fallbacks (the aggregator's per-chain RPC pool).
   - Strip the per-chain `eth_rpc_url` / `bundler_url` from
     `gateway-railway.yaml`'s `chains:` block.
   - Delete `ETHEREUM_RPC` / `BASE_RPC` / `BASE_SEPOLIA_RPC` / `BNB_RPC` and
     the per-chain `*_BUNDLER_URL` env vars from the gateway Railway service.
   Keep the top-level `eth_rpc_url` — the AVS chain (Ethereum mainnet, or
   `SEPOLIA_RPC` on testnet).

   **This is the irreversible step** — only after PRs 1–3 are in prod for a
   release cycle (same migrate-then-strip discipline as every prior phase).

## End state

The aggregator runs only as the gateway. It holds exactly one chain
connection — its **AVS chain** (Ethereum mainnet, or Sepolia on testnet),
via the top-level `eth_rpc_url` (`rpcConn` + `smartWalletRpc`), for the
EigenLayer / registry contracts — and proxies every execution-chain read,
write, and derivation to the N per-chain workers over gRPC. There is no
single-chain mode and no per-chain RPC on the gateway.

## Risks

- **`LoadDefaultSmartWallet` refactor** touches user-model wiring; it's
  used in ownership validation on a hot path. Verify the derived-address
  equivalence carefully.
- **Receipt waiting** semantics: if `ExecuteUserOp` doesn't already block
  until mined, the gateway needs *some* confirmation signal; don't drop the
  wait without a replacement.
- **Tenderly `GetStorageAt`**: a storage-slot probe over gRPC is fine
  (single slot, tiny payload), but confirm Tenderly can't just take the
  override — migrating a read that only feeds a Tenderly request would be
  wasted work.
