# EventTrigger Data and Metadata Structure Requirements

## Overview
Fix the eventTrigger response structure to match the expected format for both `runNodeImmediately` and `simulateWorkflow`, focusing on proper data and metadata population.

## Current Problem
The eventTrigger simulation path is not executing method calls properly, resulting in:
- Missing method call results in `data` field
- Missing contract read metadata in `metadata` field
- Event data being included when only method calls should be executed

## Required Response Structure

### **Data Field**
The `data` field should contain **only the method call results**, not event data:

```json
{
  "data": {
    "decimals": "8",           // From decimals() method call
    "latestAnswer": "442276990000"  // From latestAnswer() method call
  }
}
```

**NOT:**
```json
{
  "data": {
    "current": "0x...",        // ❌ Event data should not be here
    "eventName": "AnswerUpdated", // ❌ Event data should not be here
    "decimals": "8",           // ✅ Method call result
    "latestAnswer": "442..."   // ✅ Method call result
  }
}
```

### **Metadata Field**
The `metadata` field should contain the raw contract read responses and condition evaluation:

```json
{
  "metadata": {
    "decimals": {
      "methodName": "decimals",
      "success": true,
      "value": "8",
      "methodABI": {...}
    },
    "latestAnswer": {
      "methodName": "latestAnswer", 
      "success": true,
      "value": "442276990000",
      "methodABI": {...}
    },
    "conditionEvaluation": [
      {
        "fieldName": "current",
        "operator": "lt",
        "expectedValue": "1000",
        "actualValue": "442276990000",
        "passed": false,
        "reason": "Value 442276990000 is not less than 1000"
      }
    ]
  }
}
```

### **ExecutionContext**
For now, **ignore executionContext** - focus only on data and metadata structure.

## Technical Issues to Fix

### **1. Method Call Execution**
- The simulation path is not executing method calls (`decimals`, `latestAnswer`)
- Debug logs show method calls are not being processed
- Need to ensure `executeMethodCallForSimulation` is called and working

### **2. Data Population**
- Remove event data from `data` field
- Only include method call results in `data` field
- Apply proper decimal formatting if needed

### **3. Metadata Population**
- Include raw contract read responses for each method call
- Include condition evaluation results
- Structure should match `contractRead` node metadata format

### **4. Event vs Method Call Separation**
- Event data (from Tenderly simulation) should not pollute method call results
- Method calls should be direct contract reads, independent of event simulation
- The test includes both `topics` and `methodCalls`, but only method call results should be in the response

## Test Case Context
The failing test case:
- **Method calls**: `decimals()`, `latestAnswer()`
- **Condition**: `current < 1000` (should fail)
- **Expected data**: Only method call results
- **Expected metadata**: Contract read responses + condition evaluation

## Success Criteria
1. ✅ `data` field contains only method call results (`decimals`, `latestAnswer`)
2. ✅ `metadata` field contains contract read responses and condition evaluation
3. ✅ No event data pollution in method call results
4. ✅ Proper decimal formatting applied where needed
5. ✅ Response structure matches `runNodeImmediately` format

## Implementation Notes
- Focus on data and metadata first, ignore success/error logic
- Ensure method calls execute independently of event simulation
- Maintain consistency with existing `contractRead` node metadata format
- Test should show proper method call execution and data population
