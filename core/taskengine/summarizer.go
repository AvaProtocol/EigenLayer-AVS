package taskengine

import (
	"fmt"
	"html"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Summary represents composed notification content
type Summary struct {
	Subject string
	Body    string
}

// SendGridDynamicData returns a dynamic_template_data map for SendGrid Dynamic Templates.
// Decoupled design: provide minimal variables and let the template handle styling.
// - subject: email subject line
// - analysisHtml: minimal HTML (paragraphs and <br/>) derived from body
// - preheader: short preview, reuse subject
func (s Summary) SendGridDynamicData() map[string]interface{} {
	// Remove runner/owner/status boilerplate; keep only the core analysis/narrative
	clean := filterAnalysisTextForTemplate(s.Body)
	bareHTML := buildBareHTMLFromText(clean)
	return map[string]interface{}{
		"subject":      s.Subject,
		"analysisHtml": bareHTML,
		"preheader":    s.Subject,
	}
}

// BuildBranchAndSkippedSummary builds a deterministic summary (text and HTML)
// describing which nodes were skipped due to branching and which branch
// conditions were selected along with the configured condition expressions.
func BuildBranchAndSkippedSummary(vm *VM) (string, string) {
	if vm == nil {
		return "", ""
	}

	// Collect executed step names
	executed := make(map[string]struct{})
	for _, st := range vm.ExecutionLogs {
		name := st.GetName()
		if name == "" {
			name = st.GetId()
		}
		if name != "" {
			executed[name] = struct{}{}
		}
	}

	// Compute skipped nodes (by name)
	vm.mu.Lock()
	skipped := make([]string, 0, len(vm.TaskNodes))
	for _, n := range vm.TaskNodes {
		if n == nil {
			continue
		}
		if _, ok := executed[n.Name]; !ok {
			skipped = append(skipped, n.Name)
		}
	}
	vm.mu.Unlock()

	// Collect branch selections with evaluation details
	type branchInfo struct {
		Step                 string
		Path                 string
		ConditionID          string
		TargetNodeName       string // Name of the node this branch leads to
		Conditions           []struct{ ID, Type, Expr string }
		ConditionEvaluations []map[string]interface{} // Detailed evaluation results from metadata
	}
	branches := make([]branchInfo, 0, 2)
	// On-chain action accounting
	var successfulRealWrites, failedWrites, successfulSimulatedWrites int
	simulatedWriteNames := make([]string, 0, 4)
	for _, st := range vm.ExecutionLogs {
		t := strings.ToUpper(st.GetType())
		if strings.Contains(t, "BRANCH") {
			bi := branchInfo{}
			name := st.GetName()
			if name == "" {
				name = st.GetId()
			}
			bi.Step = name
			if br := st.GetBranch(); br != nil && br.GetData() != nil {
				if m, ok := br.GetData().AsInterface().(map[string]interface{}); ok {
					if cid, ok := m["conditionId"].(string); ok {
						bi.ConditionID = cid
						if idx := strings.LastIndex(cid, "."); idx >= 0 && idx+1 < len(cid) {
							switch cid[idx+1:] {
							case "0":
								bi.Path = "If"
							case "1":
								bi.Path = "Else"
							default:
								bi.Path = cid[idx+1:]
							}
						}

						// Find target node name from edges
						vm.mu.Lock()
						if vm.task != nil && vm.task.Edges != nil {
							for _, edge := range vm.task.Edges {
								if edge.Source == cid {
									// Found the edge from this condition
									if targetNode, exists := vm.TaskNodes[edge.Target]; exists && targetNode != nil {
										bi.TargetNodeName = targetNode.Name
									}
									break
								}
							}
						}
						vm.mu.Unlock()
					}
				}
			}
			// Get condition evaluations from metadata (new structured format)
			if st.GetMetadata() != nil {
				if metaMap, ok := st.GetMetadata().AsInterface().(map[string]interface{}); ok {
					if evals, ok := metaMap["conditionEvaluations"].([]interface{}); ok {
						for _, e := range evals {
							if em, ok := e.(map[string]interface{}); ok {
								bi.ConditionEvaluations = append(bi.ConditionEvaluations, em)
							}
						}
					}
				}
			}

			// Fallback to config for backward compatibility
			if len(bi.ConditionEvaluations) == 0 && st.GetConfig() != nil {
				if cfgMap, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
					if conds, ok := cfgMap["conditions"].([]interface{}); ok {
						for _, c := range conds {
							if cm, ok := c.(map[string]interface{}); ok {
								bi.Conditions = append(bi.Conditions, struct{ ID, Type, Expr string }{
									ID:   asString(cm["id"]),
									Type: asString(cm["type"]),
									Expr: asString(cm["expression"]),
								})
							}
						}
					}
				}
			}
			branches = append(branches, bi)
		}
		// Count contract writes deterministically
		if strings.Contains(t, "CONTRACT_WRITE") {
			// Determine simulation mode (default to VM setting, override with per-node config)
			isSimulated := vm.IsSimulation
			if st.GetConfig() != nil {
				if cfgMap, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
					if sim, ok := cfgMap["isSimulated"]; ok {
						switch v := sim.(type) {
						case bool:
							isSimulated = v
						case string:
							if strings.EqualFold(strings.TrimSpace(v), "true") {
								isSimulated = true
							} else if strings.EqualFold(strings.TrimSpace(v), "false") {
								isSimulated = false
							}
						}
					}
				}
			}
			if st.GetSuccess() {
				if isSimulated {
					successfulSimulatedWrites++
					name := st.GetName()
					if name == "" {
						name = st.GetId()
					}
					if name != "" {
						simulatedWriteNames = append(simulatedWriteNames, name)
					}
				} else {
					successfulRealWrites++
				}
			} else {
				failedWrites++
			}
		}
	}

	if len(skipped) == 0 && len(branches) == 0 && successfulRealWrites == 0 && failedWrites == 0 && successfulSimulatedWrites == 0 {
		return "", ""
	}

	// Compose plain text
	var tb []string
	// Add Summary heading and one-line narrative
	tb = append(tb, "Summary")
	summaryLine := ""
	if len(skipped) > 0 {
		summaryLine = "The workflow did not fully execute. Some nodes were skipped due to branching conditions."
	} else {
		summaryLine = "The workflow executed without branching skips."
	}
	// On-chain clause
	if successfulRealWrites > 0 {
		summaryLine += fmt.Sprintf(" Executed %d on-chain transaction(s).", successfulRealWrites)
	} else if successfulSimulatedWrites > 0 {
		// Include simulated node names
		if len(simulatedWriteNames) > 0 {
			summaryLine += fmt.Sprintf(" No on-chain transactions executed, due to simulated mode of %s.", strings.Join(simulatedWriteNames, ", "))
		} else {
			summaryLine += " No on-chain transactions executed, due to simulated mode."
		}
	} else if failedWrites > 0 {
		summaryLine += " No on-chain transactions executed; contract writes failed."
	} else {
		summaryLine += " No on-chain transactions executed."
	}
	tb = append(tb, summaryLine)
	tb = append(tb, "") // blank line before Skipped nodes
	if len(skipped) > 0 {
		tb = append(tb, "The below nodes were skipped due to branching conditions")
		for _, n := range skipped {
			tb = append(tb, "- "+n)
		}
	}
	for _, b := range branches {
		path := normalizeBranchType(b.Path)
		header := fmt.Sprintf("Branch '%s': selected %s condition", b.Step, path)
		tb = append(tb, header)

		// Use new structured evaluation data if available
		if len(b.ConditionEvaluations) > 0 {
			for _, eval := range b.ConditionEvaluations {
				label := asString(eval["label"])
				expr := asString(eval["expression"])
				result, _ := eval["result"].(bool)
				taken, _ := eval["taken"].(bool)

				if taken {
					tb = append(tb, fmt.Sprintf("- %s (selected)", label))
				} else if result {
					tb = append(tb, fmt.Sprintf("- %s: true", label))
				} else {
					if strings.TrimSpace(expr) == "" {
						tb = append(tb, fmt.Sprintf("- %s: false", label))
					} else {
						tb = append(tb, fmt.Sprintf("- %s: false -> %s", label, expr))
					}
				}
			}
		} else if len(b.Conditions) > 0 {
			// Fallback to old format for backward compatibility
			tb = append(tb, "Conditions:")
			for _, c := range b.Conditions {
				label := normalizeBranchType(c.Type)
				if strings.TrimSpace(c.Expr) == "" {
					tb = append(tb, fmt.Sprintf("- %s", label))
				} else {
					tb = append(tb, fmt.Sprintf("- %s -> %s", label, c.Expr))
				}
			}
		}
	}
	text := strings.Join(tb, "\n")

	// Compose minimal HTML (tight spacing, deterministic)
	var hb []string
	// Summary heading and sentence
	hb = append(hb, "<div style=\"font-weight:600; margin:8px 0 4px\">Summary</div>")
	hb = append(hb, "<p style=\"margin:0 0 8px\">"+html.EscapeString(summaryLine)+"</p>")
	// Spacer before Skipped nodes
	hb = append(hb, "<div style=\"height:8px\"></div>")
	if len(skipped) > 0 {
		hb = append(hb, "<div style=\"font-weight:600; margin:8px 0 4px\">The below nodes were skipped due to branching conditions</div>")
		hb = append(hb, "<ul style=\"margin:0 0 12px 20px; padding:0\">")
		for _, n := range skipped {
			hb = append(hb, "<li>"+html.EscapeString(n)+"</li>")
		}
		hb = append(hb, "</ul>")
	}
	for _, b := range branches {
		path := normalizeBranchType(b.Path)
		// Include target node in header, matching the execution log format
		headerText := fmt.Sprintf("Branch '%s': selected %s condition", b.Step, path)
		if b.TargetNodeName != "" {
			headerText += fmt.Sprintf(" -> led to node '%s'", b.TargetNodeName)
		} else {
			headerText += " -> no next node"
		}
		hb = append(hb, fmt.Sprintf("<div style=\"font-weight:600; margin:8px 0 4px\">%s</div>", html.EscapeString(headerText)))

		// Use new structured evaluation data if available
		if len(b.ConditionEvaluations) > 0 {
			// Use execution log format (not bullet list)
			for _, eval := range b.ConditionEvaluations {
				label := asString(eval["label"])
				expr := asString(eval["expression"])
				taken, _ := eval["taken"].(bool)

				if taken {
					// Condition that was selected - show in execution log format
					if strings.TrimSpace(expr) != "" {
						hb = append(hb, fmt.Sprintf("<p style=\"margin:4px 0 0 0; font-weight:500\">%s condition (selected)</p>", html.EscapeString(label)))
						hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 8px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
					}
				} else {
					// False condition - show operand values if available
					if strings.TrimSpace(expr) == "" {
						hb = append(hb, fmt.Sprintf("<p style=\"margin:4px 0 0 0\">%s condition resolved to false (empty)</p>", html.EscapeString(label)))
					} else {
						hb = append(hb, fmt.Sprintf("<p style=\"margin:4px 0 0 0\">%s condition resolved to false</p>", html.EscapeString(label)))
						hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 2px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))

						// Show variable values if available
						if varVals, ok := eval["variableValues"].(map[string]interface{}); ok && len(varVals) > 0 {
							hb = append(hb, "<p style=\"margin:0 0 8px 20px; font-size:0.9em; color:#6B7280\">Variable values:</p>")
							for varName, varVal := range varVals {
								// Truncate large values for readability
								valStr := fmt.Sprintf("%v", varVal)
								if len(valStr) > 100 {
									valStr = valStr[:97] + "..."
								}
								hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 2px 40px; font-family:monospace; font-size:0.85em; color:#6B7280\">%s = %s</p>", html.EscapeString(varName), html.EscapeString(valStr)))
							}
						}
					}
				}
			}
			// Add spacing after branch details
			hb = append(hb, "<div style=\"height:8px\"></div>")
		} else if len(b.Conditions) > 0 {
			// Fallback to old format for backward compatibility
			hb = append(hb, "<ul style=\"margin:0 0 12px 20px; padding:0\">")
			for _, c := range b.Conditions {
				expr := html.EscapeString(c.Expr)
				label := html.EscapeString(normalizeBranchType(c.Type))
				if strings.TrimSpace(expr) == "" {
					hb = append(hb, fmt.Sprintf("<li>%s</li>", label))
				} else {
					hb = append(hb, fmt.Sprintf("<li>%s → <code>%s</code></li>", label, expr))
				}
			}
			hb = append(hb, "</ul>")
		}
	}
	htmlOut := strings.Join(hb, "\n")
	return text, htmlOut
}

