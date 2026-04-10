# Production Incident: Dual-Workflow UserOp Collision & DAI Mislabeling in Summary

**Date:** 2026-04-10 ~06:52 UTC
**Chain:** Base (8453)
**Smart Wallet:** `0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e`
**Owner EOA:** `0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788`

---

## Issue 1: Context-Memory API Returns Wrong Token Name and Decimals

### Summary

The successful execution of task `01knv2qpgfbvj0zbs2ecjzp6cg` ("Automatically Split Incoming USDC Payments") transferred USDC but the Telegram notification reported DAI with wrong decimal formatting.

### What happened

The workflow executed two `transfer` calls on the USDC contract (`0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913`):
- **Transfer 1:** 2,000,000 raw (= **2 USDC**, 6 decimals) to `0x804e...1557` — tx `0xe3c0...764e`
- **Transfer 2:** 3,000,000 raw (= **3 USDC**, 6 decimals) to `0xc60e...C788` — tx `0x9e2c...dfb1`

### What was reported

The context-memory API (`/api/summarize`) returned:
- "Transferred **0.000000000002 DAI** to 0x804e...1557" (2,000,000 / 10^18)
- "Transferred **0.000000000003 DAI** to 0xc60e...C788" (3,000,000 / 10^18)

### Root cause

The context-memory API is using the wrong token metadata for the transfer amounts. It applied DAI's symbol and 18-decimal precision instead of USDC's 6-decimal precision. The aggregator correctly identified the contract as USDC in all execution logs (`vm_runner_contract_write.go` logs show `contract_address: 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913`).

Notably, the workflow's settings include both USDC (`0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913`) and DAI (`0x50c5725949a6f0c72e6c4a641f24049a917db0cb`) token addresses (visible from post-execution token metadata lookups from the frontend). The context-memory API may be picking up the DAI token metadata from the workflow configuration and incorrectly applying it to the USDC transfer results.

### Severity

**User-facing cosmetic bug** — the on-chain execution was correct (USDC was transferred), but the notification shows the wrong token and amount, which is confusing and could erode user trust.

### Action items

- [ ] Investigate context-memory API `/api/summarize` — why does it apply DAI metadata to USDC transfer results?
- [ ] Check whether the aggregator sends the correct `tokenMetadata` per-step in the summarize request payload (the token metadata lookup at `summarizer_context_memory.go:446` resolves the contract address and calls `GetTokenMetadata` — verify the resolved address is USDC, not DAI)
- [ ] If the contract address in the node config is a template variable (e.g., `{{value.tokenAddress}}`), verify `extractResolvedContractAddress` returns the correct runtime-resolved USDC address

---

## Issue 2: Concurrent Workflow Execution Causes UserOp Failure (Balance Exhaustion)

### Summary

Two workflows monitoring the same ERC20 Transfer event on the same smart wallet were triggered simultaneously by a single incoming 10 USDC transfer. Both attempted to send USDC from the wallet, and the second workflow's loop partially failed because the first workflow had already consumed part of the USDC balance.

### Timeline

| Time (UTC) | Event |
|---|---|
| 06:30:11 | Task `01knv1axw6d9fz7j23kx59vrpz` created (workflow 1) |
| 06:50:29 | Task `01knv2g38cv7kjjeyewm3tct7t` created ("USDC Revenue Splitter", workflow 2) |
| 06:52:11.202 | Operator triggers workflow 1 — queued as job 41190318 |
| 06:52:11.213 | Operator triggers workflow 2 — queued as job 41190319 (11ms later, same event) |
| 06:52:11.234 | **Job 41190318 starts** (workflow 1) — uses nonces 22-23 |
| 06:52:19.698 | **Job 41190318 completes successfully** — both loop iterations transferred USDC |
| 06:52:19.749 | **Job 41190319 starts** (workflow 2) |
| 06:52:20 | Workflow 2, iter 0: nonce 24, transfers 2 USDC to `0x804e...1557` — **SUCCESS** |
| 06:52:23 | Workflow 2, iter 1: nonce 25, tries to transfer 3 USDC to `0xc60e...C788` |
| 06:52:23 | Gas estimation fails: `ERC20: transfer amount exceeds balance` (3 retries) |
| 06:52:25 | UserOp sent anyway with fallback gas defaults |
| 06:52:27 | UserOp on-chain: `success=false` in UserOperationEvent — **FAILED** |
| 06:52:27 | Task execution: **partial success** (1 of 4 steps failed: loop1) |
| 06:53:00 | User deletes workflow 1 |
| 06:53:42 | User deletes workflow 2 |

