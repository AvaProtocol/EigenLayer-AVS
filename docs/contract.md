# Ava Protocol Contracts

There are two kinds of contracts in the Ava Protocol system:

1. **EigenLayer Contract**  
2. **Smart Account Contract** (Our account abstraction contract)

## Contract List

We use a consistent contract address across four networks:

- **Base Sepolia**  
- **Sepolia**  
- **Base**  
- **Ethereum**  

### Contracts Table

| Contract | Address | Base Sepolia | Base | Sepolia | Ethereum |
|----------|----------------------------------|------------------------------------------------------------------|-------------------------------------------------------------|-------------------------------------------------------------|------------------------------------------------|
| **Config (AAConfig)** | `0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564` | [View](https://sepolia.basescan.org/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://basescan.org/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://sepolia.etherscan.io/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) | [View](https://etherscan.io/address/0x5327443cF04e6E8c3B86BDBbfaE16fcB965b7564) |
| **Wallet Implementation** | `0x552D410C9c4231841413F6061baaCB5c8fBFB0DEC` | [View](https://sepolia.basescan.org/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DEC) | [View](https://basescan.org/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DEC) | [View](https://sepolia.etherscan.io/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DEC) | [View](https://etherscan.io/address/0x552D410C9c4231841413F6061baaCB5c8fBFB0DEC) |
| **Factory Proxy** | `0xB99BC2E399e06CddCF5E725c0ea341E8f0322834` | [View](https://sepolia.basescan.org/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://basescan.org/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://sepolia.etherscan.io/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) | [View](https://etherscan.io/address/0xB99BC2E399e06CddCF5E725c0ea341E8f0322834) |
| **Factory Implementation** | `0x5692D03FC5922b806F382E4F1A620479A14c96c2` | [View](https://sepolia.basescan.org/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://basescan.org/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://sepolia.etherscan.io/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) | [View](https://etherscan.io/address/0x5692D03FC5922b806F382E4F1A620479A14c96c2) |

### Pre-fund

The first transaction require siginficant higher gas to pay for contract deployment. Below is the sample pre-fund requirement to the smart contract. The smart contract address of wallet can compute ahead of time

| Network      | Prefund |
|--------------|---------|
| Ethereum     | 0.4     |
| Sepolia      | 0.4     |
| Base         | 0.001   |
| Base Sepolia | 0.001   |

### Sample Txs

This is a few sample tx that also deploy the smart wallet together with the gas cost to get an idea of pre-fund.

| Network      | Transaction Link                                                                 | Suggest Prefund(ETH) |
|--------------|----------------------------------------------------------------------------------|----------|
| Mainnet      | TBD                                                                              | 0.001    |
| Sepolia      | [View Transaction](https://sepolia.etherscan.io/tx/0xee325c48e6a6a35b91642b2483acd860255283aded8cb949a9594a8ab19c7f69) | 0.4   |
| Base         | TBD                                                                              | 0.001    |
| Base Sepolia | [View Transaction](https://sepolia.basescan.org/tx/0x946e7b6e48fd1421d17263e9b89e329e264cb37de511077844e925f414be8851) | 0.00005    |