// asString converts interface{} to string safely.
func asString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// normalizeBranchType maps various case/styles to If/ElseIf/Else titles.
func normalizeBranchType(t string) string {
	x := strings.TrimSpace(strings.ToLower(t))
	switch x {
	case "if":
		return "If"
	case "elseif", "else if", "else_if":
		return "ElseIf"
	case "else":
		return "Else"
	default:
		if x == "0" {
			return "If"
		}
		if x == "1" {
			return "Else"
		}
		return strings.Title(strings.TrimSpace(t))
	}
}

// buildBareHTMLFromText converts plain text into minimal HTML paragraphs without global styles
func buildBareHTMLFromText(body string) string {
	// Escape HTML to avoid injection
	safe := html.EscapeString(body)
	// Normalize newlines
	safe = strings.ReplaceAll(safe, "\r\n", "\n")
	// Split by paragraphs (double newline)
	parts := strings.Split(safe, "\n\n")
	var paragraphs []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// Convert single newlines within a paragraph to <br/>
		p = strings.ReplaceAll(p, "\n", "<br/>")
		paragraphs = append(paragraphs, "<p>"+p+"</p>")
	}
	return strings.Join(paragraphs, "\n")
}

// filterAnalysisTextForTemplate removes lines that duplicate data provided via separate template variables
// such as runner/eoaAddress and high-level status lines. It preserves the core narrative/actions.
func filterAnalysisTextForTemplate(body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	b := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(b, "\n")
	var kept []string
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		lower := strings.ToLower(trim)
		if trim == "" {
			// keep paragraph breaks; we'll collapse later
			kept = append(kept, trim)
			continue
		}
		// Drop runner/owner and generic completion/status lines
		if strings.HasPrefix(trim, "Smart wallet ") ||
			strings.HasPrefix(trim, "Runner smart wallet") ||
			strings.HasPrefix(trim, "All steps completed") ||
			strings.HasPrefix(trim, "Workflow '") ||
			strings.HasPrefix(trim, "The email was sent successfully via SendGrid") ||
			strings.HasPrefix(trim, "Single-node execution") ||
			strings.HasPrefix(lower, "smart wallet ") ||
			strings.HasPrefix(lower, "runner smart wallet") {
			continue
		}
		kept = append(kept, ln)
	}
	// Collapse extra blank lines: ensure at most one blank line between paragraphs
	var collapsed []string
	prevBlank := false
	for _, ln := range kept {
		if strings.TrimSpace(ln) == "" {
			if !prevBlank {
				collapsed = append(collapsed, "")
			}
			prevBlank = true
			continue
		}
		collapsed = append(collapsed, ln)
		prevBlank = false
	}
	res := strings.Join(collapsed, "\n")
	res = strings.TrimSpace(res)
	if res == "" {
		// Preserve original fallback message if present
		if strings.Contains(b, "No specific on-chain actions were recorded") {
			return "No specific on-chain actions were recorded; this may have been a simulation or a step encountered an error."
		}
	}
	return res
}

