## Foundry

**Foundry is a blazing fast, portable and modular toolkit for Ethereum application development written in Rust.**

Foundry consists of:

-   **Forge**: Ethereum testing framework (like Truffle, Hardhat and DappTools).
-   **Cast**: Swiss army knife for interacting with EVM smart contracts, sending transactions and getting chain data.
-   **Anvil**: Local Ethereum node, akin to Ganache, Hardhat Network.
-   **Chisel**: Fast, utilitarian, and verbose solidity REPL.

## Documentation

https://book.getfoundry.sh/

## Usage

### Build

```shell
$ forge build
```

### Test

```shell
$ forge test
```

### Format

```shell
$ forge fmt
```

### Gas Snapshots

```shell
$ forge snapshot
```

### Anvil

```shell
$ anvil
```

### Deploy

```shell
$ forge script script/Counter.s.sol:CounterScript --rpc-url <your_rpc_url> --private-key <your_private_key>
```

### Cast

```shell
$ cast <subcommand>
```

### Help

```shell
$ forge --help
$ anvil --help
$ cast --help
```

#### Sepolia Deployment Guide

1) Tooling and compilers
```bash
curl -L https://foundry.paradigm.xyz | bash
source ~/.bash_profile || source ~/.bashrc || true
foundryup
curl https://sh.rustup.rs -sSf | sh -s -- -y
source ~/.cargo/env
cargo install --locked svm-rs
echo 'export PATH="$HOME/.foundry/bin:$HOME/.cargo/bin:$PATH"' >> ~/.bash_profile
source ~/.bash_profile || true
svm install 0.8.12
svm install 0.8.25
cd /Users/mikasa/Code/EigenLayer-AVS/contracts
git submodule update --init --recursive
forge clean && forge build
```

2) Required environment variables
```bash
export RPC_URL="https://ethereum-sepolia-rpc.publicnode.com"
export ETHSCAN_API_KEY="<your_etherscan_key>"
export PRIVATE_KEY="0x<YOUR_PRIVATE_KEY_HEX>"   # Use the intended key

export OWNER_ADDRESS="0x<your_EOA>"
export PAUSER_ADDRESS="0x<your_EOA_or_pauser>"

# From EigenLayer Sepolia deployments
export DELEGATION_MANAGER="0x<DelegationManager_on_Sepolia>"
export AVS_DIRECTORY="0x<AVSDirectory_on_Sepolia>"
```

3) Deploy PauserRegistry
```bash
/Users/mikasa/.foundry/bin/forge script script/DeployPauserRegistry.s.sol:DeployPauserRegistry \
  --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" \
  --etherscan-api-key "$ETHSCAN_API_KEY" --broadcast --verify -vvvv

export PAUSER_REGISTRY_ADDRESS=$(jq -r '.pauserRegistryAddress' script/output/pause_registry_deploy_output.json)
```

4) Deploy AVS core (proxies + registries + service manager)
```bash
/Users/mikasa/.foundry/bin/forge script script/DeployServiceManager.s.sol:DeployServiceManager \
  --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" \
  --etherscan-api-key "$ETHSCAN_API_KEY" --broadcast --verify --slow -vvvv

export AVS_OUT=script/output/avs_deploy_output.json
export PROXY_ADMIN_ADDRESS=$(jq -r '.proxyAdmin' "$AVS_OUT")
export REGISTRY_COORDINATOR_PROXY=$(jq -r '.registryCoordinatorProxy' "$AVS_OUT")
export REGISTRY_COORDINATOR_IMPL=$(jq -r '.registryCoordinatorImpl' "$AVS_OUT")
export STAKE_REGISTRY_PROXY=$(jq -r '.stakeRegistryProxy' "$AVS_OUT")
export STAKE_REGISTRY_IMPL=$(jq -r '.stakeRegistryImpl' "$AVS_OUT")
export BLSAPKREGISTRY_PROXY=$(jq -r '.BLSApkRegistryProxy' "$AVS_OUT")
export BLSAPKREGISTRY_IMPL=$(jq -r '.BLSApkRegistryImpl' "$AVS_OUT")
export INDEX_REGISTRY_PROXY=$(jq -r '.indexRegistryProxy' "$AVS_OUT")
export INDEX_REGISTRY_IMPL=$(jq -r '.indexRegistryImpl' "$AVS_OUT")
export SERVICE_MANAGER_PROXY=$(jq -r '.avsServiceManagerProxy' "$AVS_OUT")
export SERVICE_MANAGER_IMPL=$(jq -r '.avsServiceManagerImpl' "$AVS_OUT")
export OPERATOR_STATE_RETRIEVER_ADDRESS=$(jq -r '.operatorStateRetriever' "$AVS_OUT")
```

5) Deploy AutomationTaskManager implementation (optional)
```bash
export REGISTRY_COORDINATOR_ADDRESS=$REGISTRY_COORDINATOR_PROXY
/Users/mikasa/.foundry/bin/forge script script/DeployTaskManagerImpl.s.sol:DeployTaskManagerImpl \
  --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" \
  --etherscan-api-key "$ETHSCAN_API_KEY" --broadcast --verify -vvvv
```

