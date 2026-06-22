# scripts/dev — migration rehearsal tooling

End-to-end test of the Hetzner → Railway-gateway data merge on a local
dev machine, without touching production Railway. Lets you snapshot the
live Hetzner aggregator BadgerDBs, run the merge tool against scratch
gateway storage, and verify the gateway can serve read queries against
the merged data via the SDK.

## Env vars required by `make dev-stack-rehearsal`

`config/gateway-dev-rehearsal.yaml` (copy it from the tracked `config/gateway-dev-rehearsal.example.yaml`) references everything secret via
`${REHEARSAL_*}` env vars so nothing real lives in git. Export these
in your shell (or a local untracked `.env` you `source` before the
make target) before starting the rehearsal gateway:

```bash
# Sepolia is the AVS registration chain — used by EigenLayer code paths
# at startup. The Tenderly key is read-only so it's fine to reuse.
export REHEARSAL_AVS_RPC=https://sepolia.gateway.tenderly.co/<key>
export REHEARSAL_AVS_WS=wss://sepolia.gateway.tenderly.co/<key>
export REHEARSAL_SEPOLIA_RPC=https://sepolia.gateway.tenderly.co/<key>
export REHEARSAL_SEPOLIA_WS=wss://sepolia.gateway.tenderly.co/<key>
export REHEARSAL_SEPOLIA_BUNDLER_URL=https://api.pimlico.io/v2/11155111/rpc?apikey=<pim_key>

export REHEARSAL_BASE_SEPOLIA_RPC=https://api-base-sepolia-archive.n.dwellir.com/<key>
export REHEARSAL_BASE_SEPOLIA_WS=wss://api-base-sepolia-archive.n.dwellir.com/<key>
export REHEARSAL_BASE_SEPOLIA_BUNDLER_URL=https://api.pimlico.io/v2/84532/rpc?apikey=<pim_key>

export REHEARSAL_ETHEREUM_RPC=https://api-ethereum-mainnet.n.dwellir.com/<key>
export REHEARSAL_ETHEREUM_WS=wss://api-ethereum-mainnet.n.dwellir.com/<key>
export REHEARSAL_ETHEREUM_BUNDLER_URL=https://api.pimlico.io/v2/1/rpc?apikey=<pim_key>

export REHEARSAL_BASE_RPC=https://api-base-mainnet-archive.n.dwellir.com/<key>
export REHEARSAL_BASE_WS=wss://api-base-mainnet-archive.n.dwellir.com/<key>
export REHEARSAL_BASE_BUNDLER_URL=https://api.pimlico.io/v2/8453/rpc?apikey=<pim_key>

# Cryptographic material — placeholders are fine since the rehearsal
# never signs or submits UserOps. Any 32-byte hex works.
export REHEARSAL_ECDSA_PRIVATE_KEY=<32-byte-hex>
export REHEARSAL_CONTROLLER_PRIVATE_KEY=<32-byte-hex>
export REHEARSAL_JWT_SECRET=<any-string>

# Optional — only used by Tenderly-simulated nodes and notification
# flows. Leave unset and those features degrade gracefully.
export REHEARSAL_TENDERLY_ACCESS_KEY=<tenderly-key>
export REHEARSAL_TELEGRAM_BOT_TOKEN=<bot-token>
export REHEARSAL_THEGRAPH_API_KEY=<thegraph-key>
export REHEARSAL_MORALIS_API_KEY=<moralis-jwt>
```

`make hetzner-snapshot` and `make migration-rehearse` (the steps that
actually validate the merge) do NOT need any of these — they read
donor BadgerDBs and write the gateway BadgerDB directly, no RPC or
auth involved. The env vars are only required when you bring up the
gateway to query the merged data via the SDK.

## Quick start

```bash
# 1) Pull fresh donor DBs from all 4 Hetzner aggregators.
#    Brief docker-stop per chain (~30-90s each).
make hetzner-snapshot

# 2) Dry-run the merge for all 4 chains — no writes.
#    Inspect the per-prefix counts printed at the end of each chain.
make migration-rehearse

# 3) When the dry-run output looks right, actually merge.
make migration-rehearse APPLY=1

# 4) Start the gateway against the post-merge scratch DB.
#    Runs the gateway on REST :8080 / gRPC :2206 plus 2 testnet workers.
#    Mainnet workers are listed in the config but not started here;
#    triggering a task on ethereum/base returns WORKER_UNAVAILABLE.
make dev-stack-rehearsal

# 5) In another terminal, query the SDK to confirm reads work.
#    Compare counts to the live aggregators' output before they were
#    snapshotted.
cd ../ava-sdk-js/examples
yarn start listWorkflows
yarn start getWorkflow <task-id-from-the-list>
```

