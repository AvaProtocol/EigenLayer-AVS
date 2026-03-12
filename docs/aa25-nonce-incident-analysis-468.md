# AA25 Invalid Account Nonce - Incident Analysis

**Sentry Issue:** 7326737264
**Date:** 2026-03-11T09:40:36Z
**Environment:** production
**Server:** aggregator-ethereum-prod
**Release:** v2.6.5@bab496f
**Logs:** [`aggregator-ethereum-aa25-incident.log`](./aggregator-ethereum-aa25-incident.log)

## Error

```
*rpc.jsonError: revert reason : AA25 invalid account nonce
```

## Timeline (from logs)

All timestamps are UTC. Log line references are from `aggregator-ethereum-aa25-incident.log`.

| Time | Event | Log Lines |
|------|-------|-----------|
| ~09:23:00 | Simulation run for ETH transfer (destination `0x239D...`, amount `10000000000000` wei) | L28 |
| ~09:30:01 | Real ETH transfer starts, uses paymaster, nonce assumed `0` or `1` | L66-69 |
| ~09:31:07 | First ETH transfer succeeds (tx `0x7b26...`), took ~66s | L70 |
| ~09:40:01 | Next cron tick fires, starts real ETH transfer for same sender | L110-113 |
| ~09:40:01 | Gas estimation for unwrapped calldata fails with `AA25 invalid account nonce` | L114 |
| ~09:40:02 | Gas estimation retries (3 attempts), all fail with AA25 | L115-117 |
| ~09:40:04 | UserOp send attempt 1 fails: `AA25 invalid account nonce` at **nonce `1`** | L118-119 |
| ~09:40:04 | Nonce cache reset, starts polling on-chain for fresh nonce (15s timeout) | L119 |
| ~09:40:20 | Nonce polling timeout — on-chain nonce still `1` | L121 |
| ~09:40:20 | UserOp send attempt 2 fails: same error at nonce `1` | L122-123 |
| ~09:40:36 | Nonce polling timeout again | L129 |
| ~09:40:36 | UserOp send attempt 3 fails, all retries exhausted | L130 |
| ~09:40:36 | Error logged to Sentry | L131-133 |
| ~09:50:01 | Next cron tick, ETH transfer succeeds (tx `0x27f5...`), gas used: 427339 | L163-169 |

## Root Cause

The error is a **nonce race condition** between consecutive cron-triggered executions for the same smart wallet sender. It is NOT a bundler bug — the bundler correctly rejects UserOps with stale nonces.

### Why the NonceManager didn't prevent this

The `globalNonceManager` (`pkg/erc4337/bundler/nonce_manager.go`) is designed to solve exactly this — after a successful send, `IncrementNonce` (called at `builder.go:959`) updates the cache so the next UserOp gets `nonce+1`. However, the paymaster path has a flaw:

1. **Multiple `BuildUserOpWithPaymaster` calls per send:** `sendUserOpShared` calls `BuildUserOpWithPaymaster` up to 3 times during a single UserOp flow:
   - Step 0 gas estimation (line 529) — fetches nonce from NonceManager
   - Step 1 gas estimation (line 637) — fetches nonce again
   - Final build (line 701) — fetches nonce again

   Each call hits `globalNonceManager.GetNextNonce` (`builder.go:1206`), which queries the on-chain nonce and compares with the cache. These temporary UserOps are never sent, so they don't call `IncrementNonce`, but they do interact with the NonceManager state.

2. **`sendUserOpCore` skips NonceManager for paymaster UserOps:** When a paymaster UserOp enters `sendUserOpCore`, its `Nonce` field is already set by `BuildUserOpWithPaymaster`. The check at line 857 (`if userOp.Nonce == nil`) is false, so it never consults the NonceManager.

3. **Retry logic only polls on-chain:** When AA25 is detected in `sendUserOpCore` (line 970-1012), the retry handler:
   - Calls `ResetNonce` to clear the cache (line 978)
   - Polls on-chain via `GetNextNonce` for up to 15s (line 986-1004)
   - But `ResetNonce` **deletes** the cached entry, so `GetNextNonce` falls back to on-chain state only
   - If the previous UserOp is still pending (not yet mined), the on-chain nonce hasn't advanced, so polling returns the same stale nonce

4. **The specific failure scenario:**
   - 09:30 UserOp succeeds with nonce N, `IncrementNonce` sets cache to N+1
   - 09:30 UserOp takes ~66s to mine on-chain
   - 09:40 gas estimation calls `BuildUserOpWithPaymaster` which calls `GetNextNonce` — returns `max(on-chain, cached)`
   - Gas estimation itself fails with AA25 (line 114), suggesting even the on-chain nonce query at that moment returned a nonce that was already pending in the bundler mempool
   - The retry loop resets the cache and polls on-chain, but the previous tx is still pending, so nonce never advances during the 15s timeout windows
   - All 3 retries exhaust → Sentry error
   - By 09:50, the previous tx has been mined, on-chain nonce advances, and the next execution succeeds

## Proposed Fix

Since all UserOps for a given sender are processed by the same aggregator process, the `NonceManager` cache should be the source of truth — not just the on-chain state.

Key changes needed:

1. **In the AA25 retry path (`sendUserOpCore` lines 973-1012):** Instead of calling `ResetNonce` (which discards the cache and falls back to on-chain), the retry should use `GetNextNonce` which returns `max(on-chain, cached)`. Only reset the cache if the on-chain nonce is *ahead* of the cache (meaning the pending tx was mined or dropped).

2. **For paymaster UserOps:** The nonce conflict retry in `sendUserOpCore` cannot simply bump the nonce because the paymaster signature is bound to the original nonce. The UserOp would need to be fully rebuilt (new nonce → new paymaster hash → new paymaster signature → new UserOp signature). This means the retry loop for paymaster UserOps needs to rebuild via `BuildUserOpWithPaymaster` rather than just updating the nonce field in-place.

3. **Consider per-sender serialization:** A per-sender mutex or queue in `sendUserOpShared` would prevent two concurrent executions from racing on the same nonce entirely. This is the most robust solution since the aggregator processes all UserOps for all senders.
