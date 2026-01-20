# Context-Memory API Request Refactoring Plan

## Overview

This document outlines the plan to refactor the context-memory API request building logic to reuse existing output extraction patterns from the codebase, ensuring format consistency with `ListExecutions` and other execution-related functionality.

## Rationale

### Current Problem

The `buildRequest()` function in `summarizer_context_memory.go` has **incomplete data extraction**:

1. **Trigger Config** - Only extracts from `log.GetConfig()` which works, but misses consistency with `TaskTriggerToConfig()` used elsewhere
2. **Node Config** - Only extracts `contractAddress`, `methodName`, `methodParams` for contract nodes; **misses** RestAPI, Loop, CustomCode, GraphQL, Balance, Branch, Filter node configurations
3. **Execution Output** - Only extracts `ContractRead.Data` and `ContractWrite.Data`; **misses all 13 other output types**:
   - Trigger outputs: BlockTrigger, FixedTimeTrigger, CronTrigger, EventTrigger, ManualTrigger
   - Node outputs: ETHTransfer, GraphQL, CustomCode, RestAPI, Branch, Filter, Loop, Balance

### Current Incomplete Implementation (lines 298-304)

```go
// Only handles 2 of 15 output types!
if contractRead := log.GetContractRead(); contractRead != nil && contractRead.Data != nil {
    step.OutputData = contractRead.Data.AsInterface()
}
if contractWrite := log.GetContractWrite(); contractWrite != nil && contractWrite.Data != nil {
    step.OutputData = contractWrite.Data.AsInterface()
}
```

### Benefits of Refactoring

1. **Complete Data**: Extract ALL trigger and node outputs, not just contract read/write
2. **Code Reuse**: Leverage existing `TaskTriggerToConfig()` and `ExtractNodeConfiguration()` patterns
3. **Consistency**: Ensure context-memory API receives the same data format as `ListExecutions` RPC
4. **Maintainability**: Single source of truth for output extraction logic
5. **Future-Proofing**: Easy to add new node types without modifying multiple places

## Existing Reusable Code

### Available Functions to Leverage

| Function | Location | Purpose | Reusable For |
|----------|----------|---------|--------------|
| `TaskTriggerToConfig` | `node_types.go:202` | Extracts trigger config for all trigger types | Trigger config extraction |
| `ExtractNodeConfiguration` | `vm.go:3379` | Extracts node config for all node types | Node config extraction |
| `CreateNodeExecutionStep` | `node_utils.go:102` | Uses above functions to populate step config | Pattern reference |

### Execution_Step output_data oneof (avs.proto:713-732)

All output types that need extraction:

**Trigger Outputs (5 types):**
```go
BlockTrigger.Output       // block_trigger
FixedTimeTrigger.Output   // fixed_time_trigger
CronTrigger.Output        // cron_trigger
EventTrigger.Output       // event_trigger
ManualTrigger.Output      // manual_trigger
```

**Node Outputs (10 types):**
```go
ETHTransferNode.Output    // eth_transfer
GraphQLQueryNode.Output   // graphql
ContractReadNode.Output   // contract_read ✅ Currently handled
ContractWriteNode.Output  // contract_write ✅ Currently handled
CustomCodeNode.Output     // custom_code
RestAPINode.Output        // rest_api
BranchNode.Output         // branch
FilterNode.Output         // filter
LoopNode.Output           // loop
BalanceNode.Output        // balance
```

## Current State

### Current Implementation

**Location**: `core/taskengine/summarizer_context_memory.go`

**Current Flow**:
```
VM (with ExecutionLogs) 
  → buildRequest(vm, currentStepName)
    → Extract workflow context (ownerEOA, smartWallet, chainName) from VM vars
    → Convert vm.ExecutionLogs to contextMemoryStepDigest
    → Extract nodes/edges from vm.TaskNodes and vm.task.Task.Edges
    → Extract settings from vm.vars["settings"]
    → Compute token metadata
    → Return contextMemorySummarizeRequest
```

**Key Components**:
- `buildRequest(vm *VM, currentStepName string)` - Main function
- Step conversion logic (lines 226-317) - Converts `Execution_Step` → `contextMemoryStepDigest`
- Token metadata collection (lines 361-412) - Computes token metadata map
- Node/edge extraction (lines 319-345) - Converts TaskNodes/TaskEdges to simple format

### Execution Protobuf Structure

**From**: `protobuf/avs.proto` (lines 674-741)

