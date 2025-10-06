# Balance Node - Product Requirements Document

## Overview

This document outlines the requirements for implementing a dedicated Balance Node in the EigenLayer AVS system. The Balance Node will provide a clean, user-friendly interface for retrieving wallet token balances across different blockchain networks.

## Background

### Current State
Users currently retrieve wallet balances using the Rest API node to call third-party services like Moralis. While functional, this approach has several drawbacks:
- Returns verbose responses with many unnecessary fields (logos, thumbnails, percentage changes)
- Requires users to understand provider-specific response formats
- Not reusable across workflows without duplicating configuration
- Tightly couples workflows to specific API providers

### Problem Statement
The current approach is not user-friendly for a common operation like checking wallet balances. Users need a simplified, standardized way to retrieve balance information without dealing with provider-specific implementation details.

## Goals & Objectives

### Primary Goals
1. Provide a dedicated node type specifically for wallet balance retrieval
2. Return clean, simplified responses containing only essential balance information
3. Abstract away provider-specific implementation details
4. Enable easy workflow composition with balance checks

### Secondary Goals
1. Support multiple blockchain networks
2. Allow filtering of spam/unverified tokens
3. Enable future support for multiple balance providers (Moralis, Alchemy, custom RPC)
4. Maintain consistency with other specialized nodes (ContractRead, ContractWrite)

### Non-Goals (for v1)
1. Historical balance data
2. Portfolio analytics or percentage calculations
3. Token price charts or trends
4. NFT balance retrieval (tokens only)
5. Multi-wallet balance aggregation

## User Stories

1. As a workflow creator, I want to check a wallet's token balances so I can trigger actions based on balance thresholds
2. As a workflow creator, I want spam tokens automatically filtered so I only see legitimate tokens
3. As a workflow creator, I want balance data in a simple format so I can easily use it in subsequent nodes
4. As a system administrator, I want to configure API credentials centrally so workflows don't need individual configuration

## Technical Requirements

### Node Type
- **Node Name**: `BalanceNode`
- **Category**: Data/Blockchain
- **Execution Type**: Synchronous

### Input Parameters

| Parameter | Type | Required | Description | Default |
|-----------|------|----------|-------------|---------|
| `address` | string | Yes | Wallet address to check balances for | - |
| `chain` | string/number | Yes | Chain name or ID (e.g., "ethereum", "base", 1, 8453) | - |
| `includeSpam` | boolean | No | Whether to include tokens marked as spam | false |
| `includeZeroBalances` | boolean | No | Whether to include tokens with zero balance | false |
| `minUsdValue` | number | No | Filter tokens below this USD value | 0 |

### Output Format

The Balance Node should return a simplified, standardized response:

```json
{
  "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "chain": "ethereum",
  "chainId": 1,
  "blockNumber": 23515585,
  "timestamp": "2025-10-06T12:34:56Z",
  "tokens": [
    {
      "symbol": "ETH",
      "name": "Ether",
      "balance": "1234567890000000000",
      "balanceFormatted": "1.23456789",
      "decimals": 18,
      "tokenAddress": "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
      "isNative": true,
      "usdPrice": 4528.12,
      "usdValue": 5591.23,
      "verified": true
    },
    {
      "symbol": "USDC",
      "name": "USD Coin",
      "balance": "1000000000",
      "balanceFormatted": "1000",
      "decimals": 6,
      "tokenAddress": "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
      "isNative": false,
      "usdPrice": 1.0,
      "usdValue": 1000.0,
      "verified": true
    }
  ],
  "totalUsdValue": 6591.23,
  "tokenCount": 2
}
```

### Response Fields Description

#### Root Level
- `address`: The wallet address queried
- `chain`: Human-readable chain name
- `chainId`: Numeric chain ID
- `blockNumber`: Block number at time of query
- `timestamp`: ISO 8601 timestamp of query
- `tokens`: Array of token balance objects
- `totalUsdValue`: Sum of all token USD values
- `tokenCount`: Number of tokens returned

#### Token Object
- `symbol`: Token symbol (e.g., "ETH", "USDC")
- `name`: Full token name
- `balance`: Raw balance as string (to handle large numbers)
- `balanceFormatted`: Human-readable balance with decimals applied
- `decimals`: Token decimal places
- `tokenAddress`: Token contract address (or special address for native tokens)
- `isNative`: Boolean indicating if this is the chain's native token
- `usdPrice`: Current USD price per token (null if unavailable)
- `usdValue`: Total USD value of this balance (null if price unavailable)
- `verified`: Whether the token contract is verified

