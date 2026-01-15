package taskengine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

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

// TestComposeSummary_SimulationSubjectFormat tests that simulation workflows
// generate the correct subject format matching context-memory API expectations
func TestComposeSummary_SimulationSubjectFormat(t *testing.T) {
	tests := []struct {
		name            string
		workflowName    string
		executedSteps   int
		totalSteps      int
		hasFailures     bool
		hasSkippedNodes bool
		isSimulation    bool
		expectedSubject string
		expectedSummary string
	}{
		{
			name:            "simulation successfully completed",
			workflowName:    "Test Stoploss",
			executedSteps:   7,
			totalSteps:      7,
			hasFailures:     false,
			hasSkippedNodes: false,
			isSimulation:    true,
			expectedSubject: "Simulation: Test Stoploss successfully completed",
			expectedSummary: "Your workflow 'Test Stoploss' executed 7 out of 7 total steps",
		},
		{
			name:            "simulation partially executed",
			workflowName:    "Test Stoploss",
			executedSteps:   5,
			totalSteps:      8,
			hasFailures:     false,
			hasSkippedNodes: true,
			isSimulation:    true,
			expectedSubject: "Simulation: Test Stoploss partially executed",
			expectedSummary: "Your workflow 'Test Stoploss' executed 5 out of 8 total steps",
		},
		{
			name:            "simulation failed to execute",
			workflowName:    "Test Stoploss",
			executedSteps:   3,
			totalSteps:      8,
			hasFailures:     true,
			hasSkippedNodes: false,
			isSimulation:    true,
			expectedSubject: "Simulation: Test Stoploss failed to execute",
			expectedSummary: "Your workflow 'Test Stoploss' executed 3 out of 8 total steps",
		},
		{
			name:            "deployed workflow succeeded",
			workflowName:    "Test Stoploss",
			executedSteps:   7,
			totalSteps:      7,
			hasFailures:     false,
			hasSkippedNodes: false,
			isSimulation:    false,
			expectedSubject: "Test Stoploss: succeeded (7 out of 7 steps)",
			expectedSummary: "Your workflow 'Test Stoploss' executed 7 out of 7 total steps",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVM()
			vm.IsSimulation = tt.isSimulation

			// Set up workflow name
			vm.mu.Lock()
			vm.vars = map[string]interface{}{
				"settings": map[string]interface{}{
					"name":  tt.workflowName,
					"chain": "Sepolia",
				},
			}

			// Set up task nodes (for total steps calculation)
			vm.TaskNodes = make(map[string]*avsproto.TaskNode)
			for i := 0; i < tt.totalSteps-1; i++ { // -1 because trigger is counted separately
				nodeID := fmt.Sprintf("node%d", i)
				vm.TaskNodes[nodeID] = &avsproto.TaskNode{
					Id:   nodeID,
					Name: fmt.Sprintf("step%d", i),
				}
			}

			// Set up execution logs
			vm.ExecutionLogs = make([]*avsproto.Execution_Step, 0, tt.executedSteps)
			// Add trigger (always first)
			vm.ExecutionLogs = append(vm.ExecutionLogs, &avsproto.Execution_Step{
				Id:      "trigger",
				Name:    "eventTrigger",
				Type:    "TRIGGER_TYPE_EVENT",
				Success: true,
			})

			// Add executed steps
			for i := 0; i < tt.executedSteps-1; i++ { // -1 because trigger is already added
				stepName := fmt.Sprintf("step%d", i)
				success := true
				if tt.hasFailures && i == tt.executedSteps-2 { // Last step fails
					success = false
				}
				vm.ExecutionLogs = append(vm.ExecutionLogs, &avsproto.Execution_Step{
					Id:      fmt.Sprintf("step%d", i),
					Name:    stepName,
					Type:    "NODE_TYPE_BALANCE",
					Success: success,
					Error: func() string {
						if !success {
							return "test error"
						}
						return ""
					}(),
				})
			}

			// Mark some nodes as skipped if needed (by not including them in execution logs)
			// The skipped count is calculated by comparing TaskNodes to ExecutionLogs
			vm.mu.Unlock()

			summary := ComposeSummary(vm, "email1")

			// Verify subject format
			if summary.Subject != tt.expectedSubject {
				t.Errorf("Subject mismatch:\n  expected: %q\n  got:      %q", tt.expectedSubject, summary.Subject)
			}

			// Verify summary line format
			if summary.SummaryLine != tt.expectedSummary {
				t.Errorf("SummaryLine mismatch:\n  expected: %q\n  got:      %q", tt.expectedSummary, summary.SummaryLine)
			}

			// Note: "What Executed On-Chain" / "What Executed Successfully" sections only appear
			// when there are successful contract writes. For workflows without contract writes,
			// the body will have a different format. We only verify the subject and summary line here.
		})
	}
}

// TestComposeSummary_SimulationBodyFormat tests that simulation workflows
// generate the correct subject and summary line format matching context-memory API expectations
func TestComposeSummary_SimulationBodyFormat(t *testing.T) {
	vm := NewVM()
	vm.IsSimulation = true

	vm.mu.Lock()
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":  "Test Stoploss",
			"chain": "Sepolia",
		},
		WorkflowContextVarName: map[string]interface{}{
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"runner": "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
		},
	}
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "step1"},
		"node2": {Id: "node2", Name: "step2"},
	}
	vm.mu.Unlock()

	// Create execution logs
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "trigger",
			Name:    "eventTrigger",
			Type:    "TRIGGER_TYPE_EVENT",
			Success: true,
		},
		{
			Id:      "step1",
			Name:    "step1",
			Type:    "NODE_TYPE_BALANCE",
			Success: true,
		},
		{
			Id:      "step2",
			Name:    "step2",
			Type:    "NODE_TYPE_BALANCE",
			Success: true,
		},
	}

	summary := ComposeSummary(vm, "email1")

	// Verify subject format matches context-memory API expectations
	expectedSubject := "Simulation: Test Stoploss successfully completed"
	if summary.Subject != expectedSubject {
		t.Errorf("Subject mismatch:\n  expected: %q\n  got:      %q", expectedSubject, summary.Subject)
	}

	// Verify summary line format matches context-memory API expectations
	expectedSummary := "Your workflow 'Test Stoploss' executed 3 out of 3 total steps"
	if summary.SummaryLine != expectedSummary {
		t.Errorf("SummaryLine mismatch:\n  expected: %q\n  got:      %q", expectedSummary, summary.SummaryLine)
	}

	t.Logf("Subject: %s", summary.Subject)
	t.Logf("SummaryLine: %s", summary.SummaryLine)
	t.Logf("Body: %s", summary.Body)
}
