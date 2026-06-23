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

For a node that connects to Ava's pre-deployed AVS contract, copy the
template config and fill in the placeholder values:

```bash
cp config/gateway.example.yaml config/gateway.yaml
$EDITOR config/gateway.yaml
```

The default `chains:` block in the template covers Sepolia + Base
Sepolia. Add more chains under `chains:` if you need them — see
[config/README.md](../config/README.md) for the layout.

Then build + run:

```bash
# Build the application
make build

# Start the local-dev gateway (serves every chain in config/gateway.yaml)
make gateway
```

The legacy `make aggregator-<chain>` targets were retired when the
Hetzner→Railway migration consolidated the per-chain aggregator
pattern into a single multi-chain gateway. They now print a
deprecation notice pointing at `make gateway`.

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

Running an operator locally allows you to schedule and see job execution. First, prepare the appropriate config file for your target chain, then run:

```bash
# Run operator on Sepolia
make operator-sepolia

# Run operator on Ethereum mainnet
make operator-ethereum

# Run operator on Base
make operator-base

# Run operator with default config
make operator-default
```

The operator will connect to the corresponding EigenLayer environment for the chain you select, so you will need to make sure you have existing operator keys onboarded with EigenLayer.

## Live Reload

To automatically compile and live reload the node during development, run:

```bash
make dev-live
```

**Note:** Live reload performs a full process restart on every code change, which interrupts:
- Active task executions and monitoring loops
- WebSocket connections from operators (they must reconnect)
- In-memory state and queued jobs

Persistent storage (BadgerDB) survives restarts. For testing complete workflows or debugging stateful operations, consider using manual restarts (`make gateway`) instead.

## Testing

### Test configuration

Tests come in two tiers, and most contributors only ever need the first:

- **Unit tests** — need **no configuration**. Just run `go test`. The override
  logic, slot math, mappings, and validation are all covered here (e.g.
  `core/taskengine/simulation_state_override_test.go`). These run anywhere,
  including CI on forks.
- **Integration / simulation tests** — exercise real RPC reads and Tenderly
  contract-write simulations. These load a config fixture at
  `config/test.yaml` (this is `testutil.DefaultConfigPath`). **You do not run
  any server for this** — `test.yaml` is purely a test fixture, not the gateway
  or operator config. Create it once from the template:

  ```bash
  cp config/test.example.yaml config/test.yaml
  # then fill in:
  #   eth_rpc_url        — a Sepolia RPC endpoint (your own Infura/Alchemy/etc.)
  #   tenderly_account / tenderly_project / tenderly_access_key — your Tenderly creds
  ```

  A test that can't find `config/test.yaml` either `t.Skip`s or panics with
  `testConfig is nil - test.yaml config must be loaded` — that just means the
  fixture is missing, not that your change is broken. No operator, bundler, or
  paymaster needs to run: simulation tests never broadcast on-chain.

  The owner EOA of the testing smart wallets is read separately from the
  environment (or a `.env` file in the repo root), **not** from `test.yaml`.
  It must be funded with testnet tokens so the tests can derive and exercise
  the smart wallet:

  ```bash
  # Owner EOA of the testing smart wallets (must have testnet funds)
  OWNER_EOA=0x...
  ```

  > These are the same values CI substitutes in the "Setup test configuration"
  > step of `.github/workflows/run-test-on-pr.yml`. Repo secrets are not exposed
  > to pull requests from forks, so to run these on a fork you supply your own.

#### Security notice

⚠️ **SECURITY WARNING** — only ever use throwaway test keys here:

- Never use private keys that hold real funds for testing.
- Use dedicated test keys funded only with testnet tokens.
- The fallback private key (all 1's) is insecure and only for local development.
- Always supply proper test keys via environment variables or `config/test.yaml`.

### Standard Tests

The Makefile includes two primary test configurations:

```bash
# Default test suite
go test -race -buildvcs -vet=off ./...

# Verbose test output
go test -v -race -buildvcs ./...
```

### Enhanced Test Output

For improved test result formatting, use `gotestfmt`:

1. Install the formatter:

   ```bash
   go install github.com/gotesttools/gotestfmt/v2/cmd/gotestfmt@latest
   ```

2. Run once in the current terminal session to make Bash scripts more robust and error-aware.

   ```bash
   set -euo pipefail
   ```

3. Run tests with formatted output:

   Run all tests with complete output:

   ```bash
   go test -v ./...
   ```

   or, run selected test cases:

   ```bash
   go test -json -run ^TestRestRequestErrorHandling$ ./... 2>&1 | gotestfmt --hide=all
   ```

   The `--hide=all` flag suppresses output for skipped and successful tests, showing only failures. For more output configuration options, see the [gotestfmt documentation](https://github.com/GoTestTools/gotestfmt?tab=readme-ov-file#how-do-i-make-the-output-less-verbose).

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

During development, you may need to reset storage to erase bad data due to schema changes. A migration script exists at `scripts/migration/create_migration.go` and breaking changes can be detected with `scripts/compare_storage_structure.go`, but full automatic runtime migrations are not yet implemented. To wipe out storage, run:

```bash
make clean
```

This will remove the `/tmp/ap-avs` directory and the `/tmp/ap.sock` socket file.
