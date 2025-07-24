# Trigger and Node Variable Design

## Overview

This document describes the design and implementation of the trigger and node variable system in the EigenLayer-AVS task engine. The system provides a consistent way for workflow nodes to access both configuration data and runtime output data from triggers and other nodes.

## Core Design Principles

### 1. Dual Field Access Pattern

Every trigger and node in the workflow exposes two key fields that can be accessed by subsequent nodes:

- **`.data`** - Runtime output/result data from execution
- **`.input`** - Configuration/input data that was used to configure the element

### 2. Variable Naming Convention

Variables are accessible in JavaScript using the sanitized name of the trigger/node:

```javascript
// For a trigger named "event_trigger_with_input"
eventTriggerWithInput.data    // Runtime output data
eventTriggerWithInput.input   // Configuration input data

// For a node named "api_call_node"
apiCallNode.data              // Runtime output data
apiCallNode.input             // Configuration input data
```

### 3. Cross-Node Data Flow

Nodes can access data from any previous trigger or node in the workflow through the VM variable system:

```javascript
// In a CustomCode node, access trigger data
const triggerOutput = myTrigger.data;
const triggerConfig = myTrigger.input;

// Access data from a previous node
const previousResult = previousNode.data;
const previousConfig = previousNode.input;
```

### 4. Execution Step Config Consistency

**CRITICAL REQUIREMENT**: The `config` field in execution steps must be identical to the original node/trigger creation structure. This serves as a debugging tool for tracking what was configured into each component.

```javascript
// ✅ CORRECT: Backend preserves original structure
// Original LoopNode creation:
{
  type: "loop",
  data: {
    inputNodeName: "sourceNode",
    iterVal: "value", 
    iterKey: "index",
    runner: {
      type: "restApi",
      data: {
        config: {
          url: "{{value}}",
          method: "GET",
          body: ""
        }
      }
    }
  }
}

// Execution step config field (MUST be identical):
{
  executionMode: "sequential",
  inputNodeName: "sourceNode", 
  iterVal: "value",
  iterKey: "index",
  runner: {
    type: "restApi",
    data: {
      config: {
        url: "{{value}}",      // ✅ Preserved structure
        method: "GET",         // ✅ Preserved structure  
        body: ""               // ✅ Preserved structure
      }
    }
  }
}

// ❌ WRONG: Flattened structure breaks consistency
{
  executionMode: "sequential",
  inputNodeName: "sourceNode",
  iterVal: "value", 
  iterKey: "index",
  runner: {
    type: "restApi",
    url: "{{value}}",         // ❌ Flattened - breaks debugging
    method: "GET",            // ❌ Flattened - breaks debugging
    body: ""                  // ❌ Flattened - breaks debugging
  }
}
```

**Key Requirements:**
- Backend must NOT flatten or transform the original data structure
- SDK and backend must maintain consistency between creation and execution config
- The config field is for debugging/inspection - it must match the original structure
- All runner types (restApi, customCode, contractRead, contractWrite, etc.) must preserve their nested structure

## Trigger Types and Variable Structures

### EventTrigger

EventTriggers monitor blockchain events and provide enriched event data.

```javascript
// EventTrigger variable structure
eventTrigger = {
  data: {
    // Runtime output from event execution
    // For transfer events: enriched token metadata
    blockNumber: 12345,
    transactionHash: "0x...",
    logIndex: 1,
    tokenSymbol: "USDC",           // Enriched from token metadata
    tokenName: "USD Coin",         // Enriched from token metadata
    valueFormatted: "100.50",      // Formatted with proper decimals
    from: "0x...",
    to: "0x...",
    value: "100500000",            // Raw value in smallest unit
    // ... other runtime data
  },
  input: {
    // Configuration data used to set up the trigger
    subType: "transfer",
    chainId: 11155111,
    address: "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
    tokens: [{
      name: "USD Coin",
      symbol: "USDC",
      address: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
      decimals: 6
    }]
  }
}
```

**Key Features:**
- Automatic token enrichment for transfer events
- Blockchain data extraction and formatting
- Support for custom event queries
- Real-time and historical event monitoring

### ManualTrigger

ManualTriggers accept user-provided JSON data and are commonly used for webhooks and manual workflow execution.

```javascript
// ManualTrigger variable structure
manualTrigger = {
  data: {
    // User-provided JSON data - REQUIRED and must be valid JSON
    // Can be any JSON structure: objects, arrays, primitives
    
    // Example 1: JSON Object
    apiBaseUrl: "https://api.example.com",
    apiKey: "key123",
    environment: "production",
    
    // Example 2: JSON Array (flattened access)
    // If data is [{"name": "item1"}, {"name": "item2"}]
    // Access as: manualTrigger.data (returns the full array)
    
    // Example 3: JSON Primitive
    // If data is "Hello World" or 42 or true
    // Access as: manualTrigger.data (returns the primitive value)
  },
  input: {
    // Configuration data for the manual trigger
    // Usually contains webhook configuration, expected fields, etc.
    webhookPath: "/webhook/manual",
    expectedFields: ["apiBaseUrl", "apiKey"],
    validationRules: {
      apiKey: { required: true, minLength: 8 }
    }
  }
}
```

**Important Changes (Latest Update):**
- ✅ **Data is REQUIRED**: ManualTrigger data cannot be null or undefined
- ✅ **Must be valid JSON**: Accepts any JSON structure (objects, arrays, primitives)
- ✅ **No string parsing**: Data must be provided as parsed JSON, not JSON strings
- ✅ **Flexible structure**: Supports objects, arrays, strings, numbers, booleans
- ✅ **Direct access**: For JSON objects, fields are flattened for easy access
- ✅ **Array support**: Arrays are accessible directly as `triggerName.data`

**Data Access Patterns:**
```javascript
// JSON Object data
const config = manualTrigger.data; // { apiKey: "...", baseUrl: "..." }
const apiKey = manualTrigger.apiKey; // Direct field access (if object)

// JSON Array data  
const items = manualTrigger.data; // [{"name": "item1"}, {"name": "item2"}]
const firstItem = items[0]; // {"name": "item1"}

// JSON Primitive data
const message = manualTrigger.data; // "Hello World" or 42 or true
```

**Execution Step Output Format:**
As part of the standardization effort, all triggers and nodes now use consistent **Direct Mapping** for execution step outputs:

