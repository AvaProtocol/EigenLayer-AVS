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
	totalSteps := len(vm.ExecutionLogs)

	if failed {
		return Summary{
			Subject: fmt.Sprintf("%s: failed at %s (%d steps)", workflowName, failedName, totalSteps),
			Body:    fmt.Sprintf("The workflow failed at step \"%s\". Reason: %s", failedName, firstLine(failedReason)),
		}
	}

	lastName := findLastSuccessStepName(vm)
	if lastName == "" {
		lastName = safeName(currentStepName)
	}

	// Extract runner smart wallet and owner EOA (best-effort, no panics)
	vm.mu.Lock()
	smartWallet := ""
	ownerEOA := ""
	if aaSender, ok := vm.vars["aa_sender"].(string); ok && aaSender != "" {
		smartWallet = aaSender
	}
	if wc, ok := vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
		if runner, ok := wc["runner"].(string); ok && runner != "" && smartWallet == "" {
			smartWallet = runner
		}
		if owner, ok := wc["owner"].(string); ok && owner != "" {
			ownerEOA = owner
		}
		if eoa, ok := wc["eoaAddress"].(string); ok && eoa != "" && ownerEOA == "" {
			ownerEOA = eoa
		}
	}
	vm.mu.Unlock()

	// Collect concise on-chain action lines from executed steps
	var actionLines []string
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := st.GetType()
		if t != "contractWrite" && t != "contractRead" {
			continue
		}

		contractAddr := ""
		methodName := ""
		if st.GetConfig() != nil {
			if cfg, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
				if addr, ok := cfg["contractAddress"].(string); ok {
					contractAddr = addr
				}
				if mcs, ok := cfg["methodCalls"].([]interface{}); ok && len(mcs) > 0 {
					if call, ok := mcs[0].(map[string]interface{}); ok {
						if mn, ok := call["methodName"].(string); ok {
							methodName = mn
						}
					}
				}
			}
		}

		// Include key output data when available (amountOut for swaps, approve value)
		outDetail := ""
		if t == "contractWrite" && st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
			if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
				if v, ok := m[methodName].(map[string]interface{}); ok {
					if amt, ok := v["amountOut"].(string); ok && amt != "" {
						outDetail = fmt.Sprintf("amountOut=%s", amt)
					}
					if val, ok := v["value"].(string); ok && val != "" {
						outDetail = fmt.Sprintf("value=%s", val)
					}
				}
			}
		}
		if t == "contractRead" && st.GetContractRead() != nil && st.GetContractRead().Data != nil {
			if m, ok := st.GetContractRead().Data.AsInterface().(map[string]interface{}); ok {
				if v, ok := m[methodName].(map[string]interface{}); ok {
					if amt, ok := v["amountOut"].(string); ok && amt != "" {
						outDetail = fmt.Sprintf("amountOut=%s", amt)
					}
				}
			}
		}

		line := fmt.Sprintf("- %s: %s on %s", st.GetName(), methodName, contractAddr)
		if outDetail != "" {
			line += fmt.Sprintf(" (%s)", outDetail)
		}
		actionLines = append(actionLines, line)
	}

	body := fmt.Sprintf(
		"Workflow '%s' completed successfully with %d steps.\nRunner smart wallet: %s (owner EOA: %s)\n",
		workflowName, totalSteps, smartWallet, ownerEOA,
	)
	if len(actionLines) > 0 {
		body += "On-chain actions:\n" + strings.Join(actionLines, "\n")
	} else {
		body += fmt.Sprintf("Finished %s.", lastName)
	}

	return Summary{
		Subject: fmt.Sprintf("%s: succeeded (%d steps)", workflowName, totalSteps),
		Body:    body,
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
