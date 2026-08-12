# Retire self-hosted Voltaire bundlers (in-repo path) — #725

> **Completed 2026-08-12.** The observation gate passed and the fleet is gone. See
> [Teardown (2026-08-12)](#teardown-2026-08-12) at the bottom for what was actually deleted.
> Everything under "Out of scope" below is now done.

## Thesis

Every production chain already runs `bundler_provider: alchemy`. The remaining
callers of `bundler-*.avaprotocol.org` were our own CI (and local-dev configs
still pinned to `self_hosted`). #724 moved the CI generator to Alchemy, but
`testutil.GetTestSmartWalletConfig` still forced `self_hosted` and read
`bundler_url` — so live tests never left Voltaire. This change closes that gap
and removes the CI/local fallbacks that named the shared hosts.

## What changed (this repo)

1. **CI hard-fails without `ALCHEMY_API_KEY`.**
   `generate-test-config.sh` no longer downgrades to `self_hosted`. The setup
   action requires `alchemy-api-key`; `BUNDLER_RPC` / `bundler-rpc` are gone.
2. **Test fixtures actually use Alchemy.**
   `GetTestSmartWalletConfig` / `GetTestBundlerRPC` honor the fixture's
   provider + key via `ActiveBundlerURL()` instead of pinning Voltaire.
3. **Local-dev configs and scripts.**
   Worker sepolia / base-sepolia examples, `Makefile` `dev-stack`, and
   `scripts/start-gateway.sh` require `ALCHEMY_API_KEY` and no longer demand
   `*_BUNDLER_URL`. README + debug scripts point at Alchemy endpoints.
4. **Templates drop dead `bundler_url` lines** on the alchemy path
   (`test.example.yaml`, worker examples).

## Out of scope (still #725 acceptance items)

Checked 2026-08-07 against local clones:

### avs-infra (`railway/configs/*`)

All wallet-op chains already set `bundler_provider: alchemy` + `alchemy_api_key`,
but **dead `bundler_url: ${…_BUNDLER_URL}` lines remain** on every chain in
`gateway-railway.yaml` and on every worker except `worker-bnb-mainnet-railway.yaml`
(which already dropped the line and documents that alchemy never reads it).
Worker headers still list `BUNDLER_URL` as a required sealed var and one
comment still says "Third-party bundler (self-hosted Voltaire)".

Also still documenting live services: `ARCHITECTURE.md` (incl. line 77
"dev + external consumers"), `RAILWAY_OPERATIONS.md`, `Adding_A_New_Chain.md`
Phase 2.5. Terraform `environments/*/…-aggregator.yaml` still hardcode the
public `bundler-*.avaprotocol.org` URLs (legacy Hetzner templates).

**Follow-up PR in avs-infra:** strip dead `bundler_url` / `*_BUNDLER_URL` from
railway YAMLs; then (after observation) delete Railway services + DNS and
rewrite the ops docs.

### ava-sdk-js (`config/gateway.yaml`, `config/worker-sepolia.yaml`)

Already `bundler_provider: alchemy` + `${ALCHEMY_API_KEY}`. Dead lines remain:

- `bundler_url: ${BUNDLER_URL}` in both YAMLs (gateway comment: "kept only so a
  secret-less run can fall back by setting bundler_provider: self_hosted").
- `.github/workflows/dev-test-on-pr.yml` still exports `secrets.BUNDLER_URL`
  into the envsubst render even though alchemy never reads it.

**Follow-up PR in ava-sdk-js:** drop `bundler_url` + `BUNDLER_URL` from the E2E
templates and workflow (mirror this repo's hard-fail on missing
`ALCHEMY_API_KEY`).

### Observation + infra deletion

- Confirm `bundler-sepolia` logs show no fixture-wallet `eth_sendUserOperation`
  for several days of PR traffic after this lands (observation gate from #724).
- Delete Railway services (`bundler-sepolia`, `bundler-base`,
  `bundler-base-sepolia`, `bundler-ethereum`, `bundler-proxy`) and Route53 DNS
  — avs-infra + MFA session.

## Follow-ups folded into this PR (Claude #726)

- Deleted `scripts/clear-staging-mempool.sh` — Voltaire-only `debug_bundler_clearState` helper that grepped `bundler_url` from legacy `aggregator-*.yaml`.
- Taskengine logs now emit `BundlerEndpointLabel()` (resolved endpoint with secrets redacted) + `bundler_provider`, instead of the raw `BundlerURL` field (empty on the alchemy path).

## Code path that stays

`bundler_provider: self_hosted` remains a valid option for a **locally-run**
Voltaire. Only the shared public hosts and the CI silent-fallback are retired.

## Teardown (2026-08-12)

### The observation gate

`bundler-sepolia` served **131** `eth_sendUserOperation` calls over Aug 5–7 (19 / 56 / 56),
the last at **Aug 07 07:43 UTC** — roughly four hours after #724/#726 merged. Nothing after
that, on any of the four backends. The last request of any kind from a Go client was also
Aug 07; for the final 48 hours the only traffic was 576 `eth_chainId` health checks from a
Better Stack uptime monitor.

The issue warned this had already been misread three times, so each alternative was ruled out
before deleting: the log pipeline was still live (all four backends emitting minutes before the
check), retention was not truncating the window (the same filtered query still returned Aug 05
hits), and the silence was under load — ava-sdk-js E2E ran Aug 8 (×3) and Aug 9 against the new
stack, logging `bundler_provider: alchemy` for chain 11155111 and reaching 38/43 tests passing,
so it exercised the send path rather than dying at boot.

Caveat recorded for honesty: Aug 10–12 had no E2E runs (it is `pull_request`-triggered and there
were no PRs), so the load-bearing evidence is Aug 8–9. This repo has no live-test workflow in CI
at all — the Sepolia live suite runs locally via `make test` — so "days of PR traffic" was never
going to be the signal here.

### Deleted

| What | Detail |
| --- | --- |
| Route 53 | 8 records in `avaprotocol.org` — 4 `bundler-*` CNAMEs + 4 `_railway-verify` TXT. Zone went 36 → 28 records. Deleted **first**, so no CNAME was left dangling at a freed `*.up.railway.app` target (subdomain-takeover risk). |
| Railway services | `bundler-proxy` first, then `bundler-sepolia`, `bundler-base-sepolia`, `bundler-ethereum`, `bundler-base`. Project went 13 → 8 services. |
| Railway vars | `BUNDLER_URL` on the four workers; `BASE_BUNDLER_URL`, `BASE_SEPOLIA_BUNDLER_URL`, `ETHEREUM_BUNDLER_URL`, `SEPOLIA_BUNDLER_URL` on the gateway. Dropped with `--skip-deploys` — they were already dead, and a cosmetic cleanup does not justify five production redeploys. Deployment IDs unchanged, all eight services still `SUCCESS`. |
| GitHub secrets | `BUNDLER_URL` (ava-sdk-js, `dev` env — carried the shared apikey) and `BUNDLER_RPC` (EigenLayer-AVS, `Test` env). Both confirmed unreferenced by any workflow first. |

Done with [`railway/retire-bundlers.sh`](https://github.com/AvaProtocol/avs-infra/blob/main/railway/retire-bundlers.sh)
in avs-infra, which resolves service IDs by name from the Railway API and refuses to run if it
resolves anything outside the five targets. Worth knowing: `railway service status --all` prints
**deployment** IDs, not service IDs, and the CLI's `railway delete` deletes an entire *project* —
there is no `service delete` subcommand, so the teardown goes through the GraphQL `serviceDelete`
mutation.

### Not lost

The bundler EOA keys survive: `VOLTAIRE_BUNDLER_SECRET` on the deleted services was a copy of
`environment_configs.*.bundler_private_key` in
`terraform/environments/{staging,production}/terraform.tfvars` (verified by hash before deleting —
one shared key per pair, sepolia + base-sepolia and ethereum + base). Both mainnet EOAs are dust
(0.000024 ETH on Ethereum, 0.0000007 on Base); the two testnet EOAs never tripped the 0.05 ETH
minimum-balance warning, so that Sepolia / Base-Sepolia ETH is still reclaimable.

### Left for a human

The four **Better Stack uptime monitors** on `bundler-*.avaprotocol.org` are manual UI objects —
not in terraform, no API token in avs-infra — so they could not be removed programmatically and
will now be alerting against dead hostnames. Delete them in the Better Stack dashboard.
