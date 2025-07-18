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

## Trigger Variable Structure

### EventTrigger Example

```javascript
// EventTrigger variable structure
eventTrigger = {
  data: {
    // Runtime output from event execution
    // For transfer events: tokenSymbol, valueFormatted, etc.
    // For other events: decoded event data
    blockNumber: 12345,
    transactionHash: "0x...",
    logIndex: 1,
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

### ManualTrigger Example

```javascript
// ManualTrigger variable structure
manualTrigger = {
  data: {
    // Runtime data provided during trigger execution
    // All webhook data is under .data for consistent access
    data: {
      // User-defined data payload
      apiBaseUrl: "https://api.example.com",
      apiKey: "key123",
      environment: "production"
    },
    headers: {
      // HTTP headers if this is a webhook trigger
      "Authorization": "Bearer token123",
      "Content-Type": "application/json"
    },
    pathParams: {
      // URL path parameters
      "version": "v1",
      "format": "json"
    }
  },
  input: {
    // Configuration data for the manual trigger
    // Usually contains webhook configuration, expected fields, etc.
    webhookPath: "/webhook/manual",
    expectedFields: ["apiBaseUrl", "apiKey"]
  }
}
```

## Node Variable Structure

### RestAPI Node Example

```javascript
// RestAPI node variable structure
restApiNode = {
  data: {
    // Runtime output from API call
    body: {
      // Response body data
      success: true,
      data: { /* API response */ }
    },
    headers: {
      // Response headers
      "Content-Type": "application/json",
      "Content-Length": "1234"
    },
    statusCode: 200
  },
  input: {
    // Configuration used for the API call
    url: "https://api.example.com/data",
    method: "POST",
    body: '{"key": "value"}',
    headersMap: [
      ["Content-Type", "application/json"],
      ["Authorization", "Bearer token"]
    ]
  }
}
```

### CustomCode Node Example

```javascript
// CustomCode node variable structure
customCodeNode = {
  data: {
    // Runtime output from code execution
    // This is whatever the JavaScript code returns
    result: "processed successfully",
    timestamp: "2025-01-17T10:30:00Z",
    processedItems: 42
  },
  input: {
    // Configuration for the custom code
    lang: "JavaScript",
    source: "return { result: 'processed successfully', timestamp: new Date().toISOString() };"
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
  "eventTrigger.data",              // Trigger output data (includes headers, pathParams for manual triggers)
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

### Chain Node Data Access

```javascript
// In a node that follows multiple previous nodes
const apiResult = restApiNode.data;
const triggerConfig = eventTrigger.input;
const previousProcessing = customCodeNode.data;

// For manual triggers, access webhook data under .data
const manualTriggerData = manualTrigger.data.data;
const manualTriggerHeaders = manualTrigger.data.headers;
const manualTriggerPathParams = manualTrigger.data.pathParams;

// Combine data from multiple sources
return {
  apiResponse: apiResult.body,
  triggerChain: triggerConfig.chainId,
  previousResult: previousProcessing.result,
  webhookData: manualTriggerData,
  combinedAt: new Date().toISOString()
};
```

### Template Usage

```json
{
  "type": "restApi",
  "config": {
    "url": "{{eventTrigger.input.apiBaseUrl}}/process",
    "method": "POST",
    "body": "{\"blockNumber\": {{eventTrigger.data.blockNumber}}, \"chainId\": {{eventTrigger.input.chainId}}}"
  }
}
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

### Execution Step Input Field vs VM Variables

**Important distinction:**

- **Execution Step `Input` field**: Contains configuration data used for debugging/inspection
  - For triggers: Contains trigger configuration (queries, schedules, etc.)
  - For nodes: Contains node configuration (URL, method, source code, etc.)
  
- **VM Variables**: Contains both runtime data and input data for cross-node access
  - `triggerName.data`: Runtime output from trigger execution
  - `triggerName.input`: Custom input data provided when creating the trigger
  - `nodeName.data`: Runtime output from node execution  
  - `nodeName.input`: Custom input data provided when creating the node

### EventTrigger Input Field Handling

For EventTriggers, there are two types of input data:

1. **Trigger Configuration** (in execution step `Input` field):
   ```javascript
   // Available in triggerStep.input
   {
     queries: [
       { addresses: [...], topics: [...] },
       { addresses: [...], topics: [...] }
     ]
   }
   ```

2. **Custom Input Data** (in VM variables):
   ```javascript
   // Available as eventTrigger.input in subsequent nodes
   {
     subType: "transfer",
     chainId: 11155111,
     address: "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
     tokens: [{ symbol: "USDC", ... }]
   }
   ```

The test failure indicates that the custom input data is not being properly extracted and made available in the VM variable system.

### Validation Rules

1. Every trigger must expose both `.data` and `.input` fields
2. Every node must expose both `.data` and `.input` fields
3. Subsequent nodes must be able to access previous trigger/node variables
4. Template resolution must work for both `.data` and `.input` fields
5. InputsList must accurately reflect available variables

## Future Enhancements

### Planned Improvements

1. **Type Definitions**: Add TypeScript definitions for trigger/node variable structures
2. **Validation**: Add runtime validation for variable access patterns
3. **Documentation**: Auto-generate variable documentation from protobuf definitions
4. **Debugging**: Enhanced debugging tools for variable inspection
5. **Performance**: Optimize variable creation and access patterns

### Considerations

- Backward compatibility with existing workflows
- Performance impact of variable creation and access
- Memory usage for large variable datasets
- Security implications of cross-node data access

---

This design ensures consistent, predictable access to both configuration and runtime data across the entire workflow execution, enabling powerful data flow patterns while maintaining clear separation between input configuration and output results. 