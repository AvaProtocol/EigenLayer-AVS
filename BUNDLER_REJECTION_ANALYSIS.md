# Bundler Rejection Analysis - "invalid UserOperation struct/fields"

## Current Status
The Voltaire bundler on Base is consistently rejecting all UserOps with error code `-32602` and message `"invalid UserOperation struct/fields"`.

## What We Know

### ✅ Verified Correct
1. **PaymasterAndData structure**: 149 bytes (20 + 32 + 32 + 32 + 33)
   - Paymaster address: 20 bytes
   - validUntil: 32 bytes (ABI-encoded uint48)
   - validAfter: 32 bytes (ABI-encoded uint48)
   - Signature (r+s+v): 65 bytes

2. **Paymaster configuration**:
   - verifyingSigner: `0x3b6f68B9B4577A3D3085ead6Fcdc2951719ad2e1` ✅
   - Matches our controller address ✅

3. **Smart wallet**:
   - Deployed at `0x981E18d5AadE83620A6Bd21990b5Da0c797e1e5b` ✅
   - Controller set correctly ✅
   - Salt:0 derived from TEST_PRIVATE_KEY ✅

4. **Nonces**:
   - Smart wallet nonce: 2 (on-chain)
   - Paymaster nonce: 2 (on-chain)
   - Both match what we're using ✅

5. **Timestamps**:
   - validAfter: ~1 minute in the past ✅
   - validUntil: ~15 minutes in the future ✅
   - No "time-range in the future" error anymore ✅

6. **EntryPoint address**:
   - Using EIP-55 checksummed format: `0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789` ✅

## The Mystery

Despite all fields being structurally correct, the bundler is rejecting **every single UserOp** with the same generic error. This suggests one of the following:

### Hypothesis 1: Cached/Stuck UserOp in Bundler Mempool
- **Evidence**: Paymaster nonce is stuck at 2, suggesting a previous UserOp with nonce 2 was accepted but never mined
- **Impact**: Bundler might be caching validation results and rejecting new UserOps with the same nonce as duplicates
- **Solution**: Wait for mempool to clear, or manually trigger bundler to flush

### Hypothesis 2: Bundler-Specific Validation Rules
- **Evidence**: Voltaire bundler doesn't provide detailed error messages
- **Possible issues**:
  - Custom validation rules beyond ERC-4337 spec
  - Stricter signature format requirements
  - Additional field checks not documented
- **Next step**: Need to examine Voltaire bundler source code

### Hypothesis 3: Paymaster Signature Mismatch
- **Evidence**: We fixed timestamp order and nonce inclusion, but error persists
- **Possible issues**:
  - Hash calculation might differ from what contract expects
  - Signature recovery might be failing on bundler side
- **Next step**: Compare our hash with on-chain `getHash()` call

## Recent Fixes Applied
1. ✅ Fixed timestamp order in paymasterAndData (validUntil, validAfter)
2. ✅ Fixed GetHash parameter order to match Solidity contract
3. ✅ Added senderNonce to paymaster hash calculation
4. ✅ Fixed validAfter to be in the past (now - 60s)
5. ✅ Used EIP-55 checksummed EntryPoint address
6. ✅ Defined EntryPoint as constant instead of hardcoded

## What to Try Next
1. **Manual bundler trigger**: Force the bundler to process/drop stuck UserOps
2. **Increment nonce**: Try nonce 3 to bypass any cached validation
3. **Compare hashes**: Call `paymaster.getHash()` on-chain with our UserOp params and compare with what we're signing
4. **Check bundler source**: Look at Voltaire bundler validation logic for Base-specific rules

## Bundler Configuration
- Endpoint: `https://bundler-base.avaprotocol.org/rpc?apikey=kt8qTj8MtmAQGsj17Urz1ySn4R`
- Implementation: Voltaire (Python-based)
- Staking: Disabled (`--min_stake 0 --min_unstake_delay 0`)
- Reputation whitelist: Not configured

## Key Log Evidence
```
Oct 13 19:49:55.13 DEBUG request: (...UserOp with all correct fields...)
Oct 13 19:49:55.13 WARNING ... error code:-32602 - error message:invalid UserOperation struct/fields
```

The bundler is rejecting at validation stage before even attempting on-chain simulation, which suggests it's a structural/format issue, not a contract execution issue.

---

## ✅ RESOLUTION (Oct 13, 2025)

### Root Cause
The bundler was rejecting all UserOps with error `-32602: invalid UserOperation struct/fields` because it had a **stuck UserOp in its mempool** with nonce 2 from a previous test run. The bundler was treating all new submissions with the same nonce as duplicate requests and rejecting them immediately without detailed error messages.

### Verification Steps Performed
1. ✅ Verified PaymasterAndData structure (149 bytes: 20 + 64 + 65)
2. ✅ Verified paymaster hash calculation matches contract expectation
3. ✅ Verified paymaster signature is valid (correct signer recovery)
4. ✅ Verified timestamp order and packing
5. ✅ Verified EIP-55 checksummed EntryPoint address
6. ✅ Verified controller private key matches verifyingSigner

### Solution
**Restarted the bundler** to clear its mempool cache, which immediately resolved the issue. The very next UserOp submission was accepted successfully:

```
Response Body: {"jsonrpc": "2.0", "id": 1, "result": "0x13c51e03bf9f8ae142bc0b6ba1e6a35b090cd7e461a348bce48b90c8c90fed70"}
✅ BUNDLER SEND SUCCESS - Nonce used: 2
```

### Key Learnings
1. **Bundler mempool caching**: Voltaire bundler caches validation results and can get stuck with pending UserOps
2. **Generic error messages**: `-32602` is too generic; we need better bundler error reporting
3. **Nonce management**: ERC-4337 nonces can only be incremented via EntryPoint, making stuck nonces difficult to resolve without bundler restart
4. **Our implementation is correct**: All hash calculations, signature generation, and field encoding were working perfectly

### Code Quality Verified
- ✅ Paymaster hash calculation (includes chainID, nonce, timestamps)
- ✅ Paymaster signature generation (EIP-191 prefixed)
- ✅ Timestamp ABI encoding (validUntil, validAfter order)
- ✅ PaymasterAndData structure (address + timestamps + signature)
- ✅ EntryPoint constant definition (no more hardcoding)

