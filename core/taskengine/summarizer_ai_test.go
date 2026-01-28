package taskengine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
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

func TestFormatForMessageChannels_Telegram(t *testing.T) {
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
			result := FormatForMessageChannels(tt.summary, "telegram", nil)

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

func TestFormatForMessageChannels_Discord(t *testing.T) {
	summary := Summary{
		Subject: "Deploy: succeeded (2 steps)",
		Body:    "Contract deployed to 0xabc on Base network.",
	}

	result := FormatForMessageChannels(summary, "discord", nil)

	// Discord should use markdown bold
	if !strings.Contains(result, "**Deploy: succeeded (2 steps)**") {
		t.Errorf("discord message should use markdown bold, got: %s", result)
	}
	if !strings.Contains(result, "Contract deployed to 0xabc") {
		t.Errorf("discord message should contain body content, got: %s", result)
	}
}

func TestFormatTransferMessage(t *testing.T) {
	tests := []struct {
		name     string
		data     *TransferEventData
		expected string
	}{
		{
			name: "sent_eth",
			data: &TransferEventData{
				Direction:      "sent",
				FromAddress:    "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
				ToAddress:      "0x0000000000000000000000000000000000000002",
				Value:          "1.5",
				TokenSymbol:    "ETH",
				TokenName:      "Ether",
				BlockTimestamp: 1768943005000, // milliseconds
				ChainName:      "Sepolia",
			},
			expected: "⬆️ Sent <b>1.5 ETH</b> to <code>0x0000000000000000000000000000000000000002</code> on <b>Sepolia</b>",
		},
		{
			name: "received_usdc",
			data: &TransferEventData{
				Direction:      "received",
				FromAddress:    "0x1234567890123456789012345678901234567890",
				ToAddress:      "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
				Value:          "100.50",
				TokenSymbol:    "USDC",
				TokenName:      "USD Coin",
				BlockTimestamp: 1768943005, // seconds
				ChainName:      "Ethereum",
			},
			expected: "⬇️ Received <b>100.50 USDC</b> from <code>0x1234567890123456789012345678901234567890</code> on <b>Ethereum</b>",
		},
		{
			name:     "nil_data",
			data:     nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatTransferMessage(tt.data)
			if tt.expected == "" {
				if result != "" {
					t.Errorf("expected empty string, got: %s", result)
				}
				return
			}
			if !strings.Contains(result, tt.expected) {
				t.Errorf("expected result to contain %q, got: %s", tt.expected, result)
			}
		})
	}
}

