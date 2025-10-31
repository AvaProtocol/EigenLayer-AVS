# Multi-Contract Batching Implementation Plan

## Quick Reference

### Current Status
- ✅ **`PackExecuteBatch` Function**: Implemented in `core/chainio/aa/aa.go`
- 🚧 **Contract Write Processor**: Needs extension to support multi-contract operations
- 📋 **API Design**: Documented and ready for implementation

### Key Files to Modify
1. `core/taskengine/vm_runner_contract_write.go` - Main processor logic
2. `core/chainio/aa/aa.go` - Smart wallet interaction (✅ Done)
3. `protobuf/avs.proto` - Protocol buffer definitions (if needed)

## Implementation Steps

### Step 1: Extend ContractWriteNode Proto (Optional)
If we need to modify the protocol buffer definition:

```protobuf
message ContractWriteNode {
  message MethodCall {
    string method_name = 1;
    repeated string method_params = 2;
    string call_data = 3;
    string contract_address = 4; // NEW: Per-method contract address
  }
  
  message Config {
    string contract_address = 1; // Keep for backward compatibility
    repeated google.protobuf.Value contract_abi = 2;
    repeated MethodCall method_calls = 3;
    string value = 4;
    string gas_limit = 5;
    bool batch_execution = 6; // NEW: Enable batching flag
  }
}
```

### Step 2: Modify Contract Write Processor

**File**: `core/taskengine/vm_runner_contract_write.go`

#### 2.1 Detection Logic
```go
// Add to executeMethodCall or Execute function
func (r *ContractWriteProcessor) shouldUseBatch(methodCalls []*avsproto.ContractWriteNode_MethodCall, defaultAddress string) bool {
    // Check if batchExecution flag is set
    if node.Config != nil && node.Config.BatchExecution {
        return true
    }
    
    // Auto-detect if method calls have different contract addresses
    for _, call := range methodCalls {
        if call.ContractAddress != "" && call.ContractAddress != defaultAddress {
            return true
        }
    }
    return false
}
```

#### 2.2 Batch Execution Path
```go
// Add new function
func (r *ContractWriteProcessor) executeBatchMethodCalls(
    ctx context.Context,
    methodCalls []*avsproto.ContractWriteNode_MethodCall,
    defaultAddress string,
) *avsproto.ContractWriteNode_MethodResult {
    
    var targetAddresses []common.Address
    var calldataArray [][]byte
    
    for _, methodCall := range methodCalls {
        // Determine contract address for this call
        contractAddr := defaultAddress
        if methodCall.ContractAddress != "" {
            contractAddr = methodCall.ContractAddress
        }
        
        // Generate calldata for this specific call
        callData, err := r.generateCallData(methodCall, contractAddr)
        if err != nil {
            return &avsproto.ContractWriteNode_MethodResult{
                Success: false,
                Error:   fmt.Sprintf("Failed to generate calldata: %v", err),
            }
        }
        
        targetAddresses = append(targetAddresses, common.HexToAddress(contractAddr))
        calldataArray = append(calldataArray, common.FromHex(callData))
    }
    
    // Use PackExecuteBatch for smart wallet
    batchCallData, err := aa.PackExecuteBatch(targetAddresses, calldataArray)
    if err != nil {
        return &avsproto.ContractWriteNode_MethodResult{
            Success: false,
            Error:   fmt.Sprintf("Failed to pack batch execution: %v", err),
        }
    }
    
    // Execute the batch transaction
    return r.executeRealUserOpTransaction(ctx, r.smartWalletAddress, 
        hexutil.Encode(batchCallData), "executeBatch", nil, time.Now())
}
```

#### 2.3 Integration Point
```go
// Modify the Execute function
func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
    // ... existing code ...
    
    // Check if we should use batch execution
    if r.shouldUseBatch(methodCalls, contractAddress) {
        log.WriteString("Using batch execution for multi-contract operations\n")
        
        result := r.executeBatchMethodCalls(ctx, methodCalls, contractAddress)
        results = append(results, result)
    } else {
        // Use existing single-call logic
        for _, methodCall := range methodCalls {
            result := r.executeMethodCall(ctx, parsedABI, originalAbiString, contractAddr, methodCall)
            results = append(results, result)
        }
    }
    
    // ... rest of existing code ...
}
```

