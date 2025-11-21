# Withdrawal Failure - Comprehensive Analysis

## Summary

This document provides a complete analysis of withdrawal failures in the EigenLayer AVS system, including root causes, real-world evidence, and implemented solutions.

### Key Findings

1. **ETH Withdrawal Failed**: Transaction `0x79914b82abf2c1594bdaca2abfac654fb1c897277f2c6616aac0cba5ec71f349` was confirmed but UserOp execution **failed** due to insufficient balance
2. **USDC Withdrawal Succeeded**: Transaction `0x7ec34e59c314ee25ddc02f7609a05447bc78ca859ac67995d1dd40d41a2bfdd6` completed successfully
3. **Root Cause**: Wallet balance (0.011129134 ETH) was insufficient to cover both withdrawal amount (0.009865195 ETH) and estimated gas reimbursement (0.003668515 ETH)

## Timeline of Events

### First Attempt (13:33:01) - ETH Withdrawal
- **UserOp sent**: hash=0x3543a133d999a11850f72c66ec9ba0277336976f4d97942547931cea05750d8c, nonce=10
- **Status**: Timed out waiting for confirmation after 33 seconds
- **Result**: UserOp was submitted but never confirmed (likely stuck in bundler mempool due to log query issue)

### Second Attempt (13:33:39) - USDC Withdrawal
- **Attempted nonce**: 11
- **Status**: Failed with "AA25 invalid account nonce"
- **Reason**: Nonce 10 was still pending in bundler mempool, blocking nonce 11
- **Result**: Could not proceed due to nonce conflict

### Retry (15:13:23) - ETH Withdrawal Retry
- **UserOp sent**: hash=0x6dfbf13534b595b2712bfbbcac4cb75b1053f32f1c5d0db1e25986104c1fa0ca, nonce=10
- **Transaction confirmed**: block=38445531, txHash=0x79914b82abf2c1594bdaca2abfac654fb1c897277f2c6616aac0cba5ec71f349
- **Gas used**: 131,483
- **Status**: Transaction included in block, but **UserOp execution failed** (reverted)

### USDC Withdrawal (15:13:34) - Success
- **UserOp sent**: hash=0x6db42aafff1dc71f358fb48c1d4df824efa6812ebf990bef858f85a659ea4763, nonce=11
- **Transaction confirmed**: block=38445536, txHash=0x7ec34e59c314ee25ddc02f7609a05447bc78ca859ac67995d1dd40d41a2bfdd6
- **Gas used**: 144,593
- **Status**: Transaction confirmed and UserOp execution **succeeded** ✅

## Real Numbers Proof

### Transaction Details
- **Transaction Hash**: `0x79914b82abf2c1594bdaca2abfac654fb1c897277f2c6616aac0cba5ec71f349`
- **Block**: `0x24aa1db` (38,445,531)
- **Smart Wallet**: `0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e`
- **UserOp Execution**: **FAILED** (success = false in UserOperationEvent)

### 📊 Wallet Balance

| Metric | Value (wei) | Value (ETH) |
|--------|-------------|-------------|
| **Balance Before Transaction** | 11,129,134,210,206,033 | 0.011129134 |
| **Balance After Transaction** | 10,865,195,462,037,792 | 0.010865195 |
| **Gas Cost Paid** | 263,938,748,168,241 | 0.000263939 |
| **Balance Change** | 263,938,748,168,241 | 0.000263939 |

**Note**: The balance change equals the gas cost, confirming the transaction was included but the UserOp execution failed.

### 💰 Transaction Attempted

The `executeBatchWithValues` attempted to execute **TWO operations atomically**:

1. **Withdrawal to recipient**: 9,865,195,462,037,792 wei (0.009865195 ETH)
2. **Reimbursement to paymaster owner**: 3,668,514,588,878,400 wei (0.003668515 ETH)

| Component | Value (wei) | Value (ETH) |
|-----------|-------------|-------------|
| **Withdrawal Amount** | 9,865,195,462,037,792 | 0.009865195 |
| **Estimated Reimbursement** | 3,668,514,588,878,400 | 0.003668515 |
| **Total Required** | 13,533,710,050,916,192 | 0.013533710 |