// buildStyledHTMLEmailForSummary wraps a plain-text body into a styled HTML layout
// suitable for email clients with light theme
func buildStyledHTMLEmailForSummary(subject, body string) string {
	// Escape HTML to avoid injection
	safe := html.EscapeString(body)
	// Normalize newlines
	safe = strings.ReplaceAll(safe, "\r\n", "\n")
	// Split by paragraphs (double newline)
	parts := strings.Split(safe, "\n\n")
	var paragraphs []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// Convert single newlines within a paragraph to <br/>
		p = strings.ReplaceAll(p, "\n", "<br/>")
		paragraphs = append(paragraphs, "<p style=\"margin:0 0 16px 0;\">"+p+"</p>")
	}

	content := strings.Join(paragraphs, "\n")
	// Minimal, responsive-friendly light theme
	return "<!DOCTYPE html><html><head><meta charset=\"UTF-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
		"<title>" + html.EscapeString(subject) + "</title>" +
		"<style>body{background:#FFFFFF;color:#1F2937;margin:0;padding:0;font-family:Arial,Helvetica,sans-serif;-webkit-font-smoothing:antialiased;}" +
		".container{max-width:640px;margin:0 auto;padding:32px 24px;}h1,h2,h3,h4,h5,h6{color:#111827;margin-top:24px;margin-bottom:12px;}a{color:#8B5CF6;text-decoration:none;}" +
		"p{margin:0 0 16px 0;line-height:1.6;}.divider{border-top:1px solid #E5E7EB;margin:24px 0;}" +
		"@media(max-width:480px){.container{padding:24px 16px;}h1,h2,h3{font-size:1.2rem;}}</style></head>" +
		"<body><div class=\"container\">" + content + "</div></body></html>"
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

	// Get total workflow steps (trigger + all nodes) and executed steps count
	totalWorkflowSteps := getTotalWorkflowSteps(vm)
	executedSteps := len(vm.ExecutionLogs)

	// Build subject up-front to support richer failure bodies
	subject := fmt.Sprintf("%s: succeeded (%d out of %d steps)", workflowName, executedSteps, totalWorkflowSteps)
	if failed {
		subject = fmt.Sprintf("%s: failed at %s (%d out of %d steps)", workflowName, failedName, executedSteps, totalWorkflowSteps)
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
	// Also check settings.runner for single-node executions
	if smartWallet == "" {
		if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
			if runner, ok := settings["runner"].(string); ok && strings.TrimSpace(runner) != "" {
				smartWallet = runner
			}
		}
	}
	// Fallback to TaskOwner if available
	if ownerEOA == "" && vm.TaskOwner != (common.Address{}) {
		ownerEOA = vm.TaskOwner.Hex()
	}
	vm.mu.Unlock()

	// Collect concise on-chain action lines from executed steps
	var actionLines []string
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := strings.ToUpper(st.GetType())
		isWrite := strings.Contains(t, "CONTRACT_WRITE")
		isRead := strings.Contains(t, "CONTRACT_READ")
		if !isWrite && !isRead {
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
		if isWrite && st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
			if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
				// Try common event shapes first (Approval, Swap)
				if ev, ok := m["Approval"].(map[string]interface{}); ok {
					if val, ok := ev["value"].(string); ok && val != "" {
						outDetail = fmt.Sprintf("value=%s", val)
					}
				}
				if ev, ok := m["Swap"].(map[string]interface{}); ok && outDetail == "" {
					// Prefer amountOut from event amounts if present
					if amt, ok := ev["amount1"].(string); ok && amt != "" { // token1 out in USDC/WETH pool often amount1
						outDetail = fmt.Sprintf("amountOut=%s", amt)
					} else if amt0, ok := ev["amount0"].(string); ok && amt0 != "" {
						outDetail = fmt.Sprintf("amountOut=%s", amt0)
					}
				}
			}
			// Fallback to metadata.value.amountOut for writes returning a value (e.g., exactInputSingle)
			if outDetail == "" && st.Metadata != nil {
				if meta := st.Metadata.AsInterface(); meta != nil {
					if arr, ok := meta.([]interface{}); ok && len(arr) > 0 {
						if first, ok := arr[0].(map[string]interface{}); ok {
							if val, ok := first["value"].(map[string]interface{}); ok {
								if amt, ok := val["amountOut"].(string); ok && amt != "" {
									outDetail = fmt.Sprintf("amountOut=%s", amt)
								}
							} else if amtStr, ok := first["value"].(string); ok && amtStr != "" && strings.ToLower(methodName) == "exactinputsingle" {
								outDetail = fmt.Sprintf("amountOut=%s", amtStr)
							}
						}
					}
				}
			}
		}
		if isRead && st.GetContractRead() != nil && st.GetContractRead().Data != nil {
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

	// Single-node execution enhancement: if there are no recorded steps, try to
	// synthesize concise action lines from inputVariables (carried into the
	// runNodeImmediately call) and settings. This helps users understand what
	// happened even when the workflow isn't deployed end-to-end yet.
	if totalWorkflowSteps == 1 && len(actionLines) == 0 && isSingleNodeImmediate(vm) {
		vm.mu.Lock()
		var settings map[string]interface{}
		if m, ok := vm.vars["settings"].(map[string]interface{}); ok {
			settings = m
		}
		// Read inputs directly from top-level vars (RunNodeWithInputs adds them as top-level)
		approveVar, hasApprove := vm.vars["approve_token1"].(map[string]interface{})
		quoteVar, hasQuote := vm.vars["get_quote"].(map[string]interface{})
		swapVar, hasSwap := vm.vars["run_swap"].(map[string]interface{})
		vm.mu.Unlock()

		if hasApprove || hasQuote || hasSwap {
			// Helpful addresses from settings
			var token1Addr, quoterV2Addr, swapRouter02Addr string
			if pool, ok := settings["uniswapv3_pool"].(map[string]interface{}); ok {
				if t1, ok := pool["token1"].(map[string]interface{}); ok {
					if id, ok := t1["id"].(string); ok {
						token1Addr = id
					}
				}
			}
			if contracts, ok := settings["uniswapv3_contracts"].(map[string]interface{}); ok {
				if a, ok := contracts["quoterV2"].(string); ok {
					quoterV2Addr = a
				}
				if a, ok := contracts["swapRouter02"].(string); ok {
					swapRouter02Addr = a
				}
			}

			// approve_token1
			if hasApprove {
				if data, ok := approveVar["data"].(map[string]interface{}); ok {
					if approve, ok := data["approve"].(map[string]interface{}); ok {
						val := ""
						if s, ok := approve["value"].(string); ok && s != "" {
							val = s
						}
						line := fmt.Sprintf("- approve_token1: approve on %s", token1Addr)
						if val != "" {
							line += fmt.Sprintf(" (value=%s)", val)
						}
						actionLines = append(actionLines, line)
					}
				}
			}

			// get_quote
			if hasQuote {
				if data, ok := quoteVar["data"].(map[string]interface{}); ok {
					if q, ok := data["quoteExactInputSingle"].(map[string]interface{}); ok {
						amt := ""
						if s, ok := q["amountOut"].(string); ok && s != "" {
							amt = s
						}
						line := fmt.Sprintf("- get_quote: quoteExactInputSingle on %s", quoterV2Addr)
						if amt != "" {
							line += fmt.Sprintf(" (amountOut=%s)", amt)
						}
						actionLines = append(actionLines, line)
					}
				}
			}

			// run_swap
			if hasSwap {
				if data, ok := swapVar["data"].(map[string]interface{}); ok {
					if swap, ok := data["exactInputSingle"].(map[string]interface{}); ok {
						amt := ""
						if s, ok := swap["amountOut"].(string); ok && s != "" {
							amt = s
						}
						line := fmt.Sprintf("- run_swap: exactInputSingle on %s", swapRouter02Addr)
						if amt != "" {
							line += fmt.Sprintf(" (amountOut=%s)", amt)
						}
						actionLines = append(actionLines, line)
					}
				}
			}
		}
	}

	var body string
	if failed {
		// Failure body: include runner/owner and any completed on-chain actions before failure
		body = fmt.Sprintf(
			"The workflow failed at step \"%s\". Reason: %s\nRunner smart wallet: %s (owner EOA: %s)\n",
			failedName, firstLine(failedReason), smartWallet, ownerEOA,
		)
		if len(actionLines) > 0 {
			body += "On-chain actions completed before failure:\n" + strings.Join(actionLines, "\n")
		} else {
			body += "No on-chain actions were completed before the failure."
		}
	} else {
		// Success body using concise narrative per PRD
		chainName := resolveChainName(vm)
		narrative := buildNarrativeFromLogs(vm)
		if strings.TrimSpace(narrative) != "" {
			body = fmt.Sprintf(
				"Smart wallet %s (owner %s) executed %d on-chain actions.\n\n%s\n\nAll steps completed on %s. Last step: %s.",
				smartWallet, ownerEOA, countActionsInLogs(vm), narrative, chainName, lastName,
			)
		} else {
			// Fallback to previous list format
			body = fmt.Sprintf(
				"Workflow '%s' completed successfully with %d out of %d steps.\nRunner smart wallet: %s (owner EOA: %s)\n",
				workflowName, executedSteps, totalWorkflowSteps, smartWallet, ownerEOA,
			)
			if len(actionLines) > 0 {
				body += "On-chain actions:\n" + strings.Join(actionLines, "\n")
				if executedSteps == 0 {
					body += "\nSingle-node execution using prior outputs. A full step-by-step summary will be sent after this workflow is deployed and run end-to-end."
				}
			} else {
				if executedSteps == 0 {
					body += "Single-node execution. A full step-by-step summary will be sent after this workflow is deployed and run end-to-end.\n"
				}
				body += fmt.Sprintf("Finished %s.", lastName)
			}
		}
	}

	return Summary{
		Subject: subject,
		Body:    body,
	}
}

