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
	f := &fakeSummarizer{resp: Summary{Subject: "AI subject", Body: "AI body"}}
	SetSummarizer(f)
	vm := NewVM()
	s := ComposeSummarySmart(vm, "current")
	if s.Subject != "AI subject" || s.Body != "AI body" {
		t.Fatalf("ai summarizer was not used: got %+v", s)
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
