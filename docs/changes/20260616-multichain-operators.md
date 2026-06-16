# Multi-chain operators: one operator per network tier + liveness hardening

## Why

Production ran two operators that each advertised **a single chain** —
`operator-ethereum` → `[1]`, `operator` → `[11155111]` — so Base (8453),
BNB (56) and Base-Sepolia (84532) tasks were **orphaned** (no operator
advertised their chain, so the gateway never routed or fired them).

The operator already had the machinery to monitor an **array** of chains
(`OperatorConfig.Chains`, `EffectiveChains`, per-chain trigger sets, the
liveness watermark from #581), but the Railway operator configs were never
migrated off the legacy single-chain `eth_rpc_url: ${CHAIN_ENDPOINT}` /
`target_chain` form. This finishes that migration and fixes two latent
hardening gaps that the multi-chain setup exposes.

## Intended topology

- **operator-ethereum** (mainnet tier): monitors **all mainnets** —
  Ethereum (1), Base (8453), BNB (56).
- **operator-sepolia** (testnet tier, renamed from `operator`): monitors
  **all testnets** — Sepolia (11155111), Base-Sepolia (84532).

Each operator still *registers* against one AVS chain (`eth_rpc_url`:
Ethereum mainnet / Sepolia respectively) — that's separate from the
destination `chains:` it monitors.

## Changes

### Part A — config (this repo)
- `config/operator-ethereum-railway.yaml`: replaced the single
  `target_chain` with a `chains:` array (ethereum/base/bnb), each pointing at
  its per-chain RPC env var (`ETHEREUM_RPC`, `BASE_RPC`, `BNB_RPC`).
- `config/operator-railway.yaml` → **renamed** to
  `config/operator-sepolia-railway.yaml`; `chains:` array
  (sepolia/base-sepolia → `SEPOLIA_RPC`, `BASE_SEPOLIA_RPC`).
- Updated Dockerfile/README references to the new filenames.
- Railway side (env vars + service rename + start commands) handled
  out-of-band.

### Fix 1 — a bad per-chain WS no longer crashes the whole operator
`NewBlockTrigger`/`NewEventTrigger` dial the chain's WS endpoint in their
constructors and **panic** on failure. With one operator monitoring several
chains, a single unreachable WS would take down *every* chain. The per-chain
setup now constructs the triggers under panic-recovery and **soft-skips** the
bad chain (added to the `Skipped` set, dropped from the advertised
capabilities) — the same treatment a failed HTTP dial already gets. WS URLs
are still derived from each chain's HTTPS RPC (`config.DeriveWsURL`); an
explicit per-chain `eth_ws_url` can override when a provider serves WS on a
different host/path.

### Fix 2 — an idle chain no longer un-advertises itself
The block subscription stops when a chain has no block tasks
("No more block monitoring tasks"). With it stopped, no heads arrive, the
liveness watermark goes stale, and `supportedChainIDs()` dropped the chain —
even though it was healthy and could still serve event tasks. Worse, this
**deadlocks coverage**: the gateway only routes tasks (including the first
block task) to advertised chains, so a dropped idle chain never gets tasks,
never restarts its subscription, and never re-advertises.

`supportedChainIDs()` now treats a stale watermark as a stall **only when the
chain has block work** (`BlockTrigger.HasBlockTasks()`). An idle chain stays
advertised. Adding a block task also resets the watermark so a freshly
(re)started subscription gets its full staleness window before it can be
considered stalled. (The block-work probe is injected onto `ChainTriggerSet`
so the liveness logic is unit-testable without a real WS-dialing trigger.)

## Risk / follow-up

- WS derivation is relied on for Base/BNB; if a provider needs a different WS
  host the new soft-skip surfaces it as a dropped chain (visible in
  `Skipped`/`SetChainAdvertised`) instead of a crash — set an explicit
  `eth_ws_url` for that entry.
- The watermark still only tracks *block* heads; an event-only chain with a
  dead event WS isn't caught by it (pre-existing; out of scope here).
