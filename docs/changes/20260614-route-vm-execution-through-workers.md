# Route gateway VM reads through workers

## Current architecture (corrected 2026-10-18, #851)

The June read migration did not move VM writes into workers. The previous
version of this document incorrectly said ContractWrite already used
`ExecuteUserOp` and proposed deleting the gateway's per-chain RPC and bundler
configuration. That proposed end state is superseded by the architecture below.

The gateway owns workflow execution, its BadgerDB session grants and policy
resolution. Chain workers serve read RPCs. A quiet worker log does not mean a
workflow did not run: successful reads are not all logged.

| Operation | Process / route |
|---|---|
| Workflow scheduling, graph execution and session-policy resolution | Gateway |
| Contract writes and native transfers | Gateway -> UserOp preset -> per-chain bundler/RPC |
| Withdraw sends | Gateway, since #762; the worker `ExecuteUserOp` RPC was removed |
| Contract reads, balances, gas, code, nonce, wallet derivation | Per-chain `ChainStateReader` -> worker when available |
| Block headers, transaction receipts, storage-slot reads | Per-chain reader -> worker when available |
| Tenderly simulation and construction of balance/allowance overrides | Gateway; supporting chain reads may use workers |
| EigenLayer registration and operator trigger monitoring | Gateway/operator AVS-chain connections |

## Read migration delivered

The read interface in `core/taskengine/chain_state_reader.go` has direct and
worker implementations. The gateway registry selects a reader by execution
chain ID, rather than accidentally reading every workflow on the AVS chain.

- Token metadata uses worker `GetTokenMetadata` through the token enrichment service.
- Gas price, gas estimation, code, EntryPoint nonces, and native/ERC-20 balances
  use the corresponding worker read RPCs.
- Wallet derivation uses `GetSmartWalletAddress`, including salt and factory
  overrides. Salt discovery uses `FindMatchingWalletSalt` on the worker.
- ContractRead uses `CallContract`; block stamping uses `GetBlockHeader`.
- Receipt and storage reads use `GetTransactionReceipt` and `GetStorageAt`.

In gateway mode each configured `chains[]` entry prefers its connected worker.
The existing direct-reader fallback remains when no worker client is available
at startup. The later legacy default-token initialization must not replace a
registered gateway reader. #851 removes that accidental override for the first
configured chain (commonly Sepolia). Legacy single-chain mode still registers
its direct reader.

## Writes stay in the gateway

`core/taskengine/vm_runner_contract_write.go` calls the UserOp preset in
`pkg/erc4337/preset/`. The preset resolves the session grant against gateway
storage and uses gateway configuration for the execution chain. Workers cannot
resolve those grants from their stateless processes. VM ContractWrite did not
use the old worker send RPC; only withdrawals used it before #762.

The same distinction applies to simulation. Tenderly HTTP calls and accumulated
state overrides belong to the gateway's workflow simulation. Worker storage,
balance or call reads are supporting inputs, not worker execution of a write.

Keep the gateway's per-chain `smart_wallet.eth_rpc_url`, bundler/provider,
controller, and sponsorship configuration. `vm.go` still requires the execution
chain's RPC configuration for writes. Migrating a read does not make these
settings removable. The top-level AVS-chain RPC remains necessary too.

## Verification

Inspect `gateway.log` for contract-write execution context, Tenderly requests,
state overrides, UserOp hashes, transaction hashes and confirmation outcomes.
Inspect the matching worker for connection/read failures; do not require a
successful write or Tenderly request in its log.

Regression coverage in `aggregator/task_engine_readers_test.go` checks every
configured chain, including the first one, still reads through its own worker
after legacy default initialization, and preserves the direct fallback and
single-chain behavior. Run `go test -race ./aggregator ./worker`.

## Future work

Any removal of direct read fallbacks must handle startup/reconnect behavior
explicitly. Moving writes into another process would additionally require a
new session-policy/storage design; it is not a remaining phase of this read
migration. This change does not introduce that design or remove RPC settings.
