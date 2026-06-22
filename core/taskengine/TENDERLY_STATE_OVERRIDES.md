# Tenderly State Overrides for ERC20 Testing

## Overview

When simulating contract writes involving ERC20 tokens (like Uniswap swaps), you often need to override the blockchain state to:
1. Set token balances for test wallets
2. Set token allowances without requiring separate approval transactions

This document explains how to calculate and use Tenderly state overrides for ERC20 tokens.

## Status

State overrides are implemented and accumulated by `SimulationStateMap`
(`simulation_state.go`), applied to every Tenderly call via
`BuildStateObjects()` (`tenderly_client.go`). There are two ways state overrides
get populated:

1. **Automatic** — event-trigger / multi-step workflow simulation. A Transfer
   event replayed during simulation injects a balance delta
   (`InjectERC20BalanceChange`), and each step's `raw_state_diff` is carried
   forward (`MergeRawStateDiff`) so later steps see a consistent view.
2. **User-supplied** — the `erc20_overrides` field on `RunNodeWithInputsReq`
   (REST: `erc20Overrides` on `POST /api/v1/nodes:run`). This lets a caller
   testing an isolated `RunNodeImmediately` swap seed an arbitrary balance and
   allowance up front, with no preceding trigger. See
   [User-facing API](#user-facing-api-erc20_overrides) below.

## How ERC20 Storage Works

ERC20 tokens use Solidity mappings to store balances and allowances:

```solidity
mapping(address => uint256) public balanceOf;           // Usually at storage slot 0
mapping(address => mapping(address => uint256)) public allowance;  // Usually at slot 3 or 4
```

## Calculating Storage Slots

### 1. Token Balance Slot

For a single mapping like `balanceOf[owner]`:

```
storage_slot = keccak256(abi.encode(owner_address, mapping_slot))
```

Where:
- `owner_address` is the address whose balance you want to override (32 bytes, left-padded)
- `mapping_slot` is the storage slot where the mapping is declared (usually 0 for balanceOf)

### 2. Token Allowance Slot  

For a nested mapping like `allowance[owner][spender]`:

```
inner_hash = keccak256(abi.encode(owner_address, allowance_mapping_slot))
storage_slot = keccak256(abi.encode(spender_address, inner_hash))
```

Where:
- `owner_address` is the token owner (32 bytes, left-padded)
- `spender_address` is the approved spender (32 bytes, left-padded)
- `allowance_mapping_slot` is usually 3 or 4 (check the token contract)

## Go Implementation

The slot math lives in `simulation_state.go` as `erc20BalanceSlot` and
`erc20AllowanceSlot`:

```go
// keccak256(abi.encode(holder, mappingSlot))
func erc20BalanceSlot(holder common.Address, mappingSlot int64) common.Hash

// keccak256(abi.encode(spender, keccak256(abi.encode(owner, mappingSlot))))
func erc20AllowanceSlot(owner, spender common.Address, mappingSlot int64) common.Hash
```

To seed a balance and/or allowance directly, use the higher-level helper
`SimulationStateMap.ApplyUserERC20Override`, which parses hex/decimal values,
computes the slots (defaulting to balance slot 0 / allowance slot 3) and records
the storage override:

```go
err := vm.simulationState.ApplyUserERC20Override(
    tokenAddress, ownerAddress, spenderAddress,
    "0x38d7ea4c68000", // balance: 1,000,000 USDC
    "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", // allowance: max uint256
    balanceSlotPtr,   // *uint64, nil → default 0
    allowanceSlotPtr, // *uint64, nil → default 3
)
```

## Tenderly API Format

When calling the Tenderly simulation API, include state overrides in the `state_objects` field:

```json
{
  "network_id": "11155111",
  "from": "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
  "to": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
  "input": "0x...",
  "gas": 210000,
  "gas_price": "0",
  "value": "0",
  "state_objects": {
    "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e": {
      "balance": "10000000000000000000"
    },
    "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238": {
      "storage": {
        "0x<allowance_slot>": "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
        "0x<balance_slot>": "0x38d7ea4c68000"
      }
    }
  }
}
```

Where:
- Token balance: `0x38d7ea4c68000` = 1,000,000 USDC (6 decimals)
- Token allowance: `0xffff...ffff` = max uint256 (unlimited approval)

## Common Token Storage Slots

| Token Type | balanceOf Slot | allowance Slot |
|------------|----------------|----------------|
| Standard ERC20 | 0 | 3 |
| OpenZeppelin ERC20 | 0 | 1 |
| USDC (FiatToken) | 9 | 10 |

**Note:** Always verify the actual storage layout by checking the token's contract source code or using tools like `cast storage` from Foundry.

## User-facing API: `erc20_overrides`

`RunNodeImmediately` accepts optional ERC20 overrides so an isolated
contract-write simulation can seed balances/approvals up front. The overrides
are **simulation-only** — `RunNodeImmediately` rejects them in real-execution
mode, and they never apply to deployed workflows.

### Request shape

Protobuf (`RunNodeWithInputsReq`):

```protobuf
repeated ERC20StateOverride erc20_overrides = 5;

message ERC20StateOverride {
  string token_address = 1;            // ERC20 token contract address
  string owner_address = 2;            // Address whose balance/allowance to override
  optional string spender_address = 3; // Spender to approve (required for allowance override)
  optional string balance = 4;         // Balance override (hex 0x… or decimal string)
  optional string allowance = 5;       // Allowance override (hex 0x… or decimal string)
  optional uint64 balance_slot = 6;    // Storage slot for the balanceOf mapping (default: 0)
  optional uint64 allowance_slot = 7;  // Storage slot for the allowance mapping (default: 3)
}
```

REST (`POST /api/v1/nodes:run`, camelCase): `erc20Overrides` is an array of the
same fields (`tokenAddress`, `ownerAddress`, `spenderAddress`, `balance`,
`allowance`, `balanceSlot`, `allowanceSlot`).

### SDK example

```javascript
const result = await client.runNodeWithInputs({
  node: { /* contractWrite node */ },
  inputVariables: { settings: { runner: '0x71c8f4D…' } },
  erc20Overrides: [
    {
      tokenAddress: '0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238', // USDC
      ownerAddress: '0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e',
      spenderAddress: '0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E', // SwapRouter02
      balance: '0x38d7ea4c68000',  // 1,000,000 USDC (6 decimals)
      allowance: '0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', // max uint256
      balanceSlot: 0,    // Standard ERC20 (omit to use default)
      allowanceSlot: 3,  // Standard ERC20 (omit to use default)
    },
  ],
});
```

### Validation

- `token_address` / `owner_address` must be valid hex addresses.
- An allowance override requires a valid `spender_address`.
- At least one of `balance` / `allowance` must be set.
- For non-standard layouts (e.g. USDC at 9/10) set the slots explicitly — see
  the [table below](#common-token-storage-slots).

## Example Use Case: Uniswap Swap

When simulating a Uniswap V3 swap on `SwapRouter02`, you need:

1. **ETH balance** for the transaction sender (for gas)
2. **Token balance** for the input token (e.g., USDC)
3. **Token allowance** for SwapRouter02 to spend the input token

Without these overrides, the simulation will fail with:
- "ERC20: transfer amount exceeds allowance" (no approval)
- "ERC20: transfer amount exceeds balance" (insufficient balance)

With proper state overrides, the simulation succeeds and returns the expected output amount.

## References

- [Tenderly Simulation API Docs](https://docs.tenderly.co/simulations-and-forks/simulation-api)
- [Ethereum Storage Layout](https://docs.soliditylang.org/en/latest/internals/layout_in_storage.html)
- [Solidity Mappings](https://docs.soliditylang.org/en/latest/types.html#mapping-types)