```javascript
// STANDARDIZED execution step output format (Direct Mapping)
// Input data: [{"key": "value1"}, {"key": "value2"}]
// Execution step output: [{"key": "value1"}, {"key": "value2"}]  (direct data, no wrapper)

// All triggers use Direct Mapping:
// EventTrigger execution step output: { blockNumber: 123, chainId: 11155111, ... }
// BlockTrigger execution step output: { blockNumber: 456, timestamp: 1672531200, ... }
// ManualTrigger execution step output: [{"key": "value1"}, {"key": "value2"}]
// CronTrigger execution step output: { executionTime: "2025-01-17T10:30:00Z", ... }
// FixedTimeTrigger execution step output: { executionTime: "2025-01-17T15:00:00Z", ... }

// All nodes use Direct Mapping:
// CustomCodeNode execution step output: { result: "processed", processedItems: 42, ... }
// RestAPINode execution step output: { body: {...}, statusCode: 200, headers: {...} }
// ContractReadNode execution step output: [{ methodName: "balanceOf", data: [...] }]
// ContractWriteNode execution step output: [{ transactionHash: "0x...", success: true }]
// ETHTransferNode execution step output: { transactionHash: "0x...", amountSent: "1.0" }
// BranchNode execution step output: { conditionResult: true, selectedBranch: "success" }
// LoopNode execution step output: ["item1_processed", "item2_processed", ...]
// FilterNode execution step output: [{ key: "value1" }]
// GraphQLNode execution step output: { data: { user: {...} }, errors: null }
```

**Key Benefits of Direct Mapping:**
- **Consistent Structure**: All triggers and nodes return data directly without intermediate wrappers
- **Simplified SDK Code**: Single output handling function for all types
- **Better Developer Experience**: Predictable output format across all execution steps
- **Type Safety**: Clean, native JavaScript objects/arrays without nested data fields
- **API Consistency**: Uniform response structure for all workflow components

### BlockTrigger

BlockTriggers execute at specified block intervals.

```javascript
// BlockTrigger variable structure
blockTrigger = {
  data: {
    // Runtime output from block trigger
    blockNumber: 18500000,
    blockHash: "0x...",
    timestamp: 1672531200,
    parentHash: "0x...",
    gasLimit: "30000000",
    gasUsed: "12500000"
  },
  input: {
    // Configuration for block monitoring
    interval: 100,        // Trigger every 100 blocks
    chainId: 11155111,
    startBlock: 18400000  // Optional start block
  }
}
```

### CronTrigger

CronTriggers execute on time-based schedules.

```javascript
// CronTrigger variable structure
cronTrigger = {
  data: {
    // Runtime output from cron execution
    executionTime: "2025-01-17T10:30:00Z",
    scheduledTime: "2025-01-17T10:30:00Z",
    timezone: "UTC",
    cronExpression: "0 30 10 * * *"
  },
  input: {
    // Configuration for cron schedule
    schedule: "0 30 10 * * *",  // Every day at 10:30 AM
    timezone: "America/New_York",
    description: "Daily report generation"
  }
}
```

### FixedTimeTrigger

FixedTimeTriggers execute once at a specified time.

```javascript
// FixedTimeTrigger variable structure
fixedTimeTrigger = {
  data: {
    // Runtime output from fixed time execution
    executionTime: "2025-01-17T15:00:00Z",
    scheduledTime: "2025-01-17T15:00:00Z",
    delay: 0  // Milliseconds between scheduled and actual execution
  },
  input: {
    // Configuration for fixed time execution
    executeAt: "2025-01-17T15:00:00Z",
    timezone: "UTC",
    description: "One-time data migration"
  }
}
```

## Node Types and Variable Structures

### RestAPI Node

RestAPI nodes make HTTP requests to external APIs.

```javascript
// RestAPI node variable structure
restApiNode = {
  data: {
    // Runtime output from API call
    body: {
      // Parsed response body (JSON automatically parsed)
      success: true,
      data: { 
        users: [
          { id: 1, name: "John", email: "john@example.com" },
          { id: 2, name: "Jane", email: "jane@example.com" }
        ]
      },
      pagination: { page: 1, total: 50 }
    },
    headers: {
      // Response headers
      "Content-Type": "application/json",
      "Content-Length": "1234",
      "X-RateLimit-Remaining": "99"
    },
    statusCode: 200,
    duration: 245  // Request duration in milliseconds
  },
  input: {
    // Configuration used for the API call
    url: "https://api.example.com/users",
    method: "GET",
    headers: {
      "Authorization": "Bearer {{secrets.apiToken}}",
      "Content-Type": "application/json"
    },
    body: null,  // For GET requests
    timeout: 30000,
    retryCount: 3
  }
}
```

### CustomCode Node

CustomCode nodes execute user-defined JavaScript code.

```javascript
// CustomCode node variable structure
customCodeNode = {
  data: {
    // Runtime output from code execution
    // This is whatever the JavaScript code returns
    result: "processed successfully",
    timestamp: "2025-01-17T10:30:00Z",
    processedItems: 42,
    summary: {
      totalProcessed: 100,
      successful: 98,
      failed: 2,
      errors: ["Invalid email format", "Missing required field"]
    }
  },
  input: {
    // Configuration for the custom code
    lang: "JavaScript",
    source: `
      const items = eventTrigger.data.items || [];
      let processed = 0;
      const results = [];
      
      for (const item of items) {
        if (item.email && item.email.includes('@')) {
          results.push({ ...item, status: 'valid' });
          processed++;
        }
      }
      
      return {
        result: 'processed successfully',
        timestamp: new Date().toISOString(),
        processedItems: processed,
        results: results
      };
    `,
    timeout: 30000
  }
}
```

### ContractRead Node

ContractRead nodes read data from smart contracts and support multiple method calls in a single execution.

#### Configuration Requirements

**CRITICAL**: `contractAbi` must be provided as an **array only** (not JSON string):

**⚠️ CRITICAL - NO TEMPLATE SUBSTITUTION IN ABI**: The `contractAbi` field is **NEVER** subject to template variable replacement. ABI data must be taken as-is from user input without any `{{variable}}` processing. Template substitution is only applied to other fields like `contractAddress`, `methodParams`, etc.

```javascript
// ✅ CORRECT: contractAbi as array
{
  contractAddress: "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
  contractAbi: [
    {
      constant: true,
      inputs: [],
      name: "name",
      outputs: [{ name: "", type: "string" }],
      payable: false,
      stateMutability: "view",
      type: "function"
    },
    {
      constant: true,
      inputs: [],
      name: "symbol", 
      outputs: [{ name: "", type: "string" }],
      payable: false,
      stateMutability: "view",
      type: "function"
    }
  ],
  methodCalls: [
    { callData: "0x06fdde03", methodName: "name" },
    { callData: "0x95d89b41", methodName: "symbol" }
  ]
}

// ❌ WRONG: contractAbi as JSON string
{
  contractAbi: "[{\"constant\":true,\"name\":\"name\"...}]"  // Will cause error
}
```

#### Output Data Structure

ContractRead nodes return an **array of method results**, where each result corresponds to a method call:

```javascript
// ContractRead node variable structure
contractReadNode = {
  data: [
    // Array of method results - each object corresponds to one methodCall
    {
      methodName: "name",
      value: "Wrapped Ether",           // Decoded value from contract call
      success: true,
      error: "",
      methodABI: {                      // Single ABI entry for this method
        name: "name", 
        type: "string", 
        value: "Wrapped Ether"
      }
    },
    {
      methodName: "symbol", 
      value: "WETH",                    // Decoded value from contract call
      success: true,
      error: "",
      methodABI: {                      // Single ABI entry for this method
        name: "symbol",
        type: "string", 
        value: "WETH"
      }
    }
  ],
  input: {
    // Configuration for contract interaction
    contractAddress: "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
    contractAbi: [...],                 // Full ABI array as provided in config
    methodCalls: [
      { callData: "0x06fdde03", methodName: "name" },
      { callData: "0x95d89b41", methodName: "symbol" }
    ]
  }
}
```

