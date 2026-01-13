# Context Memory Integration Guide

## Overview

This document describes how to integrate the Go aggregator with the `context-api` service for AI-powered workflow summarization. The `context-api` service provides a REST API for generating execution summaries and managing workflow context.

## Production Endpoint

**Base URL**: `https://context-api.avaprotocol.org`

The production URL is defined as a constant (`ContextAPIURL`) in `core/taskengine/summarizer_context_memory.go` and is used by default. It can be overridden via the `CONTEXT_MEMORY_URL` environment variable.

## Authentication

The aggregator must authenticate using a Bearer token:
```
Authorization: Bearer <SERVICE_AUTH_TOKEN>
```

The `SERVICE_AUTH_TOKEN` should be configured in the aggregator's environment or config.

## Integration Tests

### Prerequisites

1. **SERVICE_AUTH_TOKEN**: Authentication token for the context-api service
   - For production: Configured in your environment or aggregator config
   - For local: Check your `.env.local` file in the context-memory project
   - You can find it in the Vercel dashboard under Environment Variables

2. **API URL**: The tests can run against either:
   - **Production**: `https://context-api.avaprotocol.org` (default, uses the exported `ContextAPIURL` constant from `summarizer_context_memory.go`)
   - **Local**: `http://localhost:3000` (set `CONTEXT_MEMORY_URL` env var)
   - **Custom**: Any URL via `CONTEXT_MEMORY_URL` env var
   
   Note: The default production URL comes from the `ContextAPIURL` constant exported from `core/taskengine/summarizer_context_memory.go`. The integration test file imports and uses this constant.

### Running Integration Tests

#### Set Environment Variables

**For Production (default):**
```bash
export SERVICE_AUTH_TOKEN="your-production-token"
# Production URL (https://context-api.avaprotocol.org) is used by default
```

**For Local Development:**
```bash
export SERVICE_AUTH_TOKEN="your-local-token"  # Optional for localhost
export CONTEXT_MEMORY_URL="http://localhost:3000"
```

**Note**: For localhost URLs, if `SERVICE_AUTH_TOKEN` is not set, tests will use a default local token (`test-auth-token-12345`). For production URLs, `SERVICE_AUTH_TOKEN` must be set or tests will be skipped.

**Important**: Make sure the local context-memory server is running:
```bash
cd /path/to/context-memory
vercel dev
# Note: If port 3000 is in use, Vercel will use 3001
# Check the output to see which port is being used
# Update CONTEXT_MEMORY_URL accordingly if needed
```

#### Run All Integration Tests

```bash
cd /path/to/EigenLayer-AVS
go test -v -tags=integration -count=1 ./core/taskengine -run TestContextMemoryExecutionSummary
```

#### Run Specific Test

```bash
# Test successful workflow
go test -v -tags=integration -count=1 ./core/taskengine -run TestContextMemoryExecutionSummary_SuccessfulWorkflow

# Test failed workflow
go test -v -tags=integration -count=1 ./core/taskengine -run TestContextMemoryExecutionSummary_FailedWorkflow

# Test complete workflow (no skipped nodes)
go test -v -tags=integration -count=1 ./core/taskengine -run TestContextMemoryExecutionSummary_CompleteWorkflow

# Test with VM structure (similar to real aggregator usage)
go test -v -tags=integration -count=1 ./core/taskengine -run TestContextMemoryExecutionSummary_FromVM
```

**Note**: Integration tests require the `-tags=integration` flag to run. They are excluded from regular test runs to avoid requiring external services.

### Test Coverage

The integration tests verify:

1. **Response Structure**: Validates that the API returns the expected `ExecutionSummaryResponse` structure
2. **Status Values**: Ensures status is one of `'success'`, `'partial_success'`, or `'failure'`
3. **Field Presence**: Verifies that `branchSummary` and `skippedNodes` are always present (even if empty)
4. **Metrics Accuracy**: Validates that `executedSteps`, `totalSteps`, and `skippedSteps` are calculated correctly
5. **Skipped Nodes**: Verifies that skipped nodes are correctly identified and returned
6. **Branch Summary**: Validates that branch summary text is generated when nodes/edges are provided

### Test Scenarios

#### TestContextMemoryExecutionSummary_SuccessfulWorkflow
- Tests a workflow with 3 executed steps and 1 skipped node
- Validates `partial_success` status
- Verifies skipped node identification

#### TestContextMemoryExecutionSummary_FailedWorkflow
- Tests a workflow that fails at a step
- Validates `failure` status
- Ensures error information is preserved

#### TestContextMemoryExecutionSummary_CompleteWorkflow
- Tests a workflow where all nodes execute successfully
- Validates `success` status
- Ensures no skipped nodes

#### TestContextMemoryExecutionSummary_FromVM
- Tests using a VM structure similar to real aggregator usage
- Validates conversion from VM to API request format
- Tests with realistic workflow state (4 executed, 3 skipped)

### Expected Response Format

```json
{
  "status": "success" | "partial_success" | "failure",
  "branchSummary": "plain text summary",
  "skippedNodes": ["node1", "node2"],
  "totalSteps": 7,
  "executedSteps": 4,
  "skippedSteps": 3
}
```

