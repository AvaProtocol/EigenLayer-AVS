# Retire self-hosted Voltaire bundlers (in-repo path) — #725

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

## Code path that stays

`bundler_provider: self_hosted` remains a valid option for a **locally-run**
Voltaire. Only the shared public hosts and the CI silent-fallback are retired.
