# EventTrigger Chain-Specific Search Ranges

## Overview

The EventTrigger system has been updated to use **chain-specific search ranges** optimized for each blockchain's block time and RPC provider limitations. Instead of using generic small ranges that could miss events, we now use **3-month and 6-month historical searches** tailored to each chain.

## Benefits

1. **Reduced RPC Calls**: Only 2 search attempts instead of 5+ smaller ranges
2. **Comprehensive Coverage**: 3-6 months of historical data covers most use cases
3. **Chain-Aware**: Optimized for each blockchain's unique characteristics
4. **RPC Provider Friendly**: Respects block range limits with intelligent chunking

## Supported Chains and Block Ranges

| Chain | Chain ID | Block Time | Blocks/Day | 3 Months | 6 Months |
|-------|----------|------------|------------|----------|----------|
| **Ethereum Mainnet** | 1 | 12s | 7,200 | 648,000 | 1,296,000 |
| **Ethereum Sepolia** | 11155111 | 12s | 7,200 | 648,000 | 1,296,000 |
| **Base Mainnet** | 8453 | 2s | 43,200 | 3,888,000 | 7,776,000 |
| **Base Sepolia** | 84532 | 2s | 43,200 | 3,888,000 | 7,776,000 |
| **BNB Smart Chain** | 56 | 0.75s | 115,200 | 10,368,000 | 20,736,000 |
| **BNB Chain Testnet** | 97 | 0.75s | 115,200 | 10,368,000 | 20,736,000 |
| **Polygon Mainnet** | 137 | 2s | 43,200 | 3,888,000 | 7,776,000 |
| **Polygon Mumbai** | 80001 | 2s | 43,200 | 3,888,000 | 7,776,000 |
| **Avalanche C-Chain** | 43114 | 2s | 43,200 | 3,888,000 | 7,776,000 |
| **Avalanche Fuji** | 43113 | 2s | 43,200 | 3,888,000 | 7,776,000 |

## How It Works

### 1. Chain Detection
The system automatically detects the chain ID through the TokenEnrichmentService and selects appropriate search ranges.

### 2. Search Strategy
```go
// Get chain-specific ranges
chainID := tokenEnrichmentService.GetChainID()
searchRanges := GetChainSearchRanges(chainID) // Returns [3months, 6months]

// Search in order: 3 months first, then 6 months if needed
for _, searchRange := range searchRanges {
    // Search the last `searchRange` blocks
    // Stop if events are found
}
```

### 3. RPC Limit Handling
- **Chunked Search**: Large ranges are automatically broken into 1000-block chunks
- **Error Recovery**: RPC limit errors are caught and handled gracefully
- **Fallback Strategy**: If a range fails, continues to the next range

### 4. Block Time Calculations

#### BNB Chain (Maxwell Hardfork)
- **Block Time**: 0.75 seconds (after Maxwell hardfork upgrade)
- **Daily Blocks**: 86,400 ÷ 0.75 = 115,200 blocks
- **3 Months**: 115,200 × 90 = 10,368,000 blocks
- **6 Months**: 115,200 × 180 = 20,736,000 blocks

#### Ethereum
- **Block Time**: 12 seconds
- **Daily Blocks**: 86,400 ÷ 12 = 7,200 blocks
- **3 Months**: 7,200 × 90 = 648,000 blocks
- **6 Months**: 7,200 × 180 = 1,296,000 blocks

#### Base/Polygon
- **Block Time**: 2 seconds
- **Daily Blocks**: 86,400 ÷ 2 = 43,200 blocks
- **3 Months**: 43,200 × 90 = 3,888,000 blocks
- **6 Months**: 43,200 × 180 = 7,776,000 blocks

## Code Implementation

### Configuration
```go
// core/taskengine/blockchain_constants.go
type ChainBlockRanges struct {
    ThreeMonths uint64 // ~90 days worth of blocks
    SixMonths   uint64 // ~180 days worth of blocks
}

var BlockSearchRanges = map[uint64]ChainBlockRanges{
    1: {ThreeMonths: 648000, SixMonths: 1296000},     // Ethereum
    56: {ThreeMonths: 10368000, SixMonths: 20736000}, // BNB Chain
    8453: {ThreeMonths: 3888000, SixMonths: 7776000}, // Base
    // ... more chains
}
```

### Usage
```go
// Get search ranges for current chain
chainID := n.tokenEnrichmentService.GetChainID()
searchRanges := GetChainSearchRanges(chainID)

// Search with chain-specific ranges
for _, searchRange := range searchRanges {
    fromBlock := currentBlock - searchRange
    // ... perform search
}
```

## Migration from Old System

### Before
```go
// Fixed small ranges regardless of chain
searchRanges := []uint64{500, 1000, 2000, 5000, 10000}
```

### After
```go
// Chain-specific optimized ranges
searchRanges := GetChainSearchRanges(chainID) // [3months, 6months]
```

## Performance Impact

### Positive
- **Fewer RPC Calls**: 2 attempts vs 5+ attempts
- **Better Event Discovery**: Comprehensive historical coverage
- **Smart Chunking**: Handles large ranges efficiently

### Considerations
- **Larger Initial Ranges**: May take longer for first search on fast chains
- **RPC Usage**: Individual calls may request more blocks but total calls are reduced

## Testing

Run the test suite to verify chain-specific calculations:

```bash
# Test all blockchain constants
go test ./core/taskengine -run TestBlockSearchRanges -v

# Test block calculations
go test ./core/taskengine -run TestBlockCalculations -v

# Test EventTrigger functionality
go test ./core/taskengine -run TestEventTriggerDebugResponse -v
```

## Future Enhancements

1. **Dynamic Range Adjustment**: Adjust ranges based on RPC provider feedback
2. **Chain-Specific Timeouts**: Optimize timeouts per chain characteristics
3. **Caching Strategy**: Cache successful search ranges per chain
4. **Additional Chains**: Easy to add new chains by updating the constants map 