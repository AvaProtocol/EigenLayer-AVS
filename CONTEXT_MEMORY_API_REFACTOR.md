# Context-Memory API Consumption Refactor Plan

## Overview

The context-memory API (`/api/summarize`) has been restructured to return structured data instead of pre-rendered HTML. The aggregator (Go code) is now responsible for rendering this data into the appropriate formats for each notification channel (Email/SendGrid, Telegram, Discord).

## Current vs New API Response Structure

### Current Response (OLD)
```go
type contextMemorySummarizeResponse struct {
    Subject           string   `json:"subject"`
    Summary           string   `json:"summary"`           // One-liner summary
    AnalysisHtml      string   `json:"analysisHtml"`      // Pre-formatted HTML
    Body              string   `json:"body"`              // Plain text body
    StatusHtml        string   `json:"statusHtml"`        // Status badge HTML
    Status            string   `json:"status"`            // success/partial_success/failure
    PromptVersion     string   `json:"promptVersion"`
    Cached            bool     `json:"cached,omitempty"`
    BranchSummaryHtml string   `json:"branchSummaryHtml,omitempty"`
    SkippedNodes      []string `json:"skippedNodes,omitempty"`
}
```

### New Response (NEW)
```go
type contextMemorySummarizeResponse struct {
    Subject       string                       `json:"subject"`
    Body          contextMemorySummarizeBody   `json:"body"`
    PromptVersion string                       `json:"promptVersion"`
    Cached        bool                         `json:"cached,omitempty"`
}

type contextMemorySummarizeBody struct {
    Summary     string   `json:"summary"`      // One-line execution summary
    Status      string   `json:"status"`       // "success", "partial_success", "failure"
    Trigger     string   `json:"trigger"`      // What triggered the workflow (text)
    TriggeredAt string   `json:"triggeredAt"`  // ISO 8601 timestamp
    Executions  []string `json:"executions"`   // On-chain operation descriptions
    Errors      []string `json:"errors"`       // Error descriptions (failed steps + skipped nodes)
}
```

## Implementation Plan

### Step 1: Update Response Structs

**File:** `core/taskengine/summarizer_context_memory.go`

Update `contextMemorySummarizeResponse` to match the new API structure with a nested `body` object.

```go
// contextMemorySummarizeBody contains the structured workflow execution summary
type contextMemorySummarizeBody struct {
    Summary     string   `json:"summary"`     // One-line execution summary
    Status      string   `json:"status"`      // "success", "partial_success", "failure"
    Trigger     string   `json:"trigger"`     // Trigger description text
    TriggeredAt string   `json:"triggeredAt"` // ISO 8601 timestamp (from trigger output)
    Executions  []string `json:"executions"`  // On-chain operation descriptions
    Errors      []string `json:"errors"`      // Failed steps and skipped node descriptions
}

// contextMemorySummarizeResponse matches the new API response structure
type contextMemorySummarizeResponse struct {
    Subject       string                     `json:"subject"`
    Body          contextMemorySummarizeBody `json:"body"`
    PromptVersion string                     `json:"promptVersion"`
    Cached        bool                       `json:"cached,omitempty"`
}
```

### Step 2: Update Summary Struct

**File:** `core/taskengine/summarizer_deterministic.go`

Add new fields to the `Summary` struct to hold the structured data:

```go
type Summary struct {
    Subject      string   // Email subject line
    Body         string   // Plain text body (for backward compat / simple cases)
    SummaryLine  string   // One-line summary (body.summary)
    Status       string   // Execution status: success/partial_success/failure

    // New structured fields from API
    Trigger      string   // What triggered the workflow
    TriggeredAt  string   // ISO 8601 timestamp
    Executions   []string // On-chain operation descriptions
    Errors       []string // Error descriptions

    // Legacy fields (keep for backward compat, will be deprecated)
    AnalysisHtml      string   // Deprecated: aggregator now renders HTML
    StatusHtml        string   // Deprecated: aggregator now renders HTML
    BranchSummaryHtml string   // Deprecated: now part of Errors array
    SkippedNodes      []string // Deprecated: now part of Errors array
}
```

