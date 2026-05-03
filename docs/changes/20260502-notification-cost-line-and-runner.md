# Notification Cost Line and Runner — Multi-Token Rendering

- **Date**: 2026-05-02
- **Status**: Implemented (PR #529, final commit `937d1fd`)
- **Branch**: `staging`
- **Related**:
  - Context-memory PRD: [`context-memory/docs/changes/20260501-summary-runner-and-fees-sections.md`](../../../context-memory/docs/changes/20260501-summary-runner-and-fees-sections.md) — rendering format spec (kept after rollback to aggregator-local)
  - Aggregator fee model: [`docs/FEE_ESTIMATION.md`](../FEE_ESTIMATION.md)
  - Proto: [`protobuf/avs.proto`](../../protobuf/avs.proto) — `Execution` (711-713), step gas (737-739), `Fee` / `NodeCOGS` / `ValueFee` (1402-1463)
  - Commits on PR #529: `cba1ac8` (rollback to aggregator-local), `53f01ed` (multi-token line + price service), `9bac118` (per-token value-fee + stablecoin shortcut), `937d1fd` (TokenEnrichmentService for arbitrary ERC20s)

## Problem

Notifications (Telegram, email) lacked two things a user reads next after "did it work":

1. **Who actually ran this.** The smart wallet and owner EOA were known to the aggregator (`vm.task.SmartWalletAddress`, `vm.task.Owner`) but never surfaced — Telegram/email formatters either omitted them or stitched them in via duplicate VM-state lookups.
2. **What it cost.** Fees are a first-class concept in the aggregator (execution fee in USD, per-node gas in WEI, value-fee % per workflow), but notifications showed nothing — at most a per-step gas line buried in email.

A separate gap on simulation runs: the aggregator was sending `gas_price: 0` to Tenderly, so simulated `total_gas_cost` always came back zero, and `cogs[]` for sim summaries was empty. Even if we'd surfaced cost, the numbers would've been useless.

## Decision

### Final architecture (after one round of rollback)

**Aggregator owns Runner and Fees end-to-end. Context-memory's `/api/summarize` is unchanged.**

An earlier iteration of this work routed Runner and Fees through context-memory (request payload + response echo) — but those fields were pure passthroughs adding nothing but round-trip latency and API-shape complexity. The aggregator already has every input it needs locally; the API call exists for natural-language fields (subject, executions[], errors), not for data the aggregator can compute itself.

So the data path is:

```
VM state ──► buildRunnerFromVM ──► Summary.Runner
         ──► buildFeesFromVM   ──► Summary.Fees ──► formatters render
```

`ComposeSummary` (deterministic) and `ContextMemorySummarizer.Summarize` (when context-memory is available) both call the same helpers; `Summary.Workflow.IsSimulation` is sourced from `vm.IsSimulation` rather than the API response so the formatter's simulation branch never silently misclassifies.

### Cost line format

Single-line, multi-token, native unit first with USD parenthetical per token:

| Case | Render |
|---|---|
| ETH-only deployed | `⛽ Cost: 0.000003 ETH ($0.01)` |
| ETH gas + USDC value-fee | `⛽ Cost: 0.000003 ETH ($0.01), 1.2 USDC ($1.20)` |
| Unpriceable ERC20 | `⛽ Cost: 0.000003 ETH ($0.01), 0.005 PEPE ($?)` |
| Read-only deployed (no on-chain steps) | (line omitted) |
| Simulation | `⛽ (cost shown at the Deploy step)` — static, no numbers |

Telegram drops bullets, gas-units, and value-fee detail; email matches. Breakdowns live on the dashboard. Simulation collapses to the placeholder because sim gas prices are conservative chain defaults rather than real network conditions — any specific number would mislead.

### Price service: aggregator-only, no fabricated fallback

`PriceService` interface gains `GetERC20PriceUSD(chainID, contract) (*big.Float, error)`. Three-tier hierarchy at the call site (`tokenBucket.toTokenTotal`):

1. **`Stablecoins` map** ([`blockchain_constants.go`](../../core/taskengine/blockchain_constants.go)) — 12 fully-reserved or strongly-collateralized stablecoins (USDC, USDT, DAI, USDS, PYUSD, sDAI, FDUSD, TUSD, GUSD, LUSD, RLUSD, USDG) on the four supported chains. Treated as exactly $1.00; no network call. Algorithmic/synthetic ones (USDe, USDD, FRAX) deliberately excluded — a depeg event must surface, not be papered over.
2. **`MoralisService.GetERC20PriceUSD`** — for everything else.
3. **`$?` placeholder** — when neither path yields a price.

`FallbackPriceService` ($2,500/ETH hardcoded) was deleted. When the aggregator's `MoralisApiKey` is empty, the price service stays nil; renderers print `$?` and the aggregator logs "configure Moralis" at startup rather than emitting silently-wrong USD numbers. (Moralis is an aggregator concern; operator nodes don't deal with prices or fee rendering — see [`memory/project_aggregator_vs_operator.md`](../../../../.claude/projects/-Users-mikasa-Code-EigenLayer-AVS/memory/project_aggregator_vs_operator.md).)

