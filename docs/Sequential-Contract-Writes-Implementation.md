# Sequential Contract Writes - Implementation Plan

## Problem Statement

When workflows contain sequential `contract_write` operations where one depends on the other (e.g., approve → swap), the second operation may fail if the first hasn't been confirmed on-chain yet.

**Example Failure Scenario**:
1. Approval UserOp sent to bundler → returns immediately with `userOpHash`
2. Swap node starts execution before approval is mined
3. Swap's gas estimation fails (STF error) because approval isn't visible to RPC node yet
4. Even with fallback gas limits, swap may fail or behave unexpectedly

## Current Behavior

- `SimulateTask` calls `vm.Run()` which uses Kahn scheduler for parallel execution
- `executeNode` calls `runContractWrite` which sends UserOp and returns immediately
- No waiting mechanism between sequential contract writes
- RPC node lag can cause subsequent operations to fail

## Required Implementation

### 1. Add Waiting Logic After Contract Write Execution

**Location**: `core/taskengine/vm.go` - `executeNode()` function

**Approach**:
- After executing a `contract_write` node, check if it used real UserOps (not simulated)
- Extract `userOpHash` from the execution step's metadata/receipt
- If found, wait for on-chain confirmation using exponential backoff polling
- Add optional RPC propagation delay after confirmation

**Pseudocode**:
```go
func (v *VM) executeNode(node *avsproto.TaskNode) (*Step, error) {
    // ... existing execution logic ...
    
    executionLogForNode, err := v.runContractWrite(stepID, nodeValue)
    
    // NEW: Wait for on-chain confirmation if real execution
    if executionLogForNode != nil && !v.IsSimulation {
        if shouldWaitForConfirmation(executionLogForNode) {
            userOpHash := extractUserOpHash(executionLogForNode)
            if userOpHash != "" {
                v.logger.Info("⏳ Waiting for contract write confirmation", 
                    "nodeID", stepID, "userOpHash", userOpHash)
                
                // Wait for confirmation using exponential backoff
                receipt, waitErr := v.waitForUserOpConfirmation(userOpHash)
                if waitErr != nil {
                    v.logger.Error("❌ UserOp confirmation failed", 
                        "nodeID", stepID, "error", waitErr)
                    return nil, waitErr
                }
                
                v.logger.Info("✅ Contract write confirmed on-chain", 
                    "nodeID", stepID, "blockNumber", receipt.BlockNumber)
                
                // Add RPC propagation delay
                time.Sleep(5 * time.Second)
            }
        }
    }
    
    return executionLogForNode, err
}
```

### 2. Helper Functions

**Function 1: `shouldWaitForConfirmation`**
```go
func shouldWaitForConfirmation(step *avsproto.Execution_Step) bool {
    // Check if this is a contract_write step with pending UserOp
    if step.Metadata == nil {
        return false
    }
    
    // Extract metadata and check for userOpHash
    metadataMap := step.Metadata.AsInterface()
    if resultsArray, ok := metadataMap.([]interface{}); ok {
        for _, result := range resultsArray {
            if resultMap, ok := result.(map[string]interface{}); ok {
                if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
                    if status, hasStatus := receipt["status"].(string); hasStatus && status == "pending" {
                        return true
                    }
                }
            }
        }
    }
    
    return false
}
```

**Function 2: `extractUserOpHash`**
```go
func extractUserOpHash(step *avsproto.Execution_Step) string {
    if step.Metadata == nil {
        return ""
    }
    
    metadataMap := step.Metadata.AsInterface()
    if resultsArray, ok := metadataMap.([]interface{}); ok {
        for _, result := range resultsArray {
            if resultMap, ok := result.(map[string]interface{}); ok {
                if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
                    if userOpHash, hasHash := receipt["userOpHash"].(string); hasHash {
                        return userOpHash
                    }
                }
            }
        }
    }
    
    return ""
}
```

**Function 3: `waitForUserOpConfirmation`** (in VM)
```go
func (v *VM) waitForUserOpConfirmation(userOpHash string) (*types.Receipt, error) {
    // This requires access to eth client and bundler client
    // Need to pass these through VM initialization or smartWalletConfig
    
    // Use exponential backoff polling similar to builder.go
    intervals := []time.Duration{1, 2, 4, 8, 15, 30, 60}
    timeout := 5 * time.Minute
    startTime := time.Now()
    
    for _, interval := range intervals {
        // Check if timeout reached
        if time.Since(startTime) > timeout {
            return nil, fmt.Errorf("timeout waiting for UserOp confirmation")
        }
        
        // Try to get receipt from EntryPoint events
        receipt, err := v.getReceiptByUserOpHash(userOpHash)
        if err == nil && receipt != nil {
            return receipt, nil
        }
        
        // Wait before retry
        time.Sleep(interval * time.Second)
    }
    
    return nil, fmt.Errorf("failed to get UserOp confirmation after %v", timeout)
}
```

### 3. VM Struct Updates

**Add fields to VM**:
```go
type VM struct {
    // ... existing fields ...
    
    // For UserOp confirmation waiting
    ethClient     *ethclient.Client
    bundlerClient *bundler.BundlerClient
}
```

**Update VM initialization** in `engine.go`:
```go
vm := &VM{
    // ... existing fields ...
    ethClient:     ethClient,
    bundlerClient: bundlerClient,
}
```

### 4. Files to Modify

1. **`core/taskengine/vm.go`**
   - Add `waitForUserOpConfirmation()` method to VM
   - Update `executeNode()` to wait after contract writes
   - Add helper functions: `shouldWaitForConfirmation()`, `extractUserOpHash()`

2. **`core/taskengine/engine.go`**
   - Pass `ethClient` and `bundlerClient` to VM during initialization in `SimulateTask()`
   - Update VM struct initialization

3. **`core/taskengine/executor.go`**
   - Apply same changes for deployed workflow execution

## Testing

### Test File
`core/taskengine/simulate_sequential_contract_writes_test.go`

### Test Flow
1. Create workflow with approve + swap nodes
2. Call `SimulateTask` with real execution (`isSimulated: false`)
3. Verify:
   - Approval waits for on-chain confirmation
   - 5-second RPC propagation delay occurs
   - Swap executes after approval is confirmed
   - Both transactions succeed

### Run Command
```bash
export TEST_PRIVATE_KEY=0x...
export TEST_CHAIN=base
cd /Users/mikasa/Code/EigenLayer-AVS
go test -v -count=1 -run TestSimulateTask_SequentialContractWrites_Base ./core/taskengine/
```

## Benefits

1. ✅ Sequential contract writes work correctly without manual delays
2. ✅ Gas estimation succeeds for dependent operations
3. ✅ Same behavior for simulated and deployed workflows
4. ✅ Automatic RPC propagation handling
5. ✅ Clear logging of waiting/confirmation process

## Future Enhancements

1. Make RPC propagation delay configurable
2. Add configurable timeout for UserOp confirmation
3. Support batch UserOp submission for parallel writes
4. Add metrics for confirmation times

---

**Status**: Implementation in progress
**Next Steps**: Implement waiting logic in `vm.go` and test with Base workflow