// TestExtractTransferEventData_FromVars tests that transfer data can be extracted
// from vm.vars["transfer_monitor"] when ExecutionLogs has no Transfer event.
// This covers the single-node Telegram notification use case.
func TestExtractTransferEventData_FromVars(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars = map[string]any{
		"transfer_monitor": map[string]interface{}{
			"data": map[string]interface{}{
				"eventName":      "Transfer",
				"direction":      "sent",
				"fromAddress":    "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
				"toAddress":      "0x0000000000000000000000000000000000000002",
				"value":          "1.5",
				"tokenSymbol":    "ETH",
				"tokenName":      "Ether",
				"blockTimestamp": int64(1768943005000),
			},
		},
		"settings": map[string]interface{}{
			"chain": "base",
		},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{} // Empty - no trigger step
	vm.mu.Unlock()

	got := ExtractTransferEventData(vm)
	if got == nil {
		t.Fatal("expected non-nil TransferEventData from vars, got nil")
	}
	if got.Direction != "sent" {
		t.Errorf("expected Direction 'sent', got %q", got.Direction)
	}
	if got.Value != "1.5" {
		t.Errorf("expected Value '1.5', got %q", got.Value)
	}
	if got.TokenSymbol != "ETH" {
		t.Errorf("expected TokenSymbol 'ETH', got %q", got.TokenSymbol)
	}
	if got.FromAddress != "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f" {
		t.Errorf("expected FromAddress, got %q", got.FromAddress)
	}
	if got.ToAddress != "0x0000000000000000000000000000000000000002" {
		t.Errorf("expected ToAddress, got %q", got.ToAddress)
	}
	// ChainName should be resolved from settings (lowercase as stored)
	if got.ChainName != "base" {
		t.Errorf("expected ChainName 'base', got %q", got.ChainName)
	}

	// Verify FormatTransferMessage produces expected output
	msg := FormatTransferMessage(got)
	if !strings.Contains(msg, "1.5 ETH") {
		t.Errorf("expected message to contain '1.5 ETH', got: %s", msg)
	}
	if !strings.Contains(msg, "Sent") {
		t.Errorf("expected message to contain 'Sent', got: %s", msg)
	}
	if !strings.Contains(msg, "base") {
		t.Errorf("expected message to contain 'base', got: %s", msg)
	}
}

// TestExtractTransferEventData_ExecutionLogsTakesPrecedence verifies that
// ExecutionLogs data is used when present, even if transfer_monitor is also set.
func TestExtractTransferEventData_ExecutionLogsTakesPrecedence(t *testing.T) {
	vm := NewVM()

	// Create a protobuf struct for the event trigger data
	eventData := map[string]interface{}{
		"eventName":      "Transfer",
		"direction":      "received",
		"fromAddress":    "0x1111111111111111111111111111111111111111",
		"toAddress":      "0x2222222222222222222222222222222222222222",
		"value":          "99.0",
		"tokenSymbol":    "USDC",
		"tokenName":      "USD Coin",
		"blockTimestamp": float64(1768943005000),
	}

	// Convert to structpb.Value
	protoData, _ := structpb.NewValue(eventData)

	vm.mu.Lock()
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "trigger",
			Name:    "eventTrigger",
			Type:    "eventTrigger",
			Success: true,
			OutputData: &avsproto.Execution_Step_EventTrigger{
				EventTrigger: &avsproto.EventTrigger_Output{
					Data: protoData,
				},
			},
		},
	}
	// Also set transfer_monitor with different data
	vm.vars = map[string]any{
		"transfer_monitor": map[string]interface{}{
			"data": map[string]interface{}{
				"eventName":   "Transfer",
				"direction":   "sent",
				"value":       "1.5",
				"tokenSymbol": "ETH",
			},
		},
		"settings": map[string]interface{}{
			"chain": "ethereum",
		},
	}
	vm.mu.Unlock()

	got := ExtractTransferEventData(vm)
	if got == nil {
		t.Fatal("expected non-nil TransferEventData, got nil")
	}
	// Should use ExecutionLogs data (received USDC), not transfer_monitor (sent ETH)
	if got.Direction != "received" {
		t.Errorf("expected Direction 'received' from ExecutionLogs, got %q", got.Direction)
	}
	if got.TokenSymbol != "USDC" {
		t.Errorf("expected TokenSymbol 'USDC' from ExecutionLogs, got %q", got.TokenSymbol)
	}
	if got.Value != "99.0" {
		t.Errorf("expected Value '99.0' from ExecutionLogs, got %q", got.Value)
	}
}

// TestFormatForMessageChannels_SingleNodeExample tests that single-node executions
// without transfer data show an example message with real workflow name and chain.
func TestFormatForMessageChannels_SingleNodeExample(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	// Single node execution: no TaskID, exactly one TaskNode
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "singleNodeExecution_restApi"},
	}
	vm.vars = map[string]any{
		"settings": map[string]interface{}{
			"name":  "My Transfer Monitor",
			"chain": "Base",
		},
	}
	// No transfer_monitor data - should trigger example message
	vm.ExecutionLogs = []*avsproto.Execution_Step{}
	vm.mu.Unlock()

	// Empty summary - no structured data
	summary := Summary{}

	result := FormatForMessageChannels(summary, "telegram", vm)

	// Should contain workflow name from settings
	if !strings.Contains(result, "My Transfer Monitor") {
		t.Errorf("expected result to contain workflow name 'My Transfer Monitor', got: %s", result)
	}

	// Should contain chain name from settings
	if !strings.Contains(result, "Base") {
		t.Errorf("expected result to contain chain name 'Base', got: %s", result)
	}

	// Should contain the example execution line
	if !strings.Contains(result, "(Simulated) On-chain transaction successfully completed") {
		t.Errorf("expected result to contain example execution line, got: %s", result)
	}

	// Should contain the example notice
	if !strings.Contains(result, "This is an example") {
		t.Errorf("expected result to contain example notice, got: %s", result)
	}

	// Should be formatted as Telegram HTML
	if !strings.Contains(result, "<b>") {
		t.Errorf("expected Telegram HTML formatting with <b> tags, got: %s", result)
	}

	t.Logf("Example message:\n%s", result)
}

