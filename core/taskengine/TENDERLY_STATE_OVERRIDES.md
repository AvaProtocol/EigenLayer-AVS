# Tenderly State Overrides for ERC20 Testing

## Overview

When simulating contract writes involving ERC20 tokens (like Uniswap swaps), you often need to override the blockchain state to:
1. Set token balances for test wallets
2. Set token allowances without requiring separate approval transactions

This document explains how to calculate and use Tenderly state overrides for ERC20 tokens.

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

See `calculateERC20StorageSlots()` in `vm_runner_contract_write_uniswap_test.go` for a working example:

```go
func calculateERC20StorageSlots(owner, spender, tokenAddress common.Address, balanceSlot, allowanceSlot uint64) (balanceStorageSlot, allowanceStorageSlot string) {
	// Balance slot: keccak256(abi.encode(owner, balanceSlot))
	ownerPadded := common.LeftPadBytes(owner.Bytes(), 32)
	balanceSlotPadded := common.LeftPadBytes(big.NewInt(int64(balanceSlot)).Bytes(), 32)
	balanceData := append(ownerPadded, balanceSlotPadded...)
	balanceHash := crypto.Keccak256Hash(balanceData)
	
	// Allowance slot: keccak256(abi.encode(spender, keccak256(abi.encode(owner, allowanceSlot))))
	allowanceSlotPadded := common.LeftPadBytes(big.NewInt(int64(allowanceSlot)).Bytes(), 32)
	innerData := append(ownerPadded, allowanceSlotPadded...)
	innerHash := crypto.Keccak256Hash(innerData)
	
	spenderPadded := common.LeftPadBytes(spender.Bytes(), 32)
	outerData := append(spenderPadded, innerHash.Bytes()...)
	allowanceHash := crypto.Keccak256Hash(outerData)
	
	return balanceHash.Hex(), allowanceHash.Hex()
}
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

## Implementation TODO

To enable state overrides in production code, the `TenderlyClient.SimulateContractWrite()` method needs to be enhanced to:

1. Accept optional parameters for ERC20 overrides:
   - Token addresses to override
   - Balance and allowance values
   - Storage slot numbers

2. Calculate storage slots using the helper function

3. Include the `state_objects` in the Tenderly API payload

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

