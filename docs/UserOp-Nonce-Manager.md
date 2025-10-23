# Nonce Management Fix for UserOperation Conflicts

## Problem

The error `"invalid UserOperation struct/fields" (code: -32602)` was being returned by the Voltaire bundler when trying to send UserOperations. Investigation revealed this was **not** a struct validation error, but rather a **nonce conflict error**.

### Root Cause Analysis

1. **Misleading Error Message**: Voltaire bundler returns `"invalid UserOperation struct/fields"` when attempting to replace an existing UserOp in the mempool without meeting the 10% gas price bump requirement.

2. **Nonce Source Mismatch**: The code was fetching nonces directly from on-chain state using `aa.MustNonce()`, which returns the **last mined nonce**, not accounting for pending transactions in the bundler's mempool.

3. **Race Condition**: When a UserOp with nonce N is submitted but not yet mined:
   - On-chain nonce is still N
   - Bundler mempool has a pending UserOp with nonce N
   - Next UserOp also tries to use nonce N → **conflict!**

4. **Voltaire's Replacement Rules** (from `sender_mempool.py`):
   ```python
   # To replace an existing UserOp with same nonce, you must:
   # - Increase maxFeePerGas by 10%
   # - Increase maxPriorityFeePerGas by 10%
   # Otherwise: raise "invalid UserOperation struct/fields"
   ```

## Solution

Implemented a **NonceManager** that tracks pending nonces across UserOp submissions:

### New Component: `NonceManager`

**File**: `/Users/mikasa/Code/EigenLayer-AVS/pkg/erc4337/bundler/nonce_manager.go`

**Features**:
- Maintains in-memory cache of next expected nonce per sender
- Uses `max(on-chain nonce, cached pending nonce)` to determine next nonce
- Thread-safe (uses `sync.RWMutex`)
- Auto-increments after successful submission
- Auto-resets on nonce conflicts

**Key Methods**:
```go
// Get next nonce to use (combines on-chain + pending knowledge)
func GetNextNonce(client, sender, onChainFetcher) (*big.Int, error)

// Increment after successful submission
func IncrementNonce(sender, currentNonce)

// Reset cache on conflicts (forces fresh chain query)
func ResetNonce(sender)
```

### Changes to `builder.go`

**File**: `/Users/mikasa/Code/EigenLayer-AVS/pkg/erc4337/preset/builder.go`

1. **Global NonceManager Instance** (line ~79):
   ```go
   globalNonceManager = bundler.NewNonceManager()
   ```

2. **Updated `BuildUserOpWithPaymaster`** (line ~1064-1073):
   - Now uses `globalNonceManager.GetNextNonce()` instead of direct `aa.MustNonce()`
   - Considers both on-chain state and pending UserOps

3. **Updated `sendUserOpCore`** (line ~758-768):
   - Non-paymaster UserOps also use NonceManager
   - Consistent nonce management across all paths

4. **Success Path** (line ~830-833):
   - Calls `globalNonceManager.IncrementNonce()` after successful submission
   - Allows sequential UserOps to use nonce+1, nonce+2, etc.

5. **Enhanced Error Handling** (line ~837-887):
   - Detects both `"AA25 invalid account nonce"` and `"invalid UserOperation struct/fields"`
   - Resets nonce cache on conflicts
   - Polls for fresh on-chain nonce with exponential backoff
   - Retries with updated nonce

## How It Works

### Scenario 1: First UserOp for a sender
```
1. GetNextNonce() → no cache → fetch on-chain nonce (e.g., 110)
2. Build and send UserOp with nonce 110
3. Success → IncrementNonce() → cache = 111
4. UserOp is pending in bundler mempool
```

### Scenario 2: Sequential UserOps (before first mines)
```
1. GetNextNonce() → cache = 111, on-chain = 110 → use max(111, 110) = 111
2. Build and send UserOp with nonce 111
3. Success → IncrementNonce() → cache = 112
4. Both UserOps pending in bundler
```

### Scenario 3: Nonce Conflict (external UserOp submitted)
```
1. Try to send UserOp with nonce 110
2. Bundler returns "invalid UserOperation struct/fields"
3. Detect nonce conflict → ResetNonce() → clear cache
4. Poll on-chain nonce until it changes or timeout
5. Retry with fresh nonce
```

### Scenario 4: UserOps mine successfully
```
1. Previous UserOps mine → on-chain nonce advances to 112
2. GetNextNonce() → cache = 112, on-chain = 112 → use 112
3. Cache and chain are in sync
```

## Benefits

✅ **Eliminates nonce conflicts** between sequential UserOps  
✅ **Handles pending transactions** not yet mined  
✅ **Auto-recovers** from conflicts with external UserOps  
✅ **Thread-safe** for concurrent operations  
✅ **No breaking changes** to existing APIs  
✅ **Detailed logging** for debugging  

## Testing

### Unit Testing

Test the NonceManager directly:

```go
import "testing"

func TestNonceManager(t *testing.T) {
    nm := bundler.NewNonceManager()
    sender := common.HexToAddress("0x123...")
    
    // First call fetches from chain
    nonce1, _ := nm.GetNextNonce(client, sender, func() (*big.Int, error) {
        return big.NewInt(110), nil
    })
    assert.Equal(t, "110", nonce1.String())
    
    // Increment after submission
    nm.IncrementNonce(sender, nonce1)
    
    // Next call uses cached value
    nonce2, _ := nm.GetNextNonce(client, sender, func() (*big.Int, error) {
        return big.NewInt(110), nil  // Still 110 on-chain
    })
    assert.Equal(t, "111", nonce2.String())
}
```

### Integration Testing

Run the failing test again:

```bash
npx jest tests/templates/test-single-approve-with-simulation.test.ts \
  -t "runNodeWithInputs - contractWrite real execution validates approval persistence"
```

**Expected Result**: Should now succeed with proper nonce management.

### Monitoring Logs

Look for these log patterns to verify correct behavior:

**Success Path**:
```
🔢 NONCE MANAGER: First UserOp for sender 0x5a8A..., using on-chain nonce 110
UserOp sent (attempt 1/3): hash=0xabc..., nonce=110, sender=0x5a8A...
🔢 NONCE MANAGER: Incremented nonce for sender 0x5a8A...: 110 -> 111
```

**Conflict Recovery**:
```
⚠️ NONCE CONFLICT DETECTED: invalid UserOperation struct/fields
   This usually means a UserOp with nonce 110 is already pending in bundler
   Reset nonce cache for sender 0x5a8A...
🔢 NONCE MANAGER: Reset cached nonce for sender 0x5a8A...
✅ Updated nonce to 111 (after 1.2s)
```

## Backward Compatibility

✅ **Fully backward compatible**  
- Existing code continues to work
- NonceManager is transparent to callers
- No API changes required

## Future Improvements

1. **Persistence**: Save nonce cache to disk to survive restarts
2. **TTL**: Add expiration to cached nonces (e.g., 5 minutes)
3. **Bundler Query**: Use `eth_getUserOperationByHash` to verify pending state
4. **Metrics**: Add Prometheus metrics for nonce conflicts/recoveries
5. **Per-Bundler Caching**: Track nonces separately per bundler URL

## References

- **EIP-4337**: https://eips.ethereum.org/EIPS/eip-4337
- **Voltaire Bundler**: `/Users/mikasa/Code/voltaire/voltaire_bundler/mempool/sender_mempool.py`
- **Error Code -32602**: `ValidationExceptionCode.InvalidFields` in Voltaire
- **Nonce Management**: ERC-4337 EntryPoint `getNonce(sender, key)` function

