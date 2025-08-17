# Multi-Contract Batching Feature

## Overview

The Multi-Contract Batching feature enables atomic execution of multiple contract calls across different contracts in a single ERC4337 UserOp transaction. This solves critical issues like the approve + swap pattern where state changes must persist between operations.

## Problem Statement

### The Approval Challenge

When executing complex DeFi operations like token swaps, you often need to:

1. **Approve** a token (e.g., WETH) to a router contract
2. **Execute** the swap operation (e.g., `exactInputSingle`)

**Current Limitation**: In simulation mode, each contract write runs independently without persisting blockchain state changes. This means:

- ❌ First call: `approve(router, amount)` → Simulated (no real approval)
- ❌ Second call: `exactInputSingle(params)` → Fails with "STF" (insufficient allowance)

### The Solution

**Atomic Batching**: Execute both operations in a single UserOp transaction using the smart wallet's `executeBatch` function.

- ✅ **Single Transaction**: Both approve + swap execute atomically
- ✅ **State Persistence**: Approval is available immediately for the swap
- ✅ **Gas Efficient**: One transaction instead of two
- ✅ **Failure Safety**: Either both succeed or both fail

## Technical Architecture

### Smart Wallet Integration

The feature leverages ERC4337 smart wallets' native batching capability:

```solidity
// SimpleAccount.sol
function executeBatch(address[] dest, bytes[] func) external;
```

### Implementation Components

1. **`PackExecuteBatch` Function** (`core/chainio/aa/aa.go`)
   - Encodes multiple contract calls for smart wallet execution
   - Takes arrays of target addresses and calldata

2. **Extended Contract Write Processor** (Planned)
   - Detects multi-contract operations
   - Uses `PackExecuteBatch` instead of `PackExecute`
   - Handles mixed contract addresses in method calls

## API Design

### Current Contract Write Structure

```javascript
{
  nodeType: 'contractWrite',
  nodeConfig: {
    contractAddress: '0x...', // Single contract
    methodCalls: [
      {
        methodName: 'someMethod',
        methodParams: ['param1', 'param2']
      }
    ]
  }
}
```

### Proposed Multi-Contract Structure

```javascript
{
  nodeType: 'contractWrite',
  nodeConfig: {
    // contractAddress: omitted for multi-contract operations
    methodCalls: [
      {
        contractAddress: '0xfff9976782d46cc05630d1f6ebab18b2324d6b14', // WETH
        methodName: 'approve',
        methodParams: [
          '0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e', // Uniswap Router
          '80000000000000000' // Amount to approve
        ]
      },
      {
        contractAddress: '0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e', // Uniswap Router
        methodName: 'exactInputSingle',
        methodParams: ['{{swap_params.data}}']
      }
    ],
    batchExecution: true // Flag to enable batching
  }
}
```

## Complete Example: WETH → USDC Swap

### Input Configuration

```javascript
{
  nodeType: 'contractWrite',
  nodeConfig: {
    methodCalls: [
      // Step 1: Approve WETH to Uniswap Router
      {
        contractAddress: '0xfff9976782d46cc05630d1f6ebab18b2324d6b14', // WETH on Sepolia
        contractAbi: '[16 ABI items - 11 functions, 4 events]',
        methodName: 'approve',
        methodParams: [
          '0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e', // Uniswap V3 Router
          '{{amount_to_approve}}'
        ]
      },
      // Step 2: Execute the swap
      {
        contractAddress: '0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e', // Uniswap V3 Router
        contractAbi: '[41 ABI items - 39 functions, 0 events]',
        methodName: 'exactInputSingle',
        methodParams: ['{{swap_params.data}}']
      }
    ],
    batchExecution: true,
    gasLimit: '400000' // Higher gas limit for batch operation
  },
  inputVariables: {
    amount_to_approve: {
      data: '80000000000000000', // 0.08 WETH
      input: {
        lang: 0,
        source: 'return get_weth_balance.data.balanceOf;'
      }
    },
    swap_params: {
      data: {
        tokenIn: '0xfff9976782d46cc05630d1f6ebab18b2324d6b14', // WETH
        tokenOut: '0x1c7d4b196cb0c7b01d743fbc6116a902379c7238', // USDC
        fee: 3000,
        recipient: '{{workflowContext.runner}}',
        amountIn: '80000000000000000',
        amountOutMinimum: '{{get_quote.data.quoteExactInputSingle.amountOut}}',
        sqrtPriceLimitX96: 0
      },
      input: {
        lang: 0,
        source: `
          const input = get_quote_params.data;
          const deadline = Math.floor(Date.now() / 1000) + 15 * 60; // 15 minutes
          
          return {
            ...input,
            recipient: workflowContext.runner,
            deadline: deadline,
            amountOutMinimum: get_quote.data.quoteExactInputSingle.amountOut,
          };
        `
      }
    },
    // Supporting variables
    workflowContext: {
      runner: '0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e',
      eoaAddress: '0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788'
    },
    get_weth_balance: {
      data: { balanceOf: '80000000000000000' }
    },
    get_quote: {
      data: {
        quoteExactInputSingle: {
          amountOut: '475028875',
          gasEstimate: '123437',
          initializedTicksCrossed: '1',
          sqrtPriceX96After: '1027941430642762031665015975374767'
        }
      }
    }
  }
}
```

