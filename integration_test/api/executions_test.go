//go:build integration
// +build integration

package api_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

// TestExecutionStandalone_RequiresWorkflowId is the regression guard
// for the OpenAPI spec change that made `workflowId` mandatory on the
// standalone routes. A request without it should produce a 4xx that
// names the missing parameter — not a confusing 500.
func TestExecutionStandalone_RequiresWorkflowId(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp, body := h.authedRequest(http.MethodGet, "/api/v1/executions/01ABC123", nil)
	require.True(t, resp.StatusCode >= 400 && resp.StatusCode < 500,
		"expected 4xx, got %d body=%s", resp.StatusCode, string(body))
	assert.Contains(t, strings.ToLower(string(body)), "workflowid",
		"problem body should mention the missing param")
}

// TestExecutionStandalone_Returns404ForUnknown checks the happy-path
// shape — handler succeeds, hands the request to engine.GetExecution,
// and reports back the engine's "not found" verdict as a problem+json
// 404 (not a 500).
func TestExecutionStandalone_Returns404ForUnknown(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	url := "/api/v1/executions/01ABCDEFGHIJKLMNOPQRSTUVWX?workflowId=01ABCDEFGHIJKLMNOPQRSTUVWX"
	resp, body := h.authedRequest(http.MethodGet, url, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"expected 404 for unknown execution, got %d body=%s", resp.StatusCode, string(body))
}

// TestExecutionStream_OpensAndCloses verifies the SSE handler emits at
// least one frame and shuts down cleanly when the client cancels.
// Doesn't drive a real execution — the harness has no operator — so
// the stream emits "pending" frames then closes on context cancel.
func TestExecutionStream_OpensAndCloses(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	user := testutil.TestUser1()

	// First create a workflow so the workflowId resolves to a valid
	// owner — otherwise the engine rejects the lookup as forbidden.
	createResp, body := h.authedRequest(http.MethodPost, "/api/v1/workflows",
		newRestWorkflowRequest(user.SmartAccountAddress.Hex()))
	require.Equal(t, http.StatusCreated, createResp.StatusCode, "create body=%s", string(body))
	wf := decodeJSON[generated.Workflow](t, body)

	// Open the stream with a short cap so the test doesn't hang.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := h.server.URL + "/api/v1/executions/01PHANTOM_EXECUTION_ID:stream?workflowId=" + string(wf.Id) + "&interval=250ms"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+h.jwt)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Read the first SSE event (`data: {...}\n\n`). We just want to
	// confirm a frame arrives; status-change semantics are unit-tested
	// elsewhere.
	scanner := bufio.NewScanner(resp.Body)
	gotData := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			gotData = true
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	assert.True(t, gotData, "SSE stream should emit at least one data: frame")

	// Cancel the request — the handler should observe clientGone and
	// exit. We can't directly assert on goroutine cleanup, but the
	// next request to the same harness should succeed (proves the
	// engine isn't pinned).
	cancel()
	time.Sleep(50 * time.Millisecond)

	resp2, _ := h.authedRequest(http.MethodGet, "/api/v1/health", nil)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
}