```protobuf
message Execution {
  string id = 1;
  int64 start_at = 2;
  int64 end_at = 3;
  ExecutionStatus status = 4;
  string error = 5;
  int64 index = 6;
  string total_gas_cost = 7;
  
  message Step {
    string id = 1;
    string type = 17;
    string name = 18;
    bool success = 2;
    string error = 13;
    google.protobuf.Value config = 19;
    google.protobuf.Value metadata = 25;
    google.protobuf.Value execution_context = 26;
    oneof output_data { ... }
    // ... other fields
  }
  
  repeated Step steps = 8;
}
```

**Key Observation**: `Execution.Steps` are identical to `vm.ExecutionLogs` - both are `Execution_Step` protobufs.

### Context-Memory API Request Format

**From**: `context-memory/types/index.ts`

```typescript
interface SummarizeRequest {
  ownerEOA: string;
  name: string;
  smartWallet: string;
  steps: StepDigest[];
  chainName?: string;
  nodes?: Array<{id: string; name: string}>;
  edges?: Array<{id: string; source: string; target: string}>;
  settings?: Record<string, unknown>;
  currentNodeName?: string;
  tokenMetadata?: Record<string, TokenMetadata>;
}
```

## Analysis

### What Can Be Reused

✅ **Step Conversion Logic** (100% reusable)
- `Execution.Steps` are the same `Execution_Step` protobufs as `vm.ExecutionLogs`
- The conversion logic from `Execution_Step` → `contextMemoryStepDigest` can be extracted into a shared function
- Both sources have identical structure: `config`, `metadata`, `execution_context`, `output_data`

✅ **Step Structure**
- Both use the same protobuf structure
- Same fields: `id`, `type`, `name`, `success`, `error`, `config`, `metadata`, `execution_context`
- Same `output_data` oneof structure

### What Cannot Be Reused Directly

❌ **Top-Level Execution Fields**
- Execution protobuf doesn't include: `ownerEOA`, `smartWallet`, `chainName`
- These are extracted from VM vars (`workflowContext`, `settings`)

❌ **Workflow Definition Data**
- Execution doesn't include: `nodes`, `edges`
- These come from Task definition (`vm.TaskNodes`, `vm.task.Task.Edges`)

❌ **Workflow Settings**
- Execution doesn't include: `settings`
- This comes from `vm.vars["settings"]`

❌ **Token Metadata**
- Execution doesn't include: `tokenMetadata`
- This is computed from step data and settings (requires token enrichment service)

❌ **Current Node Name**
- Execution doesn't include: `currentNodeName`
- This is passed as a parameter to `buildRequest()`

### Data Source Comparison

| Field | VM Source | Execution Source | Reusable? |
|-------|-----------|-----------------|-----------|
| `steps` | `vm.ExecutionLogs` | `exec.Steps` | ✅ Yes (same protobuf) |
| `ownerEOA` | `vm.vars[workflowContext]["owner"]` | ❌ Not stored | ❌ No |
| `smartWallet` | `vm.vars[workflowContext]["runner"]` | ❌ Not stored | ❌ No |
| `chainName` | `vm.vars["settings"]["chain"]` | ❌ Not stored | ❌ No |
| `name` | `vm.vars["settings"]["name"]` | ❌ Not stored | ❌ No |
| `nodes` | `vm.TaskNodes` | ❌ Not stored | ❌ No |
| `edges` | `vm.task.Task.Edges` | ❌ Not stored | ❌ No |
| `settings` | `vm.vars["settings"]` | ❌ Not stored | ❌ No |
| `tokenMetadata` | Computed from steps | ❌ Not stored | ❌ No |
| `currentNodeName` | Parameter | ❌ Not stored | ❌ No |

## Recommended Approach

### Strategy: Two Separate Functions for Config and Output

Create two simple, focused functions in `node_utils.go`:
1. `ExtractStepConfig(step, taskNodes)` - extracts config using TaskNodes for full node definition
2. `ExtractStepOutput(step)` - extracts output from the oneof field

### Implementation Plan

#### Phase 1: Create Two Separate Extraction Functions

**Location:** `core/taskengine/node_utils.go`

