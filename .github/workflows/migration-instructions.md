# Backfill Wallet Salt Index — Production Runbook

One-off migration to populate the `wsalt:<owner>:<factory>:<salt>` secondary
index and flip stale wallet rows from upgraded factories. Required after
deploying the fix from PR #520.

## Prerequisites

- The new aggregator image (with `/ava backfill-wallet-salt-index` baked in)
  must already be running on the production server. Watchtower normally rolls
  it out once a release tag lands on `latest`.
- SSH access to the target server (`ap-prod1` for Base/Ethereum,
  `ap-staging1` for Sepolia/Base-Sepolia).
- A short maintenance window per chain — the aggregator must be stopped
  during the migration because BadgerDB is single-writer.

## Why this isn't a one-liner

You can't `docker exec aggregator-base /ava backfill-wallet-salt-index ...`
against the running container — `aggregator-base` already holds the
BadgerDB lock and the second `/ava` process would fail with
`Cannot acquire directory lock`. So the dance is:

1. Stop the aggregator (releases the lock)
2. Run a one-shot container that **inherits the stopped container's volume
   mounts** via `--volumes-from`, so it sees the same config and the same
   BadgerDB
3. Restart the aggregator

`--volumes-from` borrows the mounts but **not** the image — the one-shot
runs whatever image you pass in `docker run`, so as long as
`avaprotocol/ap-avs:latest` resolves to the new image, the backfill
subcommand is available.

## Step-by-step (Base, on `ap-prod1`)

```bash
ssh ap-prod1
```

### 1. Find the config filename and inspect mounts

The container's actual mounts and config path differ per chain. Always
verify before running the migration.

```bash
docker inspect aggregator-base --format '{{json .Mounts}}' | jq
```

Verified output for Base on `ap-prod1` (2026-04-11):

```json
[
  {
    "Type": "bind",
    "Source": "/home/ava/ap-aggregator-setup/base/config",
    "Destination": "/app/config",
    "Mode": "ro"
  },
  {
    "Type": "bind",
    "Source": "/home/ava/ap-aggregator-setup/base/backup",
    "Destination": "/tmp/ap-avs-backup",
    "Mode": "rw"
  },
  {
    "Type": "bind",
    "Source": "/data/base-avs-aggregator",
    "Destination": "/tmp/ap-avs",
    "Mode": "rw"
  }
]
```

The config directory is `/app/config` (read-only), so the YAML lives
inside `/home/ava/ap-aggregator-setup/<chain>/config/` on the host. Find
the actual filename:

```bash
ls /home/ava/ap-aggregator-setup/base/config/
# aggregator.yaml  bundler.env
```

Then double-check the running aggregator's launch command:

```bash
docker inspect aggregator-base --format '{{.Config.Cmd}}'
# [aggregator]
```

The container runs `/ava aggregator` with **no explicit `--config`**, so
it picks up the default `./config/aggregator.yaml` (the WORKDIR is
`/app`, which resolves to `/app/config/aggregator.yaml`). Use that same
path explicitly when invoking the backfill subcommand below — the
backfill command does NOT inherit the same default and requires an
explicit `--config` flag.

### 2. Snapshot the BadgerDB volume (cheap insurance)

Restic backs up `/tmp/ap-avs/db` every 4h, but a fresh snapshot before
mutation is free peace of mind. From inside the existing backup
container:

```bash
docker exec backup restic backup /tmp/ap-avs/db --tag pre-wallet-salt-backfill
```

Or if you'd rather just verify a recent snapshot exists:

```bash
docker exec backup restic snapshots --tag base | tail -5
```

### 3. Stop the aggregator

```bash
docker stop aggregator-base
```

This releases the BadgerDB lock. Watchtower won't try to restart it
unless a new image tag lands on `latest`, so a sub-minute window is
safe to run unsupervised.

### 4. Dry-run the backfill

```bash
docker run --rm --volumes-from aggregator-base avaprotocol/ap-avs:latest \
  backfill-wallet-salt-index --config /app/config/aggregator.yaml --dry-run
```

**Inspect the printed summary carefully.** You're looking at:

- `Total wallet rows scanned` — sanity check this against your expected
  population for the chain.