#### Multiple Method Calls

ContractRead nodes execute multiple method calls **sequentially** in the order specified:

```javascript
// Input configuration with multiple methods
{
  contractAddress: "0xA0b86a33E6441b8D9c5a8d3d7E0b8d6e4d2e8f1a",
  contractAbi: [...],  // Array of ABI entries
  methodCalls: [
    { callData: "0x06fdde03", methodName: "name" },
    { callData: "0x95d89b41", methodName: "symbol" },
    { callData: "0x313ce567", methodName: "decimals" }
  ]
}

// Output: Array with 3 results
contractReadNode.data = [
  { methodName: "name", value: "Token Name", success: true, ... },
  { methodName: "symbol", value: "TKN", success: true, ... },
  { methodName: "decimals", value: "18", success: true, ... }
]
```

### ContractWrite Node

ContractWrite nodes execute transactions on smart contracts and support multiple method calls in a single execution.

#### Configuration Requirements

**CRITICAL**: `contractAbi` must be provided as an **array only** (not JSON string):

**⚠️ CRITICAL - NO TEMPLATE SUBSTITUTION IN ABI**: The `contractAbi` field is **NEVER** subject to template variable replacement. ABI data must be taken as-is from user input without any `{{variable}}` processing. Template substitution is only applied to other fields like `contractAddress`, `methodParams`, etc.

```javascript
// ✅ CORRECT: contractAbi as array
{
  contractAddress: "0xA0b86a33E6441b8D9c5a8d3d7E0b8d6e4d2e8f1a",
  contractAbi: [
    {
      constant: false,
      inputs: [
        { name: "to", type: "address" },
        { name: "value", type: "uint256" }
      ],
      name: "transfer",
      outputs: [{ name: "", type: "bool" }],
      payable: false,
      stateMutability: "nonpayable",
      type: "function"
    },
    {
      constant: false,
      inputs: [
        { name: "spender", type: "address" },
        { name: "value", type: "uint256" }
      ],
      name: "approve",
      outputs: [{ name: "", type: "bool" }],
      payable: false,
      stateMutability: "nonpayable",
      type: "function"
    }
  ],
  methodCalls: [
    { 
      callData: "0xa9059cbb000000000000...", 
      methodName: "transfer",
      value: "0",
      gasLimit: "50000"
    },
    { 
      callData: "0x095ea7b3000000000000...", 
      methodName: "approve",
      value: "0",
      gasLimit: "45000"
    }
  ]
}

// ❌ WRONG: contractAbi as JSON string
{
  contractAbi: "[{\"constant\":false,\"name\":\"transfer\"...}]"  // Will cause error
}
```

#### Output Data Structure

ContractWrite nodes return an **array of transaction results**, where each result corresponds to a method call:

```javascript
// ContractWrite node variable structure
contractWriteNode = {
  data: [
    // Array of transaction results - each object corresponds to one methodCall
    {
      methodName: "transfer",
      methodABI: {
        // ABI entry for this specific method
        constant: false,
        inputs: [
          { name: "to", type: "address" },
          { name: "value", type: "uint256" }
        ],
        name: "transfer",
        outputs: [{ name: "", type: "bool" }],
        payable: false,
        stateMutability: "nonpayable",
        type: "function"
      },
      success: true,
      error: "",
      receipt: {
        transactionHash: "0x1234567890abcdef...",
        transactionIndex: 42,
        blockHash: "0xabcdef1234567890...",
        blockNumber: 19283712,
        from: "0xuser...",
        to: "0xcontract...",
        contractAddress: null,  // null for regular calls, address for deployments
        cumulativeGasUsed: "1234567",
        gasUsed: "65321",
        effectiveGasPrice: "20000000000",  // optional (EIP-1559 chains)
        logsBloom: "0x00000000000000000000000000000000...",
        logs: [
          // Transaction logs/events
          {
            address: "0xcontract...",
            topics: ["0x..."],
            data: "0x...",
            blockNumber: 19283712,
            transactionHash: "0x1234567890abcdef...",
            transactionIndex: 42,
            blockHash: "0xabcdef1234567890...",
            logIndex: 1,
            removed: false
          }
        ],
        status: "success",  // 'success' or 'reverted'
        type: "eip1559"     // 'legacy' | 'eip1559' | 'eip2930'
      },
      blockNumber: 19283712,
      value: true  // Return value from contract method (null if no return value)
    },
    {
      methodName: "approve",
      methodABI: {
        // ABI entry for this specific method
        constant: false,
        inputs: [
          { name: "spender", type: "address" },
          { name: "value", type: "uint256" }
        ],
        name: "approve",
        outputs: [{ name: "", type: "bool" }],
        payable: false,
        stateMutability: "nonpayable",
        type: "function"
      },
      success: true,
      error: "",
      receipt: {
        transactionHash: "0xabcdef1234567890...",
        transactionIndex: 43,
        blockHash: "0xfedcba0987654321...",
        blockNumber: 19283713,
        from: "0xuser...",
        to: "0xcontract...",
        contractAddress: null,  // null for regular calls, address for deployments
        cumulativeGasUsed: "1299888",
        gasUsed: "45123",
        effectiveGasPrice: "20000000000",  // optional (EIP-1559 chains)
        logsBloom: "0x00000000000000000000000000000000...",
        logs: [
          // Transaction logs/events
          {
            address: "0xcontract...",
            topics: ["0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"],
            data: "0x...",
            blockNumber: 19283713,
            transactionHash: "0xabcdef1234567890...",
            transactionIndex: 43,
            blockHash: "0xfedcba0987654321...",
            logIndex: 2,
            removed: false
          }
        ],
        status: "success",  // 'success' or 'reverted'
        type: "eip1559"     // 'legacy' | 'eip1559' | 'eip2930'
      },
      blockNumber: 19283713,
      value: true  // Return value from contract method (null if no return value)
    }
  ],
  input: {
    // Configuration for contract transactions
    contractAddress: "0xA0b86a33E6441b8D9c5a8d3d7E0b8d6e4d2e8f1a",
    contractAbi: [...],                 // Full ABI array as provided in config
    methodCalls: [
      { 
        callData: "0xa9059cbb000000000000...", 
        methodName: "transfer",
        value: "0",
        gasLimit: "50000"
      },
      { 
        callData: "0x095ea7b3000000000000...", 
        methodName: "approve",
        value: "0",
        gasLimit: "45000"
      }
    ]
  }
}
```

#### Multiple Method Calls

ContractWrite nodes execute multiple method calls **sequentially** in the order specified. Each method call creates a separate transaction:

