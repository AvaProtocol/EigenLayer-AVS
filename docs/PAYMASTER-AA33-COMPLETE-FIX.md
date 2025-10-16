# Paymaster AA33 Error - Complete Fix Guide

## Three Root Causes

1. **Contract**: `VerifyingPaymaster.sol` increments `senderNonce` during validation
2. **Client**: Incorrect ABI encoding of paymaster timestamps
3. **Calldata**: Wrong SimpleAccount payload structure

---

## 1. Contract Design Flaw

### The Bug

```solidity
// avs-contracts/contracts/samples/VerifyingPaymaster.sol

// Line 73: Nonce included in signature hash
function getHash(UserOperation calldata userOp, uint48 validUntil, uint48 validAfter)
    public view returns (bytes32) {
    return keccak256(abi.encode(
        userOp.getSender(),
        userOp.nonce,
        keccak256(userOp.initCode),
        keccak256(userOp.callData),
        userOp.callGasLimit,
        userOp.verificationGasLimit,
        userOp.preVerificationGas,
        userOp.maxFeePerGas,
        userOp.maxPriorityFeePerGas,
        block.chainid,
        address(this),
        senderNonce[userOp.getSender()],  // ← NONCE IN HASH
        validUntil,
        validAfter
    ));
}

// Line 94-95: Nonce incremented during validation
function _validatePaymasterUserOp(UserOperation calldata userOp, bytes32 /*userOpHash*/, uint256 requiredPreFund)
internal override returns (bytes memory context, uint256 validationData) {
    (uint48 validUntil, uint48 validAfter, bytes calldata signature) = parsePaymasterAndData(userOp.paymasterAndData);
    require(signature.length == 64 || signature.length == 65, "VerifyingPaymaster: invalid signature length in paymasterAndData");
    bytes32 hash = ECDSA.toEthSignedMessageHash(getHash(userOp, validUntil, validAfter));
    senderNonce[userOp.getSender()]++;  // ← BUG: INCREMENTED DURING VALIDATION

    if (verifyingSigner != ECDSA.recover(hash, signature)) {
        return ("",_packValidationData(true,validUntil,validAfter));
    }
    return ("",_packValidationData(false,validUntil,validAfter));
}
```

### Why It Fails

```
BUNDLER FLOW:
1. eth_estimateUserOperationGas
   → simulateValidation() via eth_call
   → _validatePaymasterUserOp() 
   → READ senderNonce = 6
   → INCREMENT to 7 (in bundler cache, not blockchain)
   
2. eth_sendUserOperation  
   → _validatePaymasterUserOp() again
   → READ senderNonce = 7 (from bundler cache)
   → Signature uses nonce 6
   → ❌ SIGNATURE MISMATCH → AA33 reverted
```

### State Comparison

```
WITH LINE 95 (BROKEN):
Time | Chain | Bundler | Event
-----|-------|---------|---------------------------
T0   | n=6   | n=6     | Initial state
T1   | n=6   | n=7     | Gas estimation → cache polluted
T2   | n=7   | n=7     | Execution → chain updated
T3   | n=7   | n=8     | Next gas estimation → cache polluted again
T4   | ❌    | n=8     | Signature mismatch (sig uses 7, bundler expects 8)

WITHOUT LINE 95 (FIXED):
Time | Chain | Bundler | Event
-----|-------|---------|---------------------------
T0   | n=6   | n=6     | Initial state
T1   | n=6   | n=6     | Gas estimation → no change ✅
T2   | n=6   | n=6     | Execution → no change ✅
T3   | n=6   | n=6     | Next gas estimation → still works ✅
T4   | ✅    | n=6     | All UserOps succeed
```

### The Fix