6) Deploy APConfig (new proxy)
```bash
export SWAP_IMPL=false
/Users/mikasa/.foundry/bin/forge script script/DeployAPConfig.s.sol:DeployAPConfig \
  --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" \
  --etherscan-api-key "$ETHSCAN_API_KEY" --broadcast --verify --slow -vvvv
export APCONFIG_PROXY=$(jq -r '.apConfigProxy' script/output/ap_config.json)
```

7) Verify proxies on Etherscan Sepolia
Note: implementations are verified by the scripts; proxies need manual CLI verify.
```bash
# APConfig proxy (0.8.25)
forge verify-contract --verifier etherscan --chain sepolia --etherscan-api-key "$ETHSCAN_API_KEY" \
  "$APCONFIG_PROXY" \
  lib/eigenlayer-middleware/lib/eigenlayer-contracts/lib/openzeppelin-contracts/contracts/proxy/transparent/TransparentUpgradeableProxy.sol:TransparentUpgradeableProxy \
  --compiler-version 0.8.25 --optimizer-runs 200 \
  --constructor-args $(cast abi-encode 'constructor(address,address,bytes)' \
    $(jq -r '.apConfigImpl' script/output/ap_config.json) \
    "$PROXY_ADMIN_ADDRESS" \
    0x)

# Core proxies (0.8.12)
forge verify-contract --verifier etherscan --chain sepolia --etherscan-api-key "$ETHSCAN_API_KEY" \
  "$INDEX_REGISTRY_PROXY" \
  lib/eigenlayer-middleware/lib/eigenlayer-contracts/lib/openzeppelin-contracts/contracts/proxy/transparent/TransparentUpgradeableProxy.sol:TransparentUpgradeableProxy \
  --compiler-version 0.8.12 --optimizer-runs 200 \
  --constructor-args $(cast abi-encode 'constructor(address,address,bytes)' "$INDEX_REGISTRY_IMPL" "$PROXY_ADMIN_ADDRESS" 0x)

forge verify-contract --verifier etherscan --chain sepolia --etherscan-api-key "$ETHSCAN_API_KEY" \
  "$STAKE_REGISTRY_PROXY" \
  lib/eigenlayer-middleware/lib/eigenlayer-contracts/lib/openzeppelin-contracts/contracts/proxy/transparent/TransparentUpgradeableProxy.sol:TransparentUpgradeableProxy \
  --compiler-version 0.8.12 --optimizer-runs 200 \
  --constructor-args $(cast abi-encode 'constructor(address,address,bytes)' "$STAKE_REGISTRY_IMPL" "$PROXY_ADMIN_ADDRESS" 0x)

forge verify-contract --verifier etherscan --chain sepolia --etherscan-api-key "$ETHSCAN_API_KEY" \
  "$BLSAPKREGISTRY_PROXY" \
  lib/eigenlayer-middleware/lib/eigenlayer-contracts/lib/openzeppelin-contracts/contracts/proxy/transparent/TransparentUpgradeableProxy.sol:TransparentUpgradeableProxy \
  --compiler-version 0.8.12 --optimizer-runs 200 \
  --constructor-args $(cast abi-encode 'constructor(address,address,bytes)' "$BLSAPKREGISTRY_IMPL" "$PROXY_ADMIN_ADDRESS" 0x)

forge verify-contract --verifier etherscan --chain sepolia --etherscan-api-key "$ETHSCAN_API_KEY" \
  "$REGISTRY_COORDINATOR_PROXY" \
  lib/eigenlayer-middleware/lib/eigenlayer-contracts/lib/openzeppelin-contracts/contracts/proxy/transparent/TransparentUpgradeableProxy.sol:TransparentUpgradeableProxy \
  --compiler-version 0.8.12 --optimizer-runs 200 \
  --constructor-args $(cast abi-encode 'constructor(address,address,bytes)' "$REGISTRY_COORDINATOR_IMPL" "$PROXY_ADMIN_ADDRESS" 0x)

forge verify-contract --verifier etherscan --chain sepolia --etherscan-api-key "$ETHSCAN_API_KEY" \
  "$SERVICE_MANAGER_PROXY" \
  lib/eigenlayer-middleware/lib/eigenlayer-contracts/lib/openzeppelin-contracts/contracts/proxy/transparent/TransparentUpgradeableProxy.sol:TransparentUpgradeableProxy \
  --compiler-version 0.8.12 --optimizer-runs 200 \
  --constructor-args $(cast abi-encode 'constructor(address,address,bytes)' "$SERVICE_MANAGER_IMPL" "$PROXY_ADMIN_ADDRESS" 0x)
```
If CLI verify still fails, use Etherscan Sepolia UI “Is this a proxy?” on each proxy page.

8) Update config
```yaml
# config/aggregator.yaml
avs_registry_coordinator_address: <REGISTRY_COORDINATOR_PROXY>
operator_state_retriever_address: <OPERATOR_STATE_RETRIEVER_ADDRESS>
```
```

- Summary
  - Added a complete, copy-paste redeploy + verify flow. Make sure to export the correct PRIVATE_KEY before running.