### 🔍 Balance Sufficiency Check

| Metric | Value (wei) | Value (ETH) |
|--------|-------------|-------------|
| **Available Balance** | 11,129,134,210,206,033 | 0.011129134 |
| **Total Required** | 13,533,710,050,916,192 | 0.013533710 |
| **Shortfall** | **2,404,575,840,710,159** | **0.002404576** |

**❌ INSUFFICIENT BALANCE!**

The wallet had **0.011129134 ETH** but needed **0.013533710 ETH**.
**Shortfall: 0.002404576 ETH (2,404,575,840,710,159 wei)**

### ⛽ Gas Cost Comparison

| Metric | Value (wei) | Value (ETH) |
|--------|-------------|-------------|
| **Estimated Reimbursement** | 3,668,514,588,878,400 | 0.003668515 |
| **Actual Gas Cost** | 263,938,748,168,241 | 0.000263939 |
| **Difference** | -3,404,575,840,710,159 | -0.003404576 |

**✅ Actual gas cost is LOWER than estimated by 0.003404576 ETH**

This means the estimation was conservative, but the failure occurred **before** the actual gas cost could be calculated.

## Root Cause Analysis

### Why ETH Withdrawal Failed

The ETH withdrawal transaction was **confirmed** (included in a block), but the **UserOp execution itself reverted**. The failure occurred because:

1. **Insufficient Balance**: The smart wallet didn't have enough ETH to cover:
   - The withdrawal amount (0.00986519 ETH)
   - Gas reimbursement to paymaster (0.00366851 ETH)
   - Total required: ~0.0135 ETH

2. **Atomic Execution Requirement**: The `executeBatchWithValues` call executes both operations atomically:
   - Withdrawal to recipient
   - Reimbursement to paymaster owner
   - If one fails, both fail

3. **Execution Revert**: The smart wallet's execute function reverted because the balance was insufficient to complete both operations.

### Why USDC Withdrawal Succeeded

The USDC withdrawal succeeded because:
- It was attempted after the ETH withdrawal (nonce 11 vs nonce 10)
- The smart wallet had sufficient USDC balance
- The execution completed successfully

## Issues Identified

### Issue 1: SDK Uses Fixed Gas Reserve (Not Accounting for Reimbursement)

**Location**: `ava-sdk-js/examples/example.ts:768-774`

**Problem**:
- SDK reserves only **0.001 ETH** for gas
- But the backend adds a **reimbursement** (~0.003668515 ETH estimated) via `executeBatchWithValues`
- SDK calculates: `withdrawal = balance - 0.001 ETH = 0.010865195 - 0.001 = 0.009865195 ETH`
- Backend needs: `withdrawal + reimbursement = 0.009865195 + 0.003668515 = 0.013533710 ETH`
- Wallet only has: `0.011129134 ETH` ❌

