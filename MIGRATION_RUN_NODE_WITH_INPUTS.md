# Migration Guide: RunNodeWithInputs API Changes

## Overview

The `RunNodeWithInputsReq` protobuf message has been updated to use a complete `TaskNode` structure instead of partial config fields. This provides better type safety, validation, and consistency with other APIs like `SimulateTask` and `CreateTask`.

## What Changed

### Protobuf Definition

**Before:**
```protobuf
message RunNodeWithInputsReq {
  NodeType node_type = 1;
  map<string, google.protobuf.Value> node_config = 2;
  map<string, google.protobuf.Value> input_variables = 3;
}
```

**After:**
```protobuf
message RunNodeWithInputsReq {
  TaskNode node = 1;  // Complete typed node structure
  map<string, google.protobuf.Value> input_variables = 2;
}
```

## Migration Examples

### Example 1: ContractWrite Node

**OLD Pattern (Deprecated):**
```typescript
const request = {
  nodeType: NodeType.NODE_TYPE_CONTRACT_WRITE,
  nodeConfig: {
    contractAddress: "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
    contractAbi: [...],
    methodCalls: [{
      methodName: "approve",
      methodParams: ["0x1234...", "1000000"]
    }],
    value: "0",
    gasLimit: "210000"
  },
  inputVariables: {
    settings: {
      runner: "0xabcd...",
      chain_id: 11155111
    }
  }
};
```

**NEW Pattern (Correct):**
```typescript
const request = {
  node: {
    id: "contract-write-1",
    name: "approve",
    type: NodeType.NODE_TYPE_CONTRACT_WRITE,
    contractWrite: {
      config: {
        contractAddress: "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
        contractAbi: [...],
        methodCalls: [{
          methodName: "approve",
          methodParams: ["0x1234...", "1000000"]
        }],
        isSimulated: true,  // Optional
        value: "0",          // Optional, but now properly typed
        gasLimit: "210000"   // Optional, but now properly typed
      }
    }
  },
  inputVariables: {
    settings: {
      runner: "0xabcd...",
      chain_id: 11155111
    }
  }
};
```

### Example 2: BalanceNode

**OLD Pattern (Deprecated):**
```typescript
const request = {
  nodeType: NodeType.NODE_TYPE_BALANCE,
  nodeConfig: {
    address: "0x5d814...",
    chain: "sepolia",
    includeSpam: false,
    includeZeroBalances: false,
    tokenAddresses: ["0x1c7d4..."]
  },
  inputVariables: {
    settings: {
      chain_id: 11155111
    }
  }
};
```

**NEW Pattern (Correct):**
```typescript
const request = {
  node: {
    id: "balance-check-1",
    name: "checkBalance",
    type: NodeType.NODE_TYPE_BALANCE,
    balance: {
      config: {
        address: "0x5d814...",
        chain: "sepolia",
        includeSpam: false,
        includeZeroBalances: false,
        tokenAddresses: ["0x1c7d4..."]
      }
    }
  },
  inputVariables: {
    settings: {
      chain_id: 11155111
    }
  }
};
```

### Example 3: CustomCode Node

**OLD Pattern (Deprecated):**
```typescript
const request = {
  nodeType: NodeType.NODE_TYPE_CUSTOM_CODE,
  nodeConfig: {
    lang: Lang.LANG_JAVASCRIPT,
    source: "return { result: 42 };"
  },
  inputVariables: {
    myVar: "test"
  }
};
```

**NEW Pattern (Correct):**
```typescript
const request = {
  node: {
    id: "custom-code-1",
    name: "myScript",
    type: NodeType.NODE_TYPE_CUSTOM_CODE,
    customCode: {
      config: {
        lang: Lang.LANG_JAVASCRIPT,
        source: "return { result: 42 };"
      }
    }
  },
  inputVariables: {
    myVar: "test"
  }
};
```

### Example 4: RestAPI Node

**OLD Pattern (Deprecated):**
```typescript
const request = {
  nodeType: NodeType.NODE_TYPE_REST_API,
  nodeConfig: {
    url: "https://api.example.com/data",
    method: "GET",
    headers: {
      "Authorization": "Bearer {{token}}"
    }
  },
  inputVariables: {
    token: "xyz..."
  }
};
```

**NEW Pattern (Correct):**
```typescript
const request = {
  node: {
    id: "api-call-1",
    name: "fetchData",
    type: NodeType.NODE_TYPE_REST_API,
    restApi: {
      config: {
        url: "https://api.example.com/data",
        method: "GET",
        headers: {
          "Authorization": "Bearer {{token}}"
        }
      }
    }
  },
  inputVariables: {
    token: "xyz..."
  }
};
```

## Benefits of the New Approach

1. **Type Safety**: All node configuration is now properly typed in protobuf schemas
2. **Consistency**: Matches the existing patterns used in `SimulateTask` and `CreateTask` APIs
3. **Completeness**: All required fields (like `value` and `gasLimit` for ContractWrite) are now part of the schema
4. **Validation**: Protobuf schema validation catches missing or incorrect fields at the API layer
5. **No Hidden Dependencies**: Config is explicit in the request structure, not mixed with runtime variables

## What's NOT Affected

The following internal functions and tests do NOT need to be updated:

- `RunNodeImmediately(nodeTypeStr, nodeConfig, inputVariables, user)` - Still accepts config as a map
- Tests that call `RunNodeImmediately` directly
- Internal workflow execution logic

Only code that constructs `RunNodeWithInputsReq` protobuf messages needs to be updated.

## Testing

See `/core/taskengine/run_node_immediately_rpc_test.go` for complete examples of:
- ContractWrite with `value` and `gasLimit` fields
- BalanceNode with token filtering
- Proper TaskNode construction patterns

## Backend Changes

The backend now uses `ExtractNodeConfiguration(node)` to convert the protobuf `TaskNode` into the internal config map format. This function properly extracts all fields including optional ones like `value` and `gasLimit` for ContractWrite nodes.
