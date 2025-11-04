# Branch Node Email Summary Example

This document shows how Branch Node execution details are now displayed in SendGrid email summaries.

## Scenario

A workflow with a branch that checks if token balance is sufficient:
- **If condition**: `{{balance1.data.find(token => token?.tokenAddress?.toLowerCase() === settings.uniswapv3_pool.token1.id.toLowerCase()).balance > Number(settings.amount)}}`
- **Else condition**: Fallback when balance is insufficient

## Execution Log (Developer View)

```
Executing NODE_TYPE_BRANCH 'branch1'
If condition resolved to false
  Expression: {{balance1.data.find(token => token?.tokenAddress?.toLowerCase() === settings.uniswapv3_pool.token1.id.toLowerCase()).balance > Number(settings.amount)}}
  Variable values:
    balance1 = {"data":[{"balance":"0","symbol":"TEST"}]}
    settings = {"amount":"1"}

BranchNode 'branch1' resolved to Else condition -> led to node 'email_report_success'
```

**Key improvements:**
- Shows **original template expression**, not the resolved value "false"
- Uses "If condition resolved to false" instead of just "If: false"
- Removed redundant "Processed: false" line
- Added blank line before final resolution for better readability
- Shows which node the branch leads to

## Email HTML Summary (User View)

### Summary
The workflow did not fully execute. Some nodes were skipped due to branching conditions. No on-chain transactions executed.

### The below nodes were skipped due to branching conditions
- email_report_success
- get_quote
- approve_token1
- run_swap

### Branch 'branch1': selected Else condition -> led to node 'email_report_success'

If condition resolved to false  
  Expression: {{balance1.data.find(token => token?.tokenAddress?.toLowerCase() === settings.uniswapv3_pool.token1.id.toLowerCase()).balance > Number(settings.amount)}}  
  Variable values:  
    balance1 = {"data":[{"balance":"0","symbol":"TEST"}]}  
    settings = {"amount":"1"}

## Key Improvements

1. **Shows Original Expression**: Displays the template expression string, not the resolved boolean value ("false")
2. **Execution Log Format**: Email HTML now matches the developer execution log format for consistency
3. **Next Node Information**: Shows which node the branch leads to (e.g., "led to node 'email_report_success'")
4. **Variable Context**: Shows variable values for false conditions to help users understand why a condition failed
5. **No Redundant Information**: Removed "Processed: false" line when it's redundant
6. **Clear Resolution**: Uses "If condition resolved to false" instead of just "If: false"

## Implementation

The Branch node now stores evaluation details in the `Execution_Step.Metadata` field:

```json
{
  "conditionEvaluations": [
    {
      "index": 0,
      "id": "0",
      "type": "if",
      "label": "If",
      "expression": "{{balance1.data.find(...)}}",
      "evaluated": true,
      "result": false,
      "taken": false,
      "processedExpr": "false",
      "variableValues": {
        "balance1": {...},
        "settings": {...}
      }
    },
    {
      "index": 1,
      "id": "1",
      "type": "else",
      "label": "Else",
      "expression": "",
      "evaluated": true,
      "result": true,
      "taken": true
    }
  ]
}
```

This structured data is then used by `BuildBranchAndSkippedSummary()` to generate the improved email HTML.