```go
// ExtractStepConfig extracts the configuration for a step.
// For trigger steps: returns trigger config from TaskTrigger definition
// For node steps: returns full node config from TaskNodes
//
// Parameters:
//   - step: The execution step
//   - taskNodes: Map of node ID to TaskNode definition (from vm.TaskNodes)
//   - trigger: The task trigger definition (from vm.task.Task.Trigger)
func ExtractStepConfig(
    step *avsproto.Execution_Step,
    taskNodes map[string]*avsproto.TaskNode,
    trigger *avsproto.TaskTrigger,
) interface{} {
    if step == nil {
        return nil
    }

    // Check if this is a trigger step
    if isTriggerStep(step.GetType()) {
        // Use TaskTriggerToConfig for trigger definition
        return TaskTriggerToConfig(trigger)  // Reuse existing function!
    }

    // For node steps, use TaskNodes for complete definition
    if taskNode, exists := taskNodes[step.GetId()]; exists && taskNode != nil {
        return ExtractNodeConfiguration(taskNode)  // Reuse existing function!
    }

    // Fallback to step.Config (when TaskNode not available)
    if step.GetConfig() != nil {
        return step.GetConfig().AsInterface()
    }

    return nil
}

// isTriggerStep checks if the step type is a trigger type
func isTriggerStep(stepType string) bool {
    upper := strings.ToUpper(stepType)
    return strings.Contains(upper, "TRIGGER_TYPE_")
}

// ExtractStepOutput extracts the output data from an Execution_Step.
// Handles all 15 output types (5 trigger + 10 node).
func ExtractStepOutput(step *avsproto.Execution_Step) interface{} {
    if step == nil {
        return nil
    }

    // Handle trigger outputs
    if out := step.GetBlockTrigger(); out != nil {
        return map[string]interface{}{
            "block_number": out.BlockNumber,
            "block_hash":   out.BlockHash,
            "timestamp":    out.Timestamp,
        }
    }
    if out := step.GetFixedTimeTrigger(); out != nil {
        return map[string]interface{}{
            "epoch": out.Epoch,
        }
    }
    if out := step.GetCronTrigger(); out != nil {
        return map[string]interface{}{
            "epoch": out.Epoch,
        }
    }
    if out := step.GetEventTrigger(); out != nil {
        return extractEventTriggerOutput(out)
    }
    if out := step.GetManualTrigger(); out != nil {
        if out.Data != nil {
            return out.Data.AsInterface()
        }
        return nil
    }

    // Handle node outputs
    if out := step.GetContractRead(); out != nil && out.Data != nil {
        return out.Data.AsInterface()
    }
    if out := step.GetContractWrite(); out != nil && out.Data != nil {
        return out.Data.AsInterface()
    }
    if out := step.GetEthTransfer(); out != nil {
        return extractEthTransferOutput(out)
    }
    if out := step.GetGraphql(); out != nil && out.Data != nil {
        return out.Data.AsInterface()
    }
    if out := step.GetCustomCode(); out != nil && out.Data != nil {
        return out.Data.AsInterface()
    }
    if out := step.GetRestApi(); out != nil && out.Data != nil {
        return out.Data.AsInterface()
    }
    if out := step.GetBranch(); out != nil {
        return map[string]interface{}{
            "condition_met": out.ConditionMet,
        }
    }
    if out := step.GetFilter(); out != nil && out.Data != nil {
        return out.Data.AsInterface()
    }
    if out := step.GetLoop(); out != nil {
        return extractLoopOutput(out)
    }
    if out := step.GetBalance(); out != nil {
        return extractBalanceOutput(out)
    }

    return nil
}

// Helper functions for complex output types
func extractEventTriggerOutput(out *avsproto.EventTrigger_Output) interface{} {
    // Extract events, transaction hash, block info
}

func extractEthTransferOutput(out *avsproto.ETHTransferNode_Output) interface{} {
    // Extract transaction hash and other fields
}

func extractLoopOutput(out *avsproto.LoopNode_Output) interface{} {
    // Handle loop iterations
}

func extractBalanceOutput(out *avsproto.BalanceNode_Output) interface{} {
    // Extract balance data
}
```

**Key design decisions:**
1. `ExtractStepConfig` takes `taskNodes` as input to access full node definitions
2. `ExtractStepConfig` reuses existing `ExtractNodeConfiguration()` for node steps
3. `ExtractStepOutput` is standalone - only needs the step
4. Two separate functions keep the code simple and focused

#### Phase 2: Replace buildRequest Step Conversion

The current implementation (lines 238-315) is **redundant** - it re-parses data that's already available:

| Current Code | What It Does | Problem |
|--------------|--------------|---------|
| Lines 238-275 | Re-parses `log.GetConfig()` to extract partial fields | Config is already complete from `ExtractNodeConfiguration()` |
| Lines 298-304 | Extracts only ContractRead/ContractWrite output | Missing 13 output types |

