# Ava Protocol AVS — Architecture & Implementation Design

## Overview

Ava Protocol AVS is a decentralized workflow automation system built on EigenLayer. Users define no-code workflows (trigger → nodes → actions) that execute automatically when on-chain or off-chain conditions are met. The system uses ERC-6900 modular smart wallets for on-chain execution and ERC-4337 account abstraction for gasless operation.

## System Architecture

```
┌──────────────────────────────────────────────────────────┐
│                  USER / CLIENT (Studio)                   │
└────────────────────────┬─────────────────────────────────┘
                         │ gRPC
                         ▼
┌──────────────────────────────────────────────────────────┐
│                   AGGREGATOR (Port 2206)                  │
│                                                          │
│  RPC Server ──── Engine ──── Queue + Worker              │
│       │            │              │                       │
│       │            ├─ Task CRUD   ├─ Async job execution  │
│       │            ├─ Trigger     └─ BadgerDB-backed      │
│       │            │   distribution                       │
│       │            ├─ Operator    Fee Estimator            │
│       │            │   management   (node-tier pricing)   │
│       │            └─ Dedup                               │
│       │                                                   │
│  Storage: BadgerDB (tasks, executions, wallets)          │
└────────────────────┬─────────────────────────────────────┘
                     │ gRPC SyncMessages stream
                     ▼
┌──────────────────────────────────────────────────────────┐
│                   OPERATOR(S) (N instances)               │
│                                                          │
│  Worker Loop ──── Trigger Engines ──── RPC Clients       │
│       │                │                    │             │
│       │           ┌────┴────┐          Blockchain RPC    │
│       │           │ Block   │          Tenderly          │
│       │           │ Event   │          Bundler           │
│       │           │ Cron    │                            │
│       │           │ Manual  │                            │
│       │           └─────────┘                            │
│       │                                                  │
│  Task Executor ──── VM (Goja JS) ──── Node Runners      │
│                          │                               │
│                     Smart Wallet (ERC-6900)              │
│                          │                               │
│                     ERC-4337 Bundler → Blockchain        │
└──────────────────────────────────────────────────────────┘
```

## Package Structure

```
├── aggregator/          Aggregator service: RPC server, operator pool, auth
├── operator/            Operator service: worker loop, trigger monitoring, message processing
├── core/
│   ├── taskengine/      Execution engine, VM, node runners, fee estimation
│   ├── apqueue/         BadgerDB-backed async job queue
│   ├── chainio/aa/      Account abstraction (ERC-6900 wallet, UserOp construction)
│   ├── config/          YAML configuration loading and defaults
│   ├── auth/            JWT-based authentication
│   ├── services/        Shared services (Moralis price feeds)
│   └── backup/migrator/ Database schema migrations
├── protobuf/            gRPC service and message definitions
├── storage/             BadgerDB persistence wrapper
├── model/               Data models (Task, User, Secret)
├── pkg/                 Utility packages (erc20, erc4337, eip1559, graphql)
├── contracts/           Smart contract Go bindings
├── cmd/                 CLI entry points (aggregator, operator, register)
└── config/              YAML configuration files per environment
```

## Core Subsystems

### 1. Aggregator

Entry: `aggregator/aggregator.go` → `Start(ctx)`

Startup sequence:
1. Load YAML config, init ETH RPC + chain ID
2. Open BadgerDB, run migrations
3. Start task engine (load existing tasks into memory)
4. Start gRPC server (port 2206)
5. Start HTTP telemetry server

Key files:
- `aggregator/rpc_server.go` — gRPC handlers: CreateTask, DeleteTask, ListTasks, SimulateTask, EstimateFees, GetWallet, etc.
- `aggregator/pool.go` — Operator connection pool management
- `aggregator/auth.go` — JWT auth with EIP-191 signature verification

### 2. Operator

Entry: `operator/operator.go` → `runWorkLoop(ctx)`

The operator connects to the aggregator via gRPC `SyncMessages` stream and:
1. Receives task assignments (trigger configs to monitor)
2. Monitors triggers locally (block headers, contract events, cron schedules)
3. Sends `NotifyTriggers` RPC when conditions are met
4. Handles `ImmediateTrigger` messages for manual execution

