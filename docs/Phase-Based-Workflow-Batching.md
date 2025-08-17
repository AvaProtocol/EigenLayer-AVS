# Phase-Based Workflow Batching

## Overview

The Phase-Based Execution Plan solves the template variable dependency challenge in workflow batching by intelligently sorting operations into dependency resolution and atomic execution phases. This approach enables true atomic multi-contract operations while maintaining the existing template variable system.

## The Dependency Challenge

### Problem Statement

When batching multiple contract operations, template variables like `{{getBalance_weth.data.balanceOf}}` cannot be resolved until their source operations complete. This creates a circular dependency problem:

```javascript
// ❌ Problematic: Need balance BEFORE creating batch
executeBatch([
  [WETH, deposit_calldata],
  [WETH, approve(router, ???)],  // ← Unknown balance
  [ROUTER, exactInputSingle(???)] // ← Unknown parameters
])
```

### Solution: Smart Dependency Sorting

The Phase-Based approach separates operations by their dependency characteristics and execution requirements.

## Phase-Based Execution Architecture

### Phase 1: Dependency Resolution
**Purpose**: Execute all operations needed to resolve template variables
**Characteristics**: 
- Read-only operations (contractRead)
- View functions (contractWrite with view methods)
- Pure computations (customCode)
- No state changes to blockchain

### Phase 2: Atomic Batch Execution  
**Purpose**: Execute all state-changing operations atomically
**Characteristics**:
- State-changing contract writes
- All template variables pre-resolved
- Single ERC4337 UserOp transaction
- Atomic success/failure

## Real-World Example: WETH → USDC Swap

### Current Sequential Flow (Problematic)
```javascript
1. getBalance_weth: balanceOf(runner) → "80000000000000000"
2. wrap_eth: deposit() → Simulated (state not persisted)
3. approve_weth: approve(router, balance) → Simulated (state not persisted)  
4. get_quote: quoteExactInputSingle() → "462319752"
5. swap_params: calculate parameters → deadline, recipient, etc.
6. exactInputSingle: swap() → ❌ FAILS (no real approval)
```

**Problem**: Steps 2-3 are simulated individually, so approval doesn't persist for step 6.

### Phase-Based Flow (Solution)

#### **Phase 1: Dependency Resolution**
```javascript
// Execute to resolve template variables (no state changes)
1. getBalance_weth (contractRead): 
   └─ balanceOf(0x71c8...32e) → "80000000000000000"

2. get_quote (contractWrite/view):
   └─ quoteExactInputSingle({
        tokenIn: "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
        tokenOut: "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238", 
        amountIn: "80000000000000000",
        fee: 3000
      }) → "462319752"

3. swap_params (customCode):
   └─ Calculate: {
        tokenIn: "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
        tokenOut: "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
        amountIn: "80000000000000000",
        amountOutMinimum: "462319752",
        fee: 3000,
        recipient: "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
        deadline: 1755313243,
        sqrtPriceLimitX96: 0
      }

// All template variables now resolved:
// ✅ {{getBalance_weth.data.balanceOf}} = "80000000000000000"
// ✅ {{get_quote.data.quoteExactInputSingle.amountOut}} = "462319752"  
// ✅ {{swap_params.data}} = {complete swap parameters}
```

#### **Phase 2: Atomic Batch Execution**
```javascript
// Single ERC4337 UserOp with all state changes
executeBatch([
  // 1. Deposit ETH to get WETH
  [
    "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", // WETH contract
    deposit_calldata // deposit()
  ],
  
  // 2. Approve WETH to Uniswap router  
  [
    "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", // WETH contract
    approve_calldata // approve("0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e", "80000000000000000")
  ],
  
  // 3. Execute swap
  [
    "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e", // Uniswap router
    exactInputSingle_calldata // exactInputSingle({...resolved_params})
  ]
], msg.value = "1000000000000000") // ETH for deposit

// ✅ Result: All operations execute atomically
// ✅ Approval persists for the swap
// ✅ Single transaction, single gas cost
// ✅ All succeed or all fail together
```

## Implementation Strategy

### Step 1: Workflow Analysis
```go
func AnalyzeWorkflowDependencies(nodes []*avsproto.TaskNode) (Phase1Nodes, Phase2Nodes) {
    phase1 := []Node{} // Dependency resolution
    phase2 := []Node{} // Atomic execution
    
    for _, node := range nodes {
        if isReadOnly(node) || isViewFunction(node) || isCustomCode(node) {
            phase1 = append(phase1, node)
        } else if isStateChanging(node) {
            phase2 = append(phase2, node)
        }
    }
    
    return phase1, phase2
}
```

