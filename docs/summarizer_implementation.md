# Summarizer Implementation

## Overview

The aggregator generates human-readable email summaries for workflow executions that explain what happened on-chain, not just whether it succeeded. Summaries are delivered via email notifications (e.g., SendGrid) when enabled in the workflow configuration.

**Important**: The aggregator has **no direct AI integration**. All AI-powered summary generation is delegated to the **context-memory API** service (`https://context-api.avaprotocol.org`), which is a separate service that handles all AI operations. This decouples email content generation from the aggregator, allowing:
- Independent iteration on summary quality
- Centralized prompt management
- User memory/context integration
- No AI dependencies in the aggregator codebase

## Architecture

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────┐
│  Aggregator │────>│ Context-Memory   │────>│   SendGrid  │
│  (pass-     │     │ API              │     │   (email)   │
│   through)  │<────│ /api/summarize   │     │             │
└─────────────┘     └──────────────────┘     └─────────────┘
```

## Configuration

### Aggregator Config (aggregator.yaml)

```yaml
notifications:
  summary:
    enabled: true
    provider: "context-memory"

macros:
  secrets:
    context_api_endpoint: "https://context-api.avaprotocol.org"
    context_api_key: "<your-auth-token>"
```

### Required Fields
- `notifications.summary.enabled`: Must be `true`
- `notifications.summary.provider`: Must be `"context-memory"`
- `macros.secrets.context_api_endpoint`: URL of the context-memory API (defaults to `https://context-api.avaprotocol.org` if empty)
- `macros.secrets.context_api_key`: Authentication token for the API

### Initialization

The aggregator initializes the summarizer during engine startup (`core/taskengine/engine.go`):

1. Reads `notifications.summary.enabled` and `notifications.summary.provider` from config
2. Reads `context_api_endpoint` and `context_api_key` from `macros.secrets`
3. Creates `ContextMemorySummarizer` instance if all required fields are present
4. Sets it as the global summarizer via `SetSummarizer()`
5. Falls back to deterministic summaries if configuration is incomplete

## Execution Flow

### 1. Workflow Execution

When a workflow executes, the aggregator maintains execution state in a `VM` (Virtual Machine) object that tracks:
- Executed steps (`ExecutionLogs`)
- Workflow nodes and edges
- Variables and context
- Success/failure status

### 2. Notification Node Execution

When a notification node (e.g., SendGrid email) executes:

1. **Check if summarization is enabled**: The node's `config.options.summarize` must be `true`
   - This is checked by `shouldSummarize()` in `vm_runner_rest.go`
   
2. **Detect notification provider**: Determines if the URL is SendGrid, Telegram, or Discord

3. **Generate summary**: Calls `ComposeSummarySmart(vm, currentStepName)`

### 3. Summary Generation (`ComposeSummarySmart`)

The `ComposeSummarySmart()` function in `core/taskengine/summarizer.go`:

1. **Check if summarizer is configured**: If `globalSummarizer` is `nil`, immediately falls back to `ComposeSummary()`

2. **Call context-memory API** (if configured):
   - Creates a 30-second timeout context
   - Calls `globalSummarizer.Summarize(ctx, vm, currentStepName)`
   - This sends a POST request to `{baseURL}/api/summarize`

3. **Validate response**:
   - Subject and body must be non-empty
   - Body must be at least 40 characters
   - Falls back to deterministic summary if validation fails

4. **Fallback to deterministic summary** (`ComposeSummary()`) if:
   - Summarizer is not configured
   - API call fails (timeout, network error, non-2xx response)
   - Response validation fails

### 4. Request Building (`buildRequest`)

The `ContextMemorySummarizer.buildRequest()` function in `core/taskengine/summarizer_context_memory.go` extracts:

- **ownerEOA**: From `workflowContext.owner` or `workflowContext.eoaAddress`
- **name**: From `settings.name`
- **smartWallet**: From `workflowContext.runner` or `settings.runner`
- **steps**: Array of execution step digests including:
  - Step name, ID, type, success status
  - Full config (for triggers) or contract address/method (for contract nodes)
  - Output data and metadata
  - Execution context (`is_simulated`, `provider`, `chain_id`)
  - Token metadata (symbol, decimals) for ERC20 interactions
