# Balance Node - Interface Specification & Implementation Guide

## Overview

This document defines the **canonical interface** for the Balance Node in the EigenLayer AVS system. The Balance Node provides a clean, user-friendly interface for retrieving wallet token balances across different blockchain networks.

### Purpose

The Balance Node abstracts away provider-specific implementation details (Moralis API) and returns simplified, standardized balance data. This eliminates the need for workflows to:
- Manage third-party API keys
- Parse verbose provider responses
- Handle spam token filtering
- Deal with inconsistent native token representations

### Key Features

- ✅ **Simplified Response Format** - Only essential fields, no logos/thumbnails/percentages
- ✅ **Automatic Spam Filtering** - Unverified tokens excluded by default
- ✅ **Native Token Handling** - ETH, BNB, etc. identified by absence of `tokenAddress` field
- ✅ **Multi-Chain Support** - Ethereum, Base, BSC, Sepolia, Base Sepolia, and more
- ✅ **Flexible Filtering** - By USD value, zero balances, specific token addresses
- ✅ **Centralized API Management** - Aggregator handles Moralis credentials

---

## Input Interface

### Parameters

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `address` | `string` | ✅ Yes | Wallet address (must be valid hex address) |
| `chain` | `string/number` | ✅ Yes | Chain identifier (e.g., "ethereum", "sepolia", 1, 11155111) |
| `tokenAddresses` | `string[]` | ❌ No | Array of **ERC20 contract addresses** to filter results |
| `includeSpam` | `boolean` | ❌ No | Include tokens marked as spam (default: `false`) |
| `includeZeroBalances` | `boolean` | ❌ No | Include tokens with zero balance (default: `false`) |
| `minUsdValue` | `number` | ❌ No | Filter out tokens below this USD value in dollars (default: `0`) |

### Native Token Handling in Requests

**⚠️ CRITICAL: Clients must NOT send native token placeholder addresses**

```typescript
// ❌ WRONG - Do NOT do this
{
  tokenAddresses: [
    "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",  // Invalid placeholder
    "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"   // USDC
  ]
}

// ✅ CORRECT - Only send valid ERC20 contract addresses
{
  tokenAddresses: [
    "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",  // USDC
    "0xdAC17F958D2ee523a2206206994597C13D831ec7"   // USDT
  ]
}

// ✅ CORRECT - Omit tokenAddresses to get all tokens (including native)
{
  address: "0x...",
  chain: "ethereum"
  // Native ETH + all ERC20 tokens will be returned
}

// ✅ CORRECT - Empty array to get all tokens (including native)
{
  tokenAddresses: []
  // Native ETH + all ERC20 tokens will be returned
}
```

### How the Aggregator Handles `tokenAddresses`

The aggregator backend (`vm_runner_balance.go`) performs the following filtering and normalization:

1. **Validates each address** using `common.IsHexAddress()`
2. **Filters out the ETH placeholder** `0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee`
3. **Removes any invalid addresses** (malformed hex)
4. **Converts to checksummed format** (EIP-55) - required by Moralis API
5. **Passes only valid, checksummed ERC20 addresses** to Moralis API
6. **Native tokens are ALWAYS included** regardless of the filter (Moralis default behavior)

```go
// Backend implementation (vm_runner_balance.go)
for _, addr := range config.TokenAddresses {
    // Skip ETH placeholder and invalid addresses
    if addr != "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" && common.IsHexAddress(addr) {
        // Convert to checksummed address (Moralis requires EIP-55 format)
        checksummedAddr := common.HexToAddress(addr).Hex()
        validAddresses = append(validAddresses, checksummedAddr)
    }
}
```

**Important**: The aggregator automatically converts addresses to checksummed format (mixed case), so clients can send lowercase addresses - they will be normalized automatically.

---

## Output Interface

### Response Format

The Balance Node returns a **direct array** of token objects (not wrapped in an object):