**Full replacement using two separate functions:**

```go
// OLD (lines 226-316) - 90 lines of partial extraction:
for _, log := range vm.ExecutionLogs {
    step := contextMemoryStepDigest{...}
    // ... 90 lines of manual parsing ...
}

// NEW - Clean extraction via two focused functions:
trigger := vm.task.Task.Trigger  // Get trigger definition once

for _, log := range vm.ExecutionLogs {
    step := contextMemoryStepDigest{
        Name:             log.GetName(),
        ID:               log.GetId(),
        Type:             log.GetType(),
        Success:          log.GetSuccess(),
        Error:            log.GetError(),
        Config:           ExtractStepConfig(log, vm.TaskNodes, trigger),  // Full config
        OutputData:       ExtractStepOutput(log),                          // All 15 output types
        Metadata:         log.GetMetadata().AsInterface(),
        ExecutionContext: log.GetExecutionContext().AsInterface(),
    }

    // Token metadata lookup (value-add logic, not extraction)
    step.TokenMetadata = lookupTokenMetadata(step.Config, log)

    steps = append(steps, step)
}
```

**Key points:**
1. `ExtractStepConfig(log, vm.TaskNodes, trigger)` - uses TaskNodes for nodes, TaskTriggerToConfig for triggers
2. `ExtractStepOutput(log)` - handles all 15 output types
3. Two separate calls keep the code simple and readable

**contextMemoryStepDigest struct change (Option B - replace entirely):**

```go
// OLD - Partial fields for contract nodes only:
type contextMemoryStepDigest struct {
    ContractAddress  string                 `json:"contractAddress,omitempty"`   // REMOVE
    MethodName       string                 `json:"methodName,omitempty"`        // REMOVE
    MethodParams     map[string]interface{} `json:"methodParams,omitempty"`      // REMOVE
    TriggerConfig    interface{}            `json:"triggerConfig,omitempty"`     // REMOVE
    // ...
}

// NEW - Unified config field for all node/trigger types:
type contextMemoryStepDigest struct {
    Name             string                      `json:"name"`
    ID               string                      `json:"id"`
    Type             string                      `json:"type"`
    Success          bool                        `json:"success"`
    Error            string                      `json:"error,omitempty"`
    Config           interface{}                 `json:"config,omitempty"`           // Full config (trigger or node)
    OutputData       interface{}                 `json:"outputData,omitempty"`       // Full output (all 15 types)
    Metadata         interface{}                 `json:"metadata,omitempty"`
    ExecutionContext interface{}                 `json:"executionContext,omitempty"`
    TokenMetadata    *contextMemoryTokenMetadata `json:"tokenMetadata,omitempty"`    // Post-processing enrichment
}
```

**Note:** This requires updating the context-memory API to handle the new `Config` field instead of the old partial fields.

#### Phase 3: Add Tests for ExtractStepConfig and ExtractStepOutput

**Location:** `core/taskengine/extract_step_output_test.go` (new file)

```go
// Tests for ExtractStepConfig
func TestExtractStepConfig_WithTaskNodes(t *testing.T) {
    // Test that config is extracted from TaskNodes when available
}

func TestExtractStepConfig_FallbackToStepConfig(t *testing.T) {
    // Test fallback to step.Config when TaskNode not found
}

func TestExtractStepConfig_TriggerStep(t *testing.T) {
    // Test trigger config extraction
}

// Tests for ExtractStepOutput - all 15 output types
func TestExtractStepOutput_TriggerOutputs(t *testing.T) {
    // Test BlockTrigger.Output
    // Test FixedTimeTrigger.Output
    // Test CronTrigger.Output
    // Test EventTrigger.Output (complex - has events array)
    // Test ManualTrigger.Output
}

func TestExtractStepOutput_NodeOutputs(t *testing.T) {
    // Test ContractReadNode.Output
    // Test ContractWriteNode.Output
    // Test ETHTransferNode.Output
    // Test GraphQLQueryNode.Output
    // Test CustomCodeNode.Output
    // Test RestAPINode.Output
    // Test BranchNode.Output
    // Test FilterNode.Output
    // Test LoopNode.Output (complex with iterations)
    // Test BalanceNode.Output
}

func TestExtractStepOutput_NilStep(t *testing.T) {
    // Verify nil handling
}

func TestExtractStepOutput_ProtobufCompatibility(t *testing.T) {
    // Verify output can be converted to structpb.Value
}
```

