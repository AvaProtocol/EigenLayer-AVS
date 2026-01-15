# AI Workflow Email Summary - Product Requirements (PRD)

## Overview

Generate human-readable email summaries for workflow executions that explain what happened on-chain, not just whether it succeeded. Summaries are delivered via email notifications (e.g., SendGrid) when `options.summarize` is enabled.

## Architecture

The aggregator delegates all AI-powered summary generation to the **context-memory API** service. This decouples the email content generation from the aggregator, allowing:
- Independent iteration on summary quality
- Centralized prompt management
- User memory/context integration

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
- `macros.secrets.context_api_endpoint`: URL of the context-memory API
- `macros.secrets.context_api_key`: Authentication token for the API

## Summary Content

### Inputs (sent to context-memory API)
- **ownerEOA**: The externally owned account associated with the workflow
- **name**: Workflow name (from `settings.name`)
- **smartWallet**: Runner address that executed on-chain operations
- **steps**: Array of execution step digests including:
  - Step name, ID, type, success status
  - Contract address and method name (for contract interactions)
  - Output data and metadata
  - Execution context (is_simulated, chain_id)
  - Token metadata (symbol, decimals) for ERC20 interactions
- **nodes**: All workflow nodes (for detecting skipped steps)
- **edges**: Workflow edges (for branch analysis)
- **settings**: Workflow settings including `isSimulation` flag
- **chainName**: Network name (e.g., "sepolia", "base")

### Outputs (from context-memory API)
| Field | Description |
|-------|-------------|
| `subject` | Email subject line |
| `summary` | One-liner summary |
| `body` | Plain text body |
| `analysisHtml` | HTML-formatted step-by-step analysis |
| `statusHtml` | Status badge HTML (green/yellow/red) |
| `status` | Execution status: "success", "partial_success", "failure" |
| `branchSummaryHtml` | HTML summary of branch decisions (when nodes are skipped) |
| `skippedNodes` | List of skipped node names |

## Behavior

### Pass-Through Architecture
The aggregator acts as a pass-through for context-memory responses:

1. **Build request** from VM execution state (`summarizer_context_memory.go`)
2. **Call context-memory API** `/api/summarize` endpoint
3. **Pass response fields** directly to SendGrid dynamic template
4. **Fallback** to deterministic summary if API unavailable

### Deterministic Fallback
When context-memory API is unavailable or returns an error:
- Uses `ComposeSummary()` in `summarizer.go`
- Provides basic workflow status, step counts, and error information
- Always succeeds - never fails the notification

### Simulation Detection
The `(Simulated)` prefix in summaries is determined by:
1. **Per-step**: `executionContext.is_simulated` field in each step
2. **Workflow-level**: `settings.isSimulation` flag

Context-memory API uses the execution context to accurately label simulated vs real executions.

## Code References

### Key Files
| File | Purpose |
|------|---------|
| `core/taskengine/summarizer_context_memory.go` | Context-memory API client |
| `core/taskengine/summarizer_ai.go` | Summarizer interface and orchestration |
| `core/taskengine/summarizer.go` | Deterministic fallback implementation |
| `core/taskengine/vm_runner_rest.go` | SendGrid integration and pass-through |

### Entry Points
- `ComposeSummarySmart()` - Main entry point, tries context-memory then fallback
- `NewContextMemorySummarizerFromAggregatorConfig()` - Creates summarizer from config
- `ContextMemorySummarizer.Summarize()` - Calls context-memory API

## Testing

### Environment Variables
- `SERVICE_AUTH_TOKEN`: Auth token for context-memory API (tests)
- `CONTEXT_MEMORY_URL`: Override API URL (defaults to production)

### Running Tests
```bash
# Run summarizer tests
go test ./core/taskengine/... -run "TestComposeSummarySmart" -v

# Run with real context-memory API
SERVICE_AUTH_TOKEN=<token> go test ./core/taskengine/... -run "TestComposeSummarySmart" -v
```

## Example Summary Output

**Subject:** `Test Stoploss: succeeded (4 out of 7 steps)`

**Body:**
```
Smart wallet 0xeCb8...4bAE (owner 0xc60e...C788) executed 4 on-chain actions.

✓ Checked balance on Sepolia
✓ Evaluated branch condition (took path: else)
✓ Sent email notification

3 steps were skipped due to branch conditions: approve_token1, get_quote, run_swap

All steps completed on Sepolia.
```

## Non-Goals
- Direct OpenAI integration in the aggregator (delegated to context-memory)
- Generating calldata on the client
- Fallback to Tenderly simulation for deployed workflows