// TestFormatForMessageChannels_SingleNodeExample_Discord tests Discord formatting
func TestFormatForMessageChannels_SingleNodeExample_Discord(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "singleNodeExecution_restApi"},
	}
	vm.vars = map[string]any{
		"settings": map[string]interface{}{
			"name":     "Price Alert",
			"chain_id": 8453, // Base chain ID
		},
	}
	vm.ExecutionLogs = []*avsproto.Execution_Step{}
	vm.mu.Unlock()

	summary := Summary{}
	result := FormatForMessageChannels(summary, "discord", vm)

	// Should use Discord markdown formatting
	if !strings.Contains(result, "**Price Alert**") {
		t.Errorf("expected Discord markdown with **workflow name**, got: %s", result)
	}

	// Chain should be resolved from chain_id
	if !strings.Contains(result, "Base") {
		t.Errorf("expected chain name 'Base' resolved from chain_id, got: %s", result)
	}

	t.Logf("Discord example message:\n%s", result)
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

			// Verify body format - with the new structured format, Body starts with "Trigger: ..."
			// The "Your workflow..." summary is now in SummaryLine field
			if !strings.HasPrefix(summary.Body, "Trigger: ") {
				t.Errorf("Body format mismatch:\n  expected to start with: %q\n  got:                      %q", "Trigger: ", summary.Body)
			}
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

	// Verify body format - with the new structured format, Body starts with "Trigger: ..."
	// The "Your workflow..." summary is now in SummaryLine field
	if !strings.HasPrefix(summary.Body, "Trigger: ") {
		t.Errorf("Body format mismatch:\n  expected to start with: %q\n  got:                      %q", "Trigger: ", summary.Body)
	}

	// Verify SummaryLine contains the expected format
	expectedSummaryLine := "Your workflow 'Test Stoploss' executed 3 out of 3 total steps"
	if summary.SummaryLine != expectedSummaryLine {
		t.Errorf("SummaryLine mismatch:\n  expected: %q\n  got:      %q", expectedSummaryLine, summary.SummaryLine)
	}

	t.Logf("Subject: %s", summary.Subject)
	t.Logf("SummaryLine: %s", summary.SummaryLine)
	t.Logf("Body: %s", summary.Body)
}

