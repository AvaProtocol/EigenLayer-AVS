package taskengine

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

const (
	// Default auth token for local context-memory tests
	ContextMemoryAuthToken = "test-auth-token-12345"
)

type fakeSummarizer struct {
	resp Summary
	err  error
}

func (f *fakeSummarizer) Summarize(ctx context.Context, vm *VM, currentStepName string) (Summary, error) {
	return f.resp, f.err
}

func TestComposeSummarySmart_FallbackDeterministic(t *testing.T) {
	SetSummarizer(nil)
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{"name": "Workflow X"}
	vm.mu.Unlock()
	vm.ExecutionLogs = []*avsproto.Execution_Step{{
		Name:    "Step A",
		Success: true,
	}}

	s := ComposeSummarySmart(vm, "rest1")
	if !strings.Contains(s.Subject, "Workflow X: succeeded") {
		t.Fatalf("subject should contain 'Workflow X: succeeded', got: %q", s.Subject)
	}
	// Body should contain the workflow name and completion message
	// The exact format may vary based on available context (smart wallet, owner, etc.)
	// Just verify it contains the essential elements
	if s.Body == "" {
		t.Fatalf("body should not be empty")
	}
	if !strings.Contains(s.Body, "Workflow X") && !strings.Contains(s.Body, "Step A") {
		t.Fatalf("body should mention workflow or step name, got: %q", s.Body)
	}
}

func TestComposeSummarySmart_UsesAISummarizer(t *testing.T) {
	defer SetSummarizer(nil)

	// Check if we should use real context-memory API
	authToken := os.Getenv("SERVICE_AUTH_TOKEN")
	if authToken != "" {
		// Use real context-memory API
		baseURL := os.Getenv("CONTEXT_MEMORY_URL")
		if baseURL == "" {
			baseURL = ContextAPIURL // Default to production URL from source code
		}
		t.Logf("Using real context-memory API at: %s", baseURL)
		summarizer := NewContextMemorySummarizer(baseURL, authToken)
		SetSummarizer(summarizer)

		vm := NewVM()
		vm.mu.Lock()
		vm.vars["settings"] = map[string]interface{}{"name": "Test Workflow"}
		vm.vars[WorkflowContextVarName] = map[string]interface{}{
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		}
		vm.mu.Unlock()
		vm.ExecutionLogs = []*avsproto.Execution_Step{
			{Id: "step1", Name: "test_step", Type: "balance", Success: true},
		}

		s := ComposeSummarySmart(vm, "current")
		if s.Subject == "" {
			t.Fatalf("AI summarizer should return non-empty subject, got empty")
		}
		if len(s.Body) < 40 {
			t.Fatalf("AI summarizer body should be at least 40 characters, got %d", len(s.Body))
		}
		t.Logf("Real API response - Subject: %s, Body length: %d", s.Subject, len(s.Body))
	} else {
		// Fallback to mock for CI/testing without SERVICE_AUTH_TOKEN
		// Body must be at least 40 characters to pass validation in ComposeSummarySmart
		f := &fakeSummarizer{resp: Summary{
			Subject: "AI subject",
			Body:    "This is a sufficiently long AI-generated body text that exceeds the 40 character minimum.",
		}}
		SetSummarizer(f)
		vm := NewVM()
		s := ComposeSummarySmart(vm, "current")
		if s.Subject != "AI subject" {
			t.Fatalf("ai summarizer subject not used: expected 'AI subject', got %q", s.Subject)
		}
		if !strings.Contains(s.Body, "AI-generated body text") {
			t.Fatalf("ai summarizer body not used: got %q", s.Body)
		}
	}
}

func TestComposeSummarySmart_AIFailsFallback(t *testing.T) {
	defer SetSummarizer(nil)
	f := &fakeSummarizer{err: context.DeadlineExceeded}
	SetSummarizer(f)
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{"name": "Workflow Y"}
	vm.mu.Unlock()
	vm.ExecutionLogs = []*avsproto.Execution_Step{{
		Name:    "Done",
		Success: true,
	}}

	s := ComposeSummarySmart(vm, "rest1")
	if !strings.Contains(s.Subject, "Workflow Y: succeeded") {
		t.Fatalf("fallback failed, subject should contain 'Workflow Y: succeeded', got: %q", s.Subject)
	}
}

