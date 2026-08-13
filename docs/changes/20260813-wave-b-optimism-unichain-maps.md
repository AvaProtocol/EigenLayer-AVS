# Wave B: Optimism + Unichain chain maps

- **Date**: 2026-08-13
- **Status**: Proposed
- **Branch**: feat/wave-b-optimism-unichain
- **Related**: avs-infra `docs/changes/20260813-chain-expansion-wave-b.md`, `@avaprotocol/protocols` Unichain catalog PR

## Problem

Wave B adds OP Mainnet (10) and Unichain (130) as product chains. Alchemy subdomains were documented but not mapped (`10` / `130` missing from `alchemyNetworkSubdomain`), so `ActiveBundlerURL` would hard-error at send time. Several Go maps still treated Eth/Base/BNB/Arb as the only named/priced networks.

## Decision

- Map `10 → opt-mainnet`, `130 → unichain-mainnet` in `alchemyNetworkSubdomain`.
- Moralis: `10 → optimism`, native pricing via OP-stack WETH `0x4200…0006`. **Do not** map Unichain — Moralis Data API does not list 130. Unichain USD conversion uses live ETH (`getETHPrice()`), which is correct because gas is ETH.
- `chainBlockTime`: OP `2s` (same family as Base); Unichain `200ms` (flashblock under-estimate vs 1s sealed blocks).
- `GetNetworkName` + balance-node aliases for Optimism. No Unichain Moralis aliases (would 400).
- Whitelist sidecars skip for both (no `optimism.json` / `unichain.json` until `make sync-tokens` can produce them).
- Local-dev worker example YAML for both.

Production Railway YAML stays in avs-infra.

## Verification

- `go test ./core/config ./core/services ./core/taskengine` — subdomain, Moralis slug, block-time, whitelist, `normalizeChainID`.
