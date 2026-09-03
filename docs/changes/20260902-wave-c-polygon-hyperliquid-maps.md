# Wave C: Polygon + Hyperliquid EVM chain maps

- **Date**: 2026-09-02
- **Status**: Implemented (maps; Railway apply still in avs-infra)
- **Branch**: feat/wave-c-polygon-hyperliquid-maps
- **Related**: avs-infra `docs/changes/20260902-chain-expansion-wave-c-polygon-hyperliquid.md`

## Problem

Wave C adds Polygon PoS (137) and Hyperliquid EVM (999). Alchemy `999 → hyperliquid-mainnet` was already mapped; `137 → polygon-mainnet` was not, so `ActiveBundlerURL` would hard-error at send time. Go maps still treated 137/999 as unknown for `GetNetworkName`, block-trigger floor, Moralis slugs, and native-token metadata (always ETH).

## Decision

- Map `137 → polygon-mainnet` in `alchemyNetworkSubdomain`. Keep `999 → hyperliquid-mainnet`.
- Moralis: `137 → polygon`, native POL via WPOL `0x0d50…1270`. **Do not** add a Moralis slug for 999. Put HYPE in `chainTokens` so `GetNativeTokenPriceUSD` cannot ETH-fallback; omit it from `nativePricingSupportedChains` so `getFallbackPrice("HYPE")` errors. `PriceService.HasLiveNativeUsdPrice` (and instance-free `services.NativeUsdPriceIsLive`) is the single source of truth. Executor fail-closes when any **billable UserOp chain** (contract-write / ETH-transfer, including loop runners) has no live native USD price — including when `priceService` is nil — rather than converting the gateway default (ETH) and proceeding unbilled. Credit-limit conversion is per ledger chain; unpriceable chains fail closed only if outstanding > 0 so listing 999 in `knownChainIDs` does not block ETH tasks. BNB/POL still fail-open on a Moralis outage.
- `chainBlockTime`: Polygon `2s`; Hyperliquid `1s` (small-block under-estimate vs 60s big blocks).
- `GetNetworkName` → `"polygon"` / `"hyperliquid"`. Whitelist sidecars skip (no `polygon.json` / `hyperliquid.json` until `make sync-tokens`).
- `nativeTokenMetadataForChain` returns BNB / POL / HYPE on those chain IDs; ETH otherwise.
- Local-dev worker example YAML for both.

Production Railway YAML stays in avs-infra.

## Verification

- `go test ./core/config ./core/services ./core/taskengine -run 'TestChainIDToMoralisChain|TestGetChainTokenMappingWaveA|TestGetFallbackPriceRefusesNonETH|TestHasLiveNativeUsdPrice|TestWhitelistFileForChain|TestNativeTokenMetadataForChain|TestGetNetworkName|TestBlockTimeForChain|TestExpansionChainsHaveAlchemySubdomains|TestBalanceNode_ChainNormalization|TestNativeUsdPriceIsGuaranteedMissing|TestBillableExecutionChainIDs|TestExecutor_FailClosed|TestExecutor_CreditCheck_Hyperliquid'`
- `go test ./core/taskengine/trigger -race -run 'TestTimeTrigger_Shutdown_ConcurrentWithRemoveCheck|TestTimeTrigger_RemoveCheck_After'`