// isSingleNodeImmediate returns true if the VM looks like a temp VM used for a
// one-off RunNodeImmediately execution (no TaskId, exactly one TaskNode).
func isSingleNodeImmediate(vm *VM) bool {
	if vm == nil {
		return false
	}
	// No lock needed for GetTaskId; lock to count TaskNodes safely
	vm.mu.Lock()
	count := len(vm.TaskNodes)
	vm.mu.Unlock()
	return vm.GetTaskId() == "" && count == 1
}

// getTotalWorkflowSteps returns the total number of steps in a workflow,
// including the trigger and all nodes. This represents the full workflow size,
// not just the executed steps so far.
func getTotalWorkflowSteps(vm *VM) int {
	if vm == nil {
		return 0
	}

	// For single-node immediate execution (testing), return 1
	if isSingleNodeImmediate(vm) {
		return 1
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Count: 1 trigger + all nodes in TaskNodes
	totalSteps := 1 + len(vm.TaskNodes)

	return totalSteps
}

// ------- Helpers for concise narrative formatting -------

func resolveChainName(vm *VM) string {
	// Prefer settings.chain then chain_id mapping, else try smartWalletConfig.ChainID
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if n, ok := settings["chain"].(string); ok && strings.TrimSpace(n) != "" {
			return n
		}
		if id, ok := settings["chain_id"].(int); ok && id != 0 {
			if name := mapChainIDToName(int64(id)); name != "" {
				return name
			}
		}
	}
	if vm.smartWalletConfig != nil && vm.smartWalletConfig.ChainID != 0 {
		if name := mapChainIDToName(vm.smartWalletConfig.ChainID); name != "" {
			return name
		}
	}
	return "unknown network"
}

