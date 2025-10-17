# Smart Wallet Controller Authorization

## Overview

This document explains how controller authorization works in the EigenLayer-AVS smart wallet system. The system uses ERC-4337 Account Abstraction to enable autonomous operation of smart wallets while maintaining user ownership.

## Architecture

### Key Components

1. **Smart Wallet Owner**: The user's Externally Owned Account (EOA) that owns the smart wallet
2. **Controller**: A separate EOA that's authorized to sign UserOperations on behalf of the smart wallet
3. **Smart Wallet Contract**: ERC-4337 compatible contract that validates signatures from both owner and controller
4. **UserOperations (UserOps)**: ERC-4337 transactions that can be signed by authorized parties

### Example Configuration

- **Controller**: `0x3b6f68B9B4577A3D3085ead6Fcdc2951719ad2e1` (from ControllerPrivateKey config)
- **Smart Wallet Owner**: 
- **Smart Wallet Address**:

## How Controller Authorization Works

### Pattern A: Controller Registration

The EigenLayer-AVS system implements **Controller Registration** rather than delegation permits. Here's how it works:

#### 1. Smart Wallet Deployment

```go
// Smart wallet address is derived from owner + salt
ownerAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
salt := big.NewInt(0)
smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, salt)
```

- Smart wallet is deployed with an `owner` (the user's EOA)
- A `controller` is registered in the contract during deployment
- The controller address is derived from the `ControllerPrivateKey` in the configuration

#### 2. Contract Structure

The smart wallet contract has these key functions:

```solidity
function owner() view returns(address)      // Returns the owner EOA
function controller() view returns(address) // Returns the authorized controller EOA
function validateUserOp(UserOperation userOp, bytes32 userOpHash, uint256 missingAccountFunds) 
    returns(uint256 validationData)
```

#### 3. UserOp Signing Process

```go
// Controller signs UserOps on behalf of the smart wallet
userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
```

Both regular UserOps and paymaster-sponsored UserOps are signed by the controller private key.

#### 4. Signature Validation Logic

The smart wallet's `validateUserOp()` function validates signatures as follows:

```solidity
function validateUserOp(UserOperation userOp, bytes32 userOpHash, uint256) {
    address signer = ECDSA.recover(userOpHash, userOp.signature);
    
    // Allow both owner and controller to sign
    if (signer == owner() || signer == controller()) {
        return 0; // Valid signature
    } else {
        return 1; // Invalid signature  
    }
}
```

## Why This Works

The controller (`0x3b6f68B9B4577A3D3085ead6Fcdc2951719ad2e1`) can sign UserOps for the smart wallet because:

1. **The smart wallet contract explicitly trusts the controller address**
2. **The controller was registered/authorized during deployment**  
3. **The validation function checks both owner AND controller signatures**
4. **This is built into the contract logic, not through external permits**

## Key Benefits

### Security
- **Dual Authorization**: Both owner and controller can authorize transactions
- **No External Dependencies**: Authorization is built into the contract, not relying on external permit systems
- **Revocable**: Controller authorization can be updated by the owner if needed

### Operational Efficiency
- **Autonomous Operation**: EigenLayer-AVS can operate smart wallets without user intervention
- **Gas Sponsorship**: Controller can use paymasters to sponsor gas fees
- **Batch Operations**: Multiple operations can be batched in a single UserOp

### User Experience
- **User Ownership**: User retains full ownership of their smart wallet
- **Seamless Integration**: Users don't need to actively sign every operation
- **Transparency**: All operations are on-chain and auditable

## Configuration

The controller is configured in the system configuration:

```yaml
smart_wallet:
  controller_private_key: "0x..." # Private key for the controller EOA
  factory_address: "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"
  entrypoint_address: "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"
```

The controller private key is loaded at startup:

```go
controllerPrivateKey, err := crypto.HexToECDSA(configRaw.SmartWallet.ControllerPrivateKey)
```

## Comparison with Other Patterns

### Pattern A: Controller Registration (Used by EigenLayer-AVS)
- ✅ **Permanent authorization** built into contract
- ✅ **No expiration** - works indefinitely
- ✅ **Simple validation** logic
- ✅ **Gas efficient** - no permit verification needed

### Pattern B: Delegation Permits (Not used)
- ❌ **Temporary authorization** with expiration
- ❌ **Complex permit** verification required  
- ❌ **Additional gas** for permit validation
- ❌ **Signature overhead** for permit creation

## Bundler Configuration for Testing

### Resolving "Insufficient Stake" Errors

When testing smart wallets that access storage slots in external contracts (like AAConfig), the bundler may require those contracts to stake ETH with the EntryPoint. For testing purposes, you can disable this requirement:

```bash
# Disable staking requirements for testing
--min_stake 0 --min_unstake_delay 0
```

**Example bundler startup command:**
```bash
voltaire_bundler \
  --bundler_secret <private_key> \
  --rpc_url <rpc_endpoint> \
  --rpc_port 3000 \
  --entrypoint 0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789 \
  --min_stake 0 \
  --min_unstake_delay 0
```

### Why This Is Needed

In ERC-4337, when a smart wallet's `validateUserOp` function accesses storage in external contracts (e.g., calling `AAConfig.controller()`), the bundler requires those contracts to stake ETH to prevent DOS attacks. For testing environments where you control all contracts, disabling the staking requirement with `--min_stake 0` simplifies the setup.

**Production Note:** In production, proper staking should be implemented for security.

## Summary

The EigenLayer-AVS smart wallet system uses **Pattern A: Controller Registration** where the controller is directly registered in the smart wallet contract as an authorized signer. This allows the controller to sign UserOps on behalf of the owner without requiring delegation permits.

The authorization is:
- **Permanent** and built into the contract state
- **Efficient** with minimal gas overhead
- **Secure** with explicit trust relationships
- **Flexible** allowing both owner and controller to operate the wallet

This design enables the EigenLayer-AVS system to operate smart wallets autonomously while maintaining user ownership and control.