### Step 3: Update Summarize Method Response Mapping

**File:** `core/taskengine/summarizer_context_memory.go`

Update the `Summarize()` method to map the new response structure:

```go
return Summary{
    Subject:     apiResp.Subject,
    SummaryLine: apiResp.Body.Summary,
    Status:      apiResp.Body.Status,
    Trigger:     apiResp.Body.Trigger,
    TriggeredAt: apiResp.Body.TriggeredAt,
    Executions:  apiResp.Body.Executions,
    Errors:      apiResp.Body.Errors,
    // Compose plain text Body for backward compat
    Body:        composePlainTextBody(apiResp.Body),
}, nil
```

### Step 4: Implement Email Rendering (SendGrid Dynamic Template)

**File:** `core/taskengine/vm_runner_rest.go`

The aggregator should render the structured data into SendGrid `dynamic_template_data`:

```go
// renderEmailData renders Summary into SendGrid dynamic template data
func renderEmailData(s Summary, chainName string) map[string]interface{} {
    data := map[string]interface{}{
        "subject":      s.Subject,
        "summary":      s.SummaryLine,
        "status":       s.Status,
        "status_color": getStatusColor(s.Status), // "green", "yellow", "red"
        "trigger":      s.Trigger,
        "triggered_at": formatTimestamp(s.TriggeredAt),
        "chain_name":   chainName,
    }

    // Executions array (on-chain operations)
    if len(s.Executions) > 0 {
        data["executions"] = s.Executions
        data["has_executions"] = true
    }

    // Errors array (failed steps + skipped nodes)
    if len(s.Errors) > 0 {
        data["errors"] = s.Errors
        data["has_errors"] = true
    }

    return data
}

func getStatusColor(status string) string {
    switch status {
    case "success":
        return "green"
    case "partial_success":
        return "yellow"
    case "failure":
        return "red"
    default:
        return "gray"
    }
}
```

**SendGrid Template Variables:**
| Variable | Description |
|----------|-------------|
| `{{subject}}` | Email subject line |
| `{{summary}}` | One-line execution summary |
| `{{status}}` | Status text: success/partial_success/failure |
| `{{status_color}}` | Color for status badge: green/yellow/red |
| `{{trigger}}` | Trigger description text |
| `{{triggered_at}}` | Formatted timestamp |
| `{{chain_name}}` | Chain name (Ethereum, Sepolia, etc.) |
| `{{#if has_executions}}` | Conditional for executions section |
| `{{#each executions}}` | Loop through execution items |
| `{{#if has_errors}}` | Conditional for errors section |
| `{{#each errors}}` | Loop through error items |

### Step 5: Implement Telegram Rendering

**File:** `core/taskengine/summarizer.go` (in `FormatSummaryForChannel`)

Update to render structured data into Telegram HTML format:

```go
func FormatSummaryForTelegram(s Summary) string {
    var sb strings.Builder

    // Status emoji
    statusEmoji := getStatusEmoji(s.Status)

    // Header: status emoji + summary
    sb.WriteString(fmt.Sprintf("%s <b>%s</b>\n\n", statusEmoji, s.SummaryLine))

    // Trigger section
    if s.Trigger != "" {
        sb.WriteString(fmt.Sprintf("<b>Trigger:</b> %s\n", s.Trigger))
        if s.TriggeredAt != "" {
            sb.WriteString(fmt.Sprintf("<i>%s</i>\n\n", formatTimestampShort(s.TriggeredAt)))
        } else {
            sb.WriteString("\n")
        }
    }

    // Executions section
    if len(s.Executions) > 0 {
        sb.WriteString("<b>Executed:</b>\n")
        for _, exec := range s.Executions {
            sb.WriteString(fmt.Sprintf("• %s\n", exec))
        }
        sb.WriteString("\n")
    }

    // Errors section (only if present)
    if len(s.Errors) > 0 {
        sb.WriteString("<b>Issues:</b>\n")
        for _, err := range s.Errors {
            sb.WriteString(fmt.Sprintf("• %s\n", err))
        }
    }

    return strings.TrimSpace(sb.String())
}

func getStatusEmoji(status string) string {
    switch status {
    case "success":
        return "✅"
    case "partial_success":
        return "⚠️"
    case "failure":
        return "❌"
    default:
        return "ℹ️"
    }
}
```