**What's Missing**:
- SDK doesn't know about the reimbursement that will be added by the backend
- SDK should calculate: `withdrawal = balance - (gas_reserve + estimated_reimbursement)`
- But SDK has no way to estimate the reimbursement amount (it's calculated on the backend)

### Issue 2: Backend Uses Overly Conservative Fallback Gas Estimation

**Location**: `pkg/erc4337/preset/builder.go:377-393`

When bundler gas estimation fails, the backend uses fallback values:

```go
// Fallback to conservative defaults when bundler estimation fails
preVerificationGas := DEFAULT_PREVERIFICATION_GAS      // 50,000 gas
verificationGas := DEFAULT_VERIFICATION_GAS_LIMIT      // 1,000,000 gas (very conservative!)
callGas := DEFAULT_CALL_GAS_LIMIT                      // 200,000 gas
ethTransferGas := ETH_TRANSFER_GAS_COST                // 21,000 gas

// Total: 50K + 1M + 200K + 21K = 1,271,000 gas
// Then applies 20% buffer: 1,271,000 × 1.2 = 1,525,200 gas
// Then applies another 20% buffer: 1,525,200 × 1.2 = 1,830,240 gas
// At ~2 gwei: 1,830,240 × 2,000,000,000 = 3,660,480,000,000,000 wei = 0.00366 ETH
```

**Problem**:
- **Estimated reimbursement**: 0.003668515 ETH (using fallback values)
- **Actual gas cost**: 0.000263939 ETH (13x lower!)
- The fallback `DEFAULT_VERIFICATION_GAS_LIMIT` (1M gas) is **way too high** for simple ETH transfers
- The estimation includes gas for the entire UserOp (verification + call + transfer), but the actual execution uses much less

**What's Inaccurate**:
1. `DEFAULT_VERIFICATION_GAS_LIMIT = 1,000,000` is too conservative for simple operations
2. The estimation doesn't account for the fact that `executeBatchWithValues` itself adds overhead
3. Two buffers are applied (20% + 20% = 44% total overhead), which compounds the overestimation

### Issue 3: SDK Doesn't Account for executeBatchWithValues Overhead

**Location**: `ava-sdk-js/examples/example.ts:768-774`

**Problem**:
- SDK calculates withdrawal as: `balance - 0.001 ETH`
- But the backend wraps it with `executeBatchWithValues` which executes:
  1. Withdrawal to recipient
  2. Reimbursement to paymaster owner
- Both operations need to be available **simultaneously** in the wallet balance
- SDK doesn't know about this atomic execution requirement

**What's Missing**:
- SDK should account for the fact that `withdrawal + reimbursement` must be available together
- SDK should either:
  - Query the backend for estimated reimbursement before calculating withdrawal amount
  - Or use a more conservative gas reserve that accounts for reimbursement

### Issue 4: No Pre-Validation of Balance Sufficiency

**Location**: `aggregator/rpc_server.go:162-311` (withdrawal handler)

**Problem**:
- The backend doesn't validate that the wallet has sufficient balance before attempting the withdrawal
- It only checks balance after the transaction fails
- No pre-flight check: `balance >= withdrawal_amount + estimated_reimbursement`

**What's Missing**:
- Pre-flight balance check before building the UserOp
- Return a clear error if balance is insufficient
- Could suggest the maximum withdrawable amount

### Issue 5: Aggregator Didn't Check UserOp Execution Success

**Location**: `pkg/erc4337/preset/builder.go` (before fix)

**Problem**:
- The aggregator only checked if the transaction was included in a block (receipt status)
- It didn't check the `success` field in the `UserOperationEvent` log
- This caused the aggregator to report "confirmed" even when the UserOp execution failed

**What's Missing**:
- Check the `success` field in `UserOperationEvent` log
- Return an error when `success = false`
- Distinguish between transaction confirmation and UserOp execution success

## Solutions Implemented

### ✅ Solution 1: SDK "Withdraw All" Support

**Implementation**: `ava-sdk-js/examples/example.ts`, `ava-sdk-js/packages/types/src/api.ts`

- SDK now supports `amount: "max"` to request withdrawal of all available balance
- Backend calculates the maximum withdrawable amount (balance - estimated gas reimbursement)
- No more manual gas reserve calculation in SDK

**Usage**:
```typescript
const response = await client.withdrawFunds({
  recipientAddress: "0x...",
  amount: "max", // Backend calculates max withdrawable
  token: "ETH",
  smartWalletAddress: "0x...",
});
```

### ✅ Solution 2: Pre-Flight Balance Validation

**Implementation**: `aggregator/rpc_server.go:244-405`

- Backend now validates balance before building UserOp
- Estimates gas reimbursement using bundler
- Calculates max withdrawable amount for "withdraw all" requests
- Returns clear error messages with shortfall information

**Features**:
- Validates balance sufficiency before attempting withdrawal
- Supports "withdraw all" by calculating max withdrawable amount
- Provides helpful error messages when balance is insufficient

### ✅ Solution 3: Improved Gas Estimation Reliability

**Implementation**: `pkg/erc4337/preset/builder.go:641-655`

- Added retry logic (3 attempts) for bundler gas estimation
- Better error handling and logging
- More reliable gas estimation reduces fallback usage

**Features**:
- Retries gas estimation up to 3 times
- Better error messages when estimation fails
- Reduces reliance on conservative fallback values

### ✅ Solution 4: UserOp Execution Success Checking

**Implementation**: `pkg/erc4337/preset/builder.go:245-320`

- Aggregator now checks `UserOperationEvent.success` field
- Returns error when UserOp execution fails
- Properly distinguishes between transaction confirmation and execution success

**Features**:
- Decodes `UserOperationEvent` log to extract `success` field
- Returns error when `success = false`
- Clear distinction between pending, confirmed, and failed states

### ✅ Solution 5: Exported Gas Reimbursement Estimation

**Implementation**: `pkg/erc4337/preset/builder.go:345-422`

- Exported `EstimateGasReimbursementAmount` function for use in withdrawal handler
- Allows pre-flight validation to estimate reimbursement accurately
- Uses bundler estimation when available, falls back to conservative defaults

## Key Insights

1. **The actual gas cost (0.000263939 ETH) was much lower than estimated (0.003668515 ETH)**, but this doesn't matter because the transaction failed before execution completed.

2. **The wallet balance was sufficient for the withdrawal alone** (0.011129134 ETH > 0.009865195 ETH), but **insufficient when including the reimbursement** (0.011129134 ETH < 0.013533710 ETH).

3. **The failure occurred atomically** - since `executeBatchWithValues` executes both operations together, if one fails, both fail. The wallet couldn't complete the withdrawal because it couldn't also pay the reimbursement.

4. **The gas cost was still deducted** (0.000263939 ETH) because the transaction was included in a block, even though the UserOp execution failed.

5. **Transaction receipt status ≠ UserOp execution success**: A transaction can be included in a block (receipt.status = 1) but the UserOp execution can still fail (UserOperationEvent.success = false).

## Comparison Table

| Component | SDK Calculation | Backend Needs | Actual |
|-----------|----------------|---------------|--------|
| **Wallet Balance** | 0.010865195 ETH | 0.011129134 ETH | 0.011129134 ETH |
| **Gas Reserve (SDK)** | 0.001 ETH | - | - |
| **Withdrawal Amount** | 0.009865195 ETH | 0.009865195 ETH | 0.009865195 ETH |
| **Estimated Reimbursement** | ❌ Not considered | 0.003668515 ETH | - |
| **Actual Gas Cost** | - | - | 0.000263939 ETH |
| **Total Required** | 0.010865195 ETH | **0.013533710 ETH** | 0.000263939 ETH |
| **Available** | 0.010865195 ETH | 0.011129134 ETH | 0.011129134 ETH |
| **Result** | ✅ Seems OK | ❌ **Shortfall: 0.002404576 ETH** | - |

## Code References

### Withdrawal Handler
- `aggregator/rpc_server.go:154-311` - Main withdrawal handler with pre-flight validation
- `aggregator/withdrawal.go` - Withdrawal calldata building and validation

### Gas Estimation
- `pkg/erc4337/preset/builder.go:345-422` - Gas reimbursement estimation
- `pkg/erc4337/preset/builder.go:606-655` - Gas estimation with retry logic
- `pkg/erc4337/preset/builder.go:35-40` - Default gas limits

### UserOp Execution Success
- `pkg/erc4337/preset/builder.go:245-320` - UserOp receipt polling and success checking
- `pkg/erc4337/preset/builder.go:245-260` - `checkUserOpExecutionSuccess` helper

### SDK
- `ava-sdk-js/examples/example.ts:767-815` - Withdrawal example with "max" support
- `ava-sdk-js/packages/types/src/api.ts:268-273` - WithdrawFundsRequest interface
- `ava-sdk-js/packages/sdk-js/src/index.ts:556-598` - withdrawFunds implementation

## Data Sources

- **Blockchain Data**: Base mainnet RPC (`https://mainnet.base.org`)
- **Transaction Receipt**: Block 0x24aa1db, Transaction 0x79914b82abf...
- **UserOperationEvent**: Decoded from transaction logs
- **Aggregator Logs**: `aggregator-base.log` (withdrawal attempts)

## Related Documentation

- Bundler configuration: See bundler setup documentation for `--logs_incremental_range` parameter
- UserOp execution: See ERC-4337 specification for UserOperationEvent structure
- Gas estimation: See bundler RPC documentation for `eth_estimateUserOperationGas` method

