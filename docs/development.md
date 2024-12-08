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
make dev-build
make dev-agg
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

For detail of each method and payload, check the protocol.md docs. Look into `examples` to run example workflow against the local node

# Live reload

To auto compile and live reload the node, run:


```
make dev-live
```

## Client SDK

We generate the client sdk for JavaScript. The code is generated based on our
protobuf definition on this file.

## Storage REPL

To inspect storage we use a simple repl.

```
telnet /tmp/ap.sock
```

The repl support a few commands:

```
list <prefix>*
get <key>
set <key> <value>
gc
``

Example:

### List everything

```
list *
```

### List active tasks

```
list t:a:*
```

### Read a key

```
get t:a:01JD3252QZKJPK20CPH0S179FH
```

### Set a key

```
set t:a:01JD3252QZKJPK20CPH0S179FH 'value here'
```

Checkout repl.go for more information


## Reset storage

During development, we may have to reset storage to erase bad data due to schema change. Once we're mature we will implement migration to migrate storage. For now to wipe out storage run:

```
make dev-clean
```