**Note:**
- Existing tests in `extract_node_config_test.go` cover `ExtractNodeConfiguration()`
- `ExtractStepConfig` reuses `ExtractNodeConfiguration()`, so it inherits that test coverage
- New tests focus on **output extraction** and **config fallback logic**

## Files to Modify

| File | Changes |
|------|---------|
| `core/taskengine/node_utils.go` | Add `ExtractStepConfig()` and `ExtractStepOutput()` functions |
| `core/taskengine/summarizer_context_memory.go` | Update `buildRequest()` to use the two new functions |
| `core/taskengine/extract_step_output_test.go` | New file: tests for both functions |

## Existing Test Coverage

| File | Tests | Status |
|------|-------|--------|
| `extract_node_config_test.go` | `ExtractNodeConfiguration()` for all node types | ✅ Exists (reused by `ExtractStepConfig`) |
| `extract_step_output_test.go` | `ExtractStepConfig()` + `ExtractStepOutput()` | ❌ Needs creation |

## Testing Strategy

1. **Unit tests for `ExtractStepConfig()`** covering:
   - Trigger step → uses `TaskTriggerToConfig()`
   - Node step with TaskNode → uses `ExtractNodeConfiguration()`
   - Node step without TaskNode → falls back to `step.GetConfig()`
   - Nil step handling

2. **Unit tests for `ExtractStepOutput()`** covering:
   - All 5 trigger output types
   - All 10 node output types
   - Nil step handling
   - Protobuf compatibility (can convert to `structpb.Value`)

3. **Integration tests** verifying `buildRequest()` produces correct output for:
   - Block trigger executions
   - Event trigger executions (with decoded events)
   - Contract read/write nodes
   - REST API nodes
   - Custom code nodes
   - Loop nodes with iterations
   - Balance nodes

## Design Decisions

1. **contextMemoryStepDigest struct change**:
   - **Decision**: Option B - Replace old fields entirely
   - Remove: `ContractAddress`, `MethodName`, `MethodParams`, `TriggerConfig`
   - Add: `Config` (unified field for all node/trigger types)
   - **Requires**: context-memory API update to handle new format

2. **Loop output handling**:
   - **Decision**: Simple solution - each iteration as nested data in the loop step's output
   - Complex cases (e.g., parallel execution results) can be handled later

3. **Token metadata lookup**:
   - **Decision**: Keep in `buildRequest` as post-processing
   - Separate concern from extraction (single responsibility)

4. **Trigger steps data flow**:
   - **Trigger Config**: From `vm.task.Task.Trigger` via `TaskTriggerToConfig()` (the trigger definition)
   - **Trigger Output**: From execution step oneof (e.g., `step.GetBlockTrigger()`) - data from operator notification
   - `ExtractStepConfig` should use `TaskTriggerToConfig(vm.task.Task.Trigger)` for trigger steps
   - `ExtractStepOutput` extracts the trigger output that operator sent back

## Migration Path

| Phase | Scope | Files | Priority |
|-------|-------|-------|----------|
| Phase 1 | Create `ExtractStepConfig()` and `ExtractStepOutput()` | `node_utils.go` | High |
| Phase 2 | Update `buildRequest()` to use the two new functions | `summarizer_context_memory.go` | High |
| Phase 3 | Add tests for config fallback and all 15 output types | `extract_step_output_test.go` | High |

## Conclusion

### Summary of Current Issues

| Data Type | Current Code | Problem |
|-----------|--------------|---------|
| **Config** | Lines 238-275: Re-parses `log.GetConfig()` | Redundant - already complete from `ExtractNodeConfiguration()` |
| **Node Config** | Only extracts contract fields | Loses RestAPI, Loop, CustomCode, GraphQL, Balance, Branch, Filter configs |
| **Output** | Lines 298-304: Only 2 types | Missing 5 trigger + 8 node outputs |

### Root Cause

The current `buildRequest` implementation doesn't recognize that full node config is available via `vm.TaskNodes`. The code redundantly re-parses `log.GetConfig()` and loses data by only extracting contract-specific fields.

### Recommended Action

**Create two focused functions and replace the step conversion logic:**

| Phase | Action | Files |
|-------|--------|-------|
| Phase 1 | Create `ExtractStepConfig(step, taskNodes)` and `ExtractStepOutput(step)` | `node_utils.go` |
| Phase 2 | Replace 90 lines of manual parsing with two function calls | `summarizer_context_memory.go` |
| Phase 3 | Add tests for config fallback and all 15 output types | `extract_step_output_test.go` |

