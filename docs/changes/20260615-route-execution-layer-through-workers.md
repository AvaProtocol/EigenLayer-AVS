# Gateway execution and worker reads

## Correction to the original execution-routing plan (#851)

This document originally proposed routing every execution-chain read and write
through workers and stripping the gateway's per-chain RPC/bundler settings.
That plan is superseded. Reads use worker-backed interfaces, while writes,
session-policy resolution and Tenderly simulation execute in the gateway.

The former claim that UserOp sends already used worker `ExecuteUserOp` was
incorrect for VM ContractWrite. The worker send RPC only served withdrawals
and was removed in #762, when withdrawals moved to the gateway. The
`protobuf/worker.proto` ChainWorker service is now read-only.

## What routes through workers

The gateway chooses a per-chain `ChainStateReader` for balances, gas, code,
nonces, `eth_call`, headers, transaction receipts, storage reads, and wallet
address/salt derivation. Token metadata has an analogous worker-routed fetcher.
The delivered read work includes:

1. `GetSmartWalletAddress` and `FindMatchingWalletSalt` for wallet validation.
2. `CallContract` for contract reads and `GetBlockHeader` for block context.
3. `GetTransactionReceipt` for supporting confirmation reads; bundler polling
   remains gateway-side.
4. `GetStorageAt` and balance reads for simulation storage-slot probing.

`aggregator/task_engine.go` registers each configured chain's reader. Connected
workers are preferred; the existing startup fallback can still register a
direct reader. The legacy default-token initialization must not overwrite the
first configured gateway chain with a direct reader. #851 makes its reader
registration single-chain-only and tests all configured gateway chains.

## What remains gateway-side

- The VM traverses the workflow and executes contract-write/native-transfer nodes.
- The UserOp preset resolves session permissions from the gateway's BadgerDB,
  builds/signs the operation, and sends it through the per-chain bundler.
- The gateway waits for confirmation and records unresolved/reverted outcomes.
  Supporting chain receipt reads may still go through a worker.
- Tenderly HTTP requests and accumulated simulation state overrides are built
  in the gateway, including the allowance produced by an earlier approve.

These are live execution paths, not dead nil-reader fallbacks. In particular,
`vm.go` validates the selected smart-wallet configuration's RPC URL for writes,
and `pkg/erc4337/preset/` uses its RPC and bundler configuration. Retain per-chain
RPC URLs, bundler/provider settings, controller configuration and sponsorship
policy/webhook settings on the gateway. Also retain the top-level AVS-chain
RPC used for EigenLayer/registry operations.

There is no approved "PR 4 env-var strip" after the read migration. Removing
unused read clients is a separate cleanup from removing settings required by
writes. Moving writes to workers would require an explicit replacement for
gateway-local session-policy resolution and is outside this change.

## Startup messages and verification

Workers do not send sponsored or unsponsored UserOps. Their legacy sponsorship
fields remain parseable, but worker startup must not warn that a missing policy
will cause unsponsored sends. Configure sponsorship on the gateway that sends.

For write/simulation verification, inspect `gateway.log`: execution context,
Tenderly/override activity, UserOp and transaction hashes, confirmation results.
Worker logs help diagnose read/connection failures. They do not log every
successful read, and missing write activity there is expected.

This correction changes no execution or sponsorship behavior. The runtime fix
only prevents the legacy default reader from overwriting gateway chain routing.