- `Newly marked stale` — for Base, this should be substantial (the
  bug fix landing). For Sepolia and Ethereum it'll likely be zero
  (factory implementation hasn't been rotated on those chains).
- `Skipped — RPC derive error` — must be 0 or very low. The label is
  slightly misleading: this counter also includes wallets with
  **negative salts** that the factory's `getAddress(uint256)` ABI
  can't accept (`abi: negatively-signed value cannot be packed into
  uint parameter`). Those are legacy bug-induced rows that the
  migration safely skips. Anything else (real RPC failures or
  unexpected ABI errors) deserves investigation before applying.
- `Skipped — missing factory` — legacy rows from before `Factory` was
  persisted on the model. Safe to leave alone; new GetWallet calls will
  populate them.

**Verified Base apply (2026-04-11):**

```
Total wallet rows scanned:        702
  Canonical (live derive matches): 34
  Stale (live derive differs):     665
  Skipped — RPC derive error:        3   (all were salt=-12 negative-salt rows)
```

That ~95% stale ratio is normal for Base and reflects how many times
the Base factory's `accountImplementation` has rotated since the
aggregator started persisting wallet rows.

### 5. Apply for real

If the dry-run looks reasonable:

```bash
docker run --rm --volumes-from aggregator-base avaprotocol/ap-avs:latest \
  backfill-wallet-salt-index --config /app/config/aggregator.yaml
```

The migration is **fail-fast**: if any individual `db.Set` or
`MarkWalletStale` operation fails, the whole run aborts with a non-zero
exit code. The migration is idempotent, so you can fix the underlying
issue and re-run from scratch.

### 6. Restart the aggregator

```bash
docker start aggregator-base
docker logs --tail 30 aggregator-base
```

Confirm it comes up cleanly (look for `Engine started successfully`).

### 7. Sanity check via the SDK from your laptop

For Base, the previously affected EOA is
`0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788`. Run:

```bash
yarn start --avs-target base getWallets 0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788
```

You should see exactly **one** `salt: '0'` entry — the canonical one for
today's factory implementation. For the bug-report EOA above, that's
`0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f`. The previous era-1 and
era-2 derivations are gone from the response entirely (filtered by the
`StaleDerivation` guard in `ListWallets`).

Note: any wallets across **other** salts (`1`, `10`, `1000`, etc.) that
were already `isHidden: true` in the pre-migration response will also
disappear from the response after the migration runs. They were stale
in the same way the salt-0 zombies were — the migration flagged them
all and the new `ListWallets` hard filter drops them. The on-chain
addresses are still in BadgerDB; they're just hidden from the gRPC
response. Recover via direct `socat` REPL access if anything funded
those addresses.

## Other chains

Same procedure, different container/host paths. Always run step 1 first
to verify, but for reference:

| Chain | Server | Container | Verified host config dir | Notes |
|---|---|---|---|---|
| Base | `ap-prod1` | `aggregator-base` | `/home/ava/ap-aggregator-setup/base/config` | ✅ Migrated 2026-04-11. 702 rows, 665 stale, 34 canonical, 3 negative-salt skipped. |
| Ethereum | `ap-prod1` | `aggregator-ethereum` | `/home/ava/ap-aggregator-setup/ethereum/config` (likely) | TBD — verify with `docker inspect`. Factory likely hasn't been rotated, expect ~0 stale. |
| Sepolia | `ap-staging1` | `aggregator-sepolia` | `/home/ava/ap-aggregator-setup/sepolia/config` (likely) | Dry-run on 2026-04-10 showed 220 rows, 0 stale. |
| Base-Sepolia | `ap-staging1` | `aggregator-base-sepolia` | `/home/ava/ap-aggregator-setup/base-sepolia/config` (likely) | TBD. |

For non-Base chains the migration will likely show zero stale rows
(factory implementation hasn't been rotated), but it's still worth
running so the secondary index is populated for any future
implementation rotation.

The config filename inside the container is **always**
`/app/config/aggregator.yaml` regardless of chain — the deployment
template uses one canonical filename per host directory, not chain-named
filenames. (Don't trust the chain in the filename, trust `docker
inspect`.)

## If something goes wrong

- **Migration aborts mid-run with a write error**: storage hit a transient
  issue. The migration is idempotent — fix the underlying cause and
  re-run from step 4. Already-written index entries are correct;
  re-running just re-validates them.
- **Aggregator won't restart after migration**: check `docker logs
  aggregator-base` for the error. The migration only writes new keys
  (`wsalt:` prefix) and flips flags on existing wallet records — it
  doesn't touch tasks, executions, or any other state. If startup fails,
  it's almost certainly unrelated to the migration; restore from the
  pre-migration restic snapshot if you need to roll back.
- **getWallets still shows zombie rows after migration**: check the
  migration's printed summary for `Newly marked stale` count and
  cross-reference against the SDK output. Make sure you bounced the
  aggregator container (step 6) — it caches some wallet state in
  memory and needs a restart to pick up the marked-stale rows.

## Background

See PR #520 for the full bug report. TL;DR: wallet records are keyed by
their derived address (`w:<owner>:<address>`), so when a factory's
account implementation is upgraded the same `(owner, factory, salt)`
triple starts deriving to a new address — and because the lookup is
keyed by address, the old row never gets touched. Multiple records
accumulate for the same logical (owner, factory, salt) slot, all
reported as `salt: "0"` in `ListWallets`. The fix adds a
`wsalt:<owner>:<factory>:<salt>` secondary index and a backfill
migration to flag the zombies.