func mapChainIDToName(id int64) string {
	switch id {
	case 1:
		return "Ethereum"
	case 5, 11155111:
		return "Sepolia"
	case 8453:
		return "Base"
	case 84531:
		return "Base Goerli"
	default:
		return "chain-" + fmt.Sprint(id)
	}
}

func countActionsInLogs(vm *VM) int {
	count := 0
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := strings.ToUpper(st.GetType())
		if strings.Contains(t, "CONTRACT_WRITE") || strings.Contains(t, "CONTRACT_READ") {
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return count
}

func buildNarrativeFromLogs(vm *VM) string {
	// Extract token context from settings (pool tokens and known contracts)
	vm.mu.Lock()
	var settings map[string]interface{}
	if m, ok := vm.vars["settings"].(map[string]interface{}); ok {
		settings = m
	}
	vm.mu.Unlock()

	token0Sym, token0Dec := "", 18
	token1Sym, token1Dec := "", 18
	var routerAddr string
	if settings != nil {
		if pool, ok := settings["uniswapv3_pool"].(map[string]interface{}); ok {
			if t0, ok := pool["token0"].(map[string]interface{}); ok {
				if s, ok := t0["symbol"].(string); ok {
					token0Sym = s
					token0Dec = defaultDecimalsForSymbol(s)
				}
			}
			if t1, ok := pool["token1"].(map[string]interface{}); ok {
				if s, ok := t1["symbol"].(string); ok {
					token1Sym = s
					token1Dec = defaultDecimalsForSymbol(s)
				}
			}
		}
		if contracts, ok := settings["uniswapv3_contracts"].(map[string]interface{}); ok {
			if a, ok := contracts["swapRouter02"].(string); ok {
				routerAddr = a
			}
		}
	}

	// Build sentences
	var sentences []string
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		t := strings.ToUpper(st.GetType())
		if !(strings.Contains(t, "CONTRACT_WRITE") || strings.Contains(t, "CONTRACT_READ")) {
			continue
		}

		methodName := ""
		if st.GetConfig() != nil {
			if cfg, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
				if mcs, ok := cfg["methodCalls"].([]interface{}); ok && len(mcs) > 0 {
					if call, ok := mcs[0].(map[string]interface{}); ok {
						if mn, ok := call["methodName"].(string); ok {
							methodName = strings.ToLower(mn)
						}
					}
				}
			}
		}

		switch methodName {
		case "approve":
			value := ""
			spender := ""
			if st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
				if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
					if ev, ok := m["Approval"].(map[string]interface{}); ok {
						if v, ok := ev["value"].(string); ok {
							value = v
						}
						if sp, ok := ev["spender"].(string); ok {
							spender = sp
						}
					}
				}
			}
			if spender == "" {
				spender = routerAddr
			}
			if value != "" && token1Sym != "" {
				sentences = append(sentences, fmt.Sprintf("Approved %s %s to Uniswap V3 router %s for trading.", formatAmount(value, token1Dec), token1Sym, spender))
			}

		case "quoteexactinputsingle":
			amountOut := ""
			if st.GetContractRead() != nil && st.GetContractRead().Data != nil {
				if m, ok := st.GetContractRead().Data.AsInterface().(map[string]interface{}); ok {
					if q, ok := m["quoteExactInputSingle"].(map[string]interface{}); ok {
						if a, ok := q["amountOut"].(string); ok {
							amountOut = a
						}
					}
				}
			}
			if amountOut != "" && token0Sym != "" {
				sentences = append(sentences, fmt.Sprintf("Quoted ~%s %s via Uniswap V3.", formatAmount(amountOut, token0Dec), token0Sym))
			}

		case "exactinputsingle":
			// Prefer metadata.value.amountOut on writes
			amountOut := ""
			if st.Metadata != nil {
				if meta := st.Metadata.AsInterface(); meta != nil {
					if arr, ok := meta.([]interface{}); ok && len(arr) > 0 {
						if first, ok := arr[0].(map[string]interface{}); ok {
							if val, ok := first["value"].(map[string]interface{}); ok {
								if amt, ok := val["amountOut"].(string); ok {
									amountOut = amt
								}
							} else if amtStr, ok := first["value"].(string); ok {
								amountOut = amtStr
							}
						}
					}
				}
			}
			if amountOut != "" && token1Sym != "" && token0Sym != "" {
				// We cannot reliably know tokenIn amount without decoding params; use narrative focused on output
				sentences = append(sentences, fmt.Sprintf("Swapped for ~%s %s via Uniswap V3 (exactInputSingle).", formatAmount(amountOut, token0Dec), token0Sym))
			}
		}
	}
	// Join sentences with blank lines for readability
	return strings.Join(sentences, "\n\n")
}