```json
[
  {
    "symbol": "ETH",
    "name": "Ether",
    "balance": "1234567890000000000",
    "balanceFormatted": "1.23456789",
    "decimals": 18,
    "usdPrice": 4528.12,
    "usdValue": 5591.23
  },
  {
    "symbol": "USDC",
    "name": "USD Coin",
    "balance": "1000000000",
    "balanceFormatted": "1000",
    "decimals": 6,
    "tokenAddress": "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
    "usdPrice": 1.0,
    "usdValue": 1000.0
  }
]
```

### Native Token vs ERC20 Token

**The ONLY way to identify a native token is by the absence of the `tokenAddress` field.**

**Technical Note**: Internally, Moralis returns native tokens with a placeholder address (`0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee`). The aggregator automatically removes this field during response processing to provide a cleaner API for clients.

```typescript
// TypeScript example
interface TokenBalance {
  symbol: string;
  name: string;
  balance: string;
  balanceFormatted: string;
  decimals: number;
  tokenAddress?: string;  // ⚠️ ONLY present for ERC20 tokens
  usdPrice?: number;      // Optional - omitted if unavailable
  usdValue?: number;      // Optional - omitted if unavailable
}

// Identifying token type
function isNativeToken(token: TokenBalance): boolean {
  return token.tokenAddress === undefined;
}

// Usage
const tokens: TokenBalance[] = await balanceNode.execute();
const nativeTokens = tokens.filter(isNativeToken);
const erc20Tokens = tokens.filter(t => !isNativeToken(t));
```

### Field Presence Rules

| Field | Always Present | Notes |
|-------|---------------|-------|
| `symbol` | ✅ Yes | Token symbol (e.g., "ETH", "USDC") |
| `name` | ✅ Yes | Full token name |
| `balance` | ✅ Yes | Raw balance as string |
| `balanceFormatted` | ✅ Yes | Human-readable balance |
| `decimals` | ✅ Yes | Token decimals |
| `tokenAddress` | ❌ **No** | **Only for ERC20 tokens** |
| `usdPrice` | ❌ No | **Only if price data available** |
| `usdValue` | ❌ No | **Only if price data available** |

### Important Guarantees

1. **Native tokens NEVER have `tokenAddress` field**
   - Not set to `null`
   - Not set to empty string `""`
   - Not set to placeholder `0xeeee...`
   - **The field is simply omitted**

2. **ERC20 tokens ALWAYS have `tokenAddress` field**
   - Always a valid 40-character hex address
   - Always checksummed format
   - Never a placeholder value

3. **USD fields are omitted when unavailable**
   - Not set to `null`
   - Not set to `0`
   - **The fields are simply omitted**

---

## Error Handling

### Client-side Validation (SDK)

The SDK should validate inputs **before** sending to aggregator:

```typescript
// Example SDK validation
function validateBalanceNodeConfig(config: BalanceNodeConfig): void {
  if (!config.address || !common.isHexAddress(config.address)) {
    throw new Error("Invalid wallet address");
  }
  
  if (config.tokenAddresses) {
    for (const addr of config.tokenAddresses) {
      if (addr === "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee") {
        throw new Error(
          "Do not use 0xeeee... placeholder. " +
          "Native tokens are included automatically."
        );
      }
      if (!common.isHexAddress(addr)) {
        throw new Error(`Invalid token address: ${addr}`);
      }
    }
  }
}
```

### Server-side Validation (Aggregator)

The aggregator validates and sanitizes inputs:

1. **Address validation**: Rejects invalid hex addresses
2. **Chain validation**: Rejects unsupported chains
3. **Token address filtering**: Silently filters out invalid addresses
4. **MinUsdValue validation**: Rejects negative values

```go
// Aggregator validation (vm_runner_balance.go)

// 1. Validate wallet address
if !common.IsHexAddress(address) {
    return nil, fmt.Errorf("invalid wallet address: %s", address)
}

// 2. Validate chain
moralisChain, err := normalizeChainID(chain)
if err != nil {
    return nil, err  // "unsupported chain: xxx"
}

// 3. Validate minUsdValue
if config.MinUsdValueCents < 0 {
    return nil, fmt.Errorf("minUsdValue cannot be negative")
}

// 4. Filter invalid token addresses (silent)
validAddresses := []string{}
for _, addr := range config.TokenAddresses {
    if addr != "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" && 
       common.IsHexAddress(addr) {
        validAddresses = append(validAddresses, addr)
    }
}
```