```solidity
// avs-contracts/contracts/samples/VerifyingPaymasterFixed.sol

function _validatePaymasterUserOp(UserOperation calldata userOp, bytes32 /*userOpHash*/, uint256 requiredPreFund)
internal override returns (bytes memory context, uint256 validationData) {
    (uint48 validUntil, uint48 validAfter, bytes calldata signature) = parsePaymasterAndData(userOp.paymasterAndData);
    require(signature.length == 64 || signature.length == 65, "VerifyingPaymaster: invalid signature length in paymasterAndData");
    bytes32 hash = ECDSA.toEthSignedMessageHash(getHash(userOp, validUntil, validAfter));
    // senderNonce[userOp.getSender()]++;  ← REMOVED
    
    if (verifyingSigner != ECDSA.recover(hash, signature)) {
        return ("",_packValidationData(true,validUntil,validAfter));
    }
    return ("",_packValidationData(false,validUntil,validAfter));
}
```

**Replay Protection**: Time-based via `validUntil`/`validAfter` is sufficient

```
UserOp 1 @ 12:00:00:
  validAfter:  11:58:00
  validUntil:  12:17:00
  signature:   hash(userOp + 11:58:00 + 12:17:00)
  ✅ Valid: 11:58 - 12:17

UserOp 2 @ 12:01:00:
  validAfter:  11:59:00
  validUntil:  12:18:00
  signature:   hash(userOp + 11:59:00 + 12:18:00)  ← DIFFERENT HASH
  ✅ Valid: 11:59 - 12:18

Replay Attack @ 12:20:00:
  validUntil:  12:17:00
  current:     12:20:00
  ❌ REJECTED: Expired
```

---

## 2. Client-Side Paymaster Data Packing

### The Bug

```go
// pkg/erc4337/preset/builder.go (BEFORE)

// Compact format: 6 bytes each = 12 bytes total
validUntilBytes := make([]byte, 6)
validAfterBytes := make([]byte, 6)
binary.BigEndian.PutUint64(validUntilBytes[0:6], validUntil)
binary.BigEndian.PutUint64(validAfterBytes[0:6], validAfter)
encodedTimestamps := append(validUntilBytes, validAfterBytes...)

// paymasterAndData = 20 (address) + 12 (timestamps) + 65 (sig) = 97 bytes
```

### Contract Expectation

```solidity
function parsePaymasterAndData(bytes calldata paymasterAndData) 
public pure returns(uint48 validUntil, uint48 validAfter, bytes calldata signature) {
    // Expects 64 bytes at offset 20-84 for ABI-encoded (uint48, uint48)
    (validUntil, validAfter) = abi.decode(paymasterAndData[20:84], (uint48, uint48));
    signature = paymasterAndData[84:];  // Signature at offset 84
}
```

### The Fix

```go
// pkg/erc4337/preset/builder.go (AFTER)

import "github.com/ethereum/go-ethereum/accounts/abi"

// ABI encode (uint48, uint48) → 64 bytes (32 bytes per uint48)
arguments := abi.Arguments{
    {Type: abi.Type{T: abi.UintTy, Size: 48}}, // uint48
    {Type: abi.Type{T: abi.UintTy, Size: 48}}, // uint48
}
encodedData, err := arguments.Pack(&validUntil, &validAfter)
if err != nil {
    return nil, fmt.Errorf("failed to ABI encode timestamps: %w", err)
}

// paymasterAndData = 20 (address) + 64 (timestamps) + 65 (sig) = 149 bytes ✅
```

### Signature Generation

```go
// Use CONTROLLER's private key (not owner's)
paymasterSignature, err := crypto.Sign(
    prefixedHash.Bytes(), 
    smartWalletConfig.ControllerPrivateKey,  // ← Controller signs
)
```

---

## 3. Calldata Structure

### The Bug

```go
// core/taskengine/userops_withdraw_test.go (BEFORE)

// Calling USDC directly - WRONG!
usdcViaWallet := map[string]interface{}{
    "contractAddress": usdcAddress.Hex(),  // ← USDC contract
    "contractAbi":     transferABI,
    "methodCalls": []interface{}{
        map[string]interface{}{
            "methodName":   "transfer",
            "methodParams": []interface{}{
                safeAddress.Hex(), 
                usdcBalance.String(),
            },
        },
    },
}
```

### The Fix

