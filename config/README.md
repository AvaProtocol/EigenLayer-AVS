# `config/` — service configuration

The aggregator binary (`ap`) runs as one of three roles depending on its
subcommand: **gateway**, **worker**, or **operator**. Each role needs a
YAML config passed via `--config=<path>`. This directory holds those
configs grouped by deployment target.

## Layout

```
config/
├── README.md                            — this file
├── archived/                            — pre-Railway deployment shape (kept for reference)
│   ├── README.md
│   ├── aggregator.example.yaml          — old "one aggregator per chain" template
│   └── operator.example.yaml            — old per-chain operator template
│
├── gateway-railway.yaml                 — prod gateway (Railway-deployed)
├── gateway-dev.example.yaml             — local-dev gateway template
├── gateway-dev.yaml                     — local-dev gateway, real (gitignored)
├── gateway-dev-rehearsal.yaml           — rehearsal-gateway config used by ops drills
│
├── worker-<chain>-railway.yaml          — per-chain prod workers (Railway-deployed)
├── worker-<chain>-dev.example.yaml      — per-chain local-dev worker template
├── worker-<chain>-dev.yaml              — per-chain local-dev worker, real (gitignored)
│
├── operator-railway.yaml                — Railway operator template
├── operator-<chain>-railway.yaml        — per-chain operator overrides
└── operator-<chain>.yaml                — local operator configs (gitignored symlinks)
```

## When to use which

| Scenario | Config file |
|---|---|
| Production aggregator on Railway | `gateway-railway.yaml` |
| Production worker for chain N on Railway | `worker-<chain>-railway.yaml` |
| Production operator on Railway | `operator-railway.yaml` (+ optional per-chain override) |
| Local dev gateway | `gateway-dev.yaml` (copy from `gateway-dev.example.yaml`, fill in secrets) |
| Local dev worker for chain N | `worker-<chain>-dev.yaml` (same copy pattern) |
| Operating drill / rehearsal | `gateway-dev-rehearsal.yaml` |

`scripts/start.sh` in the studio repo wires up the local-dev gateway +
all workers + operator pane via these config files. See that script
for the exact `--config=` invocations.

## `.example` template convention

Templates checked into git carry the `.example.yaml` suffix. The real
file (same name, no `.example.`) is gitignored and carries actual
secrets — controller keys, JWT signing keys, paymaster ownership keys.
Copy template → real:

```bash
cp config/gateway-dev.example.yaml config/gateway-dev.yaml
$EDITOR config/gateway-dev.yaml      # fill in <placeholder> values
```

The `.gitignore` excludes any `config/*-dev.yaml` and a few specific
names (`gateway-dev.yaml`, `aggregator.yaml`, `operator.yaml`) — see
the top-level `.gitignore` for the full list.

## `archived/` — pre-Railway deployment shape

The pre-Railway deployment model ran one aggregator per chain (the
`aggregator.example.yaml` template) and a single operator binary
(`operator.example.yaml`). Both templates documented that shape.

The Railway migration (PRs AvaProtocol/EigenLayer-AVS#538, #544, #550)
collapsed the per-chain aggregators into a single multi-chain
**gateway** + per-chain **workers**. The per-chain aggregator configs
are still referenced by some Makefile targets and integration tests
(see the migration roadmap below), but the example templates that
described the old shape now live in `archived/` to keep the active
directory focused on the current deployment model.

See [`archived/README.md`](./archived/README.md) for what's in there
and why.

## Migration roadmap (Option C — multi-phase config cleanup)

Phase 1 (this commit): documentation + archived templates + deleted
the stale `_legacy/` snapshot. No code paths changed.

Phase 2 (separate PR): migrate `core/testutil/utils.go` and the
integration tests under `core/taskengine/*_test.go` off the per-chain
`aggregator-<chain>.yaml` pattern onto the gateway config pattern.
About 5 test files affected.

Phase 3 (separate PR): replace the Makefile
`aggregator-{sepolia,ethereum,base,base-sepolia}` targets with a
single `make gateway-dev` target. Update `scripts/aa-wallet-toolkit.sh`
and `start.sh` references at the same time.

Phase 4 (separate PR): with no live callers left, the per-chain
`aggregator-<chain>.yaml` and `operator-<chain>.yaml` configs become
truly unused. Move them to `archived/` (or delete entirely if no
historical value).

Each phase is independent — Phase 1 stands alone if 2-4 never ship.
