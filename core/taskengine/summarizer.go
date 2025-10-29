package taskengine

import (
	"fmt"
	"strings"
)

// Summary represents composed notification content
type Summary struct {
	Subject string
	Body    string
}

// ComposeSummary generates a conservative, deterministic summary based on the VM's
// currently available execution context. It never panics and always returns a fallback.
//
// Semantics (MVP):
//   - If any prior step failed, subject: "{workflowName}: failed at {stepName}",
//     body: "The workflow failed at step \"{stepName}\". Reason: {first line of error}" (truncated internally)
//   - Otherwise, subject: "{workflowName}: succeeded", body: "Finished {lastStepName}."
//   - workflowName: settings.name || workflowContext.name || "Workflow"
//   - step selection: earliest failure; else last successful step if any; else currentStepName fallback
func ComposeSummary(vm *VM, currentStepName string) Summary {
	workflowName := resolveWorkflowName(vm)
	failed, failedName, failedReason := findEarliestFailure(vm)
	if failed {
		return Summary{
			Subject: fmt.Sprintf("%s: failed at %s", workflowName, failedName),
			Body:    fmt.Sprintf("The workflow failed at step \"%s\". Reason: %s", failedName, firstLine(failedReason)),
		}
	}

	lastName := findLastSuccessStepName(vm)
	if lastName == "" {
		lastName = safeName(currentStepName)
	}
	return Summary{
		Subject: fmt.Sprintf("%s: succeeded", workflowName),
		Body:    fmt.Sprintf("Finished %s.", lastName),
	}
}

func resolveWorkflowName(vm *VM) string {
	// Try settings.name
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if n, okn := settings["name"].(string); okn && strings.TrimSpace(n) != "" {
			return n
		}
	}
	if wc, ok := vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
		if n, okn := wc["name"].(string); okn && strings.TrimSpace(n) != "" {
			return n
		}
	}
	return "Workflow"
}

func findEarliestFailure(vm *VM) (bool, string, string) {
	steps := vm.ExecutionLogs
	for _, s := range steps {
		if !s.GetSuccess() {
			name := s.GetName()
			if name == "" {
				name = s.GetId()
			}
			return true, safeName(name), s.GetError()
		}
	}
	return false, "", ""
}

func findLastSuccessStepName(vm *VM) string {
	steps := vm.ExecutionLogs
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].GetSuccess() {
			name := steps[i].GetName()
			if name == "" {
				name = steps[i].GetId()
			}
			return safeName(name)
		}
	}
	return ""
}

func firstLine(s string) string {
	if s == "" {
		return "unknown error"
	}
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

func safeName(s string) string {
	if strings.TrimSpace(s) == "" {
		return "step"
	}
	return s
}