---

## Examples

### Example 1: Get All Tokens (including native ETH)

```typescript
const result = await client.runNodeWithInputs({
  nodeType: NodeType.Balance,
  nodeConfig: {
    address: "0x1234...",
    chain: "ethereum"
  }
});
// Returns: [ETH, USDC, USDT, DAI, ...] up to 100 tokens
```

### Example 2: Get Only Specific ERC20 Tokens

```typescript
const result = await client.runNodeWithInputs({
  nodeType: NodeType.Balance,
  nodeConfig: {
    address: "0x1234...",
    chain: "ethereum",
    tokenAddresses: [
      "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",  // USDC
      "0xdAC17F958D2ee523a2206206994597C13D831ec7"   // USDT
    ]
  }
});
// Returns: [ETH (always), USDC, USDT]
// Note: Native ETH is still included
```

### Example 3: Filter by Minimum USD Value

```typescript
const result = await client.runNodeWithInputs({
  nodeType: NodeType.Balance,
  nodeConfig: {
    address: "0x1234...",
    chain: "ethereum",
    minUsdValue: 10.0  // Only tokens worth >= $10
  }
});
// Returns: Only tokens with usdValue >= 10.0
```

### Example 4: Identify Native vs ERC20 in Response

```typescript
const tokens = result.data;

for (const token of tokens) {
  if (!token.tokenAddress) {
    console.log(`Native token: ${token.symbol} = ${token.balanceFormatted}`);
  } else {
    console.log(`ERC20 token: ${token.symbol} at ${token.tokenAddress}`);
  }
}

// Output:
// Native token: ETH = 1.234
// ERC20 token: USDC at 0xA0b8699...
// ERC20 token: USDT at 0xdAC17F9...
```

---

## Migration Guide

### If You're Currently Using Rest API Node

**Before (using Rest API node):**
```typescript
{
  nodeType: NodeType.RestAPI,
  nodeConfig: {
    url: "https://deep-index.moralis.io/api/v2.2/{address}/erc20",
    method: "GET",
    headers: {
      "X-API-Key": "{{secrets.moralis_api_key}}"
    }
  }
}
```

**After (using Balance Node):**
```typescript
{
  nodeType: NodeType.Balance,
  nodeConfig: {
    address: "0x1234...",
    chain: "ethereum"
  }
}
```

**Benefits:**
- ✅ No need to manage API keys in workflow
- ✅ Clean, simplified response format
- ✅ Automatic spam filtering
- ✅ Native token handling built-in
- ✅ Type-safe with SDK

---

## Implementation Details

### Supported Chains

The Balance Node currently supports the following chains (via `chainIDMap` in `vm_runner_balance.go`):

| Chain | Identifiers | Moralis Chain ID |
|-------|------------|------------------|
| **Ethereum Mainnet** | `"ethereum"`, `"eth"`, `"1"`, `"0x1"` | `"eth"` |
| **Sepolia Testnet** | `"sepolia"`, `"11155111"`, `"0xaa36a7"` | `"sepolia"` |
| **Base Mainnet** | `"base"`, `"8453"`, `"0x2105"` | `"base"` |
| **Base Sepolia** | `"base-sepolia"`, `"base sepolia"`, `"basesepolia"`, `"84532"`, `"0x14a34"` | `"base-sepolia"` |
| **BSC Mainnet** | `"bsc"`, `"binance"`, `"56"`, `"0x38"` | `"bsc"` |

Additional chains can be added by extending the `chainIDMap`.

### Backend Implementation

**Key Files:**
- `core/taskengine/vm_runner_balance.go` - Balance node execution logic
- `core/taskengine/vm_runner_balance_test.go` - Unit tests
- `core/taskengine/node_types.go` - Node type registration
- `protobuf/avs.proto` - Protobuf definitions

**Configuration:**
- Moralis API key stored in `config/aggregator.yaml` under `macros.secrets.moralis_api_key`
- HTTP timeout: 30 seconds
- Rate limiting: Handled by Moralis API (25 req/s free tier)