```javascript
// Input configuration with multiple methods
{
  contractAddress: "0xA0b86a33E6441b8D9c5a8d3d7E0b8d6e4d2e8f1a",
  contractAbi: [...],  // Array of ABI entries
  methodCalls: [
    { callData: "0xa9059cbb...", methodName: "transfer", gasLimit: "50000" },
    { callData: "0x095ea7b3...", methodName: "approve", gasLimit: "45000" },
    { callData: "0x23b872dd...", methodName: "transferFrom", gasLimit: "55000" }
  ]
}

// Output: Array with 3 transaction results
contractWriteNode.data = [
  { 
    methodName: "transfer", 
    methodABI: {...}, 
    success: true, 
    receipt: {...}, 
    blockNumber: 19283712,
    value: true
  },
  { 
    methodName: "approve", 
    methodABI: {...}, 
    success: true, 
    receipt: {...}, 
    blockNumber: 19283713,
    value: true
  },
  { 
    methodName: "transferFrom", 
    methodABI: {...}, 
    success: true, 
    receipt: {...}, 
    blockNumber: 19283714,
    value: true
  }
]
```

### EthTransfer Node

EthTransfer nodes send ETH to addresses.

```javascript
// EthTransfer node variable structure
ethTransferNode = {
  data: {
    // Runtime output from ETH transfer
    transactionHash: "0x...",
    blockNumber: 18500001,
    gasUsed: "21000",
    status: "success",
    amountSent: "1000000000000000000",  // 1 ETH in wei
    recipient: "0x742d35Cc6634C0532925a3b8D8FA4fF5C6E4a1e6"
  },
  input: {
    // Configuration for ETH transfer
    destination: "0x742d35Cc6634C0532925a3b8D8FA4fF5C6E4a1e6",
    amount: "1.0",  // Amount in ETH
    gasLimit: "21000"
  }
}
```

### Branch Node

Branch nodes implement conditional logic in workflows.

```javascript
// Branch node variable structure
branchNode = {
  data: {
    // Runtime output from branch evaluation
    conditionResult: true,
    selectedBranch: "success_branch",
    evaluatedConditions: [
      { expression: "eventTrigger.data.value > 1000", result: true },
      { expression: "eventTrigger.data.from !== '0x0'", result: true }
    ]
  },
  input: {
    // Configuration for branch logic
    conditions: [
      {
        expression: "eventTrigger.data.value > 1000",
        target: "success_branch"
      },
      {
        expression: "eventTrigger.data.from !== '0x0'",
        target: "validation_branch"
      }
    ],
    defaultTarget: "error_branch"
  }
}
```

### Filter Node

Filter nodes filter data based on conditions.

```javascript
// Filter node variable structure
filterNode = {
  data: {
    // Runtime output from filter operation - the filtered result
    key: "value1"  // Data that passed the filter condition
  },
  input: {
    // Configuration for filtering
    inputNodeName: "manualTrigger",  // Node name to get data from
    expression: "value.key === 'value1'",  // Filter expression
    description: "Filter items with specific key value"
  }
}
```

### Loop Node

Loop nodes iterate over arrays or objects and support various runner types including contract interactions.

#### Basic Loop Structure

```javascript
// Loop node variable structure
loopNode = {
  data: [
    // Runtime output from loop execution - flat array of results
    "item1_processed",
    "item2_processed", 
    "item3_processed"
  ],
  input: {
    // Configuration for loop operation
    inputNodeName: "manualTrigger",  // Node name to get array from
    iterVal: "item",                 // Variable name for current item
    iterKey: "index",                // Variable name for current index
    executionMode: "sequential",     // or "parallel"
    maxIterations: 100,
    runner: {
      type: "customCode",            // Loop runner type
      data: {
        config: {
          lang: "JavaScript",
          source: "return item + '_processed';"
        }
      }
    }
  }
}
```

#### Loop with ContractRead Runner

Loop nodes can execute contract read operations for each iteration. **CRITICAL**: `contractAbi` must be provided as an **array only**.

**⚠️ CRITICAL - NO TEMPLATE SUBSTITUTION IN ABI**: The `contractAbi` field is **NEVER** subject to template variable replacement. ABI data must be taken as-is from user input without any `{{variable}}` processing. Template substitution is only applied to other fields like `contractAddress`, `methodParams`, etc.

```javascript
// Loop node configuration with contractRead runner
{
  type: "loop",
  inputNodeName: "manualTrigger",
  iterVal: "value",
  iterKey: "index", 
  executionMode: "sequential",
  runner: {
    type: "contractRead",
    config: {
      contractAddress: "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
      contractAbi: [
        // ✅ CORRECT: Array format only
        {
          constant: true,
          inputs: [],
          name: "name",
          outputs: [{ name: "", type: "string" }],
          payable: false,
          stateMutability: "view",
          type: "function"
        },
        {
          constant: true,
          inputs: [],
          name: "symbol",
          outputs: [{ name: "", type: "string" }],
          payable: false,
          stateMutability: "view", 
          type: "function"
        }
      ],
      methodCalls: [
        { callData: "0x06fdde03", methodName: "name" },
        { callData: "0x95d89b41", methodName: "symbol" }
      ]
    }
  }
}

// ❌ WRONG: contractAbi as JSON string will cause error
{
  runner: {
    type: "contractRead",
    config: {
      contractAbi: "[{\"constant\":true,\"name\":\"name\"...}]"  // Will fail
    }
  }
}
```

**Loop with ContractRead Output Structure:**

```javascript
// Loop node output when using contractRead runner
loopNode = {
  data: [
    // Each iteration returns an array of method results
    [
      { methodName: "name", value: "Wrapped Ether", success: true, ... },
      { methodName: "symbol", value: "WETH", success: true, ... }
    ],
    [
      { methodName: "name", value: "Wrapped Ether", success: true, ... },
      { methodName: "symbol", value: "WETH", success: true, ... }
    ]
  ],
  input: { /* loop configuration */ }
}
```

**Expected Output Format for Multiple Method Calls:**

For a loop with 2 iterations and 2 method calls per iteration:

```javascript
// Input: manualTrigger.data = [{ key: "value1" }, { key: "value2" }]
// methodCalls: [{ methodName: "name" }, { methodName: "symbol" }]

// Expected output structure:
loopNode.data = [
  // Iteration 1 result (array of method results)
  [
    {
      methodName: "name",
      value: "Wrapped Ether",
      success: true,
      error: "",
      methodABI: { name: "name", type: "string", value: "Wrapped Ether" }
    },
    {
      methodName: "symbol", 
      value: "WETH",
      success: true,
      error: "",
      methodABI: { name: "symbol", type: "string", value: "WETH" }
    }
  ],
  // Iteration 2 result (array of method results)
  [
    {
      methodName: "name",
      value: "Wrapped Ether", 
      success: true,
      error: "",
      methodABI: { name: "name", type: "string", value: "Wrapped Ether" }
    },
    {
      methodName: "symbol",
      value: "WETH",
      success: true, 
      error: "",
      methodABI: { name: "symbol", type: "string", value: "WETH" }
    }
  ]
]
```

#### Loop with ContractWrite Runner

Loop nodes can execute contract write operations for each iteration. **CRITICAL**: `contractAbi` must be provided as an **array only**.