### Fields Removed from Moralis Response
The following fields from the Moralis API response will NOT be included:
- `logo`, `thumbnail` - Not needed for automation
- `possibleSpam` - Filtered out by default (unless `includeSpam=true`)
- `portfolioPercentage` - Can be calculated if needed
- `percentageRelativeToTotalSupply` - Rarely useful
- `securityScore` - Redundant with verified flag
- `usdPrice_24hrPercentChange` - Historical data (out of scope for v1)
- `usdPrice_24hrUsdChange` - Historical data (out of scope for v1)
- `usdValue_24hrUsdChange` - Historical data (out of scope for v1)
- `totalSupply`, `totalSupplyFormatted` - Rarely needed
- `verifiedContract` - Simplified to `verified` boolean

## Implementation Plan

### Phase 1: Backend Implementation

#### 1.1 Node Definition
- Add `BalanceNode` to node type constants
- Define node structure in protobuf
- Add validation for input parameters

**Files to modify:**
- `core/taskengine/types.go` - Add node type constant
- `protobuf/avs.proto` - Add BalanceNode message
- `core/taskengine/validation_constants.go` - Add validation rules

#### 1.2 Node Executor
- Implement balance retrieval logic
- Call Moralis API (or other provider)
- Transform response to standardized format
- Apply filters (spam, zero balances, min USD value)

**New file:**
- `core/taskengine/vm_runner_balance.go` - Balance node execution logic

#### 1.3 Configuration
- Add Moralis API key configuration to aggregator.yaml
- Support multiple chain configurations

**Files to modify:**
- `core/config/config.go` - Add balance provider config

**Example config:**
```yaml
balance_provider:
  type: moralis
  api_key: ${MORALIS_API_KEY}
  timeout: 30s
  supported_chains:
    - ethereum
    - base
    - polygon
    - arbitrum
```

#### 1.4 Error Handling
- Handle API rate limits
- Handle invalid addresses
- Handle unsupported chains
- Return meaningful error messages

#### 1.5 Testing
- Unit tests for balance node execution
- Integration tests with mock Moralis responses
- Test all filtering options
- Test error scenarios

**New file:**
- `core/taskengine/vm_runner_balance_test.go`

### Phase 2: SDK Implementation (ava-sdk-js)

#### 2.1 Type Definitions
- Add Balance Node types
- Add input/output interfaces

**Files to modify:**
- `packages/types/src/nodes.ts` - Add BalanceNode types
- `packages/types/src/responses.ts` - Add response types

#### 2.2 SDK Methods
- Add `createBalanceNode()` method
- Add validation for inputs

**Files to modify:**
- `packages/sdk-js/src/workflow.ts` - Add balance node creation method

#### 2.3 Testing
- Add SDK tests for balance node creation
- Test with deployed workflows

**New file:**
- `tests/nodes/balanceNode.test.ts`

### Phase 3: Client App Implementation

#### 3.1 UI Components
- Add Balance Node to node palette
- Create Balance Node configuration form
- Add chain selector dropdown
- Add filter options UI

#### 3.2 Node Editor
- Display balance results in execution logs
- Show formatted token list in UI
- Add error state handling

## Success Criteria

### Functional Requirements
- ✅ Balance Node can successfully retrieve balances for any valid wallet address
- ✅ Response contains only essential fields (no logos, thumbnails, percentages)
- ✅ Spam tokens are filtered by default
- ✅ Multiple chains are supported
- ✅ Zero balance tokens can be filtered
- ✅ USD values are included when available
- ✅ Response format is consistent and well-documented

### Non-Functional Requirements
- ✅ API response time < 5 seconds (95th percentile)
- ✅ Graceful error handling for API failures
- ✅ Clear error messages for invalid inputs
- ✅ Unit test coverage > 80%
- ✅ Integration tests pass consistently

### User Experience
- ✅ Users can create balance nodes without understanding Moralis API
- ✅ Configuration is simple and intuitive
- ✅ Response data is easy to use in subsequent workflow nodes
- ✅ Error messages are actionable

## API Provider Details

### Moralis API (v1 Implementation)

**Endpoint:**
```
GET https://deep-index.moralis.io/api/v2.2/{address}/erc20
```