### Benefits

- ✅ **Simple & Clean**: Two focused functions, single responsibility each
- ✅ **Code Reuse**: `ExtractStepConfig` reuses existing `ExtractNodeConfiguration()`
- ✅ **Complete Data**: Full config (via TaskNodes) + all 15 output types
- ✅ **Code Reduction**: ~90 lines → ~10 lines in `buildRequest`
- ✅ **No Data Loss**: Full node config preserved (not just contract fields)
- ✅ **Maintainability**: Add new node type once, works everywhere

### Data Flow After Refactoring

```
buildRequest():
  trigger = vm.task.Task.Trigger

  for log in vm.ExecutionLogs:
    Config = ExtractStepConfig(log, vm.TaskNodes, trigger)
      → if isTriggerStep: TaskTriggerToConfig(trigger)        // Trigger definition
      → elif taskNodes[log.Id] exists: ExtractNodeConfiguration(taskNode)  // Node definition
      → else: log.GetConfig().AsInterface()                    // Fallback

    Output = ExtractStepOutput(log)
      → handles all 15 output types from oneof
      → for triggers: data from operator notification (block_trigger, event_trigger, etc.)
      → for nodes: execution results (contract_read, contract_write, rest_api, etc.)
```

### Existing Code to Leverage

| Function | Location | Reuse |
|----------|----------|-------|
| `ExtractNodeConfiguration()` | `vm.go:3379` | Called by `ExtractStepConfig()` for node steps |
| `TaskTriggerToConfig()` | `node_types.go:202` | Already used during execution |
| `CreateNodeExecutionStep()` | `node_utils.go:102` | Sets `step.Config` using above functions |

### Existing Test Reference

Tests in `extract_node_config_test.go` cover `ExtractNodeConfiguration()` - this coverage is inherited by `ExtractStepConfig()` since it reuses that function.

---

## Context-Memory API Migration Guide

### Breaking Changes in Step Schema

The `StepDigest` structure has been updated to use a unified `config` field instead of partial extraction fields.

**Old Schema (Deprecated)**:
```typescript
interface StepDigest {
  name: string;
  id: string;
  type: string;
  success: boolean;
  error?: string;
  contractAddress?: string;      // ❌ REMOVED
  methodName?: string;           // ❌ REMOVED
  methodParams?: Record<string, unknown>;  // ❌ REMOVED
  triggerConfig?: unknown;       // ❌ REMOVED
  stepDescription?: string;      // ❌ REMOVED
  outputData?: unknown;
  metadata?: unknown;
  executionContext?: unknown;
  tokenMetadata?: TokenMetadata;
}
```

**New Schema**:
```typescript
interface StepDigest {
  name: string;
  id: string;
  type: string;
  success: boolean;
  error?: string;
  config?: unknown;              // ✅ NEW: Full config for ALL node/trigger types
  outputData?: unknown;          // ✅ ENHANCED: Now includes all 15 output types
  metadata?: unknown;
  executionContext?: unknown;
  tokenMetadata?: TokenMetadata;
}
```

### Migration Instructions

The context-memory API should update its TypeScript types and parsing logic:

1. **Remove deprecated field handling**:
   - `contractAddress`, `methodName`, `methodParams` → extract from `config.contractAddress`, `config.methodCalls[0].methodName`, etc.
   - `triggerConfig` → now unified in `config` field for trigger steps

2. **Update config extraction logic**:
   - For trigger steps (`type` contains `TRIGGER_TYPE_`): `config` contains trigger definition (queries, intervals, schedules, etc.)
   - For node steps: `config` contains full node configuration from `ExtractNodeConfiguration()`

3. **Update output handling**:
   - `outputData` now includes all 15 output types (was only ContractRead/ContractWrite before)
   - Trigger outputs: block data, event data, cron/fixed-time epochs, manual trigger data
   - Node outputs: contract results, REST API responses, GraphQL data, custom code results, etc.

### Full Example Request

Below is a complete example of the new request format with various step types:

