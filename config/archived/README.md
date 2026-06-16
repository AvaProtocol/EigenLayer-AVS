# `config/archived/` — pre-Railway deployment templates

These templates document the **pre-Railway deployment model**, retained
for historical reference. The current deployment model is the unified
**gateway + workers** shape described in [`../README.md`](../README.md).

## What's archived

| File | Documented |
|---|---|
| `aggregator.example.yaml` | The "one aggregator per chain" template — Sepolia, Ethereum, Base, and Base Sepolia each had their own aggregator process and config file. |
| `operator.example.yaml` | The single-binary operator template — one operator process registered with the AVS and subscribed to its chain. |

## Why these are here

The Railway migration (PRs #538, #544, #550) collapsed the per-chain
aggregator model into a single multi-chain **gateway** that fans out
to per-chain **workers**. The new templates live one level up:

- Gateway → [`../gateway-dev.example.yaml`](../gateway-dev.example.yaml) +
  [`../gateway-railway.yaml`](../gateway-railway.yaml)
- Workers → [`../worker-<chain>-dev.example.yaml`](..) +
  [`../worker-<chain>-railway.yaml`](..)
- Operator → [`../operator-sepolia-railway.yaml`](../operator-sepolia-railway.yaml) (testnets),
  [`../operator-ethereum-railway.yaml`](../operator-ethereum-railway.yaml) (mainnets)

If you're reading the codebase trying to understand how the pre-Railway
shape looked — what fields the old aggregator config had, how a
per-chain operator was wired — these templates are the most concise
answer. They're not active configs; nothing in the running system
references them.

## What's NOT here

The per-chain operator configs (`operator-sepolia.yaml`,
`operator-ethereum.yaml`, `operator-base.yaml`,
`operator-base-sepolia.yaml`) **stay in the parent `config/`
directory** because operators are inherently per-chain by EigenLayer's
design — one operator process per AVS registration per chain — and
the per-chain Makefile targets that run them are still active.

The per-chain aggregator configs (`aggregator-sepolia.yaml` etc.)
are no longer referenced by any in-repo code path after the Phase 2-4
cleanup. They typically exist locally as gitignored symlinks to a
secrets-sync directory; the symlinks can be removed from local dev
environments at any time, or relocated into this `archived/`
directory (where the new `.gitignore` rule keeps them out of git).
See `../README.md`'s "Known exception" section for the one Base
mainnet test that still loads `aggregator-base.yaml`.

## Restoring an archived template

Nothing prevents copying one of these back into use if a partner
deployment still runs the old per-chain shape:

```bash
cp config/archived/aggregator.example.yaml config/aggregator.yaml
$EDITOR config/aggregator.yaml
./out/ap aggregator --config=config/aggregator.yaml
```

The binary still accepts that config shape — the pre-Railway code
path hasn't been deleted, just supplanted as the recommended
deployment model.