Key files:
- `operator/worker_loop.go` — Main event loop, trigger monitoring
- `operator/process_message.go` — Handle SyncMessages commands (monitor, disable, delete, immediate)

Connection resilience: exponential backoff on disconnect (1s → 60s max), error debouncing (same error logged max every 3 min).

### 3. Task Engine

Core: `core/taskengine/engine.go`

The engine manages the full task lifecycle:

```
CreateTask → Store in BadgerDB → Distribute triggers to operators
                                        ↓
                              Operator monitors trigger
                                        ↓
                              NotifyTriggers (with dedup)
                                        ↓
                              Queue execution job
                                        ↓
                              TaskExecutor.RunTask()
                                        ↓
                              Store execution result
```

Key responsibilities:
- **Task CRUD** — create, list, enable/disable, delete
- **Operator management** — streaming assignments, heartbeat tracking
- **Trigger distribution** — batch-send tasks to operators every 3s
- **Deduplication** — `trigger_request_id` tracking prevents duplicate executions
- **Execution queueing** — async job queue backed by BadgerDB

### 4. Trigger System

Location: `core/taskengine/trigger/`

Five trigger types:
| Type | How it works | Dedup key |
|------|-------------|-----------|
| Manual | User/webhook-initiated | N/A |
| FixedTime | One-time at specified epoch | Timestamp |
| Cron | Recurring schedule (cron syntax) | Timestamp |
| Block | Every N blocks | Block number |
| Event | Contract event log matching + conditions | `tx_hash:log_index` |

Event triggers support:
- Multi-address, multi-topic filtering
- ABI-based log decoding
- Condition evaluation on decoded fields (gt, lt, eq, etc.)
- Token metadata enrichment (decimals, symbol via Moralis)
- Safety limits: max events per block, overload reporting to aggregator

### 5. Node Execution

Location: `core/taskengine/vm.go` + `vm_runner_*.go`

The VM executes workflow nodes sequentially using the Goja JavaScript runtime. Each node has access to previous node outputs via `node_name.data`.

Node types and their runners:
| Node | Runner | Purpose |
|------|--------|---------|
| `eth_transfer` | `vm_runner_eth_transfer.go` | Send ETH/ERC20 via smart wallet |
| `contract_write` | `vm_runner_contract_write.go` | Write to contracts (UserOp submission) |
| `contract_read` | `vm_runner_contract_read.go` | Query contract state |
| `rest_api` | `vm_runner_rest.go` | HTTP API calls (SendGrid, external services) |
| `custom_code` | `vm_runner_custom_code.go` | Arbitrary JavaScript execution |
| `graphql_query` | `vm_runner_graphql.go` | GraphQL queries |
| `branch` | `vm_runner_branch.go` | Conditional routing |
| `filter` | `vm_runner_filter.go` | Array filtering |
| `loop` | Loop helpers | Iterate over arrays (sequential or parallel) |
| `balance` | Balance runner | Fetch wallet token balances |

Execution flow for on-chain writes:
1. Resolve variables and template expressions
2. Encode contract call data (ABI encoding)
3. Simulate via Tenderly (optional dry-run)
4. Construct ERC-4337 UserOperation
5. Submit to bundler → EntryPoint → blockchain

### 6. Smart Wallet System

Location: `core/chainio/aa/`

Uses ERC-6900 modular accounts with ERC-4337 account abstraction:
- Factory deploys per-user smart wallets: `factory.createAccount(owner, salt)`
- Operator constructs UserOperations with calldata for contract writes
- Bundler aggregates and submits UserOps to the EntryPoint contract
- Optional paymaster covers gas costs

### 7. Fee Estimation

Location: `core/taskengine/fee_estimator.go`

Fees have two independent components:

**1. Operational costs (estimated separately):**
- **Gas fees** — estimated per on-chain node (contract_write, eth_transfer, loop). Handled by `estimateCOGS()`, which returns `NodeCOGS` entries.
- **External API costs** — estimated per REST API node calling paid services (future). Estimated like gas.
- Non-execution nodes (logic, reads) have zero operational cost.