### Transfer extraction

`extractOutgoingTransfers` walks `vm.ExecutionLogs` for `eth_transfer`, single `contract_write`, and `loop` step outputs (each loop iteration's child output is examined), filters for `from == smartWallet`, and returns `outgoingTransfer` records. `resolveTokenInfo` resolves `(symbol, decimals)` via Stablecoins → `TokenEnrichmentService.GetTokenMetadata` (same source `/api/summarize` uses) → `("?", 18)` fallback. Per-token aggregation in `buildTotalsFromVM` applies the workflow's value-fee tier percentage to each token's outgoing total; native-ETH transfers fold into the native bucket; ERC20s become separate entries; entries that round to zero at the token's display precision are omitted.

### Simulation-cogs fix (related)

To make simulation cogs[] non-empty in the first place, the Tenderly client now sends `gas_price: GetDefaultGasPrice(chainID)` (per-chain conservative defaults — 5 gwei mainnet, 0.5 gwei Sepolia, 0.05 gwei Base, 0.01 gwei Base Sepolia) rather than `0`. The response parser refuses to silently fall back if the echoed `gas_price` is zero — it returns an error so the bug surfaces upstream rather than producing zeroed cogs. ETH-transfer simulation paths similarly populate `Step.GasUsed/GasPrice/TotalGasCost` from `StandardGasCost × GetDefaultGasPrice(chainID)` so cogs[] aggregates correctly across loop iterations.

## Alternatives considered

- **Compute USD totals on the API server (context-memory).** Rejected: the aggregator already has the price service for `EstimateFees()`. Sending pre-formatted strings or rounded USD numbers from the API locks every channel into the same wording and creates two systems that must agree on which steps are on-chain. The aggregator stays the source of truth.
- **Hardcode `$2,500/ETH` fallback for ergonomics.** Rejected: silently fabricated USD numbers when the price service is unavailable mask a real configuration issue. `$?` is honest and points operators at "configure Moralis."
- **Show per-step gas bullets and value-fee subtitle in notifications.** Rejected on simplicity grounds — notifications stay scannable; the dashboard owns the breakdown.
- **Include algorithmic stablecoins (USDe, USDD, FRAX) in the $1.00 map.** Rejected: incorrect ≈$1.00 assumption miscomputes fees on a depeg, which is exactly when accurate accounting matters. They go through the price service like any other ERC20.
- **Compute per-iteration cogs entries for loop steps.** Rejected for v1: the existing loop helper rolls child gas into a parent step, and shipping the per-iteration breakdown affects billing reconciliation — bigger decision than a render improvement.

## Verification

- `go test ./core/taskengine/ -count=1` green (full suite covers formatters, fee builders, simulation cogs path, multi-token render, stablecoin lookup, percent-of-raw math).
- New focused tests:
  - `TestLookupStablecoin` — chain-keyed map coverage.
  - `TestPercentOfRaw` — value-fee percentage math against raw amounts.
  - `TestTokenBucketToTokenTotal` — stablecoin shortcut, zero-rounding omission, missing price service path.
  - `TestFormatTelegramFromStructured_RunnerAndFees` and `TestFormatTelegramFromStructured_MultiToken_USDPlaceholder` — end-to-end render assertions.
- Production verification: the next deployed run on Sepolia after `cba1ac8` showed the Telegram block ordering with Runner-and-Network folded onto one line; the simulation summary correctly rendered `⛽ (cost shown at the Deploy step)` after `4f784c9` (`vm.IsSimulation` flowing through to the formatter).
- The aggregator's existing tests pass with the price service possibly nil: executor (`executor.go:321,478`), fee estimator (`fee_estimator.go:195`), and the new `buildTotalsFromVM` all guard against `nil` rather than panicking.

## Cross-repo coordination

The context-memory PRD ([`context-memory/docs/changes/20260501-summary-runner-and-fees-sections.md`](../../../context-memory/docs/changes/20260501-summary-runner-and-fees-sections.md)) was rewritten to be a rendering-format spec only — the API stays unchanged, so no context-memory code change is required for this work. The PRD's Decision section retains the Cost-line format table because that's the contract between the aggregator and any future renderer (web dashboard, mobile, etc.).
