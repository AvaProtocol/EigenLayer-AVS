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

### Inspecting Aggregator Data (BadgerDB)

To read the live aggregator's stored task/execution state (e.g. the actual EventTrigger topics array as persisted, not the high-level CreateTask log summary), use the `examples/example.ts` CLI in the `ava-sdk-js` repo. It speaks the gRPC API against whatever endpoint the loaded `.env` points at (`localhost:2206` for the local `dev` aggregator).

```bash
cd <path-to-ava-sdk-js>/examples
yarn start getWorkflow <task_id>     # full task: trigger queries, nodes, edges, inputVariables
yarn start getExecution <task_id> <execution_id>
yarn start listWorkflows
```

The `getWorkflow` output is the ground truth — it shows the trigger's `topics`, `addresses`, and `inputVariables` exactly as stored. Prefer this over inferring state from log lines, since `engine.CreateTask` does not dump the full trigger payload to the aggregator log.

## Code Style

### Go Naming Conventions
- Local variables use `lowerCamelCase`. Never start a local variable or function parameter with an uppercase letter — in Go, uppercase means exported.
- When renaming something, review each change site individually. Don't blindly find-and-replace all substring matches — internal variables that happen to share a substring with an API field are separate concerns.

### Protobuf and API Changes
- Renaming a proto field changes the JSON wire name (e.g., `trigger_input` → `input_variables` changes JSON from `"triggerInput"` to `"inputVariables"`). Treat this as a **breaking API change** — coordinate with SDK/client consumers and document the migration path.
- When renaming a proto field, only update the `.proto` definition and code that directly references the generated Go accessor (e.g., `payload.TriggerInput` → `payload.InputVariables`). Do not rename unrelated internal variables that happen to share a name substring.
- If `make protoc-gen` produces unrelated diffs from a toolchain version change (e.g., `protoc` or `protoc-gen-go` upgrade), split that into a separate commit or PR to keep diffs reviewable.

## Branching and Pull Requests
- All feature branches and PRs must target `staging`, never `main` directly.
- The `main` branch is updated only by merging `staging` → `main` after migration checks pass.
- Before merging to `main`, run `go run scripts/compare_storage_structure.go main` to check for breaking storage changes.
- **PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/) / semantic-release format** (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `ci:`, `perf:`, `BREAKING CHANGE:` footer for majors). Squash merges use the PR title as the commit subject, and `.github/workflows/release-on-pr-close.yml` runs `go-semantic-release` against it to compute the next version. A non-conforming title (e.g. `release: staging → main ...`) yields no version bump and no release. For staging→main release PRs, pick the highest-impact prefix across the bundled changes (e.g. `feat:` if any feature is included).

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
