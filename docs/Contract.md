# Ava Protocol Contracts

Ava Protocol implements ERC4337 (Account Abstraction) for smart wallet operations. The system consists of several key contracts:

1. **EigenLayer Contract** - For AVS operations
2. **Smart Account Contract** - Our account abstraction implementation

## Contract Architecture

We use a consistent contract address across six networks:

- **Base Sepolia** (Testnet)
- **Sepolia** (Testnet)
- **Base** (Mainnet)
- **Ethereum** (Mainnet)
- **Soneium** (Mainnet)
- **Minato** (Testnet)

### Core Contracts

| Contract                   | Address                                      | Base Sepolia                                                                            | Base                                                                            | Sepolia                                                                                 | Ethereum                                                                        | Soneium                                                                        | Minato                                                                                |
| -------------------------- | -------------------------------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| **Config (AAConfig)**      | `0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564` | [View](https://sepolia.basescan.org/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://basescan.org/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://sepolia.etherscan.io/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://etherscan.io/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://soneium.blockscout.com/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://soneium-minato.blockscout.com/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) |
| **Wallet Implementation**  | `0x552D410C9c4231841413F6061baaCB5c8fBFB0DE` | [View](https://sepolia.basescan.org/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DE) | [View](https://basescan.org/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DE) | [View](https://sepolia.etherscan.io/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DE) | [View](https://etherscan.io/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DE) | [View](https://soneium.blockscout.com/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DE) | [View](https://soneium-minato.blockscout.com/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DE) |
| **Factory Proxy**          | `0xB99BC2E399e06CddCF5E725c0ea341E8f0322834` | [View](https://sepolia.basescan.org/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://basescan.org/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://sepolia.etherscan.io/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://etherscan.io/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://soneium.blockscout.com/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://soneium-minato.blockscout.com/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) |
| **Factory Implementation** | `0x5692D03FC5922b806F382E4F1A620479A14c96c2` | [View](https://sepolia.basescan.org/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://basescan.org/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://sepolia.etherscan.io/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://etherscan.io/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://soneium.blockscout.com/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://soneium-minato.blockscout.com/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) |
| **Paymaster Contract**     | `0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f` | [View](https://sepolia.basescan.org/address/0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f) | [View](https://basescan.org/address/0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f) | [View](https://sepolia.etherscan.io/address/0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f) | [View](https://etherscan.io/address/0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f) | [View](https://soneium.blockscout.com/address/0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f) | [View](https://soneium-minato.blockscout.com/address/0xf023eA291F5bEDA4Bf59BbDC9004F1d18be19D6f) |

### Bundler Addresses

| Network       | Address                                      | Chain ID | Type | Notes                            |
| ------------- | -------------------------------------------- | -------- | ---- | -------------------------------- |
| Ethereum      | `0x6A99324303928aF456aA21f3C88dc58E812D9B40` | 1        | EOA  | Production bundler for Ethereum  |
| Base          | `0x6A99324303928aF456aA21f3C88dc58E812D9B40` | 8453     | EOA  | Production bundler for Base      |
| Sepolia       | `0xE164dd09e720640F6695cB6cED0308065ceFECd9` | 11155111 | EOA  | Testnet bundler for Sepolia      |
| Base Sepolia  | `0xE164dd09e720640F6695cB6cED0308065ceFECd9` | 84532    | EOA  | Testnet bundler for Base Sepolia |

## Fee Structure and Pre-funding

### Understanding Pre-funding

Pre-funding refers to depositing ETH to your smart wallet address to cover transaction fees. This is necessary because:

1. Initial contract deployment requires higher gas costs than normal operations
2. The smart wallet needs ETH to pay for gas when executing tasks
3. The Factory Proxy contract deploys new smart wallets, which requires gas

### Fee Payment Flow

The fee payment process follows ERC4337 architecture:

1. **Bundler** (EOA addresses listed above):

   - Sends transactions to the chain
   - Initially spends its own ETH for gas
   - Gets refunded through the EntryPoint contract
   - May charge a small fee (configurable)

2. **EntryPoint Contract**:

   - Coordinates the execution flow
   - Handles fee refunds to the bundler
   - Gets funds from either:
     - Smart wallet (user pays)
     - Paymaster (sponsored transactions)

3. **Paymaster**:
   - Cannot hold ETH directly
   - Requires deposits through the deposit function
   - Immediately transfers funds to EntryPoint
   - Used for sponsored transactions

### Pre-fund Requirements

| Network      | Pre-fund (ETH) | Sample Transaction                                                                                         |
| ------------ | -------------- | ---------------------------------------------------------------------------------------------------------- |
| Ethereum     | 0.4            | TBD                                                                                                        |
| Sepolia      | 0.4            | [View](https://sepolia.etherscan.io/tx/0xee325c48e6a6a35b91642b2483acd860255283aded8cb949a9594a8ab19c7f69) |
| Base         | 0.001          | TBD                                                                                                        |
| Base Sepolia | 0.00005        | [View](https://sepolia.basescan.org/tx/0x946e7b6e48fd1421d17263e9b89e329e264cb37de511077844e925f414be8851) |
| Soneium      | TBD            | TBD                                                                                                        |
| Minato       | TBD            | TBD                                                                                                        |

### Sponsored Transactions

For tasks where we cover the fee:

1. User's smart wallet doesn't need to hold ETH
2. Paymaster must have sufficient funds
3. Funds must be deposited to Paymaster through the deposit function
4. Paymaster immediately transfers funds to EntryPoint

### Important Notes

- The Factory Proxy is responsible for deploying new smart wallets
- Smart wallets are dynamically generated with new addresses
- The bundler is an EOA (Externally Owned Account) that can send transactions. Production bundler address: `0x6A99324303928aF456aA21f3C88dc58E812D9B40` (Ethereum & Base), Testnet bundler address: `0xE164dd09e720640F6695cB6cED0308065ceFECd9` (Sepolia & Base Sepolia)
- We use [Voltaire](https://github.com/candidelabs/voltaire) as our bundler implementation
- The execution flow follows [ERC4337](https://eips.ethereum.org/EIPS/eip-4337) specification