**⚠️ CRITICAL - NO TEMPLATE SUBSTITUTION IN ABI**: The `contractAbi` field is **NEVER** subject to template variable replacement. ABI data must be taken as-is from user input without any `{{variable}}` processing. Template substitution is only applied to other fields like `contractAddress`, `methodParams`, etc.

```javascript
// Loop node configuration with contractWrite runner
{
  type: "loop",
  inputNodeName: "manualTrigger",
  iterVal: "recipient",
  iterKey: "index",
  executionMode: "sequential", 
  runner: {
    type: "contractWrite",
    config: {
      contractAddress: "0xA0b86a33E6441b8D9c5a8d3d7E0b8d6e4d2e8f1a",
      contractAbi: [
        // ✅ CORRECT: Array format only
        {
          constant: false,
          inputs: [
            { name: "to", type: "address" },
            { name: "value", type: "uint256" }
          ],
          name: "transfer",
          outputs: [{ name: "", type: "bool" }],
          payable: false,
          stateMutability: "nonpayable",
          type: "function"
        }
      ],
      methodCalls: [
        {
          callData: "0xa9059cbb{{recipient.address}}{{recipient.amount}}", 
          methodName: "transfer",
          value: "0",
          gasLimit: "50000"
        }
      ]
    }
  }
}
```

**Loop with ContractWrite Output Structure:**

```javascript
// Loop node output when using contractWrite runner
loopNode = {
  data: [
    // Each iteration returns an array of transaction results
    [
      {
        methodName: "transfer",
        methodABI: {
          // ABI entry for this specific method
          constant: false,
          inputs: [
            { name: "to", type: "address" },
            { name: "value", type: "uint256" }
          ],
          name: "transfer",
          outputs: [{ name: "", type: "bool" }],
          payable: false,
          stateMutability: "nonpayable",
          type: "function"
        },
        success: true,
        error: "",
        receipt: {
          transactionHash: "0x1234567890abcdef...",
          transactionIndex: 42,
          blockHash: "0xabcdef1234567890...",
          blockNumber: 19283712,
          from: "0xuser...",
          to: "0xcontract...",
          contractAddress: null,  // null for regular calls, address for deployments
          cumulativeGasUsed: "1234567",
          gasUsed: "65321",
          effectiveGasPrice: "20000000000",  // optional (EIP-1559 chains)
          logsBloom: "0x00000000000000000000000000000000...",
          logs: [
            {
              address: "0xcontract...",
              topics: ["0x..."],
              data: "0x...",
              blockNumber: 19283712,
              transactionHash: "0x1234567890abcdef...",
              transactionIndex: 42,
              blockHash: "0xabcdef1234567890...",
              logIndex: 1,
              removed: false
            }
          ],
          status: "success",  // 'success' or 'reverted'
          type: "eip1559"     // 'legacy' | 'eip1559' | 'eip2930'
        },
        blockNumber: 19283712,
        value: true  // Return value from contract method (null if no return value)
      }
    ],
    [
      {
        methodName: "transfer",
        methodABI: {
          // ABI entry for this specific method
          constant: false,
          inputs: [
            { name: "to", type: "address" },
            { name: "value", type: "uint256" }
          ],
          name: "transfer",
          outputs: [{ name: "", type: "bool" }],
          payable: false,
          stateMutability: "nonpayable",
          type: "function"
        },
        success: true,
        error: "",
        receipt: {
          transactionHash: "0xabcdef1234567890...",
          transactionIndex: 43,
          blockHash: "0xfedcba0987654321...",
          blockNumber: 19283713,
          from: "0xuser...",
          to: "0xcontract...",
          contractAddress: null,  // null for regular calls, address for deployments
          cumulativeGasUsed: "1299888",
          gasUsed: "65321",
          effectiveGasPrice: "20000000000",  // optional (EIP-1559 chains)
          logsBloom: "0x00000000000000000000000000000000...",
          logs: [
            {
              address: "0xcontract...",
              topics: ["0x..."],
              data: "0x...",
              blockNumber: 19283713,
              transactionHash: "0xabcdef1234567890...",
              transactionIndex: 43,
              blockHash: "0xfedcba0987654321...",
              logIndex: 2,
              removed: false
            }
          ],
          status: "success",  // 'success' or 'reverted'
          type: "eip1559"     // 'legacy' | 'eip1559' | 'eip2930'
        },
        blockNumber: 19283713,
        value: true  // Return value from contract method (null if no return value)
      }
    ]
  ],
  input: { /* loop configuration */ }
}
```

#### Key Requirements for Contract Runners in Loops

1. **Array-Only contractAbi**: Both `contractRead` and `contractWrite` runners in loops must use array format for `contractAbi`
2. **Multiple Method Support**: Each iteration can execute multiple method calls sequentially
3. **Consistent Output**: Each iteration returns an array of results, even for single method calls
4. **Template Variables**: Loop runners support template variables like `{{value}}`, `{{index}}` for dynamic configuration
5. **Error Handling**: Individual method failures don't stop the loop; errors are captured in the result structure

### GraphQL Node

GraphQL nodes query GraphQL APIs.

```javascript
// GraphQL node variable structure  
graphqlNode = {
  data: {
    // Runtime output from GraphQL query
    data: {
      // GraphQL response data
      user: {
        id: "1",
        name: "John Doe",
        email: "john@example.com",
        posts: [
          { id: "1", title: "Hello World", content: "..." },
          { id: "2", title: "GraphQL Guide", content: "..." }
        ]
      }
    },
    errors: null,  // GraphQL errors if any
    extensions: {}
  },
  input: {
    // Configuration for GraphQL query
    endpoint: "https://api.example.com/graphql",
    query: `
      query GetUser($id: ID!) {
        user(id: $id) {
          id
          name
          email
          posts {
            id
            title
            content
          }
        }
      }
    `,
    variables: { id: "1" },
    headers: {
      "Authorization": "Bearer {{secrets.graphqlToken}}"
    }
  }
}
```

## Advanced Features

### Template Variable Resolution

All node configurations support template variables that are resolved at runtime:

**⚠️ CRITICAL - ABI FIELDS NEVER SUPPORT TEMPLATE VARIABLES**: Any field named `contractAbi` (in ContractRead, ContractWrite, Loop nodes, or EventTrigger) is **NEVER** subject to template variable replacement. ABI data must be taken as-is from user input without any `{{variable}}` processing. This ensures ABI integrity and prevents template injection vulnerabilities.

#### Template Variable Support by Field

**✅ Fields that SUPPORT template variables:**
- `contractAddress` - Contract addresses can be dynamically resolved
- `methodParams` - Method parameters support dynamic values like `["{{value.address}}"]`
- `callData` - Pre-encoded call data can include template variables
- `url` - API endpoints can be dynamically constructed
- `body` - Request bodies can include dynamic data
- `headers` - Request headers can include dynamic tokens/values
- `methodName` - Method names can be dynamically resolved
- All other non-ABI configuration fields

**❌ Fields that NEVER support template variables:**
- `contractAbi` - ABI arrays are always used as-is from user input
- `applyToFields` - Field names are literal identifiers, not template variables