### SDK Implementation

**Key Files:**
- `packages/types/src/enums.ts` - `NodeType.Balance` enum
- `packages/types/src/node.ts` - `BalanceNodeData` interface
- `packages/sdk-js/src/models/node/balance.ts` - `BalanceNode` class
- `tests/nodes/balanceNode.test.ts` - SDK tests

**TypeScript Types:**
```typescript
interface BalanceNodeData {
  address: string;
  chain: string;
  includeSpam?: boolean;
  includeZeroBalances?: boolean;
  minUsdValue?: number;  // Dollars (converted to cents in backend)
  tokenAddresses?: string[];
}
```

### Moralis API Details

**Endpoint:**
```
GET https://deep-index.moralis.io/api/v2.2/{address}/erc20
```

**Query Parameters:**
- `chain` - Chain identifier (e.g., "eth", "base", "sepolia")
- `exclude_spam` - Boolean (default: true)
- `token_addresses` - Comma-separated list of ERC20 addresses (optional)

**Response Format:**
- Returns array of token balances (or wrapper object with `result` array in older API versions)
- Native tokens have `native_token: true` field
- Spam tokens have `possible_spam: true` field

**Rate Limits:**
- Free tier: 25 requests/second
- Pro tier: Higher limits based on plan

### Fields Removed from Moralis Response

The Balance Node filters out these Moralis fields to provide a cleaner interface:
- `logo`, `thumbnail` - Visual assets not needed for automation
- `possible_spam` - Already filtered (unless `includeSpam=true`)
- `portfolio_percentage` - Can be calculated client-side if needed
- `percentage_relative_to_total_supply` - Rarely useful
- `security_score` - Spam filtering provides adequate security
- `usd_price_24hr_percent_change` - Historical data (out of scope)
- `usd_price_24hr_usd_change` - Historical data (out of scope)
- `usd_value_24hr_usd_change` - Historical data (out of scope)
- `total_supply`, `total_supply_formatted` - Rarely needed
- `verified_contract` - All returned tokens are verified
- `block_number`, `cursor`, `page`, `page_size` - Pagination (max 100 items)
- `native_token` - Replaced by absence of `tokenAddress` field

---

## Testing

### Backend Tests

Run balance node tests:
```bash
cd /Users/mikasa/Code/EigenLayer-AVS
go test ./core/taskengine -run TestBalanceNode -v
```

**Test Coverage:**
- Basic balance fetching
- Token address filtering
- Invalid address filtering (ETH placeholder)
- Spam filtering
- Zero balance filtering
- MinUsdValue filtering
- Chain normalization
- Error handling (invalid address, missing chain, negative minUsdValue)
- Balance formatting (when Moralis returns empty `balanceFormatted`)

### SDK Tests

Run SDK tests:
```bash
cd /Users/mikasa/Code/ava-sdk-js
yarn build
yarn test tests/nodes/balanceNode.test.ts
```

**Test Coverage:**
- Basic balance retrieval
- Template variable support
- USD value filtering
- Chain identifier variations (name, decimal ID, hex ID)
- Token address filtering
- Response consistency (runNodeWithInputs, simulateTask, deployed workflow)
- Error handling (invalid address, unsupported chain)
- Protobuf serialization/deserialization

---

## Security Considerations

### API Key Management
- Moralis API key stored in `config/aggregator.yaml` (never exposed to client)
- Supports environment variable override: `${MORALIS_API_KEY}`
- API key never logged or returned in responses

### Input Validation
- **Wallet Address**: Must be valid hex address (validated with `common.IsHexAddress()`)
- **Chain**: Must be in supported chain list (validated with `normalizeChainID()`)
- **Token Addresses**: Invalid addresses silently filtered (including ETH placeholder)
- **MinUsdValue**: Must be non-negative (validated on backend)

### Rate Limiting
- Moralis API enforces rate limits (25 req/s free tier)
- No internal caching in v1 (always returns fresh data)
- Failed requests return clear error messages

