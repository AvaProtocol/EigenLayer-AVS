# Protobuf Design Improvement Proposal

## Current Issues

The current `avs.proto` has several inconsistencies and redundancies:

### 1. Multiple Trigger Type Definitions
- `TaskTrigger` uses `oneof trigger_type` with field names: `manual`, `fixed_time`, `cron`, `block`, `event`
- `TriggerReason` defines a separate `enum TriggerType` with values: `Unset`, `Manual`, `FixedTime`, `Cron`, `Block`, `Event`
- The field numbers don't align (TaskTrigger starts at 2, TriggerReason enum has different numbering)

### 2. No Reusable Node Type Enum
- Node types are only defined as `oneof` in `TaskNode`
- `RunNodeWithInputsReq` uses `string node_type` instead of a proper enum
- No single source of truth for supported node types

### 3. Inconsistent Naming
- `TaskTrigger` uses snake_case: `fixed_time`, `cron`, `block`, `event`
- `TriggerReason.TriggerType` uses PascalCase: `FixedTime`, `Cron`, `Block`, `Event`

### 4. Unnecessary `Unset` Value
- The `Unset` value in `TriggerReason.TriggerType` is unnecessary and confusing

## Proposed Solution

### 1. Create Top-Level Enums

```protobuf
// Single source of truth for trigger types
enum TriggerType {
  TRIGGER_TYPE_UNSPECIFIED = 0;  // Better than "Unset"
  TRIGGER_TYPE_MANUAL = 1;
  TRIGGER_TYPE_FIXED_TIME = 2;
  TRIGGER_TYPE_CRON = 3;
  TRIGGER_TYPE_BLOCK = 4;
  TRIGGER_TYPE_EVENT = 5;
}

// Single source of truth for node types
enum NodeType {
  NODE_TYPE_UNSPECIFIED = 0;
  NODE_TYPE_ETH_TRANSFER = 1;
  NODE_TYPE_CONTRACT_WRITE = 2;
  NODE_TYPE_CONTRACT_READ = 3;
  NODE_TYPE_GRAPHQL_QUERY = 4;
  NODE_TYPE_REST_API = 5;
  NODE_TYPE_CUSTOM_CODE = 6;
  NODE_TYPE_BRANCH = 7;
  NODE_TYPE_FILTER = 8;
  NODE_TYPE_LOOP = 9;
}
```

### 2. Improve TaskTrigger Structure

```protobuf
message TaskTrigger {
  string name = 1;
  string id = 2;
  
  // Use the enum for type identification
  TriggerType type = 3;
  
  // Use oneof for the actual trigger configuration
  oneof trigger_config {
    bool manual = 10;
    FixedTimeTrigger fixed_time = 11;
    CronTrigger cron = 12;
    BlockTrigger block = 13;
    EventTrigger event = 14;
  }
}
```

### 3. Improve TaskNode Structure

```protobuf
message TaskNode {
  string id = 1;
  string name = 2;
  
  // Use the enum for type identification
  NodeType type = 3;
  
  // Use oneof for the actual node configuration
  oneof node_config {
    ETHTransferNode eth_transfer = 10;
    RestAPINode rest_api = 11;
    CustomCodeNode custom_code = 12;
    // ... other node types
  }
}
```

### 4. Improve RunNodeWithInputs API

```protobuf
message RunNodeWithInputsReq {
  // Use the enum instead of string
  NodeType node_type = 1;
  map<string, google.protobuf.Value> node_config = 2;
  map<string, google.protobuf.Value> input_variables = 3;
}

message RunNodeWithInputsResp {
  bool success = 1;
  string error = 2;
  string node_id = 3;
  
  // Use the enum for type identification
  NodeType executed_type = 4;
  
  oneof output_data {
    // ... node outputs
  }
}
```

### 5. Consistent TriggerReason

```protobuf
message TriggerReason {
  uint64 block_number = 1;
  uint64 log_index = 2;
  string tx_hash = 3;
  uint64 epoch = 4;
  
  // Use the same enum as TaskTrigger for consistency
  TriggerType type = 5;
}
```

## Benefits

1. **Single Source of Truth**: Enums provide a centralized definition of all supported types
2. **Type Safety**: Using enums instead of strings prevents typos and invalid values
3. **Consistency**: All trigger and node type references use the same enum values
4. **Better Code Generation**: Enums generate better constants in target languages
5. **API Clarity**: Clear separation between type identification and configuration
6. **Extensibility**: Easy to add new types by extending the enums

## Migration Strategy

1. **Phase 1**: Add new enums alongside existing fields
2. **Phase 2**: Update client code to use new enum-based APIs
3. **Phase 3**: Deprecate old string-based fields
4. **Phase 4**: Remove deprecated fields in next major version

## Alignment with Go Constants

The enum values would map to our Go constants:

```go
// Generated from protobuf enums
const (
    NodeType_NODE_TYPE_REST_API     = "restApi"      // Maps to NodeTypeRestAPI
    NodeType_NODE_TYPE_CUSTOM_CODE  = "customCode"   // Maps to NodeTypeCustomCode
    NodeType_NODE_TYPE_BLOCK_TRIGGER = "blockTrigger" // Maps to NodeTypeBlockTrigger
    // etc.
)
```

This creates a true single source of truth where:
- Protobuf enums define the canonical types
- Go constants provide the string representations
- APIs use enums for type safety
- String values remain consistent across all implementations 