- **nodes**: All workflow nodes (for detecting skipped steps)
- **edges**: Workflow edges (for branch analysis)
- **settings**: Workflow settings including `isSimulation` flag
- **chainName**: Network name (e.g., "sepolia", "base")
- **tokenMetadata**: Map of all tokens involved, keyed by address (lowercase)

### 5. API Request

**Endpoint**: `POST {baseURL}/api/summarize`

**Headers**:
```
Content-Type: application/json
Authorization: Bearer {context_api_key}
```

**Request Body**:
```json
{
  "ownerEOA": "0x...",
  "name": "Workflow Name",
  "smartWallet": "0x...",
  "steps": [...],
  "chainName": "Sepolia",
  "nodes": [...],
  "edges": [...],
  "settings": {...},
  "currentNodeName": "...",
  "tokenMetadata": {...}
}
```

### 6. API Response

**Response Format**:
```json
{
  "subject": "Email subject line",
  "summary": "One-liner summary",
  "body": "Plain text body",
  "analysisHtml": "HTML-formatted step-by-step analysis",
  "statusHtml": "Status badge HTML (green/yellow/red)",
  "status": "success" | "partial_success" | "failure",
  "branchSummaryHtml": "HTML summary of branch decisions (when nodes are skipped)",
  "skippedNodes": ["node1", "node2"],
  "promptVersion": "...",
  "cached": false
}
```

### 7. SendGrid Integration

The summary is passed to SendGrid via dynamic template data (`vm_runner_rest.go`):

1. **Template ID**: Automatically set to `SendGridSummaryTemplateID` for AI summaries

2. **Dynamic Data**: Extracted via `summary.SendGridDynamicData()`:
   - `subject`: Email subject
   - `analysisHtml`: HTML-formatted analysis (from context-memory or generated from body)
   - `statusHtml`: Status badge HTML (if available)
   - `summary`: One-liner summary (if available)
   - `branchSummaryHtml`: Branch summary HTML (if available)
   - `skippedNodes`: List of skipped node names (if available)
   - `preheader`: Short preview (reuses subject)
   - `runner`: Smart wallet address
   - `eoaAddress`: Owner EOA address (shortened)
   - `year`: Current year for email footer

3. **Skipped Nodes Calculation**: Computed by comparing workflow nodes to executed step names

## Deterministic Fallback

When context-memory API is unavailable or returns an error, the aggregator uses `ComposeSummary()` in `core/taskengine/summarizer_deterministic.go`:

- Provides basic workflow status, step counts, and error information
- Generates plain text body (no HTML tags) to match context-memory format
- Always succeeds - never fails the notification
- Body format starts with summary line: `"Your workflow 'X' executed Y out of Z total steps"`

### Fallback Triggers

The fallback is used when:
1. `notifications.summary.enabled` is `false`
2. `notifications.summary.provider` is not `"context-memory"`
3. `macros.secrets.context_api_endpoint` is missing or empty
4. `macros.secrets.context_api_key` is missing or empty
5. API request fails (timeout, network error, non-2xx response)
6. API response is invalid (empty subject/body, body too short)

## Simulation Detection

The `(Simulated)` prefix in summaries is determined by:

1. **Per-step**: `executionContext.is_simulated` field in each step
2. **Workflow-level**: `settings.isSimulation` flag

Context-memory API uses the execution context to accurately label simulated vs real executions.

## Code Structure

The summarizer implementation is split across three files, each with a specific responsibility:

### File Organization

| File | Purpose | Responsibility |
|------|---------|----------------|
| `summarizer.go` | Orchestration layer | Entry point (`ComposeSummarySmart`) that tries context-memory API first, then falls back to deterministic summary |
| `summarizer_context_memory.go` | Context-memory API client | HTTP client that calls the external context-memory API service (no AI code in aggregator) |
| `summarizer_deterministic.go` | Deterministic fallback | Non-AI summary generation using rule-based logic (always available, never fails) |

### Why Three Files?

The separation follows a clear architectural pattern:

1. **`summarizer.go`** - **Orchestration Layer**
   - Contains `ComposeSummarySmart()` - the main entry point
   - Implements the fallback strategy: try context-memory API → validate response → fall back to deterministic
   - No direct AI or HTTP code - pure orchestration logic

2. **`summarizer_context_memory.go`** - **External API Client**
   - Implements the `Summarizer` interface
   - Makes HTTP requests to context-memory API (separate service)
   - Handles request building, HTTP communication, and response parsing
   - **No AI code** - just an HTTP client

3. **`summarizer_deterministic.go`** - **Deterministic Fallback**
   - Contains `ComposeSummary()` - rule-based summary generation
   - Always available, never requires external services
   - Generates summaries from execution logs using deterministic logic
   - No AI, no external dependencies

### Execution Flow

```
Notification Node
    ↓
ComposeSummarySmart() [summarizer.go]
    ↓
    ├─→ ContextMemorySummarizer.Summarize() [summarizer_context_memory.go]
    │       ↓
    │   HTTP POST to context-memory API
    │       ↓
    │   Success? → Return AI summary
    │   Failure? → Fall back
    │
    └─→ ComposeSummary() [summarizer_deterministic.go]
            ↓
        Return deterministic summary (always succeeds)
```

### Entry Points

- `ComposeSummarySmart()` - Main entry point (in `summarizer.go`), tries context-memory then fallback
- `NewContextMemorySummarizerFromAggregatorConfig()` - Creates summarizer from config (in `summarizer.go`)
- `ContextMemorySummarizer.Summarize()` - Calls context-memory API (in `summarizer_context_memory.go`)
- `ComposeSummary()` - Deterministic fallback (in `summarizer_deterministic.go`)
- `SetSummarizer()` - Sets global summarizer instance (in `summarizer.go`)

## Testing

### Environment Variables

- `SERVICE_AUTH_TOKEN`: Auth token for context-memory API (for tests)
- `CONTEXT_MEMORY_URL`: Override API URL (defaults to production constant)

### Running Tests

```bash
# Run summarizer tests (uses mock if SERVICE_AUTH_TOKEN not set)
go test ./core/taskengine/... -run "TestComposeSummarySmart" -v

# Run with real context-memory API
SERVICE_AUTH_TOKEN=<token> go test ./core/taskengine/... -run "TestComposeSummarySmart" -v

# Run integration tests (requires -tags=integration)
go test -v -tags=integration -count=1 ./core/taskengine -run TestContextMemoryExecutionSummary
```

### Test Files

- **`summarizer_ai_test.go`**: Tests `ComposeSummarySmart()` with real API or mocks (note: test file name may be updated in future)
- **`summarizer_context_memory_integration_test.go`**: Integration tests against real API (requires `-tags=integration`)
- **`summarizer_run_node_test.go`**: Tests deterministic `ComposeSummary()` fallback

## Example Summary Output

**Subject:** `Test Stoploss: succeeded (4 out of 7 steps)`

**Body:**
```
Your workflow 'Test Stoploss' executed 4 out of 7 total steps

Smart wallet 0xeCb8...4bAE (owner 0xc60e...C788) executed 4 on-chain actions.

What Executed On-Chain
✓ Checked balance on Sepolia
✓ Evaluated branch condition (took path: else)
✓ Sent email notification

3 steps were skipped due to branch conditions: approve_token1, get_quote, run_swap

All steps completed on Sepolia.
```

## Non-Goals

- **Direct AI integration in the aggregator** - All AI operations are handled by the context-memory API service
- **OpenAI API keys in aggregator config** - The aggregator has no AI dependencies and cannot use AI directly
- Generating calldata on the client
- Fallback to Tenderly simulation for deployed workflows

## Architecture Principles

1. **AI Decoupling**: The aggregator has zero AI dependencies. All AI operations are handled by the external context-memory API service.

2. **Graceful Degradation**: The system always provides summaries, even if the context-memory API is unavailable. The deterministic fallback ensures notifications never fail due to AI service issues.

3. **Separation of Concerns**: 
   - Orchestration logic (`summarizer.go`) is separate from HTTP client code (`summarizer_context_memory.go`)
   - Fallback logic (`summarizer_deterministic.go`) is independent and always available
   - Each file has a single, clear responsibility