**Required Headers:**
- `X-API-Key`: Moralis API key from config

**Query Parameters:**
- `chain`: Chain name or hex chain ID
- `exclude_spam`: true/false

**Supported Chains:**
- Ethereum (`eth`, `0x1`)
- Base (`base`, `0x2105`)
- Polygon (`polygon`, `0x89`)
- Arbitrum (`arbitrum`, `0xa4b1`)
- BSC (`bsc`, `0x38`)
- Optimism (`optimism`, `0xa`)
- And more...

**Rate Limits:**
- Free tier: 25 requests/second
- Pro tier: Variable based on plan

**Error Codes:**
- 400: Invalid parameters
- 401: Invalid API key
- 403: API key limit exceeded
- 429: Rate limit exceeded

### Future Provider Support

The implementation should be designed to easily support additional providers:
- **Alchemy**: Similar balance API
- **Custom RPC**: Direct blockchain queries (slower but no API key needed)
- **The Graph**: Subgraph queries for specific tokens

## Security Considerations

1. **API Key Management**
   - Store Moralis API key in aggregator config
   - Never expose API key to client
   - Support environment variable override

2. **Input Validation**
   - Validate wallet addresses (checksum format)
   - Validate chain IDs against supported list
   - Sanitize all inputs to prevent injection

3. **Rate Limiting**
   - Implement internal rate limiting to prevent API abuse
   - Cache results when appropriate (short TTL)

4. **Data Privacy**
   - Balance queries are read-only (no private key needed)
   - Log minimal PII (wallet addresses are public)

## Migration Path

Since this is a new node type, no migration is needed. Existing workflows using Rest API nodes for Moralis can continue to work. Users can gradually migrate to the Balance Node for better UX.

## Future Enhancements (Post-v1)

1. **Historical Balances**
   - Query balances at specific block numbers
   - Balance change tracking

2. **NFT Support**
   - Retrieve NFT balances alongside tokens
   - Include NFT metadata

3. **Multi-Wallet**
   - Check balances across multiple addresses
   - Aggregate totals

4. **Price Alerts**
   - Built-in comparison operators
   - Trigger workflows based on balance changes

5. **Custom Token Lists**
   - Allow users to specify which tokens to check
   - Support for custom/unknown tokens

6. **Batch Queries**
   - Check multiple addresses in single node
   - Optimize for efficiency

## Open Questions

1. **Caching Strategy**: Should we cache balance results? For how long?
   - Recommendation: Short TTL cache (60 seconds) to reduce API calls

2. **Native Token Address**: What standard address should we use for native tokens?
   - Recommendation: `0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee` (common convention)

3. **Chain Support**: Which chains should be supported in v1?
   - Recommendation: Start with Ethereum, Base, Polygon, Arbitrum (most common)

4. **Failure Behavior**: Should the node fail or return partial results if API is down?
   - Recommendation: Fail fast with clear error message (consistent with other nodes)

5. **USD Price Source**: If Moralis doesn't return prices, should we fetch from elsewhere?
   - Recommendation: Return null for v1, add price oracle support in v2

## Documentation Requirements

1. **User Guide**: How to use Balance Node in workflows
2. **API Reference**: Input/output schema documentation  
3. **Examples**: Common use cases (balance threshold triggers, token gating)
4. **Troubleshooting**: Common errors and solutions
5. **Migration Guide**: How to migrate from Rest API node to Balance Node

## Timeline Estimate

- **Phase 1 (Backend)**: 1-2 weeks
  - Node implementation: 3-4 days
  - Testing: 2-3 days
  - Review & refinement: 2-3 days

- **Phase 2 (SDK)**: 3-5 days
  - Type definitions: 1 day
  - SDK methods: 1-2 days
  - Testing: 1-2 days

- **Phase 3 (Client App)**: 1-2 weeks
  - UI components: 3-4 days
  - Integration: 2-3 days
  - Testing & polish: 2-3 days

**Total Estimate**: 3-4 weeks for full implementation

## Approval & Sign-off

- [ ] Product Owner Review
- [ ] Technical Lead Review
- [ ] Security Review
- [ ] Documentation Complete
- [ ] Testing Complete
- [ ] Ready for Release

---

**Document Version**: 1.0  
**Created**: 2025-10-06  
**Last Updated**: 2025-10-06  
**Author**: AI Assistant  
**Status**: Draft