```javascript
// Template examples in node configurations
{
  "url": "{{eventTrigger.input.apiBaseUrl}}/users/{{eventTrigger.data.userId}}",
  "headers": {
    "Authorization": "Bearer {{secrets.apiToken}}",
    "X-User-ID": "{{eventTrigger.data.from}}"
  },
  "body": JSON.stringify({
    "blockNumber": "{{eventTrigger.data.blockNumber}}",
    "amount": "{{eventTrigger.data.valueFormatted}}",
    "timestamp": "{{eventTrigger.data.timestamp}}"
  })
}
```

### Secret Management

Secrets are securely managed and accessible through the `secrets` namespace:

```javascript
// Accessing secrets in templates
"{{secrets.databaseUrl}}"
"{{secrets.apiKey}}"
"{{secrets.webhookSecret}}"

// Accessing secrets in CustomCode
const apiKey = secrets.apiKey;
const dbUrl = secrets.databaseUrl;
```

### Error Handling

Comprehensive error information is available for debugging:

```javascript
// Node with error
errorNode = {
  data: null,  // No data when node fails
  input: { /* original configuration */ },
  error: {
    message: "Request timeout after 30000ms",
    code: "TIMEOUT",
    details: {
      url: "https://slow-api.example.com/data",
      duration: 30001,
      retryAttempt: 3
    }
  }
}
```

## Implementation Details

### VM Variable Creation

The VM creates variables for triggers and nodes using the following pattern:

1. **Trigger Variables** (created during VM initialization):
   ```go
   // In vm.go NewVMWithData function
   triggerVarData := buildTriggerVariableData(task.Trigger, triggerDataMap, triggerInputData)
   v.AddVar(triggerNameStd, triggerVarData)
   ```

2. **Node Variables** (created during node execution):
   ```go
   // In CommonProcessor.SetOutputVarForStep
   nodeVar["data"] = processedData
   
   // In CommonProcessor.SetInputVarForStep  
   nodeVar["input"] = processedInput
   ```

### Data Flow in Workflow Execution

1. **Trigger Execution**:
   - Trigger runs and produces output data
   - VM creates trigger variable with `.data` (output) and `.input` (config)
   - Variable is available to all subsequent nodes

2. **Node Execution**:
   - Node accesses trigger/previous node data via VM variables
   - Node processes data and produces output
   - VM creates/updates node variable with `.data` (output) and `.input` (config)
   - Variable is available to all subsequent nodes

3. **Template Resolution**:
   - Nodes can use template variables like `{{triggerName.data.field}}`
   - VM preprocesses templates before node execution
   - Both `.data` and `.input` fields are available for templating

### InputsList Generation

The `inputsList` field in execution steps shows what variables were available during execution:

```javascript
// Example inputsList for a node
inputsList: [
  "apContext.configVars",           // Global configuration
  "workflowContext",                // Workflow metadata
  "eventTrigger.data",              // Trigger output data
  "eventTrigger.input",             // Trigger configuration
  "previousNode.data",              // Previous node output
  "previousNode.input",             // Previous node configuration
  "userVariable1",                  // User-defined variables
  "userVariable2"
]
```

## Usage Examples

### Accessing Trigger Data in Nodes

```javascript
// In a CustomCode node following an EventTrigger
const eventData = eventTrigger.data;
const eventConfig = eventTrigger.input;

// Use trigger output data
console.log(`Block number: ${eventData.blockNumber}`);
console.log(`Transaction: ${eventData.transactionHash}`);
console.log(`Token: ${eventData.tokenSymbol}`);
console.log(`Amount: ${eventData.valueFormatted}`);

// Use trigger configuration
console.log(`Monitoring chain: ${eventConfig.chainId}`);
console.log(`Target address: ${eventConfig.address}`);

// Process based on configuration
if (eventConfig.subType === "transfer") {
  // Handle transfer events
  const tokenInfo = eventConfig.tokens[0];
  console.log(`Token: ${tokenInfo.symbol}`);
}
```

### ManualTrigger Data Access Patterns

```javascript
// In a CustomCode node following a ManualTrigger

// Example 1: JSON Object data
const userData = manualTrigger.data;
// Direct field access if data is an object
const apiKey = manualTrigger.apiKey;        // Direct access
const baseUrl = manualTrigger.apiBaseUrl;   // Direct access

// Example 2: JSON Array data
const items = manualTrigger.data;  // Full array
const firstItem = items[0];        // First array element
const itemNames = items.map(item => item.name);  // Extract names

// Example 3: Processing different data types
if (Array.isArray(manualTrigger.data)) {
  console.log(`Processing ${manualTrigger.data.length} items`);
  return manualTrigger.data.map(item => ({
    ...item,
    processed: true,
    timestamp: new Date().toISOString()
  }));
} else if (typeof manualTrigger.data === 'object') {
  console.log('Processing object data');
  return { ...manualTrigger.data, processed: true };
} else {
  console.log('Processing primitive data');
  return { value: manualTrigger.data, processed: true };
}
```

### Chain Node Data Access

```javascript
// In a node that follows multiple previous nodes
const apiResult = restApiNode.data;
const triggerConfig = eventTrigger.input;
const previousProcessing = customCodeNode.data;

// Combine data from multiple sources
return {
  apiResponse: apiResult.body,
  triggerChain: triggerConfig.chainId,
  previousResult: previousProcessing.result,
  blockNumber: eventTrigger.data.blockNumber,
  combinedAt: new Date().toISOString(),
  
  // Error handling
  hasErrors: !apiResult || apiResult.statusCode !== 200,
  errors: apiResult ? [] : ['API call failed']
};
```

### Template Usage Examples

```json
{
  "type": "restApi",
  "config": {
    "url": "{{eventTrigger.input.apiBaseUrl}}/transactions/{{eventTrigger.data.transactionHash}}",
    "method": "POST",
    "headers": {
      "Authorization": "Bearer {{secrets.apiToken}}",
      "Content-Type": "application/json",
      "X-Chain-ID": "{{eventTrigger.input.chainId}}"
    },
    "body": {
      "blockNumber": "{{eventTrigger.data.blockNumber}}",
      "amount": "{{eventTrigger.data.valueFormatted}}",
      "token": "{{eventTrigger.data.tokenSymbol}}",
      "from": "{{eventTrigger.data.from}}",
      "to": "{{eventTrigger.data.to}}"
    }
  }
}
```

### Loop Node with ManualTrigger Array

```javascript
// ManualTrigger with array data
manualTrigger.data = [
  { name: "item1", value: 100 },
  { name: "item2", value: 200 },
  { name: "item3", value: 300 }
];

// Loop node configuration
{
  "type": "loop",
  "config": {
    "inputNodeName": "manualTrigger",  // Access the full data array
    "iterVal": "item",
    "iterKey": "index",
    "executionMode": "sequential",
    "runner": {
      "type": "customCode",
      "config": {
        "lang": "JavaScript",
        "source": "return `${item.name}: ${item.value * 2}`;"
      }
    }
  }
}

// Loop node output - flat array of results
loopNode.data = [
  "item1: 200",
  "item2: 400", 
  "item3: 600"
];
```