### Data Privacy
- Balance queries are read-only (no private keys involved)
- Wallet addresses are public on-chain data (minimal PII logging)

---

## Troubleshooting

### Common Errors

**Error: "invalid wallet address"**
- **Cause**: Address is not a valid hex format
- **Solution**: Ensure address starts with `0x` and is 42 characters (40 hex + `0x`)

**Error: "unsupported chain: xxx"**
- **Cause**: Chain identifier not recognized
- **Solution**: Use supported chain names ("ethereum", "sepolia", "base", etc.) or chain IDs (1, 11155111, 8453, etc.)

**Error: "moralis API returned status 400: token_addresses with value '...' is not a valid hex address"**
- **Cause**: Client sent invalid token address (e.g., `0xeeee...` placeholder)
- **Solution**: Remove invalid addresses from `tokenAddresses` array. Native tokens are included automatically.

**Error: "moralis API key is not configured"**
- **Cause**: Backend missing Moralis API key in config
- **Solution**: Add `moralis_api_key` to `macros.secrets` in `config/aggregator.yaml`

**Error: "minUsdValue cannot be negative"**
- **Cause**: Negative value passed for `minUsdValue`
- **Solution**: Use 0 or positive values only

### Empty Results

**Why am I getting an empty array?**
1. **Wallet has no tokens** - Verify address on block explorer
2. **All tokens below `minUsdValue`** - Lower or remove the filter
3. **All tokens filtered by `tokenAddresses`** - Check your token address list
4. **Zero balances excluded** - Set `includeZeroBalances: true`
5. **Testnet tokens missing USD prices** - USD filtering may exclude testnet tokens

### Performance Issues

**Slow response times (>5 seconds)?**
1. **Moralis API throttling** - Check API rate limits
2. **Large number of tokens** - Use `tokenAddresses` to filter specific tokens
3. **Network latency** - Verify aggregator network connectivity

---

## Future Enhancements

### Planned for v2
1. **Historical Balances** - Query balances at specific block numbers
2. **NFT Support** - Include NFT balances alongside ERC20 tokens
3. **Multi-Wallet** - Check balances across multiple addresses in one call
4. **Caching** - Short-lived cache (60s TTL) to reduce API calls
5. **Additional Providers** - Alchemy, The Graph, direct RPC fallback

### Under Consideration
1. **Price Oracles** - Fetch prices from multiple sources when Moralis unavailable
2. **Balance Change Tracking** - Compare current vs previous balance
3. **Custom Token Lists** - User-defined whitelist/blacklist
4. **Batch Queries** - Optimize multiple balance checks

---

## Summary

### DO ✅
- Use `tokenAddresses` to filter for **specific ERC20 contracts**
- Omit `tokenAddresses` or use `[]` to get **all tokens**
- Check for **absence of `tokenAddress` field** to identify native tokens
- Expect `usdPrice` and `usdValue` to be **omitted** when unavailable
- Use supported chain identifiers (name, decimal ID, or hex ID)
- Set `minUsdValue` in dollars (e.g., 1.0 = $1.00)

### DON'T ❌
- Include `0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee` in `tokenAddresses`
- Assume native tokens have a `tokenAddress` field
- Check for `tokenAddress === null` (field is omitted, not null)
- Expect `usdPrice` or `usdValue` to always be present
- Use unsupported chain identifiers
- Set `minUsdValue` in cents (backend handles conversion)

---

## References

- **PRD**: `BALANCE_NODE_PRD.md` (deprecated, superseded by this document)
- **Backend Implementation**: `core/taskengine/vm_runner_balance.go`
- **SDK Implementation**: `packages/sdk-js/src/models/node/balance.ts`
- **Tests**: `core/taskengine/vm_runner_balance_test.go`, `tests/nodes/balanceNode.test.ts`
- **Moralis API Docs**: https://docs.moralis.io/web3-data-api/evm/reference/wallet-api

---

**Document Version**: 2.0  
**Created**: 2025-10-06  
**Last Updated**: 2025-10-06  
**Status**: Canonical Interface Specification & Implementation Guide  
**Supersedes**: BALANCE_NODE_PRD.md

