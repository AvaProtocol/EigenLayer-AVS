# CLAUDE.md

## Repository Overview

Ava Protocol Automation AVS (Actively Validated Service) on EigenLayer - a task automation system that enables scheduled and triggered execution of on-chain actions through smart wallets. Dual-language codebase: Go (backend/operators) and Solidity (smart contracts).

## Development Commands

### Building and Running

```bash
make build           # Build main binary
make dev-build       # Build development version
make dev-agg         # Run aggregator locally
make dev-op          # Run operator locally
make up              # Start docker compose stack
make dev-live        # Live reload during development
```

### Testing

```bash
make test                              # All standard tests (excludes long-running integration tests)
make test/cover                        # Tests with coverage
make test/quick                        # No race detection, fast feedback
make test/package PKG=./core/taskengine # Specific package tests
make test/clean                        # Tests with clean cache
make test/integration                  # Integration tests (often fail, debugging only)
```

### Code Quality

```bash
make audit    # Linter and quality checks
make tidy     # Format code and tidy modules
```

### Protobuf

```bash
make protoc-gen  # Regenerate protobuf bindings after modifying .proto files
```

### Storage Migrations

```bash
go run scripts/compare_storage_structure.go main       # Check for breaking changes
go run scripts/migration/create_migration.go main      # Generate migration files
```

### Contracts (Foundry)

```bash
cd contracts && forge build   # Build contracts
cd contracts && forge test    # Run contract tests
```

## High-Level Architecture

- **Task Engine (`core/taskengine/`)** - Manages task lifecycle, scheduling, and execution
- **Aggregator (`aggregator/`)** - Central coordinator: receives tasks, distributes to operators, aggregates results
- **Operator** - Worker nodes that check trigger conditions and execute tasks via gRPC
- **Smart Wallets (ERC6900)** - Per-user wallets for secure automated execution via Account Abstraction

### Data Flow

1. Users submit tasks via HTTP API to aggregator
2. Aggregator syncs active tasks to operators via gRPC streams
3. Operators monitor trigger conditions (time, events, blocks)
4. Triggered tasks queue for execution through user's ERC6900 smart wallet
5. Results recorded and state updated across the system

### Trigger System

- **Triggers**: Entry points (FixedTimeTrigger, CronTrigger, BlockTrigger, EventTrigger, ManualTrigger) - have Config but no Input
- **Nodes**: Process data from predecessors (ETHTransferNode, ContractWriteNode, etc.) - have Config and Input

### Storage

- **BadgerDB**: Primary storage for tasks, executions, secrets, operator state
- **BigCache**: In-memory caching layer
- **Migrations**: Versioned database migrations for schema changes

## Development Guidelines

- After modifying `.proto` files, always run `make protoc-gen`
- Breaking protobuf changes require manual migration analysis
- Check for breaking storage changes before merging: `go run scripts/compare_storage_structure.go main`
- Use structured logging: `logger.Error("message", "key", value)`
- Critical errors reported to Sentry in production
- Use `testutil` package for common test setup
- Integration tests excluded from regular runs due to instability

### Go Test Standards for Node Execution

- **Never use `vm.AddVar()` to inject test data directly** — this bypasses the real execution path and hides bugs.
- **Single-node tests**: Use `vm.RunNodeWithInputs(node, inputVariables)` to test a node in isolation. Pass input data via the `inputVariables` map, which mirrors how the SDK's `runNodeWithInputs` API works.
- **Workflow tests**: Build a proper task with preceding nodes (e.g., a CustomCode node that outputs test data), connect them with edges, and execute the full workflow. This tests the real data flow between nodes.
- **Template variables**: Loop and Filter nodes use `inputVariable` with `{{variable.path}}` syntax (e.g., `"{{settings.address_list}}"`, `"{{custom_code1.data}}"`). Tests must use this format, not bare variable names.
- Macro variables in tasks use `{{variableName}}` syntax
- Operator notifications are batched every 3 seconds

## Environment Setup

- Go 1.22+, Node.js, Docker/Docker Compose, Foundry
- `config/operator_sample.yaml` - Operator config template
- `config/aggregator.yaml` - Aggregator config (development)
- Contract addresses in README.md (Ethereum Mainnet, Sepolia Testnet)