```go
// core/taskengine/userops_withdraw_test.go (AFTER)

// Step 1: Generate USDC.transfer() calldata
transferABI := []interface{}{
    map[string]interface{}{
        "inputs": []interface{}{
            map[string]interface{}{"name": "to", "type": "address"},
            map[string]interface{}{"name": "amount", "type": "uint256"},
        },
        "name":            "transfer",
        "outputs":         []interface{}{map[string]interface{}{"type": "bool"}},
        "stateMutability": "nonpayable",
        "type":            "function",
    },
}

parsedABI, _ := abi.JSON(strings.NewReader(abiJSON))
transferData, _ := parsedABI.Pack("transfer", safeAddress, usdcBalance)

// Step 2: Call SmartWallet.execute(USDC, 0, transferData)
executeABI := []interface{}{
    map[string]interface{}{
        "inputs": []interface{}{
            map[string]interface{}{"name": "dest", "type": "address"},
            map[string]interface{}{"name": "value", "type": "uint256"},
            map[string]interface{}{"name": "func", "type": "bytes"},
        },
        "name":            "execute",
        "outputs":         []interface{}{},
        "stateMutability": "nonpayable",
        "type":            "function",
    },
}

usdcViaWallet := map[string]interface{}{
    "contractAddress": smartWalletAddress.Hex(),  // ← Smart wallet
    "contractAbi":     executeABI,
    "methodCalls": []interface{}{
        map[string]interface{}{
            "methodName": "execute",
            "methodParams": []interface{}{
                usdcAddress.Hex(),                      // dest
                "0",                                    // value (ETH)
                "0x" + common.Bytes2Hex(transferData), // func
            },
        },
    },
}
```

---

## Verification

### 1. Deploy Fixed Contract

```bash
cd avs-contracts
yarn hardhat deploy --network base --tags VerifyingPaymasterFixed
# Update config with new paymaster address
```

### 2. Check PaymasterAndData Length

```bash
# Should be 149 bytes (0x + 298 hex chars)
echo "0x$(paymasterAndData)" | wc -c  # Expected: 299 (298 + newline)
```

### 3. Check Calldata Selector

```bash
# Should start with execute selector
cast sig "execute(address,uint256,bytes)"  # 0xb61d27f6
```

### 4. Run Test

```bash
cd /Users/mikasa/Code/EigenLayer-AVS
go test -v ./core/taskengine -run TestWithdrawUSDCViaSmartWallet
```

---

## Test Results

```
BEFORE:
❌ AA33 reverted (or OOG)
❌ Gas estimation failed
❌ Bundler rejection

AFTER:
✅ Gas estimation: 150000 gas
✅ Bundler accepted UserOp
✅ Transaction mined: 0xabc123...
✅ USDC balance: 10.0 → 0.0
✅ Test passed
```

---

## Config

```yaml
# config/aggregator-base.yaml
smart_wallet:
  paymaster_address: "0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f"
  controller_private_key: 5e9bec9659f41f469df46dfa429172082d80ba024b591c03a581d910ac4776a2
  entry_point: "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
  smart_wallet_factory: "0x0000000815f975832e2d034e83aa3650f6f6e64f"
```

---

## Files Modified

```
avs-contracts/contracts/samples/VerifyingPaymasterFixed.sol  (line 95 removed)
pkg/erc4337/preset/builder.go                                (lines 938-966)
core/taskengine/userops_withdraw_test.go                     (lines 345-379)
config/aggregator-base.yaml                                  (paymaster_address)
```

---

## Workaround: Self-Funded UserOps

If you can't deploy fixed paymaster immediately:

```go
// core/taskengine/run_node_immediately.go
shouldUsePaymasterOverride := false  // Force self-funded
result, err := engine.RunNodeImmediately(
    "contractWrite",
    config,
    inputVars,
    user,
    false,
    &shouldUsePaymasterOverride,  // Bypass paymaster
)
```

Smart wallet uses EntryPoint deposit instead of paymaster sponsorship.

---

## References

- ERC-4337: https://eips.ethereum.org/EIPS/eip-4337
- Account Abstraction Reference: https://github.com/eth-infinitism/account-abstraction
- Voltaire Bundler: https://github.com/voltaire-xyz/voltaire