**Example Telegram Output:**
```
✅ Your workflow 'Token Swap' executed 3 out of 3 steps

Trigger: The current WETH/USDC pool value (2,500.00) moved above your condition (2,000.00).
2026-01-20 10:30

Executed:
• Approved 20.99 USDC to 0x3bFA...e48E for trading
• Swapped for ~0.00088 WETH via Uniswap V3
```

### Step 6: Update Fallback (Deterministic) Summary

**File:** `core/taskengine/summarizer_deterministic.go`

Update `ComposeSummary()` to also return structured data that matches the new format, ensuring consistent behavior when the API is unavailable:

```go
func ComposeSummary(vm *VM, currentStepName string) Summary {
    // ... existing logic ...

    return Summary{
        Subject:     subject,
        SummaryLine: summaryLine,
        Status:      status,
        Trigger:     triggerDesc,
        TriggeredAt: triggeredAt,
        Executions:  executions,
        Errors:      errors,
        Body:        composePlainTextBody(...),
    }
}
```

## File Changes Summary

| File | Changes |
|------|---------|
| `summarizer_context_memory.go` | Update response struct, mapping logic |
| `summarizer_deterministic.go` | Add new structured fields to Summary, update fallback |
| `summarizer.go` | Update FormatSummaryForChannel for new format |
| `vm_runner_rest.go` | Add email rendering function, update RestAPI handling |

## Migration Notes

1. **Backward Compatibility:** Keep legacy fields (`AnalysisHtml`, `StatusHtml`, etc.) but mark as deprecated. The `Body` field will be composed from structured data for channels that need plain text.

2. **Rendering Responsibility Shift:** The API no longer returns pre-rendered HTML. The aggregator is responsible for:
   - Email: Pass structured data to SendGrid dynamic templates
   - Telegram: Render HTML using `<b>`, `<i>`, `<code>` tags
   - Discord: Render markdown using `**`, `*`, backticks

3. **Transfer Notifications:** The existing `TransferEventData` and `FormatTransferMessage()` logic remains unchanged - these bypass the context-memory API entirely for simple transfer events.

## Request Fields

### runNumber (New)

The `runNumber` field has been added to the API request to support execution numbering in deployed workflows:

```go
type contextMemorySummarizeRequest struct {
    // ... existing fields ...
    RunNumber int64 `json:"runNumber,omitempty"` // 1-based execution number
}
```

**Behavior:**
- `isSimulation` (in `settings`) is the explicit flag that determines simulation vs real execution
- For real executions (`isSimulation: false`): `runNumber` is 1-based (1 = first run, 2 = second run, etc.)
- For simulations (`isSimulation: true`): `runNumber` is ignored; use "Simulation:" prefix in subject

**Usage in API:**
- If `isSimulation` is true: Use "Simulation:" prefix (ignore `runNumber`)
- If `isSimulation` is false: Include "Run #N" in subject (e.g., "Run #5: Token Swap completed")

**Source:** The run number is extracted from `workflow.executionCount` in the VM context, which is set from `task.ExecutionCount` (incremented before each real execution in `executor.go`).

## Testing

1. Update `summarizer_ai_test.go` to mock the new response structure
2. Test email rendering with mock SendGrid template
3. Test Telegram output formatting
4. Verify fallback deterministic summary matches new structure
