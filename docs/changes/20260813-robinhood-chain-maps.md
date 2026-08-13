# Robinhood Chain (4663) maps

- **Date**: 2026-08-13
- **Status**: Implemented (`v4.16.0`)
- **Branch**: feat/robinhood-chain-maps
- **Related**: avs-infra `docs/changes/20260813-chain-expansion-wave-b.md`, `@avaprotocol/protocols` Robinhood catalog PR

## Problem

Robinhood Chain (4663) is the next named target after Wave B. Alchemy `4663 → robinhood-mainnet` is already mapped. Remaining Go maps still treat it as unknown: `GetNetworkName` returns `"unknown"`, block-trigger floor uses the 1s default (too loose vs 100ms blocks).

## Decision

- Keep `alchemyNetworkSubdomain[4663] = robinhood-mainnet` (already shipped).
- **Do not** add Moralis slug or `chainTokens[4663]`. Data API does not list 4663; USD conversion uses live ETH (`getETHPrice()`), correct because gas is ETH.
- `chainBlockTime[4663] = 100ms` (advertised Arb Orbit cadence; under-estimate).
- `GetNetworkName` → `"robinhood"`. Whitelist sidecar skip (no `robinhood.json`).
- No balance-node Moralis aliases (would 400).
- Local-dev `config/worker-robinhood.example.yaml`.

Production Railway YAML stays in avs-infra.

## Verification

- `go test ./core/config ./core/services ./core/taskengine -run 'TestChainIDToMoralisChain|TestGetChainTokenMapping|TestWhitelistFileForChain|TestBalanceNode_ChainNormalization|TestExpansionChainsHaveAlchemySubdomains'`