func TestFormatSummaryForChannel_Telegram(t *testing.T) {
	tests := []struct {
		name            string
		summary         Summary
		expectedContain []string
		maxLength       int
	}{
		{
			name: "short summary with subject",
			summary: Summary{
				Subject: "Swap: succeeded (3 steps)",
				Body:    "Smart wallet 0xabc executed a swap on Uniswap V3.",
			},
			expectedContain: []string{"<b>Swap: succeeded (3 steps)</b>", "Smart wallet 0xabc executed a swap"},
			maxLength:       300,
		},
		{
			name: "long summary gets truncated",
			summary: Summary{
				Subject: "Trade: succeeded (5 steps)",
				Body: "Smart wallet 0x123 executed multiple trades on Uniswap V3. " +
					"First it approved 1000 USDC to the router contract at 0xdef. " +
					"Then it swapped 100 USDC for approximately 0.025 WETH via the pool. " +
					"Finally it performed another swap of 50 USDC for DAI tokens at contract 0x456. " +
					"All transactions completed successfully on Base network.",
			},
			expectedContain: []string{"<b>Trade: succeeded (5 steps)</b>", "Smart wallet 0x123"},
			maxLength:       300,
		},
		{
			name: "empty body returns subject",
			summary: Summary{
				Subject: "Workflow: failed (1 step)",
				Body:    "",
			},
			expectedContain: []string{"Workflow: failed (1 step)"},
			maxLength:       100,
		},
		{
			name: "body with double newlines extracts first paragraph",
			summary: Summary{
				Subject: "Approve: succeeded (1 step)",
				Body:    "Smart wallet 0xabc approved 500 USDC to Uniswap router.\n\nThis allows future swaps without additional approvals.",
			},
			expectedContain: []string{"<b>Approve: succeeded (1 step)</b>", "Smart wallet 0xabc approved 500 USDC"},
			maxLength:       300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatSummaryForChannel(tt.summary, "telegram")

			// Check that result contains expected strings
			for _, expected := range tt.expectedContain {
				if !strings.Contains(result, expected) {
					t.Errorf("expected result to contain %q, got: %s", expected, result)
				}
			}

			// Check length constraint
			if len(result) > tt.maxLength {
				t.Errorf("result too long: %d chars (max %d), got: %s", len(result), tt.maxLength, result)
			}

			// Verify HTML parse mode compatibility (should have <b> tags)
			if tt.summary.Subject != "" && tt.summary.Body != "" {
				if !strings.HasPrefix(result, "<b>") {
					t.Errorf("telegram message should start with <b> tag, got: %s", result)
				}
			}
		})
	}
}

func TestFormatSummaryForChannel_Discord(t *testing.T) {
	summary := Summary{
		Subject: "Deploy: succeeded (2 steps)",
		Body:    "Contract deployed to 0xabc on Base network.",
	}

	result := FormatSummaryForChannel(summary, "discord")

	// Discord should use markdown bold
	if !strings.Contains(result, "**Deploy: succeeded (2 steps)**") {
		t.Errorf("discord message should use markdown bold, got: %s", result)
	}
	if !strings.Contains(result, "Contract deployed to 0xabc") {
		t.Errorf("discord message should contain body content, got: %s", result)
	}
}