func defaultDecimalsForSymbol(sym string) int {
	switch strings.ToUpper(sym) {
	case "USDC", "USDT":
		return 6
	default:
		return 18
	}
}

func formatAmount(raw string, decimals int) string {
	if raw == "" {
		return "0.0000"
	}
	bi, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return raw
	}
	if decimals <= 0 {
		return bi.String()
	}
	// Convert to decimal with 4 fractional digits
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	// value = bi / denom
	intPart := new(big.Int).Div(bi, denom)
	rem := new(big.Int).Mod(bi, denom)
	// Scale remainder to four decimals
	scaleDigits := int64(4)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(scaleDigits), nil)
	frac := new(big.Int).Mul(rem, scale)
	frac.Div(frac, denom)
	fracStr := frac.String()
	// Left-pad with zeros to fixed width
	for len(fracStr) < int(scaleDigits) {
		fracStr = "0" + fracStr
	}
	return fmt.Sprintf("%s.%s", intPart.String(), fracStr)
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

// formatComparisonForHTML formats comparison operands as HTML for email summaries.
// It uses the same parsing/formatting logic as execution logs but renders as HTML.
func formatComparisonForHTML(operandData map[string]interface{}) string {
	if operandData == nil {
		return ""
	}

	leftExpr, _ := operandData["leftExpr"].(string)
	rightExpr, _ := operandData["rightExpr"].(string)
	operator, _ := operandData["operator"].(string)
	left := operandData["left"]
	right := operandData["right"]

	if leftExpr == "" || rightExpr == "" || operator == "" {
		return ""
	}

	// Format operand values using shared concise formatter
	leftVal := formatValueConcise(left)
	rightVal := formatValueConcise(right)

	return fmt.Sprintf(
		"<p style=\"margin:0 0 2px 20px; font-family:monospace; font-size:0.9em\">%s %s %s</p>"+
			"<p style=\"margin:0 0 8px 20px; font-family:monospace; font-size:0.9em\">%s %s %s</p>",
		html.EscapeString(leftExpr),
		html.EscapeString(operator),
		html.EscapeString(rightExpr),
		html.EscapeString(leftVal),
		html.EscapeString(operator),
		html.EscapeString(rightVal),
	)
}

// formatValueConcise formats a value concisely for display (shared by logs and emails)
func formatValueConcise(v interface{}) string {
	if v == nil {
		return "null"
	}

	switch val := v.(type) {
	case string:
		if len(val) > 50 {
			return fmt.Sprintf("\"%s...\"", val[:47])
		}
		return fmt.Sprintf("\"%s\"", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case float64, int, int64:
		return fmt.Sprintf("%v", val)
	case map[string]interface{}:
		return fmt.Sprintf("{object with %d keys}", len(val))
	case []interface{}:
		return fmt.Sprintf("[array with %d items]", len(val))
	default:
		s := fmt.Sprintf("%v", val)
		if len(s) > 50 {
			return s[:47] + "..."
		}
		return s
	}
}
