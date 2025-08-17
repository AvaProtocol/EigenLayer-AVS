# EventTrigger Response Structure Improvement

## Problem Statement

Currently, when eventTrigger conditions don't match, the system returns `data: null`, discarding valuable method call results. This makes debugging and data analysis difficult.

### Current Behavior (Problematic)
```javascript
// Oracle price check with condition: current < 4000
{
  triggerConfig: {
    methodCalls: [{ methodName: 'decimals' }],
    conditions: [{ fieldName: 'current', operator: 'lt', value: '4000' }]
  }
}

// If current price = 4200 (condition fails)
Response: {
  success: true,
  data: null,  // ❌ Lost valuable data!
  executionContext: { isSimulated: true }
}
```

### Proposed Behavior (Improved)
```javascript
// Same configuration, but always return method call data
Response: {
  success: true,
  data: {
    decimals: 18,           // ✅ Always include method call results
    current: 4200,          // ✅ Always include current oracle data
    conditionsMet: false,   // ✅ Clear indication of condition status
    matchedConditions: []   // ✅ Which conditions passed/failed
  },
  executionContext: { isSimulated: true }
}
```

## Implementation Strategy

### Phase 1: Separate Data Collection from Condition Evaluation

#### Current Flow (Problematic):
```
1. Execute methodCalls → Get data
2. Evaluate conditions → Pass/Fail
3. If fail → return null (discard data)
4. If pass → return data
```

#### Improved Flow:
```
1. Execute methodCalls → Get data
2. Evaluate conditions → Pass/Fail  
3. ALWAYS return data + condition status
4. Let workflow decide what to do with condition status
```

### Phase 2: Enhanced Response Structure

```go
type EventTriggerResponse struct {
    // Always present: method call results
    MethodCallData map[string]interface{} `json:"methodCallData"`
    
    // Always present: condition evaluation results
    ConditionsMet bool `json:"conditionsMet"`
    MatchedConditions []ConditionResult `json:"matchedConditions"`
    
    // Optional: event data (if actual events found)
    EventData map[string]interface{} `json:"eventData,omitempty"`
    
    // Metadata
    ExecutionContext ExecutionContext `json:"executionContext"`
}

type ConditionResult struct {
    FieldName    string      `json:"fieldName"`
    Operator     string      `json:"operator"`
    ExpectedValue string     `json:"expectedValue"`
    ActualValue  interface{} `json:"actualValue"`
    Passed       bool        `json:"passed"`
    Reason       string      `json:"reason,omitempty"`
}
```

### Phase 3: Backward Compatibility

```go
func buildEventTriggerResponse(methodData map[string]interface{}, conditionResults []ConditionResult) map[string]interface{} {
    allConditionsMet := true
    for _, result := range conditionResults {
        if !result.Passed {
            allConditionsMet = false
            break
        }
    }
    
    response := make(map[string]interface{})
    
    // Always include method call data
    for key, value := range methodData {
        response[key] = value
    }
    
    // Add condition evaluation metadata
    response["conditionsMet"] = allConditionsMet
    response["matchedConditions"] = conditionResults
    
    return response
}
```

## Use Case Examples

### Example 1: Oracle Price Monitoring
```javascript
// Configuration
{
  methodCalls: [
    { methodName: 'decimals' },
    { methodName: 'latestAnswer' }
  ],
  conditions: [
    { fieldName: 'current', operator: 'lt', value: '4000' }
  ]
}

// Response (condition failed: price = 4200)
{
  success: true,
  data: {
    decimals: 18,
    latestAnswer: 420000000000,  // Raw value
    current: 4200,               // Formatted value
    conditionsMet: false,
    matchedConditions: [{
      fieldName: 'current',
      operator: 'lt',
      expectedValue: '4000',
      actualValue: 4200,
      passed: false,
      reason: 'Value 4200 is not less than 4000'
    }]
  }
}
```

### Example 2: Multi-Condition Oracle Check
```javascript
// Configuration
{
  methodCalls: [
    { methodName: 'decimals' },
    { methodName: 'latestAnswer' },
    { methodName: 'updatedAt' }
  ],
  conditions: [
    { fieldName: 'current', operator: 'lt', value: '4000' },
    { fieldName: 'updatedAt', operator: 'gt', value: '1735689600' }
  ]
}

// Response (mixed results)
{
  success: true,
  data: {
    decimals: 18,
    latestAnswer: 385000000000,
    current: 3850,
    updatedAt: 1735689500,
    conditionsMet: false,  // Not all conditions passed
    matchedConditions: [
      {
        fieldName: 'current',
        operator: 'lt',
        expectedValue: '4000',
        actualValue: 3850,
        passed: true,
        reason: 'Value 3850 is less than 4000'
      },
      {
        fieldName: 'updatedAt',
        operator: 'gt', 
        expectedValue: '1735689600',
        actualValue: 1735689500,
        passed: false,
        reason: 'Value 1735689500 is not greater than 1735689600'
      }
    ]
  }
}
```

## Benefits

### ✅ Better Debugging
- Always see actual values vs expected values
- Clear indication of which conditions passed/failed
- Detailed reasoning for condition failures

### ✅ Enhanced Workflow Logic
- Workflows can make decisions based on actual data
- Partial condition matching can trigger different actions
- More granular control over trigger behavior

### ✅ Improved User Experience
- No more mysterious `data: null` responses
- Rich data for dashboard displays
- Better error messages and alerts

### ✅ Backward Compatibility
- Existing workflows continue to work
- `conditionsMet` field provides the old boolean logic
- Method call data structure remains unchanged

## Implementation Plan

### Step 1: Modify `runEventTriggerWithTenderlySimulation`
```go
// Instead of returning null when conditions fail:
if !conditionsMet {
    return nil, nil  // ❌ Current behavior
}

// Return data with condition status:
response := buildEventTriggerResponse(methodCallData, conditionResults)
return response, nil  // ✅ New behavior
```

### Step 2: Enhance Condition Evaluation
```go
func evaluateConditionsWithDetails(data map[string]interface{}, conditions []Condition) []ConditionResult {
    results := make([]ConditionResult, len(conditions))
    
    for i, condition := range conditions {
        actualValue := data[condition.FieldName]
        passed := evaluateCondition(actualValue, condition)
        
        results[i] = ConditionResult{
            FieldName:     condition.FieldName,
            Operator:      condition.Operator,
            ExpectedValue: condition.Value,
            ActualValue:   actualValue,
            Passed:        passed,
            Reason:        buildConditionReason(actualValue, condition, passed),
        }
    }
    
    return results
}
```

### Step 3: Update Response Building
```go
func buildEventTriggerResponse(methodData map[string]interface{}, conditionResults []ConditionResult) map[string]interface{} {
    // Implementation as shown above
}
```

## Migration Strategy

### Phase 1: Implement Enhanced Response (Non-Breaking)
- Add new fields to response structure
- Keep existing behavior for backward compatibility
- Add feature flag for new behavior

### Phase 2: Update Documentation and Examples
- Update API documentation
- Provide migration examples
- Update SDK to handle new response structure

### Phase 3: Deprecate Old Behavior
- Mark old null-return behavior as deprecated
- Provide migration timeline
- Eventually remove null-return logic

## Conclusion

This improvement transforms eventTrigger from a boolean condition checker into a rich data provider with condition evaluation metadata. It maintains backward compatibility while providing significantly more value for debugging, monitoring, and workflow decision-making.
