# Alchemy bundler routing via `bundler_provider`

**Date:** 2026-07-20

## Summary

Adds a per-chain `bundler_provider` switch (`alchemy` | `self_hosted`, default
`alchemy`) so UserOps can be relayed through Alchemy's Rundler instead of the
self-hosted Voltaire bundler, with no change to the paymaster (still our
per-chain `VerifyingPaymaster`, per-chain deposits — only the relay moves).

## What changed

- `SmartWalletConfig` gains `BundlerProvider` + `AlchemyAPIKey`;
  `ActiveBundlerURL()` resolves the endpoint (alchemy URL derived from
  `AlchemyAPIKey` + a chainId→subdomain map; `self_hosted` uses `bundler_url`).
  The alchemy path never falls back to `bundler_url` — a missing key or unmapped
  chain is a hard error (fail closed). `BundlerConfigured()` replaces the old
  `bundler_url != ""` connectivity-only guard.
- Two ERC-4337 portability fixes for Rundler: `eth_simulateUserOperation` (a
  Voltaire-only method) is now best-effort; and verification gas is taken from
  the real estimate rather than the 1M default, which tripped Rundler's
  verification-gas-limit-efficiency floor.
- Subdomain map covers Ethereum (1), Base (8453), Sepolia (11155111),
  Base-Sepolia (84532), BNB (56 → `bnb-mainnet`).

## Rollout

Config ships via `avs-infra` (`sync-configs.sh --apply`) separately from the
code release. Every wallet-op chain sets `bundler_provider` explicitly and
`ALCHEMY_API_KEY` is a Railway sealed var. BNB stays `self_hosted` until it has a
real deployed + funded paymaster on BNB mainnet (its config paymaster is a
placeholder today, so making `BundlerConfigured()` true would fail the fatal
startup paymaster probe).

Validated end-to-end on Sepolia: a real UserOp lands through both providers.