### Step 2: Template Variable Resolution
```go
func ResolveTemplateVariables(phase1Nodes []Node) (map[string]interface{}, error) {
    resolvedVars := make(map[string]interface{})
    
    // Execute Phase 1 nodes to populate template variables
    for _, node := range phase1Nodes {
        result, err := executeNode(node)
        if err != nil {
            return nil, err
        }
        resolvedVars[node.Name] = result
    }
    
    return resolvedVars, nil
}
```

### Step 3: Batch Construction
```go
func BuildAtomicBatch(phase2Nodes []Node, resolvedVars map[string]interface{}) (*BatchTransaction, error) {
    addresses := []common.Address{}
    calldatas := [][]byte{}
    totalValue := big.NewInt(0)
    
    for _, node := range phase2Nodes {
        // Resolve all template variables in node configuration
        resolvedNode := resolveTemplates(node, resolvedVars)
        
        // Generate calldata for resolved node
        calldata, err := generateCalldata(resolvedNode)
        if err != nil {
            return nil, err
        }
        
        addresses = append(addresses, resolvedNode.ContractAddress)
        calldatas = append(calldatas, calldata)
        totalValue.Add(totalValue, resolvedNode.Value)
    }
    
    // Use existing PackExecuteBatch function
    batchCalldata, err := aa.PackExecuteBatch(addresses, calldatas)
    if err != nil {
        return nil, err
    }
    
    return &BatchTransaction{
        Calldata: batchCalldata,
        Value:    totalValue,
    }, nil
}
```

### Step 4: Result Distribution
```go
func DistributeBatchResults(batchResult *TenderlyResult, phase2Nodes []Node) map[string]interface{} {
    results := make(map[string]interface{})
    
    // Parse batch transaction logs and events
    // Map results back to individual node names
    for i, node := range phase2Nodes {
        nodeResult := extractNodeResult(batchResult, i, node)
        results[node.Name] = nodeResult
    }
    
    return results
}
```

## Benefits

### ✅ Atomic Execution
- All state changes succeed or fail together
- No partial execution states
- Eliminates approval persistence issues

### ✅ Gas Efficiency  
- Single transaction instead of multiple
- Reduced total gas costs
- Single bundler fee

### ✅ Template Compatibility
- Maintains existing template variable system
- No breaking changes to workflow definitions
- Backward compatible with sequential execution

### ✅ Predictable Results
- Deterministic execution order
- Clear dependency resolution
- Consistent simulation vs. real execution

## Limitations and Considerations

### Workflow Compatibility
**✅ Compatible Workflows:**
- Clear separation between read and write operations
- Resolvable template dependencies
- Predictable state changes

**❌ Incompatible Workflows:**
- Circular dependencies between contract writes
- Dynamic intra-batch dependencies  
- Unpredictable state changes that affect subsequent operations

### Error Handling
- **Batch Failure**: If any operation in Phase 2 fails, entire batch reverts
- **Dependency Failure**: If Phase 1 operations fail, batching is aborted
- **Fallback Strategy**: System can fall back to sequential execution

### Performance Considerations
- **Phase 1 Latency**: Additional pre-execution time for dependency resolution
- **Batch Size Limits**: ERC4337 UserOp gas limits may constrain batch size
- **Simulation Complexity**: More complex Tenderly simulations for batch operations

## Future Enhancements

### Advanced Dependency Analysis
- Automatic detection of circular dependencies
- Intelligent dependency graph optimization
- Support for conditional batching based on workflow complexity

### Dynamic Batching Strategies
- Hybrid batching for partially compatible workflows
- Multi-phase batching for complex dependency chains
- Adaptive batch sizing based on gas limits

### Enhanced Error Recovery
- Partial batch execution with rollback points
- Intelligent retry strategies for failed batches
- Detailed error mapping for individual operations within batches

## Conclusion

The Phase-Based Execution Plan provides a robust solution for atomic multi-contract operations while preserving the flexibility and usability of the existing template system. By intelligently separating dependency resolution from atomic execution, it enables complex DeFi workflows like approve + swap to execute reliably in a single transaction.

This approach transforms the problematic sequential simulation pattern into a deterministic, atomic execution model that maintains backward compatibility while providing significant improvements in reliability and gas efficiency.
