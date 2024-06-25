# Run Operators

To run the AVS operator, there are 2 steps

1. Register to become an EigenLayer operator by following [EigenLayer Operator Guide](https://docs.eigenlayer.xyz/eigenlayer/operator-guides/operator-introduction)
2. Once become an operator, you can register for OAK AVS follow below step

### Run OAK AVS on Holesky testnet

Download the latest release from https://github.com/OAK-Foundation/ap-avs/releases for your platform. You can compile for yourself by simply running `go build` at the root level.

First, Generate OAK AVS config file. You can put it anywhere. Example `config/operator.yaml` with below content

```
# this sets the logger level (true = info, false = debug)
production: true

operator_address: your-operator-address


avs_registry_coordinator_address: 0x90c6d6f2A78d5Ce22AB8631Ddb142C03AC87De7a
operator_state_retriever_address: 0xb7bb920538e038DFFEfcB55caBf713652ED2031F

eth_rpc_url: a holesky rpc endpoint for http
eth_ws_url: a holesky rpc endpoint for wss

ecdsa_private_key_store_path: path-to-your.ecdsa.key.json
bls_private_key_store_path: path-to-your.bls.key.json

aggregator_server_ip_port_address: https://aggregator-holesky.api.oak.tech

# avs node spec compliance https://eigen.nethermind.io/docs/spec/intro
eigen_metrics_ip_port_address: your-public-ip:9090
enable_metrics: true
node_api_ip_port_address: your-public-ip:9010
enable_node_api: true
```

Configure 2 env var for your ECDSA and BLS password. Recall that these are
generated when you onboard your operator to EigenLayer.

```
export OPERATOR_BLS_KEY_PASSWORD=
export OPERATOR_ECDSA_KEY_PASSWORD=
```

Now, we can start the registration process.

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