### Step 3: ABI Handling for Multiple Contracts

```go
// Add helper function
func (r *ContractWriteProcessor) getABIForContract(contractAddress string, methodCalls []*avsproto.ContractWriteNode_MethodCall) (*abi.ABI, string, error) {
    // For batch operations, we need to handle multiple ABIs
    // This could be enhanced to support per-method ABI specification
    
    // For now, use the default ABI and warn if mixing contracts
    if node.Config != nil && len(node.Config.ContractAbi) > 0 {
        return r.parseABI(node.Config.ContractAbi)
    }
    
    return nil, "", fmt.Errorf("ABI required for contract operations")
}
```

## Testing Plan

### Unit Tests
```go
func TestPackExecuteBatch(t *testing.T) {
    addresses := []common.Address{
        common.HexToAddress("0xA0b86a33E6441E6De5B3D3C8dA8e7C5c8f8e7B6d"),
        common.HexToAddress("0xB0b86a33E6441E6De5B3D3C8dA8e7C5c8f8e7B6d"),
    }
    
    calldataArray := [][]byte{
        common.FromHex("0xa9059cbb000000000000000000000000..."), // approve
        common.FromHex("0x04e45aaf000000000000000000000000..."), // exactInputSingle
    }
    
    result, err := aa.PackExecuteBatch(addresses, calldataArray)
    
    assert.NoError(t, err)
    assert.NotEmpty(t, result)
    // Verify the result contains proper executeBatch encoding
}
```

### Integration Tests
```go
func TestApproveAndSwapBatch(t *testing.T) {
    // Test the complete approve + swap flow
    // Verify both operations execute atomically
    // Check that approval is available for swap
}
```

## Configuration Examples

### Simple Batch (Same Contract)
```javascript
{
  nodeType: 'contractWrite',
  nodeConfig: {
    contractAddress: '0x...', // Default contract
    methodCalls: [
      { methodName: 'method1', methodParams: ['param1'] },
      { methodName: 'method2', methodParams: ['param2'] }
    ],
    batchExecution: true
  }
}
```

### Multi-Contract Batch
```javascript
{
  nodeType: 'contractWrite',
  nodeConfig: {
    methodCalls: [
      { 
        contractAddress: '0xWETH...',
        methodName: 'approve',
        methodParams: ['0xRouter...', '1000000000000000000']
      },
      {
        contractAddress: '0xRouter...',
        methodName: 'exactInputSingle',
        methodParams: ['{{swapParams}}']
      }
    ],
    batchExecution: true,
    gasLimit: '400000'
  }
}
```

## Rollout Strategy

### Phase 1: Basic Implementation
- Implement core batching logic
- Support `batchExecution: true` flag
- Basic error handling

### Phase 2: Multi-Contract Support  
- Add `contractAddress` to method calls
- Support different contracts in same batch
- Enhanced ABI handling

### Phase 3: Advanced Features
- Conditional execution
- Dynamic gas estimation
- Optimized result processing

### Phase 4: Production Readiness
- Comprehensive testing
- Performance optimization
- Documentation and examples

## Risk Mitigation

### Backward Compatibility
- Keep existing single-call logic intact
- Make batching opt-in via flag
- Graceful fallback for unsupported operations

### Gas Management
- Conservative gas limit estimation
- Warn users about higher gas costs
- Provide gas estimation tools

### Error Handling
- Clear error messages for batch failures
- Individual operation status tracking
- Detailed debugging information

---

**Next Steps**: 
1. Implement detection logic in contract write processor
2. Add batch execution path using `PackExecuteBatch`
3. Create comprehensive tests
4. Update API documentation

**Estimated Effort**: 2-3 days development + 1 day testing