### Trigger details

Both tasks were triggered by the **same on-chain event**:
- Block: `44507292`
- Tx: `0x740714cde10eeb329de9c6b55b4c9bd2663cc312aa04d691ab80ee47d01e7cd7`
- Transfer: 10 USDC from `0xc60e...C788` to smart wallet `0x71c8...232e`

### What went wrong

1. **No mutual exclusion on same-wallet execution.** Both workflows share the same smart wallet (`0x71c8...232e`). When both are triggered by the same event, their UserOps compete for the wallet's token balance.

2. **Sequential queue doesn't prevent balance races.** Job 41190318 (workflow 1) started 8.5 seconds before job 41190319 (workflow 2). Workflow 1 completed at 06:52:19.698, and workflow 2 started processing 51ms later. However, workflow 2 assumed the full 10 USDC was available, not accounting for workflow 1's transfers.

3. **Gas estimation failure not treated as abort signal.** When the bundler returned `ERC20: transfer amount exceeds balance` on all 3 gas estimation retries, the aggregator still sent the UserOp with fallback gas defaults (`builder.go:741`). This wasted gas on a transaction guaranteed to fail on-chain.

### Failed transaction

- UserOp hash: `0xc1e74c4636437e7010036f08c3d07dd42b7262f9eac391ac1e641200ee7c8b61`
- On-chain tx: `0xaa757381f1627e9d711180d02c85cda566b1198d4b48a7249747c7347e3a4035`
- Error: `UserOp execution failed (success=false in UserOperationEvent)`

### Severity

**Medium** — real funds were wasted on gas for a doomed UserOp. The user also received a misleading "partial success" notification for workflow 2. In a higher-value scenario, this race condition could cause unexpected fund movements.

### Action items

- [ ] **Same-wallet execution locking**: When a task begins executing UserOps for a given smart wallet, other tasks queued for the same wallet should wait until the first task completes (or at minimum, re-read the wallet's token balance before constructing UserOps)
- [ ] **Abort on persistent gas estimation revert**: If gas estimation fails with an ERC20 revert error (like `transfer amount exceeds balance`) on all retry attempts, do NOT send the UserOp with fallback gas. Fail the step immediately with a clear error instead of burning gas on a guaranteed-to-fail transaction
- [ ] **Consider deduplication for same-event triggers**: If multiple tasks for the same wallet are triggered by the exact same on-chain event (same block, same log), the system should either warn the user at task creation time or coordinate execution to avoid balance conflicts

---

## Bundler Logs (relevant excerpt)

```
06:52:11 - eth_sendUserOperation (workflow 1, iter 0)
06:52:16 - eth_sendUserOperation (workflow 1, iter 1)
06:52:20 - eth_sendUserOperation (workflow 2, iter 0, nonce 24) — success
06:52:23 - eth_estimateUserOperationGas — ERROR: ERC20: transfer amount exceeds balance
06:52:24 - eth_estimateUserOperationGas — ERROR: ERC20: transfer amount exceeds balance (retry 2)
06:52:25 - eth_estimateUserOperationGas — ERROR: ERC20: transfer amount exceeds balance (retry 3)
06:52:25 - eth_sendUserOperation (workflow 2, iter 1, nonce 25) — sent with fallback gas
06:52:27 - UserOp confirmed: success=false
```
