# Development Guide

## Installation Prerequisites

Before you can start developing, you'll need to install the following tools:

```bash
# For working with gRPC
brew install grpc protobuf

# For generating ABI bindings
go install github.com/ethereum/go-ethereum/cmd/abigen@latest

# For gRPC tools
npm install -g grpc-tools
```

## Running a Local Node

For a node that connects to Ava pre-deployed AVS contract on Holesky testnet, you need to create a `config/aggregator.yaml` file. Contact a developer for help with constructing this file.

After having the config file, you can run the aggregator:

```bash
make dev-build
make dev-agg
```

Or use Docker Compose directly:

```bash
# Only needed when code changes
docker compose build

# Start the services
docker compose up
```

Once Docker Compose is up, there are two services running:

1. The aggregator on `localhost:2206`
2. A gRPC web UI to inspect and debug content on `localhost:8080`

Visit http://localhost:8080 to interactively create and construct requests to the gRPC node.

For details on methods and payloads, check the `Protocol.md` documentation. Look into the `examples` directory to run example workflows against the local node.

## Running an Operator Locally

Running an operator locally allows you to schedule and see job execution. First, prepare a `config/operator.yaml` file, then run:

```bash
make dev-op
```

This local operator will connect to the EigenLayer Holesky environment, so you will need to make sure you have existing operator keys onboarded with EigenLayer.

## Live Reload

To automatically compile and live reload the node during development, run:

```bash
make dev-live
```

## Client SDK

We generate the client SDK for JavaScript. The code is generated based on our protobuf definition files.

## Storage REPL

To inspect storage, we use a simple REPL (Read-Eval-Print Loop) interface. Connect to it with:

```bash
telnet /tmp/ap.sock
```

### Supported Commands

The REPL supports the following commands:

#### `list <prefix>*`
Lists all keys that match the given prefix pattern. The asterisk (*) is used as a wildcard.

Example:
```bash
# List all keys in the database
list *

# List all active tasks
list t:a:*
```

#### `get <key>`
Retrieves and displays the value associated with the specified key.

Example:
```bash
# Get the value of a specific task
get t:a:01JD3252QZKJPK20CPH0S179FH
```

#### `set <key> <value>`
Sets a key to the specified value. You can also load values from files by prefixing the file path with an @ symbol.

Examples:
```bash
# Set a key with a direct value
set t:a:01JD3252QZKJPK20CPH0S179FH 'value here'

# Set a key with the contents of a file
set t:a:01JD3252QZKJPK20CPH0S179FH @/path/to/file
```

#### `gc`
Triggers garbage collection on the database with a ratio of 0.7. This helps reclaim space from deleted data.

```bash
gc
```

#### `exit`
Closes the REPL connection.

```bash
exit
```

#### `rm <prefix>*`
Deletes all keys that match the given prefix pattern. The command first lists all matching keys and then deletes them.

```bash
# Delete keys with a specific prefix
rm prefix:*
```

#### `backup <directory>`
Creates a backup of the database to the specified directory. The backup is stored in a timestamped subdirectory with a filename of `badger.backup`.

```bash
# Backup the database to the /tmp/backups directory
backup /tmp/backups
```

#### `trigger`
This command is currently under development and not fully implemented.

## Resetting Storage

During development, you may need to reset storage to erase bad data due to schema changes. In the future, we will implement migration to properly migrate storage. For now, to wipe out storage, run:

```bash
make dev-clean
```