```json
{
  "ownerEOA": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
  "name": "Uniswap Token Swap Workflow",
  "smartWallet": "0x1234567890123456789012345678901234567890",
  "chainName": "ethereum",
  "currentNodeName": "swap_tokens",
  "settings": {
    "name": "Uniswap Token Swap Workflow",
    "chain": "ethereum",
    "runner": "0x1234567890123456789012345678901234567890",
    "isSimulation": false,
    "uniswapv3_pool": {
      "tokens": {
        "input": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
        "output": "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"
      }
    }
  },
  "nodes": [
    {"id": "trigger", "name": "EventTrigger"},
    {"id": "check_balance", "name": "CheckBalance"},
    {"id": "approve_token", "name": "ApproveToken"},
    {"id": "swap_tokens", "name": "SwapTokens"}
  ],
  "edges": [
    {"id": "e1", "source": "trigger", "target": "check_balance"},
    {"id": "e2", "source": "check_balance", "target": "approve_token"},
    {"id": "e3", "source": "approve_token", "target": "swap_tokens"}
  ],
  "steps": [
    {
      "name": "EventTrigger",
      "id": "trigger",
      "type": "TRIGGER_TYPE_EVENT",
      "success": true,
      "config": {
        "queries": [
          {
            "addresses": ["0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"],
            "topics": [
              ["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"]
            ]
          }
        ],
        "cooldown_seconds": 300
      },
      "outputData": {
        "tx_hash": "0xabc123def456...",
        "block_number": 18500000,
        "events": [
          {
            "name": "Transfer",
            "address": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
            "args": {
              "from": "0x...",
              "to": "0x...",
              "value": "1000000000"
            }
          }
        ]
      },
      "metadata": null,
      "executionContext": {
        "is_simulated": false,
        "provider": "chainRPC"
      }
    },
    {
      "name": "CheckBalance",
      "id": "check_balance",
      "type": "NODE_TYPE_BALANCE",
      "success": true,
      "config": {
        "address": "0x1234567890123456789012345678901234567890",
        "chain": "ethereum",
        "includeNativeBalance": true,
        "tokenAddresses": ["0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"]
      },
      "outputData": {
        "nativeBalance": "1500000000000000000",
        "tokens": [
          {
            "tokenAddress": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
            "balance": "5000000000",
            "symbol": "USDC",
            "decimals": 6
          }
        ]
      },
      "metadata": null,
      "executionContext": {
        "is_simulated": false,
        "provider": "chainRPC"
      }
    },
    {
      "name": "ApproveToken",
      "id": "approve_token",
      "type": "NODE_TYPE_CONTRACT_WRITE",
      "success": true,
      "config": {
        "contractAddress": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
        "methodCalls": [
          {
            "methodName": "approve",
            "methodParams": [
              "0x68b3465833fb72A70ecDF485E0e4C7bD8665Fc45",
              "1000000000"
            ]
          }
        ],
        "contractAbi": [
          {
            "inputs": [
              {"name": "spender", "type": "address"},
              {"name": "amount", "type": "uint256"}
            ],
            "name": "approve",
            "outputs": [{"name": "", "type": "bool"}],
            "stateMutability": "nonpayable",
            "type": "function"
          }
        ]
      },
      "outputData": {
        "approve": {
          "success": true,
          "tx_hash": "0x789abc...",
          "receipt": {
            "status": "0x1",
            "blockNumber": 18500001,
            "gasUsed": "46000"
          }
        }
      },
      "metadata": [
        {
          "methodName": "approve",
          "success": true,
          "receipt": {
            "status": "0x1",
            "to": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
            "blockNumber": 18500001
          }
        }
      ],
      "executionContext": {
        "is_simulated": false,
        "provider": "chainRPC",
        "chain_id": 1
      },
      "tokenMetadata": {
        "symbol": "USDC",
        "decimals": 6,
        "name": "USD Coin"
      }
    },
    {
      "name": "SwapTokens",
      "id": "swap_tokens",
      "type": "NODE_TYPE_CONTRACT_WRITE",
      "success": true,
      "config": {
        "contractAddress": "0x68b3465833fb72A70ecDF485E0e4C7bD8665Fc45",
        "methodCalls": [
          {
            "methodName": "exactInputSingle",
            "methodParams": [
              {
                "tokenIn": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
                "tokenOut": "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2",
                "fee": 3000,
                "recipient": "0x1234567890123456789012345678901234567890",
                "amountIn": "1000000000",
                "amountOutMinimum": "400000000000000000",
                "sqrtPriceLimitX96": 0
              }
            ]
          }
        ],
        "contractAbi": [
          {
            "inputs": [{"components": [/*...*/], "name": "params", "type": "tuple"}],
            "name": "exactInputSingle",
            "outputs": [{"name": "amountOut", "type": "uint256"}],
            "stateMutability": "payable",
            "type": "function"
          }
        ]
      },
      "outputData": {
        "exactInputSingle": {
          "success": true,
          "tx_hash": "0xdef789...",
          "amountOut": "450000000000000000",
          "receipt": {
            "status": "0x1",
            "blockNumber": 18500002,
            "gasUsed": "185000"
          }
        }
      },
      "metadata": [
        {
          "methodName": "exactInputSingle",
          "success": true,
          "receipt": {
            "status": "0x1",
            "to": "0x68b3465833fb72A70ecDF485E0e4C7bD8665Fc45",
            "blockNumber": 18500002
          }
        }
      ],
      "executionContext": {
        "is_simulated": false,
        "provider": "chainRPC",
        "chain_id": 1
      }
    }
  ],
  "tokenMetadata": {
    "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48": {
      "symbol": "USDC",
      "decimals": 6,
      "name": "USD Coin"
    },
    "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2": {
      "symbol": "WETH",
      "decimals": 18,
      "name": "Wrapped Ether"
    }
  }
}
```