// TestFormatTelegramFromStructured_PRDFormat tests the PRD-based Telegram format
// that uses body.network and body.executions array with emoji prepended by aggregator
func TestFormatTelegramFromStructured_PRDFormat(t *testing.T) {
	tests := []struct {
		name            string
		summary         Summary
		expectedContain []string
		notContain      []string
	}{
		{
			name: "simulation with transfer - PRD format",
			summary: Summary{
				Subject:     "Simulation: Recurring Payment successfully completed", // No emoji from API
				Status:      "success",
				Network:     "Sepolia",
				Trigger:     "(Simulated) Your scheduled task (every 3 days at 11:00 PM) triggered on Sepolia.",
				TriggeredAt: "2026-01-22T04:51:18.509Z",
				Executions: []string{
					"(Simulated) Transferred 0.01 ETH to 0xc60e...C788",
				},
			},
			expectedContain: []string{
				"✅ Simulation: <b>Recurring Payment</b> successfully completed", // Only workflow name is bold
				"<b>Network:</b> Sepolia",
				"<b>Time:</b> 2026-01-22T04:51:18.509Z",
				"<b>Trigger:</b> (Simulated) Your scheduled task (every 3 days at 11:00 PM) triggered on Sepolia.",
				"<b>Executed:</b>",
				"• (Simulated) Transferred 0.01 ETH to 0xc60e...C788",
			},
			notContain: []string{},
		},
		{
			name: "deployed run with transfer - PRD format",
			summary: Summary{
				Subject:     "Run #3: Recurring Payment successfully completed", // No emoji from API
				Status:      "success",
				Network:     "Sepolia",
				TriggeredAt: "2026-01-22T04:51:18.509Z",
				Executions: []string{
					"Transferred 0.01 ETH to 0xc60e...C788",
				},
			},
			expectedContain: []string{
				"✅ Run #3: <b>Recurring Payment</b> successfully completed", // Only workflow name is bold
				"<b>Network:</b> Sepolia",
				"<b>Time:</b> 2026-01-22T04:51:18.509Z",
				"<b>Executed:</b>",
				"• Transferred 0.01 ETH to 0xc60e...C788",
			},
			notContain: []string{
				"No on-chain transaction was executed", // No simulation notice for real runs
			},
		},
		{
			name: "failed run - PRD format",
			summary: Summary{
				Subject:     "Run #5: Recurring Payment failed to execute", // No emoji from API
				Status:      "failure",
				Network:     "Sepolia",
				TriggeredAt: "2026-01-22T04:51:18.509Z",
				Errors:      []string{"transfer1: Insufficient balance for transfer"},
			},
			expectedContain: []string{
				"❌ Run #5: <b>Recurring Payment</b> failed to execute", // Only workflow name is bold
				"<b>Network:</b> Sepolia",
				"<b>Error:</b> transfer1: Insufficient balance for transfer",
			},
			notContain: []string{
				"<b>Executed:</b>",
			},
		},
		{
			name: "partial success - PRD format",
			summary: Summary{
				Subject:     "Run #2: My Workflow partially executed", // No emoji from API
				Status:      "partial_success",
				Network:     "Ethereum",
				TriggeredAt: "2026-01-22T04:51:18.509Z",
				Executions: []string{
					"Approved 100 USDC to router",
				},
			},
			expectedContain: []string{
				"⚠️ Run #2: <b>My Workflow</b> partially executed", // Only workflow name is bold
				"<b>Network:</b> Ethereum",
				"<b>Executed:</b>",
				"• Approved 100 USDC to router",
			},
			notContain: []string{},
		},
		{
			name: "network fallback from workflow.chain",
			summary: Summary{
				Subject: "Simulation: Test successfully completed",
				Status:  "success",
				Network: "", // Empty network from API
				Workflow: &WorkflowInfo{
					Chain:   "Base",
					ChainID: 8453,
				},
				Executions: []string{"(Simulated) Test executed"},
			},
			expectedContain: []string{
				"<b>Network:</b> Base", // Falls back to workflow.chain
			},
		},
		{
			name: "network fallback from chainID",
			summary: Summary{
				Subject: "Simulation: Test successfully completed",
				Status:  "success",
				Network: "", // Empty network from API
				Workflow: &WorkflowInfo{
					Chain:   "", // Empty chain name
					ChainID: 11155111,
				},
				Executions: []string{"(Simulated) Test executed"},
			},
			expectedContain: []string{
				"<b>Network:</b> Sepolia", // Derived from chainID
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatForMessageChannels(tt.summary, "telegram", nil)

			// Check expected strings are present
			for _, expected := range tt.expectedContain {
				if !strings.Contains(result, expected) {
					t.Errorf("expected result to contain %q\nGot:\n%s", expected, result)
				}
			}

			// Check unwanted strings are not present
			for _, notExpected := range tt.notContain {
				if strings.Contains(result, notExpected) {
					t.Errorf("result should NOT contain %q\nGot:\n%s", notExpected, result)
				}
			}
		})
	}
}

