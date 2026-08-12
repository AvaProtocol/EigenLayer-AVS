# Add Arbitrum (and finish BNB) to Moralis + token catalog maps

- **Date**: 2026-08-12
- **Status**: Implemented
- **Branch**: feat/wave-a-arbitrum-chain-support
- **Related**: avs-infra `docs/changes/20260812-chain-expansion-wave-a.md`, `@avaprotocol/protocols@0.10.0`

## Problem

Production is adding Arbitrum One (42161) as a chain worker and graduating BNB (56) past connectivity-only. The gateway already walks `config.Chains` and registers a `TokenEnrichmentService` per chain, but several Go maps still treated Eth/Base as the only priced/named networks:

- Moralis `chainIDToMoralisChain` returned `""` for 56 and 42161, so fee USD lines and ERC-20 prices fell back or failed.
- Balance-node `chainIDMap` knew BNB but not Arbitrum.
- Token whitelist `LoadWhitelist` fell back to `ethereum.json` for any unknown chain — wrong addresses if an Arb worker asked for metadata.
- `GetNetworkName(42161)` returned `"unknown"`.

`alchemyNetworkSubdomain` already mapped `42161 → arb-mainnet`; no bundler change.

## Decision

- Add `ChainIDArbitrumOne = 42161`.
- Map Moralis slugs `56 → bsc`, `42161 → arbitrum`. Enable native pricing for both mainnets.
- Do **not** hand-seed `tokenwhitelist/arbitrum.json`. The drift gate (`make sync-tokens`) requires that directory to match `@avaprotocol/protocols@$PROTOCOLS_VERSION`, and 0.10.0 has no Arb tokens sidecar. Enrichment on 42161 falls back to RPC until a sidecar lands.
- Add Arbitrum aliases to the balance-node chain map.
- Add `config/worker-arbitrum.example.yaml` for local-dev.

Production Railway YAML stays in avs-infra.

## Verification

- `go test ./core/taskengine ./core/services` — catalog + `normalizeChainID` cases for 56/42161.
