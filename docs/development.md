# Install protoc

```
# For working with grpc
brew install grpc protobuf

# For generate abibinding
go install github.com/ethereum/go-ethereum/cmd/abigen@latest

npm install -g grpc-tools
```

# Spin up local node

For node that connect to Ava pre-deployed AVS contract on Holesky testnet, we need to create a `config/aggregator.yaml`. Ask a dev on how to construct this file.

After having the config file, we can run the aggregator:

```
make build-docker
make up
```

Or use docker compsose directly:

```
# Only  need to rebuild when having code change
docker compose build

docker compose up
```

Once the docker compose is up, there are 2 services running:

1. The aggregator on localhost:2206
2. A GRPC webui to inspect and debug content on localhost:8080

Visit http://localhost:8080 and you can interactively create and construct
request to our grpc node.

For detail of each method and payload, check the protocol.md docs.


## Client SDK

We generate the client sdk for JavaScript. The code is generated based on our
protobuf definition on this file.