// TestTruncateAddress tests the address truncation helper function
func TestTruncateAddress(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f", "0x5d814...434f"},
		{"0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788", "0xc60e7...C788"},
		{"0x1234567890abcdef1234567890abcdef12345678", "0x12345...5678"},
		{"short", "short"}, // Too short to truncate
		{"", ""},           // Empty string
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateAddress(tt.input)
			if result != tt.expected {
				t.Errorf("truncateAddress(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestGetBlockExplorerURL tests the block explorer URL helper function
func TestGetBlockExplorerURL(t *testing.T) {
	tests := []struct {
		name     string
		chain    string
		chainID  int64
		expected string
	}{
		// Test by chain ID (preferred)
		{"chainID_ethereum", "", 1, "https://etherscan.io"},
		{"chainID_sepolia", "", 11155111, "https://sepolia.etherscan.io"},
		{"chainID_polygon", "", 137, "https://polygonscan.com"},
		{"chainID_arbitrum", "", 42161, "https://arbiscan.io"},
		{"chainID_optimism", "", 10, "https://optimistic.etherscan.io"},
		{"chainID_base", "", 8453, "https://basescan.org"},
		{"chainID_base_sepolia", "", 84532, "https://sepolia.basescan.org"},
		{"chainID_bsc", "", 56, "https://bscscan.com"},
		{"chainID_avalanche", "", 43114, "https://snowtrace.io"},

		// Test by chain name fallback
		{"name_mainnet", "Mainnet", 0, "https://etherscan.io"},
		{"name_ethereum", "Ethereum", 0, "https://etherscan.io"},
		{"name_sepolia", "Sepolia", 0, "https://sepolia.etherscan.io"},
		{"name_polygon", "Polygon", 0, "https://polygonscan.com"},
		{"name_arbitrum", "Arbitrum", 0, "https://arbiscan.io"},
		{"name_arbitrum_one", "Arbitrum One", 0, "https://arbiscan.io"},
		{"name_optimism", "Optimism", 0, "https://optimistic.etherscan.io"},
		{"name_base", "Base", 0, "https://basescan.org"},
		{"name_base_sepolia", "Base-Sepolia", 0, "https://sepolia.basescan.org"},
		{"name_bsc", "BSC", 0, "https://bscscan.com"},
		{"name_avalanche", "Avalanche", 0, "https://snowtrace.io"},
		{"name_unknown", "Unknown", 0, "https://etherscan.io"}, // Default fallback
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getBlockExplorerURL(tt.chain, tt.chainID)
			if result != tt.expected {
				t.Errorf("getBlockExplorerURL(%q, %d) = %q, want %q", tt.chain, tt.chainID, result, tt.expected)
			}
		})
	}
}

// TestGetChainDisplayName tests the chain ID to display name helper function
func TestGetChainDisplayName(t *testing.T) {
	tests := []struct {
		chainID  int64
		expected string
	}{
		{1, "Ethereum"},
		{11155111, "Sepolia"},
		{137, "Polygon"},
		{42161, "Arbitrum One"},
		{10, "Optimism"},
		{8453, "Base"},
		{84532, "Base Sepolia"},
		{56, "BNB Chain"},
		{43114, "Avalanche"},
		{999999, ""}, // Unknown chain
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("chainID_%d", tt.chainID), func(t *testing.T) {
			result := getChainDisplayName(tt.chainID)
			if result != tt.expected {
				t.Errorf("getChainDisplayName(%d) = %q, want %q", tt.chainID, result, tt.expected)
			}
		})
	}
}

// TestGetStatusEmoji tests the status emoji helper function
func TestGetStatusEmoji(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"success", "✅"},
		{"failure", "❌"},
		{"partial_success", "⚠️"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			result := getStatusEmoji(tt.status)
			if result != tt.expected {
				t.Errorf("getStatusEmoji(%q) = %q, want %q", tt.status, result, tt.expected)
			}
		})
	}
}

// TestFormatSubjectWithBoldName tests the subject formatting with bold workflow name
func TestFormatSubjectWithBoldName(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		expected string
	}{
		{
			name:     "simulation success",
			subject:  "Simulation: My Workflow successfully completed",
			expected: "Simulation: <b>My Workflow</b> successfully completed",
		},
		{
			name:     "simulation failure",
			subject:  "Simulation: Test Workflow failed to execute",
			expected: "Simulation: <b>Test Workflow</b> failed to execute",
		},
		{
			name:     "simulation partial",
			subject:  "Simulation: Another Workflow partially executed",
			expected: "Simulation: <b>Another Workflow</b> partially executed",
		},
		{
			name:     "run number success",
			subject:  "Run #3: Payment Flow successfully completed",
			expected: "Run #3: <b>Payment Flow</b> successfully completed",
		},
		{
			name:     "run number failure",
			subject:  "Run #15: Swap Workflow failed to execute",
			expected: "Run #15: <b>Swap Workflow</b> failed to execute",
		},
		{
			name:     "no prefix success",
			subject:  "Simple Workflow successfully completed",
			expected: "<b>Simple Workflow</b> successfully completed",
		},
		{
			name:     "no prefix failure",
			subject:  "Basic Task failed to execute",
			expected: "<b>Basic Task</b> failed to execute",
		},
		{
			name:     "workflow name with special chars",
			subject:  "Simulation: Copy of Recurring Payment & Report successfully completed",
			expected: "Simulation: <b>Copy of Recurring Payment &amp; Report</b> successfully completed",
		},
		{
			name:     "unknown format - fallback to escaped",
			subject:  "Some random subject",
			expected: "Some random subject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatSubjectWithBoldName(tt.subject)
			if result != tt.expected {
				t.Errorf("formatSubjectWithBoldName(%q)\n  got:  %q\n  want: %q", tt.subject, result, tt.expected)
			}
		})
	}
}
