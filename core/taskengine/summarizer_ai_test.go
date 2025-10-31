package taskengine

import (
	"context"
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
