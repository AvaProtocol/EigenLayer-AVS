# merge_hetzner_into_gateway

One-shot tool to merge a Hetzner per-chain aggregator BadgerDB (the **donor**) into the unified Railway gateway BadgerDB. Used once during the 2026-07-01 Hetzner decom to bring forward the ~3 weeks of production data that Studio wrote to the Hetzner aggregators after the May 2026 Railway migration but before this cutover.

Full context: [`avs-infra/docs/changes/20260604-hetzner-data-restore-plan.md`](https://github.com/AvaProtocol/avs-infra/blob/main/docs/changes/20260604-hetzner-data-restore-plan.md).

## What it does

For each prefix it knows about, the tool walks every donor key and applies a per-prefix policy:

| Prefix family | Policy |
|---|---|
| `t:` | Pass-through (chain-scoped; validate embedded chain ID matches donor) |
| `u:` | Pass-through (chain-scoped) |
| `history:` | Pass-through (chain-scoped) |
| `w:` | Stamp donor chain ID into the key (legacy → chain-scoped) |
| `wsalt:` | Stamp donor chain ID; **post-merge, run** `BackfillWalletSaltIndex` against the gateway DB to rebuild any salt-index drift |
| `fl:` | Stamp donor chain ID |
| `fr:` | Stamp donor chain ID |
| `secret:` | Import as-is (already user/org/workflow scoped, chain doesn't apply) |
| `execution_index_counter:` | Import as-is; on collision keep the larger value |
| `ct:cw:` | Drop (cosmetic) |
| `pending:` | Drop (transient) |
| `trigger:` | Drop (reconstructible from `history:`) |
| `migration:` | Drop (donor's migration history, not gateway's) |

**Every write is skip-if-exists.** The gateway has been accumulating its own data since deploy — the tool never overwrites anything already there. This is the load-bearing safety property; the test `TestSetIfAbsent_SkipsWhenKeyPresent` pins it.

Any prefix the tool does not recognize causes a hard-fail by default. To inspect a problem donor without aborting, pass `--fail-on-unknown-prefix=false` — but be aware that those keys are silently dropped from the merge.

## Usage

```bash
# 1. Stop the donor aggregator and capture its BadgerDB to a workstation:
ssh ap-prod1 'sudo systemctl stop avs-aggregator-ethereum'
ssh ap-prod1 'sudo tar czf - /data/mainnet-avs-aggregator/db' > ethereum-donor-2026-06-04.tar.gz
tar xzf ethereum-donor-2026-06-04.tar.gz -C ./donors/

# 2. Stop the Railway gateway service (Railway dashboard → gateway → "Stop").
#    Pull the gateway volume to a workstation if running locally, or run the
#    tool from a Railway one-off task mounting the gateway volume.

# 3. Dry-run first against a scratch copy. Inspect counts + collisions:
go run ./scripts/migration/merge_hetzner_into_gateway \
    --donor-path     ./donors/db \
    --donor-chain-id 1 \
    --gateway-path   /tmp/gateway-db-scratch-copy \
    --dry-run        \
    --verbose

# 4. When the dry-run looks right, apply for real:
go run ./scripts/migration/merge_hetzner_into_gateway \
    --donor-path     ./donors/db \
    --donor-chain-id 1 \
    --gateway-path   /data \
    --dry-run=false

# 5. Repeat steps 1, 3, 4 for each chain:
#    --donor-chain-id 8453      Base mainnet
#    --donor-chain-id 11155111  Sepolia testnet
#    --donor-chain-id 84532     Base-Sepolia testnet

# 6. After all four merges, run BackfillWalletSaltIndex against the gateway
#    to rebuild the wsalt: secondary index (the merge stamps the keys but
#    doesn't re-derive the canonical pointers):
go run ./scripts/migration/backfill_wallet_salt_index --config config/gateway-railway.yaml

# 7. Start the Railway gateway service. Smoke-test workflows + execution
#    queries across all four chains.
```

## Flags

| Flag | Required | Default | Notes |
|---|---|---|---|
| `--donor-path` | yes | — | Path to the donor BadgerDB directory |
| `--donor-chain-id` | yes | — | 1 / 8453 / 11155111 / 84532 |
| `--gateway-path` | yes | — | Path to the Railway gateway BadgerDB |
| `--dry-run` | no | `true` | Defaults to TRUE. Pass `--dry-run=false` to actually write. |
| `--verbose` | no | false | One line per key processed |
| `--fail-on-unknown-prefix` | no | true | Hard-fail when the donor has unrecognized prefixes |

## Output

The tool prints a per-prefix summary table at the end:

```
Summary
-------
Donor chain: 1 (ethereum)

Prefix                          Policy                                              Scanned   Copied  Stamped  Existed  Dropped   Errors
----------------------------- -------------------------------------------------- -------- -------- -------- -------- -------- --------
t:                              pass-through (chain-scoped)                            8421     8410        0       11        0        0
u:                              pass-through (chain-scoped)                            8421     8410        0       11        0        0
history:                        pass-through (chain-scoped)                           12734    12734        0        0        0        0
w:                              stamp chain ID                                          184      184      184        0        0        0
wsalt:                          stamp chain ID (rebuild wsalt index post-merge)         184      184      184        0        0        0
fl:                             stamp chain ID                                          141      141      141        0        0        0
fr:                             stamp chain ID                                         2104     2104     2104        0        0        0
secret:                         import as-is                                            317      317        0        0        0        0
execution_index_counter:        max-on-collision                                       8421     8410        0       11        0        0
ct:cw:                          drop (cosmetic)                                          14        0        0        0       14        0
pending:                        drop (transient)                                          3        0        0        0        3        0
trigger:                        drop (reconstructible from history)                   12734        0        0        0    12734        0
migration:                      drop (donor history not gateway's)                        4        0        0        0        4        0
----------------------------- -------------------------------------------------- -------- -------- -------- -------- -------- --------
TOTAL                                                                                  53682    32494     2613       33    12759        0
```

(Made-up numbers, but the column shape is real.)

## Safety rules

1. **The Railway gateway must be stopped before `--dry-run=false`.** BadgerDB is single-writer; the tool opens the same `db_path` directly. The tool will error on open if another process is holding the lock.
2. **Take a fresh pre-merge backup of the gateway volume immediately before running.** That snapshot is the only rollback target.
3. **Always run `--dry-run` first against a scratch *copy* of the gateway DB** (rsync it to a sidecar dir on the Railway volume). Inspect the per-prefix counts and collisions before touching the live volume.
4. **Never replace `setIfAbsent` with `storage.Storage.Load`.** Load is bulk upsert and will silently clobber live gateway data. The test `TestSetIfAbsent_SkipsWhenKeyPresent` is the regression guard.
5. **Run the tool sequentially per donor**, not in parallel. The tool acquires a Badger write lock on the gateway DB.
6. **Keep the donor aggregators stopped, not deleted, until 2026-06-30** — hot rollback window. Delete on 2026-07-01 after the decom closes.

## What's NOT in this tool

- **wsalt: index rebuild.** The merge stamps the chain ID into wsalt keys but doesn't re-derive any canonical wallet pointers. After the merge, run [`scripts/migration/backfill_wallet_salt_index`](../backfill_wallet_salt_index/) against the gateway DB to refresh those.
- **Gateway read-path updates for chain-stamped families.** The merge produces `w:<chainID>:<owner>:<addr>` keys. The gateway code that reads `w:` keys needs a corresponding update to look at chain-scoped form too — that ships as a separate PR alongside any read paths for `wsalt:`, `fl:`, `fr:`.
- **Recovery from a botched merge.** If smoke tests fail after the merge runs, the recovery is "restore the pre-merge gateway backup, investigate, dry-run again." There's no in-place rollback inside the tool.

## Related references

- Policy matrix source of truth: [`avs-infra/docs/changes/20260604-hetzner-data-restore-plan.md`](https://github.com/AvaProtocol/avs-infra/blob/main/docs/changes/20260604-hetzner-data-restore-plan.md#per-prefix-decision-matrix-proposed-awaiting-sign-off)
- Existing migration tool the structure follows: [`scripts/migration/backfill_wallet_salt_index/`](../backfill_wallet_salt_index/)
- Chain-scoped key parsers used to validate donor pass-through keys: [`core/taskengine/chain_scoped_keys.go`](../../../core/taskengine/chain_scoped_keys.go)
- Storage API contract: [`storage/db.go`](../../../storage/db.go) — note that `Load` is upsert and must not be used here.