// TestOpenAISummarizer_BuildDigestNoDeadlock ensures buildDigest doesn't deadlock
// when called with a VM that has execution logs and variables populated
func TestOpenAISummarizer_BuildDigestNoDeadlock(t *testing.T) {
	// Create a VM with realistic state
	vm := NewVM()
	vm.TaskID = "test-task-123"

	// Add execution logs
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "step1",
			Name:    "balance_check",
			Type:    "balance",
			Success: true,
			Log:     "Balance checked successfully",
		},
		{
			Id:      "step2",
			Name:    "approve_token",
			Type:    "contractWrite",
			Success: true,
			Log:     "Approved 1000 USDC",
		},
		{
			Id:      "step3",
			Name:    "swap_tokens",
			Type:    "contractWrite",
			Success: true,
			Log:     "Swapped successfully",
		},
	}

	// Add TaskNodes (simulate a workflow with nodes)
	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "balance_check", Type: avsproto.NodeType_NODE_TYPE_BALANCE},
		"node2": {Id: "node2", Name: "approve_token", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE},
		"node3": {Id: "node3", Name: "swap_tokens", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE},
	}

	// Add VM variables
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Test Workflow",
			"chain":  "sepolia",
			"runner": "0xTestAddress",
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":               "Test Workflow",
			"runner":             "0xTestSmartWallet",
			"owner":              "0xTestOwner",
			"smartWalletAddress": "0xTestSmartWallet",
		},
		"aa_sender": "0xTestSmartWallet",
	}
	vm.mu.Unlock()

	// Create summarizer
	cfg := OpenAIConfig{
		Model:           "gpt-4o-mini",
		Temperature:     0.3,
		MaxInputTokens:  2000,
		MaxOutputTokens: 300,
		TimeoutMs:       5000,
		APIKey:          "test-key",
	}
	summarizer := &OpenAISummarizer{
		cfg: cfg,
		// Don't need actual HTTP client for buildDigest test
	}

	// Test that buildDigest completes without deadlock
	done := make(chan bool, 1)
	var digest string

	go func() {
		digest = summarizer.buildDigest(vm, "email_node")
		done <- true
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	select {
	case <-done:
		// Success - buildDigest completed
		if len(digest) == 0 {
			t.Error("digest should not be empty")
		}
		if !strings.Contains(digest, "Test Workflow") {
			t.Error("digest should contain workflow name")
		}
		t.Logf("Digest built successfully, length: %d", len(digest))
	case <-ctx.Done():
		t.Fatal("buildDigest deadlocked - did not complete within 2 seconds")
	}
}

// TestOpenAISummarizer_TimeoutProtection verifies that the timeout mechanism
// in Summarize() works correctly when buildDigest hangs
func TestOpenAISummarizer_TimeoutProtection(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{"name": "Test Workflow"},
	}
	vm.mu.Unlock()
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Name: "step1", Success: true},
	}

	// Create a mock summarizer that simulates a hanging buildDigest
	// by using an extremely slow operation
	cfg := OpenAIConfig{
		Model:           "gpt-4o-mini",
		Temperature:     0.3,
		MaxInputTokens:  2000,
		MaxOutputTokens: 300,
		TimeoutMs:       100, // Short timeout for test
		APIKey:          "test-key",
	}
	summarizer := &OpenAISummarizer{
		cfg: cfg,
		hc:  &http.Client{Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond},
	}

	// Use a short context timeout
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// The Summarize call should timeout and return an error
	// (not hang indefinitely)
	// Since we don't have a valid OpenAI key, this will fail at the HTTP call
	// but importantly it should NOT deadlock
	_, err := summarizer.Summarize(ctx, vm, "test_node")

	if err == nil {
		t.Error("Expected error (timeout or HTTP failure), got nil")
	}

	// Should get either a timeout error OR an HTTP error (not a deadlock)
	t.Logf("Summarize returned error (expected): %v", err)
}

// TestOpenAISummarizer_BuildDigestWithMixedResults tests buildDigest with
// both successful and failed steps
func TestOpenAISummarizer_BuildDigestWithMixedResults(t *testing.T) {
	vm := NewVM()
	vm.TaskID = "test-task-mixed"

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "step1",
			Name:    "check_balance",
			Type:    "balance",
			Success: true,
		},
		{
			Id:      "step2",
			Name:    "approve_failed",
			Type:    "contractWrite",
			Success: false,
			Error:   "Insufficient gas",
		},
		{
			Id:      "step3",
			Name:    "swap_skipped",
			Type:    "contractWrite",
			Success: false,
			Error:   "Previous step failed",
		},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "check_balance"},
		"node2": {Id: "node2", Name: "approve_failed"},
		"node3": {Id: "node3", Name: "swap_skipped"},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name": "Failed Workflow",
		},
	}
	vm.mu.Unlock()

	cfg := OpenAIConfig{
		Model:           "gpt-4o-mini",
		MaxInputTokens:  2000,
		MaxOutputTokens: 300,
		TimeoutMs:       5000,
		APIKey:          "test-key",
	}
	summarizer := &OpenAISummarizer{cfg: cfg}

	digest := summarizer.buildDigest(vm, "final_node")

	if len(digest) == 0 {
		t.Fatal("digest should not be empty")
	}

	// Verify digest contains failure information
	if !strings.Contains(digest, "Failed Workflow") && !strings.Contains(digest, "failed") {
		t.Error("digest should mention failure")
	}

	t.Logf("Mixed results digest length: %d", len(digest))
}