**2. Value-capture fees (tier-based % of transaction value):**

Only on-chain execution nodes have value-capture fees. Tiers are based on urgency/importance of the action:

| Tier | Criteria | Default % |
|------|----------|-----------|
| Tier 1 | Simple execution — user could do manually | 0.03% |
| Tier 2 | Optimization/convenience — nice to have, delay doesn't cause loss | 0.09% |
| Tier 3 | Risk/urgency — must execute or user loses money | 0.18% |

**Decision rule:** "If this action fails or is delayed, does the user lose money immediately?" YES → Tier 3. NO but improves outcome → Tier 2. Simple → Tier 1. Tiers are pure pricing groups — meaning comes from classification logic, not the label.

Non-execution nodes (contract_read, rest_api, branch, filter, custom_code, balance, graphql) have **no fees** — they are free.

V1: all on-chain nodes default to Tier 1. V2 will classify by protocol + action (e.g., AAVE.repay → Tier 3, Uniswap.swap → Tier 1).

See [docs/FEE_ESTIMATION.md](FEE_ESTIMATION.md) for full details.

Total workflow fee = execution_fee + COGS (gas + API costs) + value_fee (tier % of tx value). Hard costs (execution_fee + COGS) are enforced atomically in the UserOp; value_fee is post-paid.

## Communication Protocol

### Aggregator ↔ Operator (gRPC, defined in `protobuf/node.proto`)

```protobuf
service Node {
  rpc Ping(Checkin) returns (CheckinResp);
  rpc SyncMessages(SyncMessagesReq) returns (stream SyncMessagesResp);
  rpc Ack(AckMessageReq) returns (BoolValue);
  rpc NotifyTriggers(NotifyTriggersReq) returns (NotifyTriggersResp);
  rpc ReportEventOverload(EventOverloadAlert) returns (EventOverloadResponse);
  rpc HealthCheck(HealthCheckRequest) returns (HealthCheckResponse);
}
```

Data flow:
1. **Operator → Aggregator**: `Ping` heartbeat every 5s (uptime, queue depth, block number)
2. **Aggregator → Operator**: `SyncMessages` stream pushes task assignments (monitor, disable, delete, immediate trigger)
3. **Operator → Aggregator**: `NotifyTriggers` when conditions met (includes trigger output data + dedup ID)
4. **Operator → Aggregator**: `ReportEventOverload` if event processing exceeds safety limits

### Client ↔ Aggregator (gRPC, defined in `protobuf/avs.proto`)

Task management, wallet operations, simulation, fee estimation — all via gRPC on port 2206.

## Storage

**Database**: BadgerDB (embedded key-value store)

Key schema:
```
t:a:<taskId>                    Active task
t:c:<taskId>                    Canceled task
t:e:<taskId>                    Expired task
history:<taskId>:<executionId>  Execution record
trigger:<taskId>:<executionId>  Cached trigger output
q:pending:<jobId>               Pending queue job
q:inprogress:<jobId>            In-progress queue job
user:<userAddr>                 User info
wallet:<ownerAddr>              Smart wallet mapping
```

Data format: Protocol Buffer JSON serialization. Migrations run on startup (idempotent).

## External Integrations

| Service | Purpose | Used by |
|---------|---------|---------|
| Moralis API | Token metadata, price feeds | Token enrichment, fee estimation |
| Tenderly | Transaction simulation, gas estimation | Contract write dry-run |
| ERC-4337 Bundler | UserOp submission | On-chain execution |
| EigenLayer contracts | Operator registration, BLS signing | Operator lifecycle |
| SendGrid | Email notifications | REST API node |

## Configuration

Environment-specific YAML files in `config/`:
- `aggregator.example.yaml` — Reference config with all fields documented
- `aggregator-sepolia.yaml`, `aggregator-base.yaml`, `aggregator-ethereum.yaml` — Per-chain configs
- `operator-*.yaml` — Operator configs per chain

Key config sections: smart wallet (RPC, bundler, factory, entrypoint), fee rates (tier-based pricing), macros/secrets (global workflow variables), approved operators list.
