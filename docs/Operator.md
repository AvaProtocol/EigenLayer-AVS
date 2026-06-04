# Run Operators

To run the AVS operator, there are 2 steps

1. Register to become an EigenLayer operator by following [EigenLayer Operator Guide](https://docs.eigenlayer.xyz/eigenlayer/operator-guides/operator-introduction)
2. Once become an operator, you can register for Ava Protocol AVS by following the below step

### Run Ava Protocol AVS on Ethereum mainnet

Download the latest release from https://github.com/AvaProtocol/EigenLayer-AVS/releases for your platform. You can compile for yourself by simply running `go build` at the root level.

First, Generate Ava Protocol AVS config file. You can put it anywhere. Example `config/operator.yaml` with below content:

```
# this sets the logger level (true = info, false = debug)
production: true

operator_address: <operator_address>

# Ava Protocol AVS contract addresses on Ethereum mainnet
avs_registry_coordinator_address: 0x8DE3Ee0dE880161Aa0CD8Bf9F8F6a7AfEeB9A44B
operator_state_retriever_address: 0xb3af70D5f72C04D1f490ff49e5aB189fA7122713

ecdsa_private_key_store_path: <path_to_operator_ecdsa_key_json>
bls_private_key_store_path: <path_to_operator_bls_key_json>

# Unified aggregator endpoint — serves all mainnet chains (Ethereum, Base, ...)
# from a single gRPC connection. The gateway routes per-chain internally.
aggregator_server_ip_port_address: "aggregator.avaprotocol.org:2206"

# avs node spec compliance https://eigen.nethermind.io/docs/spec/intro
eigen_metrics_ip_port_address: <operator_public_ip>:9090
enable_metrics: true
node_api_ip_port_address: <operator_public_ip>:9010
enable_node_api: true
```

> **Note**: Third-party operator support is mainnet-only. Sepolia/Holesky testnet operator coordination is handled internally by Ava Protocol and is not open to external operators.

Configure 2 env var for your ECDSA and BLS password. Recall that these are generated when you onboard your operator to EigenLayer. In case your password contains special characters to the command-line, here we export both variables by disabling history expansion temporarily.
```
set +H
export OPERATOR_BLS_KEY_PASSWORD="<operator_bls_password>"
export OPERATOR_ECDSA_KEY_PASSWORD="<operator_ecdsa_password>"
set -H
```

Now, we can start the registration process by running our `ap-avs` AVS release binary.

```
ap-avs register --config=./config/operator.yaml
```

At the end of process, you should see something like this:

```
successfully registered operator with AVS registry coordinator

Registered operator with avs registry coordinator
```

The status can also be checked with `ap-avs status --config=./config/operator.yaml`

At this point, you're ready to run our operator node by simply do

```
ap-avs operator --config=./config/operator.yaml
```

# Running operator with docker compose

To help simplify the process and enable auto update you can use our [official
operator setup repository](https://github.com/AvaProtocol/ap-operator-setup)