### Expected Output

```javascript
{
  success: true,
  data: {
    // Results from approve call
    approve: {
      owner: '0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e',
      spender: '0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E',
      value: '80000000000000000'
    },
    // Results from exactInputSingle call
    exactInputSingle: {
      amountOut: '475028875',
      gasEstimate: '123437',
      initializedTicksCrossed: '1',
      sqrtPriceX96After: '1027941430642762031665015975374767'
    }
  },
  metadata: [
    {
      methodName: 'approve',
      success: true,
      contractAddress: '0xfff9976782d46cc05630d1f6ebab18b2324d6b14'
    },
    {
      methodName: 'exactInputSingle', 
      success: true,
      contractAddress: '0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e'
    }
  ]
}
```

## Implementation Plan

### Phase 1: Core Infrastructure ✅ 

- [x] **`PackExecuteBatch` Function**: Added to `core/chainio/aa/aa.go`
- [x] **Smart Wallet Integration**: Leverages existing `executeBatch` function
- [x] **ABI Support**: Uses existing SimpleAccount ABI bindings

### Phase 2: Contract Write Processor Extension

- [ ] **Multi-Contract Detection**: Detect when `methodCalls` contain different `contractAddress` values
- [ ] **Batch Mode Toggle**: Support `batchExecution: true` flag
- [ ] **Calldata Generation**: Generate individual calldata for each contract call
- [ ] **Batch Assembly**: Use `PackExecuteBatch` to combine all calls
- [ ] **Result Processing**: Handle results from multiple contracts

### Phase 3: Testing & Validation

- [ ] **Unit Tests**: Test `PackExecuteBatch` function
- [ ] **Integration Tests**: Test full approve + swap flow
- [ ] **Gas Optimization**: Benchmark vs sequential transactions
- [ ] **Error Handling**: Ensure proper failure modes

### Phase 4: Documentation & Examples

- [x] **Feature Documentation**: This document
- [ ] **API Reference**: Update contract write node documentation  
- [ ] **Example Workflows**: Common DeFi patterns
- [ ] **Migration Guide**: Converting sequential to batch operations

## Benefits

### For Users
- **Atomic Operations**: Complex workflows execute as single transactions
- **Lower Gas Costs**: Batch operations are more gas efficient
- **Better UX**: No waiting between dependent transactions
- **Reduced Failure Points**: Either all operations succeed or all fail

### For Developers  
- **Simplified Logic**: No need to manage state between transactions
- **Better Testing**: Atomic operations are easier to test
- **Cleaner Code**: Single configuration instead of multiple nodes

## Use Cases

### DeFi Operations
- **Token Swaps**: Approve + swap in one transaction
- **Liquidity Provision**: Approve multiple tokens + add liquidity
- **Yield Farming**: Approve + stake + claim rewards

### NFT Operations
- **Batch Transfers**: Transfer multiple NFTs atomically
- **Marketplace Operations**: Approve + list + purchase

### DAO Operations
- **Multi-Sig Execution**: Execute multiple proposals atomically
- **Treasury Management**: Multiple token transfers in one transaction

## Migration Path

### Current Sequential Pattern
```javascript
// Node 1: Approve
{ nodeType: 'contractWrite', nodeConfig: { contractAddress: 'WETH', methodCalls: [{ methodName: 'approve' }] } }

// Node 2: Swap  
{ nodeType: 'contractWrite', nodeConfig: { contractAddress: 'Router', methodCalls: [{ methodName: 'exactInputSingle' }] } }
```

### New Batched Pattern
```javascript  
// Single Node: Approve + Swap
{ 
  nodeType: 'contractWrite', 
  nodeConfig: { 
    methodCalls: [
      { contractAddress: 'WETH', methodName: 'approve' },
      { contractAddress: 'Router', methodName: 'exactInputSingle' }
    ],
    batchExecution: true 
  } 
}
```

## Technical Considerations

### Gas Limits
- Batch operations require higher gas limits
- Each additional call adds ~21,000 gas overhead
- Complex operations may hit block gas limits

### Error Handling
- If any operation fails, the entire batch reverts
- Detailed error reporting for individual operations
- Graceful fallback to sequential execution

### ABI Management
- Each method call must specify its contract ABI
- ABI validation for each contract address
- Efficient ABI caching for repeated operations

### Security
- All operations execute with smart wallet permissions
- No privilege escalation between operations
- Same security model as individual transactions

## Future Enhancements

### Conditional Execution
- Execute operations based on previous results
- Skip operations if conditions aren't met

### Cross-Chain Batching
- Batch operations across multiple chains
- Atomic cross-chain swaps and transfers

### Advanced Patterns
- Loops and iterations within batch operations
- Dynamic operation generation based on runtime data

---

**Status**: 🚧 **In Development**  
**Version**: 1.0.0-alpha  
**Last Updated**: August 15, 2025
