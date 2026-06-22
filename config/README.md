# `config/` — service configuration

The aggregator binary (`ap`) runs as one of three roles depending on its
subcommand: **gateway**, **worker**, or **operator**. Each role needs a
YAML config passed via `--config=<path>`.

> **Production Railway configs moved.** The prod `*-railway.yaml` configs now
> live in the `avs-infra` repo (`railway/configs/`) and are delivered to each
> Railway service via the `AP_CONFIG_YAML` env var (the image entrypoint writes
> it to `config/runtime.yaml` at boot). They are no longer in this repo or baked
> into the public image. This directory now holds only **local-dev and sample**
> configs.

## Layout

```
config/
├── README.md                            — this file
├── gateway-dev.example.yaml             — local-dev gateway template
├── gateway-dev.yaml                     — local-dev gateway, real (gitignored)
│
├── worker-<chain>-dev.example.yaml      — per-chain local-dev worker template
├── worker-<chain>-dev.yaml              — per-chain local-dev worker, real (gitignored)
│
└── operator-<chain>.yaml                — local operator configs (gitignored symlinks)
```

Production `*-railway.yaml` configs live in `avs-infra` (`railway/configs/`),
not here — see the note above.

## When to use which

| Scenario | Config file |
|---|---|
| Production (any role) on Railway | `avs-infra` → `railway/configs/<svc>-railway.yaml`, delivered via `AP_CONFIG_YAML` |
| Local dev gateway | `gateway-dev.yaml` (copy from `gateway-dev.example.yaml`, fill in secrets) |
| Local dev worker for chain N | `worker-<chain>-dev.yaml` (same copy pattern) |

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

## Pre-Railway bare-metal templates (moved)

The pre-Railway deployment model (one aggregator per chain + a single
operator binary) and its config templates moved to **avs-infra**
(`terraform/docs/archived-baremetal-templates/`), next to the terraform
that deployed that bare-metal stack — which is now being decommissioned.
Per-chain aggregator configs that may still exist locally as gitignored
symlinks to a secrets-sync directory are no longer referenced by any
in-repo code path, except the one documented exception below.

## Migration history (Option C — multi-phase config cleanup, all phases now done)

**Phase 1** (this PR's first commit): documentation + `archived/`
templates + deleted the stale `_legacy/` snapshot.

**Phase 2** (this PR): migrated `core/testutil/utils.go` and the
integration tests under `core/taskengine/*_test.go` +
`integration_test/` off the per-chain `aggregator-<chain>.yaml`
pattern onto `gateway-dev.yaml`. `testutil.DefaultConfigPath` now
points at `gateway-dev.yaml`; the CI bootstrap script
(`.github/scripts/generate-test-config.sh`) generates
`gateway-dev.yaml` from `gateway-dev.example.yaml`.

**Phase 3** (this PR): replaced the Makefile
`aggregator-{sepolia,ethereum,base,base-sepolia}` targets with a
single `make gateway-dev`. The old targets remain as redirect stubs
that print a deprecation notice and exit non-zero, so old runbooks
fail loudly rather than silently doing the wrong thing. Updated
`scripts/aa-wallet-toolkit.sh`, the backfill-tool docstrings, and
`docs/Development.md` at the same time.

**Phase 4** (this PR): with no live callers left, the per-chain
`aggregator-<chain>.yaml` symlinks (pointing at gitignored configs
in a separate env repo) are no longer referenced by any in-repo code
path. They can be deleted from local dev environments at any time;
nothing in the repo tracks them.

### Known exception

`core/taskengine/userops_withdraw_test.go:30` still loads
`config/aggregator-base.yaml` — but the test is gated by
`TEST_CHAIN=base` and gracefully skips when the config is missing.
Base mainnet (chain ID 8453) isn't in `gateway-dev.yaml`'s default
`chains:` block (which covers Sepolia + Base Sepolia for local dev),
so this developer-only opt-in test keeps its own per-chain fixture
until either:

- Base mainnet is added to `gateway-dev.yaml`'s `chains:` and the
  test is refactored to select that block, or
- The test is removed as obsolete.

Neither is in scope for this cleanup.
