# WARP.md

This file provides guidance to WARP (warp.dev) when working with code in this repository.

## Top Rules

**IMPORTANT**: Do NOT run `git commit` unless explicitly asked to do so in the user's message. Only stage changes with `git add` when appropriate, but never commit automatically.

## Repository Overview

Ava Protocol Automation AVS (Actively Validated Service) on EigenLayer - a task automation system that enables scheduled and triggered execution of on-chain actions through smart wallets. This is a dual-language codebase with Go (backend/operators) and Solidity (smart contracts).

## Development Commands

### Building and Running

```bash
# Build the main binary 
make build

# Build development version
make dev-build

# Run aggregator locally (development)
make dev-agg

# Run operator locally (development) 
make dev-op

# Start docker compose stack
make up
```

### Testing

```bash
# Run all standard tests (excludes long-running integration tests)
make test

# Run tests with coverage
make test/cover

# Run quick tests (no race detection, for fast feedback)
make test/quick

# Run specific package tests
make test/package PKG=./core/taskengine

# Run tests with clean cache
make test/clean

# Run integration tests (often fail, for debugging only)
make test/integration
```

### Code Quality

```bash
# Run linter and quality checks
make audit

# Format code and tidy modules
make tidy

# Run individual tools
golangci-lint run ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

### Protobuf Management

```bash
# Regenerate protobuf bindings after modifying .proto files
make protoc-gen

# Required after changes to protobuf/avs.proto or protobuf/node.proto
```

### Migration and Storage

```bash
# Check for storage structure changes between branches
go run scripts/compare_storage_structure.go main

# Generate migration files for breaking changes
go run scripts/migration/create_migration.go main

# Compare protobuf changes manually
git diff origin/main..staging protobuf/
```

### Development Workflow

```bash
# Live reload during development
make dev-live

# Build unstable Docker image
make unstable-build

# Deploy to production (requires confirmation)
make production/deploy
```

## High-Level Architecture

### Core Components

**Task Engine (`core/taskengine/`)** - The heart of the system that manages task lifecycle, scheduling, and execution. Handles task validation, operator assignment, and execution coordination.

**Aggregator (`aggregator/`)** - The central coordinator that receives task submissions from clients, distributes tasks to operators, aggregates results, and manages consensus. Includes HTTP/gRPC servers and operator connection pooling.

**Operator** - Worker nodes that receive tasks from the aggregator, check trigger conditions, and execute approved tasks. Communicate via gRPC with the aggregator.

**Smart Wallets (ERC6900)** - Each user gets a deployed smart wallet that handles task execution and spending approvals. Enables secure automation without giving up custody.

### Key Data Flow

1. **Task Creation**: Users submit tasks via HTTP API to aggregator
2. **Task Distribution**: Aggregator syncs active tasks to connected operators via gRPC streams
3. **Trigger Monitoring**: Operators continuously check trigger conditions (time, events, blocks)
4. **Execution Queueing**: When triggers fire, tasks are queued for execution in the task engine
5. **Smart Wallet Execution**: Tasks execute through user's ERC6900 smart wallet with AA (Account Abstraction)
6. **State Updates**: Results are recorded and task state is updated across the system

### Trigger System Architecture

**Triggers vs Nodes**: 
- Triggers are entry points (FixedTimeTrigger, CronTrigger, BlockTrigger, EventTrigger, ManualTrigger)
- Nodes process data from preceding triggers/nodes (ETHTransferNode, ContractWriteNode, etc.)
- Triggers have Config (static) but no Input (no predecessors)
- Nodes have Config (static) and Input (from predecessors or runNodeWithInputs RPC)

### Storage and Persistence

**BadgerDB**: Primary storage for tasks, executions, secrets, and operator state
**BigCache**: In-memory caching layer for performance
**Migrations**: Versioned database migrations handle schema changes between versions

### Network Architecture

**Operator Communication**: gRPC bidirectional streaming for real-time task synchronization
**Client API**: HTTP REST API and gRPC for task management
**Blockchain Integration**: Ethereum/Sepolia via JSON-RPC for contract interactions and event monitoring

## Development Guidelines

### Task Engine Development

- Tasks must be validated during creation - use `task.EnsureInitialized()` after loading from storage
- Always check `task.IsRunable()` before execution
- Use proper locking patterns when accessing shared state (`engine.lock`, `engine.streamsMutex`)
- Operator notifications are batched every 3 seconds for efficiency

### Protobuf Changes

- After modifying `.proto` files, always run `make protoc-gen`
- Breaking protobuf changes require manual migration analysis
- Test protobuf changes thoroughly as they affect operator-aggregator communication

### Storage Migrations

- Check for breaking changes before merging to main: `go run scripts/compare_storage_structure.go main`
- Non-backward-compatible changes need migration files
- Focus migrations on data cleanup rather than backward compatibility

### Error Handling

- Use structured logging with context: `logger.Error("message", "key", value)`
- Report critical errors to Sentry in production
- Use circuit breaker patterns for external service calls (RPC, bundlers)
- Graceful degradation when external dependencies fail

### Testing Best Practices

- Use `testutil` package for common test setup
- Mock external dependencies (RPC, bundlers) in unit tests
- Integration tests are excluded from regular runs due to instability
- Test with clean cache when debugging storage issues

### Configuration

- Environment-specific configs in `config/` directory
- Macro variables available in tasks via `{{variableName}}` syntax
- Approved operators configured via `ApprovedOperators` in config
- Smart wallet config includes factory, entrypoint, and paymaster addresses

## Contract Development

The `contracts/` directory contains Solidity smart contracts using Foundry:

```bash
# Navigate to contracts directory
cd contracts

# Build contracts
forge build

# Run contract tests  
forge test

# Deploy scripts available for different environments
./deploy-avs.sh
./deploy-task-manager-impl.sh
```

## Environment Setup

### Prerequisites
- Go 1.22+
- Node.js (for contract tooling)
- Docker and Docker Compose
- foundry (for smart contracts)

### Configuration Files
- `config/operator_sample.yaml` - Operator configuration template
- `config/aggregator.yaml` - Aggregator configuration (development)
- `.env` files for environment-specific variables

### Network Addresses
Refer to README.md for current deployed contract addresses on:
- Ethereum Mainnet
- Sepolia Testnet  
- Holesky Testnet (deprecated)

## Migration and Deployment

Critical migrations are documented in README.md. Before production deployment:

1. Run storage structure comparison
2. Generate migration files for breaking changes
3. Test migrations on staging environment
4. Backup critical data before deployment

Active migrations include token metadata field additions and protobuf structure cleanup that may cancel incompatible workflows.