## What the rehearsal validates

- The merge tool dispatch matrix matches the actual on-disk shape of
  each donor (no unknown prefixes, no parse errors)
- Chain-scoped keys (`w:<chain>:`, `wsalt:<chain>:`, `fl:<chain>:`,
  `fr:<chain>:`, `t:<chain>:`, `u:<chain>:`, `history:<chain>:`) are
  written correctly and the gateway can READ each family for each
  chain
- Aggregate counts (workflows, executions, wallets, secrets) match
  the donor totals — nothing was silently dropped or duplicated
- `execution_index_counter:` max-on-collision picks the right value
  when both donor and gateway have entries for the same task
- `wsalt:` index is consistent post-merge — `LookupCanonicalWalletAddress`
  on a (chainID, owner, factory, salt) tuple returns the same wallet
  the primary `w:<chain>:<owner>:<wallet>` record points to

## What it does NOT validate

- Real UserOp execution against mainnet (would cost real ETH; out of
  scope, the merge tool does not change the execution path)
- Operator-side trigger evaluation (operators aren't part of the
  rehearsal stack)
- The Railway gateway's actual storage volume — the rehearsal runs
  against `./tmp/rehearsal-gateway-db` only. Promote to production by
  taking a fresh pre-merge backup of the Railway gateway volume, then
  running the same merge sequence in the production maintenance window.

## Iterating without re-downloading

`make hetzner-snapshot` always pulls fresh and overwrites `./donors/`.
The merge rehearsal is non-destructive to the donor data — only the
scratch gateway DB at `./tmp/rehearsal-gateway-db` gets reset on each
`make migration-rehearse APPLY=1` run.

So a tight iteration loop is:

```bash
make hetzner-snapshot       # once per day, when fresh data matters
make migration-rehearse     # dry-run, iterate on dispatch matrix
make migration-rehearse APPLY=1  # apply, then validate via SDK
```

To only snapshot or rehearse one chain:

```bash
make hetzner-snapshot CHAIN=sepolia
make migration-rehearse CHAIN=sepolia
make migration-rehearse CHAIN=sepolia APPLY=1
```

## File map

| File | What |
|---|---|
| `snapshot-hetzner-donors.sh` | SSH + docker-stop + tarball each Hetzner aggregator. Extracts to `./donors/<chain>/db/`. |
| `run-merge-rehearsal.sh` | Runs the merge tool sequentially per donor against `./tmp/rehearsal-gateway-db`. Calls `count-gateway-keys.go` at the end. |
| `count-gateway-keys.go` | One-shot scan of the gateway DB that prints per-prefix per-chain key counts. Helps spot unexpected key distributions after the merge. |
| `../../config/gateway-dev-rehearsal.example.yaml` | Template for the rehearsal gateway config (all 4 chains, scratch DB). Copy to `gateway-dev-rehearsal.yaml` (gitignored) for `make dev-stack-rehearsal`. |

## Disk + downtime budget

| Chain | DB size (2026-06-04) | Hetzner downtime per snapshot |
|---|---|---|
| sepolia | 34 MB | ~5s |
| base-sepolia | 26 MB | ~5s |
| ethereum | 1.0 GB | ~30s |
| base | 1.2 GB | ~30s |
| **total** | **~2.3 GB** | **~70s total wall** |

`make hetzner-snapshot` runs chains sequentially per host but
mainnet/testnet hosts are independent — the two pairs could be
parallelized in a future revision if the downtime budget becomes
tight.

## Cleanup

```bash
rm -rf donors/                          # delete all snapshotted donor data
rm -rf ./tmp/rehearsal-gateway-db        # delete the scratch gateway DB
rm -rf ./tmp/rehearsal-gateway-backup    # and its backup dir
```

## Related

- [`scripts/migration/merge_hetzner_into_gateway/`](../migration/merge_hetzner_into_gateway/) — the merge tool itself
- [`avs-infra/docs/changes/20260604-hetzner-data-restore-plan.md`](https://github.com/AvaProtocol/avs-infra/blob/main/docs/changes/20260604-hetzner-data-restore-plan.md) — the policy matrix the merge tool implements