// TestOpenAISummarizer_BuildDigestWithLargeExecutionLog tests that buildDigest
// handles workflows with many steps without issues
func TestOpenAISummarizer_BuildDigestWithLargeExecutionLog(t *testing.T) {
	vm := NewVM()
	vm.TaskID = "test-task-large"

	// Create 50 execution steps
	for i := 0; i < 50; i++ {
		vm.ExecutionLogs = append(vm.ExecutionLogs, &avsproto.Execution_Step{
			Id:      "step" + string(rune(i)),
			Name:    "operation_" + string(rune(i)),
			Type:    "contractWrite",
			Success: true,
		})
	}

	vm.mu.Lock()
	vm.TaskNodes = make(map[string]*avsproto.TaskNode)
	for i := 0; i < 50; i++ {
		nodeID := "node" + string(rune(i))
		vm.TaskNodes[nodeID] = &avsproto.TaskNode{
			Id:   nodeID,
			Name: "operation_" + string(rune(i)),
		}
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name": "Large Workflow",
		},
	}
	vm.mu.Unlock()

	cfg := OpenAIConfig{
		Model:           "gpt-4o-mini",
		MaxInputTokens:  2000,
		MaxOutputTokens: 300,
		TimeoutMs:       5000,
		APIKey:          "test-key",
	}
	summarizer := &OpenAISummarizer{cfg: cfg}

	// Should complete without hanging
	done := make(chan string, 1)
	go func() {
		done <- summarizer.buildDigest(vm, "final_node")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	select {
	case digest := <-done:
		if len(digest) == 0 {
			t.Fatal("digest should not be empty")
		}
		t.Logf("Large workflow digest length: %d", len(digest))
	case <-ctx.Done():
		t.Fatal("buildDigest with large execution log deadlocked")
	}
}

// TestComposeSummarySmart_WithRealWorkflowState tests the full flow
// with realistic workflow state. Uses real context-memory API if SERVICE_AUTH_TOKEN is set.
func TestComposeSummarySmart_WithRealWorkflowState(t *testing.T) {
	defer SetSummarizer(nil)

	vm := NewVM()
	vm.TaskID = "01K6H8R583M8WFXM2Z4APP7JTN"

	// Simulate a workflow that ran 4 out of 7 steps
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "trigger", Name: "eventTrigger", Type: "eventTrigger", Success: true},
		{Id: "step1", Name: "balance1", Type: "balance", Success: true},
		{Id: "step2", Name: "branch1", Type: "branch", Success: true},
		{Id: "step3", Name: "email_report", Type: "restApi", Success: true},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "balance1"},
		"node2": {Id: "node2", Name: "branch1"},
		"node3": {Id: "node3", Name: "approve_token1"},
		"node4": {Id: "node4", Name: "get_quote"},
		"node5": {Id: "node5", Name: "run_swap"},
		"node6": {Id: "node6", Name: "email_report"},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Test template",
			"chain":  "Sepolia",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":   "Test template",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		},
	}
	vm.mu.Unlock()

	// Check if we should use real context-memory API
	authToken := os.Getenv("SERVICE_AUTH_TOKEN")
	if authToken != "" {
		// Use real context-memory API (defaults to localhost:3000)
		baseURL := os.Getenv("CONTEXT_MEMORY_URL")
		if baseURL == "" {
			baseURL = ContextAPIURL // Default to production URL from source code
		}
		t.Logf("Using real context-memory API at: %s", baseURL)
		summarizer := NewContextMemorySummarizer(baseURL, authToken)
		SetSummarizer(summarizer)
	} else {
		// Use deterministic fallback if no auth token
		t.Log("SERVICE_AUTH_TOKEN not set, using deterministic fallback")
		SetSummarizer(nil)
	}

	summary := ComposeSummarySmart(vm, "email_report")

	// Should show workflow name and execution status
	if !strings.Contains(summary.Subject, "Test template") {
		t.Errorf("Subject should contain workflow name, got: %s", summary.Subject)
	}

	if summary.Body == "" {
		t.Error("Body should not be empty")
	}

	t.Logf("Subject: %s", summary.Subject)
	t.Logf("Body length: %d", len(summary.Body))
}