### Config Structure by Step Type

#### Trigger Steps (type contains `TRIGGER_TYPE_`)

| Trigger Type | Config Structure |
|--------------|------------------|
| `TRIGGER_TYPE_EVENT` | `{ queries: [...], cooldown_seconds?: number }` |
| `TRIGGER_TYPE_BLOCK` | `{ interval: number }` |
| `TRIGGER_TYPE_CRON` | `{ schedules: string[] }` |
| `TRIGGER_TYPE_FIXED_TIME` | `{ epochs: number[] }` |
| `TRIGGER_TYPE_MANUAL` | `{ data?: any, headers?: object, pathParams?: object, lang?: string }` |

#### Node Steps (type contains `NODE_TYPE_`)

| Node Type | Config Structure |
|-----------|------------------|
| `NODE_TYPE_CONTRACT_READ` | `{ contractAddress, methodCalls: [...], contractAbi: [...] }` |
| `NODE_TYPE_CONTRACT_WRITE` | `{ contractAddress, methodCalls: [...], contractAbi: [...], isSimulated? }` |
| `NODE_TYPE_REST_API` | `{ url, method, headers?, body?, options? }` |
| `NODE_TYPE_GRAPHQL_QUERY` | `{ url, query, variables? }` |
| `NODE_TYPE_CUSTOM_CODE` | `{ lang, source }` |
| `NODE_TYPE_ETH_TRANSFER` | `{ destination, amount }` |
| `NODE_TYPE_BALANCE` | `{ address, chain, includeNativeBalance?, tokenAddresses? }` |
| `NODE_TYPE_BRANCH` | `{ conditions: [{ id, type, expression }] }` |
| `NODE_TYPE_FILTER` | `{ expression, inputNodeName }` |
| `NODE_TYPE_LOOP` | `{ inputNodeName, iterVal, iterKey, executionMode, runner: {...} }` |

### Extracting Contract Details (Migration Helper)

If the context-memory API needs to extract contract details from the new unified `config` field:

```typescript
function extractContractDetails(step: StepDigest): {
  contractAddress?: string;
  methodName?: string;
  methodParams?: unknown[];
} {
  const config = step.config as Record<string, unknown> | undefined;
  if (!config) return {};

  const contractAddress = config.contractAddress as string | undefined;

  let methodName: string | undefined;
  let methodParams: unknown[] | undefined;

  const methodCalls = config.methodCalls as Array<Record<string, unknown>> | undefined;
  if (methodCalls && methodCalls.length > 0) {
    methodName = methodCalls[0].methodName as string;
    methodParams = methodCalls[0].methodParams as unknown[];
  }

  return { contractAddress, methodName, methodParams };
}
```

### Output Data by Step Type

All step types now populate `outputData` with their execution results:

| Step Type | OutputData Structure |
|-----------|---------------------|
| Triggers | Event data, block info, epoch timestamps, manual input data |
| `CONTRACT_READ` | Decoded return values keyed by method name |
| `CONTRACT_WRITE` | Transaction receipts, decoded events, return values |
| `REST_API` | HTTP response body (parsed JSON or raw text) |
| `GRAPHQL_QUERY` | GraphQL response data |
| `CUSTOM_CODE` | Return value from JavaScript execution |
| `BALANCE` | Native balance + token balances array |
| `BRANCH` | Matched condition info |
| `FILTER` | Filtered array of items |
| `LOOP` | Aggregated results from all iterations |
| `ETH_TRANSFER` | Transaction hash and receipt |