## Error Handling and Edge Cases

### Missing Data

- If `.data` is not available, it will be `undefined` in JavaScript
- If `.input` is not available, it will be `undefined` in JavaScript
- Template variables that resolve to undefined will be replaced with `"undefined"` string

### Variable Name Conflicts

- Variable names are sanitized to be valid JavaScript identifiers
- Special characters are replaced with underscores
- Duplicate names are handled by the VM's variable management system

### Type Safety

- All data is converted to JavaScript-compatible types
- Complex objects are properly serialized/deserialized
- Protobuf structures are converted to plain JavaScript objects

### ManualTrigger Validation

- Data is required and cannot be null or undefined
- Data must be valid JSON (objects, arrays, primitives)
- String representations of JSON are not parsed
- Invalid data types result in clear error messages

## Testing and Validation

### Test Structure

Tests should validate both `.data` and `.input` access:

```javascript
// Test trigger data access in VM variables
expect(triggerStep.output.data).toBeDefined();

// Test trigger input data access in VM variables (NOT execution step input)
expect(nodeStep.inputsList).toContain("triggerName.input");

// Test node data access  
expect(nodeStep.output.data).toBeDefined();
expect(nodeStep.input).toBeDefined(); // Node execution step input contains config

// Test variable availability in inputsList
expect(nodeStep.inputsList).toContain("triggerName.data");
expect(nodeStep.inputsList).toContain("triggerName.input");
```

### ManualTrigger Test Examples

```javascript
// Test JSON object data
const objectData = { apiKey: "test123", baseUrl: "https://api.test.com" };
const result = await client.runTrigger({
  triggerType: TriggerType.Manual,
  triggerConfig: { data: objectData }
});
expect(result.success).toBe(true);
expect(result.data).toEqual(objectData);

// Test JSON array data
const arrayData = [{ name: "item1" }, { name: "item2" }];
const result = await client.runTrigger({
  triggerType: TriggerType.Manual,
  triggerConfig: { data: arrayData }
});
expect(result.success).toBe(true);
expect(result.data).toEqual(arrayData);

// Test primitive data
const primitiveData = "Hello World";
const result = await client.runTrigger({
  triggerType: TriggerType.Manual,
  triggerConfig: { data: primitiveData }
});
expect(result.success).toBe(true);
expect(result.data).toEqual(primitiveData);

// Test validation errors
const result = await client.runTrigger({
  triggerType: TriggerType.Manual,
  triggerConfig: { data: null }
});
expect(result.success).toBe(false);
expect(result.error).toContain("ManualTrigger data is required");
```

### Execution Step Input Field vs VM Variables

**Important distinction:**

- **Execution Step `Input` field**: Contains configuration data used for debugging/inspection
  - For triggers: Contains trigger configuration (queries, schedules, etc.)
  - For nodes: Contains node configuration (URL, method, source code, etc.)

- **Execution Step `Output` field**: Contains runtime execution results
  - For most triggers: Wrapped in data field (e.g., `{ data: { blockNumber: 123 } }`)
  - For ManualTrigger: Raw data directly (e.g., `[{"key": "value1"}]` not wrapped)
  - For nodes: Contains processed output data
  
- **VM Variables**: Contains both runtime data and input data for cross-node access
  - `triggerName.data`: Runtime output from trigger execution
  - `triggerName.input`: Custom input data provided when creating the trigger
  - `nodeName.data`: Runtime output from node execution  
  - `nodeName.input`: Custom input data provided when creating the node

### Validation Rules

1. Every trigger must expose both `.data` and `.input` fields
2. Every node must expose both `.data` and `.input` fields
3. Subsequent nodes must be able to access previous trigger/node variables
4. Template resolution must work for both `.data` and `.input` fields
5. InputsList must accurately reflect available variables
6. ManualTrigger data must be valid JSON and cannot be null

## Best Practices

### 1. Data Structure Design

- Keep trigger input data focused on configuration
- Use consistent naming conventions across triggers and nodes
- Structure complex data hierarchically for easy access
- Include metadata for debugging and monitoring

### 2. Error Handling

- Always check for data availability before accessing
- Provide meaningful error messages
- Include context in error objects
- Use try-catch blocks in CustomCode nodes

### 3. Performance Considerations

- Minimize data copying between nodes
- Use efficient data structures for large datasets
- Consider memory usage for long-running workflows
- Implement proper cleanup for temporary data

### 4. Security

- Validate all external data inputs
- Use secrets management for sensitive data
- Sanitize data before using in templates
- Implement proper access controls

## Output Data Structure Standardization

### Standardization Plan (2025)

To improve code maintainability and provide consistent developer experience, we are standardizing all triggers and nodes to use **Direct Mapping** with `google.protobuf.Value data = 1` structure.

#### **Phase 1: Protobuf Standardization**

All trigger and node Output messages will use the standardized structure:

```protobuf
// STANDARDIZED structure for ALL triggers
message BlockTrigger {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Standardized (was: multiple fields)
  }
}

message EventTrigger {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct
  }
}

message ManualTrigger {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct
  }
}

message CronTrigger {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Standardized (was: multiple fields)
  }
}

message FixedTimeTrigger {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Standardized (was: multiple fields)
  }
}

// STANDARDIZED structure for ALL nodes
message CustomCodeNode {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct
  }
}

message RestAPINode {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct
  }
}

message ContractReadNode {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct
  }
}

message ContractWriteNode {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct
  }
}

message ETHTransferNode {
  message Output {
    google.protobuf.Value data = 1;  // ❌ Currently: string transaction_hash = 1;
  }
}

message BranchNode {
  message Output {
    google.protobuf.Value data = 1;  // ❌ Currently: string condition_id = 1;
  }
}

message LoopNode {
  message Output {
    google.protobuf.Value data = 1;  // ❌ Currently: string data = 1;
  }
}

message FilterNode {
  message Output {
    google.protobuf.Value data = 1;  // ✅ Already correct (uses Any, will migrate to Value)
  }
}

message GraphQLQueryNode {
  message Output {
    google.protobuf.Value data = 1;  // ❌ Currently: google.protobuf.Any data = 1;
  }
}
```

#### **Phase 2: Backend Implementation**

Update all backend trigger and node implementations to populate the `data` field:

```go
// Example: BlockTrigger backend implementation
func createBlockTriggerOutput(blockInfo *BlockInfo) *avsproto.BlockTrigger_Output {
    outputData := map[string]interface{}{
        "blockNumber": blockInfo.Number,
        "blockHash": blockInfo.Hash,
        "timestamp": blockInfo.Timestamp,
        "parentHash": blockInfo.ParentHash,
        "difficulty": blockInfo.Difficulty,
        "gasLimit": blockInfo.GasLimit,
        "gasUsed": blockInfo.GasUsed,
    }
    
    dataValue, err := convertToProtobufValue(outputData)
    if err != nil {
        return nil
    }
    
    return &avsproto.BlockTrigger_Output{
        Data: dataValue, // ✅ Standardized approach
    }
}

// Example: ETHTransferNode backend implementation
func createETHTransferOutput(txHash string, amount string, recipient string) *avsproto.ETHTransferNode_Output {
    outputData := map[string]interface{}{
        "transactionHash": txHash,
        "amountSent": amount,
        "recipient": recipient,
        "status": "success",
    }
    
    dataValue, err := convertToProtobufValue(outputData)
    if err != nil {
        return nil
    }
    
    return &avsproto.ETHTransferNode_Output{
        Data: dataValue, // ✅ Standardized approach
    }
}
```