### Debugging

If you encounter issues:

1. **500 errors**: Check the server logs in the terminal where `vercel dev` is running
2. **401/403 errors**: Verify `SERVICE_AUTH_TOKEN` matches your `.env.local` file
3. **Connection errors**: Ensure the server is running and `CONTEXT_MEMORY_URL` points to the correct port
4. **Test output**: The test output will show request/response details for debugging

## Other Test Files

The following test files test the aggregator's **internal summary logic** (not the context-api):

### `summarizer_run_node_test.go`
- Tests `ComposeSummary()` function
- **Deterministic only** - No AI Gateway, no context-api
- Uses hardcoded logic to generate summaries
- This is the fallback mechanism
- **No console.log output** - Pure Go logic, no external API calls

### `summarizer_ai_test.go`
- Tests `ComposeSummarySmart()` function
- **Uses real context-api** when `SERVICE_AUTH_TOKEN` is set (defaults to production URL)
- Falls back to mock (`fakeSummarizer`) if `SERVICE_AUTH_TOKEN` is not set (for CI/testing)
- Falls back to `ComposeSummary()` if API fails or is not configured
- Tests both AI success and fallback scenarios
- **Shows console.log output** when using real API (check your server terminal)

**Key Difference:**
- `ComposeSummary()` = Deterministic, no AI, always works
- `ComposeSummarySmart()` = Tries AI first (if configured), falls back to deterministic

**Important:** These tests call the **real context-api** by default when `SERVICE_AUTH_TOKEN` is set. You'll see console.log output in your server terminal.

To run those tests:
```bash
# Test deterministic summary generation (no AI, no console.log)
go test -v ./core/taskengine -run TestComposeSummary

# Test smart summary with real context-api (requires SERVICE_AUTH_TOKEN)
export SERVICE_AUTH_TOKEN="your-token"
export CONTEXT_MEMORY_URL="http://localhost:3000"  # Optional, defaults to production
go test -v ./core/taskengine -run TestComposeSummarySmart
```

## Implementation Details

### API Endpoints

The context-api service provides two main endpoints:

1. **`POST /api/summarize`**: Generates AI-powered summaries (used by `ComposeSummarySmart`)
2. **`POST /api/execution-summary`**: Generates execution summaries with derived fields (used by integration tests)

### Request Format

The API accepts a flattened request structure:

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
  "currentNodeName": "..."
}
```

### Response Format

The API returns a presentation-agnostic response:

```json
{
  "status": "success" | "partial_success" | "failure",
  "branchSummary": "plain text summary",
  "skippedNodes": ["node1", "node2"],
  "totalSteps": 7,
  "executedSteps": 4,
  "skippedSteps": 3
}
```

### Configuration

The aggregator can be configured to use context-api via config YAML:

```yaml
notifications_summary:
  enabled: true
  provider: "context-memory"
  base_url: "https://context-api.avaprotocol.org"  # Optional, defaults to ContextAPIURL constant
  auth_token: "${CONTEXT_MEMORY_AUTH_TOKEN}"  # From environment or macro secrets
```

### Environment Variables

Set in aggregator environment:
```bash
CONTEXT_MEMORY_AUTH_TOKEN=your-secret-token-here
CONTEXT_MEMORY_URL=https://context-api.avaprotocol.org  # Optional, defaults to constant
```

Or in aggregator config macro secrets:
```yaml
macro_secrets:
  context_memory_auth_token: "your-secret-token-here"
```

## Migration Notes

- The existing `ComposeSummarySmart` function will continue to work - it calls the global summarizer
- If context-api is not configured, it falls back to deterministic summary (`ComposeSummary`)
- The response format matches the existing `Summary` struct, so no changes needed in consumers
- Tests are skipped if `SERVICE_AUTH_TOKEN` is not set (to avoid failures in CI)
- The API endpoint is: `POST https://context-api.avaprotocol.org/api/execution-summary` (or local URL)
- Authentication uses Bearer token: `Authorization: Bearer <SERVICE_AUTH_TOKEN>`

## Code Structure

### Source Files

- **`core/taskengine/summarizer_context_memory.go`**: Implements `Summarizer` interface using context-api
  - Defines `ContextAPIURL` constant (production URL)
  - `NewContextMemorySummarizer()`: Creates summarizer instance
  - `Summarize()`: Calls context-api to generate summaries

### Test Files

- **`core/taskengine/summarizer_context_memory_integration_test.go`**: Integration tests (requires `-tags=integration`)
  - Tests against real context-api endpoints
  - Validates API response structure and content
  - Can override URL via `CONTEXT_MEMORY_URL` env var
  - Can override auth token via `SERVICE_AUTH_TOKEN` env var

- **`core/taskengine/summarizer_ai_test.go`**: Unit tests for `ComposeSummarySmart`
  - Uses real context-api when `SERVICE_AUTH_TOKEN` is set
  - Falls back to mocks for CI/testing

- **`core/taskengine/summarizer_run_node_test.go`**: Unit tests for `ComposeSummary`
  - Tests deterministic fallback logic
  - No external dependencies
