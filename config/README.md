# `config/` — service & test configuration

The `ap` binary runs as one of three roles depending on its subcommand:
**gateway**, **worker**, or **operator**. Each role needs a YAML config passed
via `--config=<path>`. This directory holds the **local-dev** templates for
those roles plus the **Go test-suite** fixture.

> **Production configs live in `avs-infra`.** The prod `*-railway.yaml` configs
> are maintained in the `avs-infra` repo and delivered to each service via the
> `AP_CONFIG_YAML` env var (the image entrypoint writes it to
> `config/runtime.yaml` at boot). They are not kept in this repo.

## Layout

```
config/
├── README.md                         — this file
│
├── test.example.yaml                 — Go test-suite fixture template
├── test.yaml                         — real fixture, gitignored
│
├── gateway.example.yaml              — local-dev gateway template
├── gateway.yaml                      — real, gitignored
│
├── worker-<chain>.example.yaml       — per-chain local-dev worker template
├── worker-<chain>.yaml               — real, gitignored
│
├── operator-<chain>.example.yaml     — per-chain local-dev operator template
└── operator-<chain>.yaml             — real, gitignored
```

Naming is uniform across the three roles: `<role>[-<chain>].yaml` for the local
config, `<role>[-<chain>].example.yaml` for the checked-in template. Production
counterparts are `<role>[-<chain>]-railway.yaml` over in `avs-infra`.

## When to use which

| Scenario | Config file |
|---|---|
| Production (any role) | `avs-infra` → `railway/configs/<svc>-railway.yaml`, delivered via `AP_CONFIG_YAML` |
| Running the Go test suite | `test.yaml` (copy from `test.example.yaml`, fill in RPC + Tenderly). Loaded as `testutil.DefaultConfigPath`; **not** a server config. |
| Local dev gateway | `gateway.yaml` (copy from `gateway.example.yaml`) — `make gateway` |
| Local dev worker for chain N | `worker-<chain>.yaml` (copy from the template) |
| Local dev operator for chain N | `operator-<chain>.yaml` (copy from the template) |

## `.example` template convention

Templates checked into git carry the `.example.yaml` suffix. The real file
(same name, no `.example.`) is gitignored because it carries secrets —
controller keys, JWT signing keys, key-store paths. Copy template → real:

```bash
cp config/gateway.example.yaml config/gateway.yaml
$EDITOR config/gateway.yaml      # fill in <placeholder> values
```

`.gitignore` ignores every `config/*.yaml` and keeps `config/*.example.yaml`,
so the real configs stay untracked while the templates are versioned.

## Known config exception

`core/taskengine/userops_withdraw_test.go:30` still loads
`config/aggregator-base.yaml` — but the test is gated by
`TEST_CHAIN=base` and gracefully skips when the config is missing.
Base mainnet (chain ID 8453) isn't in `gateway.yaml`'s default
`chains:` block (which covers Sepolia + Base Sepolia for local dev),
so this developer-only opt-in test keeps its own per-chain fixture
until either:

- Base mainnet is added to `gateway.yaml`'s `chains:` and the
  test is refactored to select that block, or
- The test is removed as obsolete.