#### **Phase 3: SDK Simplification**

Replace the complex switch statement with a unified output handler:

```typescript
// SIMPLIFIED SDK mapping - ONE function handles ALL cases
static getOutput(step: avs_pb.Execution.Step): OutputDataProps {
  const outputData = this.extractOutputData(step);
  if (!outputData) return null;
  
  // ALL triggers and nodes use the same Direct Mapping logic
  if (typeof outputData.hasData === "function" && outputData.hasData()) {
    try {
      return convertProtobufValueToJs(outputData.getData());
    } catch (error) {
      console.warn("Failed to convert protobuf Value to JavaScript:", error);
      return outputData.getData();
    }
  } else if (outputData.data) {
    // For plain objects, try to convert or use directly
    return typeof outputData.data.getKindCase === "function"
      ? convertProtobufValueToJs(outputData.data)
      : outputData.data;
  }
  
  return null;
}

private static extractOutputData(step: avs_pb.Execution.Step): any {
  // Simple switch to get the output object - all handled identically
  const outputCase = this.getOutputDataCase(step);
  switch (outputCase) {
    case avs_pb.Execution.Step.OutputDataCase.BLOCK_TRIGGER:
      return typeof step.getBlockTrigger === "function" 
        ? step.getBlockTrigger() 
        : (step as any).blockTrigger;
    case avs_pb.Execution.Step.OutputDataCase.EVENT_TRIGGER:
      return typeof step.getEventTrigger === "function" 
        ? step.getEventTrigger() 
        : (step as any).eventTrigger;
    case avs_pb.Execution.Step.OutputDataCase.MANUAL_TRIGGER:
      return typeof step.getManualTrigger === "function" 
        ? step.getManualTrigger() 
        : (step as any).manualTrigger;
    case avs_pb.Execution.Step.OutputDataCase.CUSTOM_CODE:
      return typeof step.getCustomCode === "function" 
        ? step.getCustomCode() 
        : (step as any).customCode;
    case avs_pb.Execution.Step.OutputDataCase.REST_API:
      return typeof step.getRestApi === "function" 
        ? step.getRestApi() 
        : (step as any).restApi;
    // ... all other cases follow the same pattern
    default:
      return null;
  }
}
```

#### **Migration Priority**

1. **High Priority** (Breaking inconsistencies):
   - ✅ **LoopNode**: Fix `string data = 1` → `google.protobuf.Value data = 1`
   - ✅ **GraphQLNode**: Migrate `google.protobuf.Any data = 1` → `google.protobuf.Value data = 1`

2. **Medium Priority** (Standardize triggers):
   - ✅ **BlockTrigger**: Migrate multiple fields → `google.protobuf.Value data = 1`
   - ✅ **CronTrigger**: Migrate multiple fields → `google.protobuf.Value data = 1`  
   - ✅ **FixedTimeTrigger**: Migrate multiple fields → `google.protobuf.Value data = 1`

3. **Low Priority** (Simple nodes):
   - ✅ **ETHTransferNode**: Migrate `string transaction_hash = 1` → `google.protobuf.Value data = 1`
   - ✅ **BranchNode**: Migrate `string condition_id = 1` → `google.protobuf.Value data = 1`

#### **Backward Compatibility Strategy**

During migration, support both old and new formats:

```typescript
// Backward compatibility during migration
static getOutput(step: avs_pb.Execution.Step): OutputDataProps {
  // Try new standardized approach first
  const standardizedOutput = this.getStandardizedOutput(step);
  if (standardizedOutput !== undefined) {
    return standardizedOutput;
  }
  
  // Fallback to legacy format for unmigrated types
  return this.getLegacyOutput(step);
}

private static getStandardizedOutput(step: avs_pb.Execution.Step): OutputDataProps | undefined {
  // Handle all types that have been migrated to google.protobuf.Value data = 1
  const outputData = this.extractOutputData(step);
  if (!outputData) return undefined;
  
  // Check if this type uses the new standardized format
  if (this.usesStandardizedFormat(step)) {
    return this.convertProtobufValueOutput(outputData);
  }
  
  return undefined;
}

private static getLegacyOutput(step: avs_pb.Execution.Step): OutputDataProps {
  // Handle legacy formats during migration period
  const outputCase = this.getOutputDataCase(step);
  switch (outputCase) {
    case avs_pb.Execution.Step.OutputDataCase.ETH_TRANSFER:
      // Legacy: direct transaction_hash field
      const ethTransfer = this.extractOutputData(step);
      return { transactionHash: ethTransfer?.transactionHash };
    case avs_pb.Execution.Step.OutputDataCase.BRANCH:
      // Legacy: direct condition_id field  
      const branch = this.extractOutputData(step);
      return { conditionId: branch?.conditionId };
    // ... other legacy cases
    default:
      return null;
  }
}
```

### Benefits of Standardization

1. **Code Simplification**:
   - **Before**: 15+ different output handling cases with special logic
   - **After**: 1 unified output handling function

2. **Consistency**:
   - **Before**: Mixed output structures (`{data: ...}` vs direct vs wrapped)
   - **After**: All outputs are JavaScript native objects/arrays

3. **Developer Experience**:
   - **Before**: Different output formats require different access patterns
   - **After**: Predictable, consistent output structure across all components

4. **Type Safety**:
   ```typescript
   // Clean, predictable types after standardization
   interface StepOutput {
     output: string | number | boolean | object | array | null; // Always direct data
   }
   ```

5. **Maintainability**:
   - Single output conversion function
   - Consistent protobuf structure
   - Unified backend implementation patterns

## Future Enhancements

### Planned Improvements

1. **Type Definitions**: Add TypeScript definitions for trigger/node variable structures
2. **Validation**: Add runtime validation for variable access patterns
3. **Documentation**: Auto-generate variable documentation from protobuf definitions
4. **Debugging**: Enhanced debugging tools for variable inspection
5. **Performance**: Optimize variable creation and access patterns
6. **Schema Validation**: JSON schema validation for ManualTrigger data
7. **Data Transformation**: Built-in data transformation utilities
8. **Caching**: Intelligent caching for frequently accessed variables

### Considerations

- Backward compatibility with existing workflows
- Performance impact of variable creation and access
- Memory usage for large variable datasets
- Security implications of cross-node data access
- Scalability for complex workflow graphs

---

This design ensures consistent, predictable access to both configuration and runtime data across the entire workflow execution, enabling powerful data flow patterns while maintaining clear separation between input configuration and output results. The recent updates to ManualTrigger provide more flexible JSON data handling while maintaining strict validation requirements. 