package taskengine

import (
	"fmt"
	"html"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Summary represents composed notification content
type Summary struct {
	Subject      string
	Body         string
	SummaryLine  string // One-liner summary (e.g., "Your workflow 'Test Stoploss' executed 7 out of 7 total steps")
	AnalysisHtml string // Pre-formatted HTML content with ✓ symbols (from context-memory)
	StatusHtml   string // Status badge HTML (green/yellow/red badge with icon) - from context-memory
	Status       string // Execution status: "success", "partial_success", "failure" - from context-memory
}

// SendGridDynamicData returns a dynamic_template_data map for SendGrid Dynamic Templates.
// Decoupled design: provide minimal variables and let the template handle styling.
// - subject: email subject line
// - analysisHtml: minimal HTML (paragraphs and <br/>) derived from body, or use pre-formatted AnalysisHtml if available
// - preheader: short preview, reuse subject
func (s Summary) SendGridDynamicData() map[string]interface{} {
	// If AnalysisHtml is provided (from context-memory), use it directly
	// Otherwise, build from body using the deterministic method
	var analysisHtml string
	if s.AnalysisHtml != "" {
		analysisHtml = s.AnalysisHtml
	} else {
		// Remove runner/owner/status boilerplate; keep only the core analysis/narrative
		clean := filterAnalysisTextForTemplate(s.Body)
		analysisHtml = buildBareHTMLFromText(clean)
	}
	data := map[string]interface{}{
		"subject":      s.Subject,
		"analysisHtml": analysisHtml,
		"preheader":    s.Subject,
	}
	// Include statusHtml from context-memory if available
	if s.StatusHtml != "" {
		data["statusHtml"] = s.StatusHtml
	}
	// Include summary line if available
	if s.SummaryLine != "" {
		data["summary"] = s.SummaryLine
	}
	return data
}

// BuildBranchAndSkippedSummary builds a deterministic summary (text and HTML)
// describing which nodes were skipped due to branching and which branch
// conditions were selected along with the configured condition expressions.
// currentNodeName: optional name of the currently executing node (not yet in ExecutionLogs)
func BuildBranchAndSkippedSummary(vm *VM, currentNodeName ...string) (string, string) {
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
	// Include the current node if provided (it's executing but not yet in ExecutionLogs)
	if len(currentNodeName) > 0 && currentNodeName[0] != "" {
		executed[currentNodeName[0]] = struct{}{}
	}

	// Compute skipped nodes (by name)
	// Skip branch condition pseudo-nodes (e.g., "branch1.0", "branch1.1") which are routing points, not actual nodes
	vm.mu.Lock()
	skipped := make([]string, 0, len(vm.TaskNodes))
	for nodeID, n := range vm.TaskNodes {
		if n == nil {
			continue
		}
		// Skip branch condition nodes (they have IDs like "nodeId.conditionId")
		// These are routing/edge nodes, not actual executable nodes
		if strings.Contains(nodeID, ".") {
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
			// Determine simulation mode strictly from node config (source of truth from client request)
			// No fallbacks. If missing, treat as real execution (isSimulated=false).
			isSimulated := false
			if st.GetConfig() != nil {
				if cfgMap, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
					if sim, ok := cfgMap["isSimulated"]; ok {
						switch v := sim.(type) {
						case bool:
							isSimulated = v
						case string:
							isSimulated = strings.EqualFold(strings.TrimSpace(v), "true")
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

	// Build "What Executed Successfully" section with checkmarks for successful contract writes
	successfulStepsOverview := buildStepsOverview(vm)

	// Build "What Went Wrong" section for failed steps
	failedStepsOverview := buildFailedStepsOverview(vm)

	// Count failed nodes and check if there are any failures
	failedNodeCount := 0
	hasFailures := false
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			hasFailures = true
			failedNodeCount++
		}
	}

	// Calculate total workflow steps consistently with vm_runner_rest.go
	// Use executedSteps + skippedCount instead of getTotalWorkflowSteps(vm)
	// because vm.TaskNodes includes nodes from ALL branch paths, not just the executed path
	executedSteps := len(vm.ExecutionLogs)
	skippedCount := len(skipped)
	totalWorkflowSteps := executedSteps
	if skippedCount > 0 {
		totalWorkflowSteps = executedSteps + skippedCount
	}

	// If there are no branches, no successful steps, and no failures, return empty
	if len(branches) == 0 && strings.TrimSpace(successfulStepsOverview) == "" && !hasFailures {
		return "", ""
	}

	// Compose plain text
	var tb []string
	// Add Summary heading and one-line narrative
	tb = append(tb, "Summary")
	summaryLine := ""
	if hasFailures {
		summaryLine = fmt.Sprintf("%d out of %d nodes failed during execution.", failedNodeCount, totalWorkflowSteps)
	} else if len(skipped) > 0 {
		summaryLine = "The workflow did not fully execute. Some nodes were skipped due to branching conditions."
	} else {
		summaryLine = "All nodes were executed successfully."
	}
	tb = append(tb, summaryLine)

	// On-chain clause on separate line
	onchainLine := ""
	if successfulRealWrites > 0 {
		if successfulSimulatedWrites > 0 {
			// Both real and simulated transactions
			onchainLine = fmt.Sprintf("Executed %d on-chain transaction(s) and %d simulated transaction(s).", successfulRealWrites, successfulSimulatedWrites)
		} else {
			// Only real transactions
			onchainLine = fmt.Sprintf("Executed %d on-chain transaction(s).", successfulRealWrites)
		}
	} else if successfulSimulatedWrites > 0 {
		// Include simulated node names with proper grammar
		if len(simulatedWriteNames) > 0 {
			nodeList := FormatStringListWithAnd(simulatedWriteNames)
			onchainLine = fmt.Sprintf("No on-chain transactions were sent, as %s ran in simulation mode.", nodeList)
		} else {
			onchainLine = "No on-chain transactions were sent, as all contract write nodes ran in simulation mode."
		}
	} else if failedWrites > 0 {
		onchainLine = "No on-chain transactions executed."
	} else if successfulRealWrites == 0 && successfulSimulatedWrites == 0 {
		onchainLine = "No on-chain transactions executed."
	}
	if onchainLine != "" {
		tb = append(tb, onchainLine)
	}

	// Add "What Went Wrong" section if there are failures
	if strings.TrimSpace(failedStepsOverview) != "" {
		tb = append(tb, "What Went Wrong")
		// Split by newlines to add each step as a separate line
		steps := strings.Split(failedStepsOverview, "\n")
		for _, step := range steps {
			if strings.TrimSpace(step) != "" {
				tb = append(tb, step)
			}
		}
	}

	// Add "What Executed Successfully" section if there are successful contract writes
	if strings.TrimSpace(successfulStepsOverview) != "" {
		tb = append(tb, "What Executed Successfully")
		// Split by newlines to add each step as a separate line
		steps := strings.Split(successfulStepsOverview, "\n")
		for _, step := range steps {
			if strings.TrimSpace(step) != "" {
				tb = append(tb, step)
			}
		}
	}

	if len(skipped) > 0 {
		tb = append(tb, "") // blank line before Skipped nodes
		tb = append(tb, "The below nodes were skipped due to branching conditions")
		for _, n := range skipped {
			tb = append(tb, "- "+n)
		}
	}
	// Only show branch details if there are skipped nodes (PartialSuccess scenario)
	if len(skipped) > 0 {
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
	}
	text := strings.Join(tb, "\n")

	// Compose minimal HTML (tight spacing, deterministic)
	var hb []string
	// Summary heading and sentence
	hb = append(hb, "<div style=\"font-weight:600; margin:8px 0 4px\">Summary</div>")
	hb = append(hb, "<p style=\"margin:0 0 4px\">"+html.EscapeString(summaryLine)+"</p>")
	// On-chain line on separate paragraph
	if onchainLine != "" {
		hb = append(hb, "<p style=\"margin:0 0 8px\">"+html.EscapeString(onchainLine)+"</p>")
	}

	// Add "What Went Wrong" section if there are failures
	if strings.TrimSpace(failedStepsOverview) != "" {
		hb = append(hb, "<div style=\"font-weight:600; margin:8px 0 4px\">What Went Wrong</div>")
		// Split by newlines to add each step as a separate paragraph
		steps := strings.Split(failedStepsOverview, "\n")
		for _, step := range steps {
			if strings.TrimSpace(step) != "" {
				// Escape HTML but preserve the X mark character
				escapedStep := html.EscapeString(step)
				hb = append(hb, "<p style=\"margin:0 0 4px\">"+escapedStep+"</p>")
			}
		}
	}

	// Add "What Executed Successfully" section if there are successful contract writes
	if strings.TrimSpace(successfulStepsOverview) != "" {
		hb = append(hb, "<div style=\"font-weight:600; margin:8px 0 4px\">What Executed Successfully</div>")
		// Split by newlines to add each step as a separate paragraph
		steps := strings.Split(successfulStepsOverview, "\n")
		for _, step := range steps {
			if strings.TrimSpace(step) != "" {
				// Escape HTML but preserve the checkmark character
				escapedStep := html.EscapeString(step)
				hb = append(hb, "<p style=\"margin:0 0 4px\">"+escapedStep+"</p>")
			}
		}
	}

	// Spacer before Skipped nodes (only if there are skipped nodes or branches)
	if len(skipped) > 0 || len(branches) > 0 {
		hb = append(hb, "<div style=\"height:8px\"></div>")
	}
	if len(skipped) > 0 {
		hb = append(hb, "<div style=\"font-weight:600; margin:8px 0 4px\">The below nodes were skipped due to branching conditions</div>")
		hb = append(hb, "<ul style=\"margin:0 0 12px 20px; padding:0\">")
		for _, n := range skipped {
			hb = append(hb, "<li>"+html.EscapeString(n)+"</li>")
		}
		hb = append(hb, "</ul>")
	}
	// Only show branch details if there are skipped nodes
	if len(skipped) > 0 {
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
						// Condition that was selected - show comparison operands if available (like false conditions)
						if strings.TrimSpace(expr) != "" {
							hb = append(hb, fmt.Sprintf("<p style=\"margin:4px 0 0 0; font-weight:500\">%s condition (selected)</p>", html.EscapeString(label)))

							// Check if we have structured comparison operand data (same as false condition logic)
							operandData, hasOperandData := eval["variableValues"].(map[string]interface{})
							if hasOperandData {
								leftExpr, hasLeft := operandData["leftExpr"].(string)
								rightExpr, hasRight := operandData["rightExpr"].(string)
								operator, hasOp := operandData["operator"].(string)
								if hasLeft && hasRight && hasOp && leftExpr != "" && rightExpr != "" && operator != "" {
									// This is comparison operand data - format it using shared formatter
									comparisonHTML := formatComparisonForHTML(operandData)
									if comparisonHTML != "" {
										hb = append(hb, comparisonHTML)
									} else {
										// Fallback to showing expression
										hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 8px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
									}
								} else {
									// Not comparison data - show expression
									hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 8px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
								}
							} else {
								// No operand data - just show expression
								hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 8px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
							}
						}
					} else {
						// False condition - show comparison operands if available, otherwise fall back to expression
						if strings.TrimSpace(expr) == "" {
							hb = append(hb, fmt.Sprintf("<p style=\"margin:4px 0 0 0\">%s condition resolved to false (empty)</p>", html.EscapeString(label)))
						} else {
							hb = append(hb, fmt.Sprintf("<p style=\"margin:4px 0 0 0\">%s condition resolved to false</p>", html.EscapeString(label)))

							// Check if we have structured comparison operand data (from consolidated parser)
							// Comparison operand data has leftExpr, rightExpr, operator keys
							operandData, hasOperandData := eval["variableValues"].(map[string]interface{})
							if hasOperandData {
								leftExpr, hasLeft := operandData["leftExpr"].(string)
								rightExpr, hasRight := operandData["rightExpr"].(string)
								operator, hasOp := operandData["operator"].(string)
								if hasLeft && hasRight && hasOp && leftExpr != "" && rightExpr != "" && operator != "" {
									// This is comparison operand data - format it using shared formatter
									comparisonHTML := formatComparisonForHTML(operandData)
									if comparisonHTML != "" {
										hb = append(hb, comparisonHTML)
									} else {
										// Fallback to showing expression
										hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 2px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
									}
								} else {
									// Not comparison data - show expression
									hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 2px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
								}
							} else {
								// No operand data - just show expression
								hb = append(hb, fmt.Sprintf("<p style=\"margin:0 0 2px 20px; font-family:monospace; font-size:0.9em\">Expression: %s</p>", html.EscapeString(expr)))
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
// Preserves safe HTML tags (<strong>, <em>, <br/>, etc.) while escaping potentially dangerous content
//
// SECURITY NOTE: This function uses a simple string replacement approach which has limitations.
// It does not validate HTML attributes, so tags like <strong onclick="..."> would pass through.
// For production use with untrusted input, consider using a proper HTML sanitizer library like
// github.com/microcosm-cc/bluemonday. This current implementation is acceptable for trusted
// AI-generated content from context-memory or OpenAI, but should be reviewed if processing
// user-provided content.
func buildBareHTMLFromText(body string) string {
	// Normalize newlines first
	normalized := strings.ReplaceAll(body, "\r\n", "\n")

	// Check if body already contains HTML tags (from AI summaries)
	// If it does, preserve safe HTML tags and only escape unsafe content
	// Note: This check could be fooled by legitimate text containing comparison operators
	hasHTML := strings.Contains(normalized, "<") && strings.Contains(normalized, ">")

	if hasHTML {
		// Body already contains HTML - preserve safe tags and escape only unsafe content
		// First, temporarily replace safe HTML tags with placeholders
		safeTags := map[string]string{
			"<strong>":  "___STRONG_OPEN___",
			"</strong>": "___STRONG_CLOSE___",
			"<em>":      "___EM_OPEN___",
			"</em>":     "___EM_CLOSE___",
			"<br/>":     "___BR___",
			"<br>":      "___BR___",
		}

		// Replace safe tags with placeholders
		withPlaceholders := normalized
		for tag, placeholder := range safeTags {
			withPlaceholders = strings.ReplaceAll(withPlaceholders, tag, placeholder)
		}

		// Escape all remaining HTML (potentially dangerous)
		safe := html.EscapeString(withPlaceholders)

		// Restore safe tags
		for tag, placeholder := range safeTags {
			safe = strings.ReplaceAll(safe, placeholder, tag)
		}

		normalized = safe
	} else {
		// Plain text - escape everything
		normalized = html.EscapeString(normalized)
	}

	// Split by paragraphs (double newline)
	parts := strings.Split(normalized, "\n\n")
	var paragraphs []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// Convert single newlines within a paragraph to <br/> (only if not already HTML)
		if !hasHTML {
			p = strings.ReplaceAll(p, "\n", "<br/>")
		}
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
	singleNode := isSingleNodeImmediate(vm)

	var subject string
	if singleNode {
		if failed {
			subject = fmt.Sprintf("Run Node: %s failed at %s", workflowName, safeName(failedName))
		} else {
			subject = fmt.Sprintf("Run Node: %s succeeded", workflowName)
		}
	} else {
		subject = fmt.Sprintf("%s: succeeded (%d out of %d steps)", workflowName, executedSteps, totalWorkflowSteps)
		if failed {
			subject = fmt.Sprintf("%s: failed at %s (%d out of %d steps)", workflowName, failedName, executedSteps, totalWorkflowSteps)
		}
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
		// Failure body: include runner/owner, what executed successfully, and what didn't run
		successfulSteps := buildStepsOverview(vm)

		if strings.TrimSpace(successfulSteps) != "" {
			// Include steps that succeeded before failure
			body = fmt.Sprintf(
				"Smart wallet %s (owner %s) started workflow execution but encountered a failure.\n\n<strong>What Executed Successfully</strong>\n%s\n\n<strong>What Didn't Run</strong>\nFailed at step '%s': %s",
				smartWallet, ownerEOA, successfulSteps, safeName(failedName), firstLine(failedReason),
			)
		} else {
			// No successful steps before failure
			body = fmt.Sprintf(
				"Smart wallet %s (owner %s) started workflow execution but encountered a failure.\n\nNo on-chain contract writes were completed before the failure.\n\n<strong>What Didn't Run</strong>\nFailed at step '%s': %s",
				smartWallet, ownerEOA, safeName(failedName), firstLine(failedReason),
			)
		}
	} else {
		// Success body with What Executed Successfully format (matching AI summarizer output)
		chainName := resolveChainName(vm)
		actionCount := countActionsInLogs(vm)

		// Build What Executed Successfully section with checkmarks for each successful contract write
		successfulSteps := buildStepsOverview(vm)

		if singleNode {
			body = composeSingleNodeSuccessBody(vm, smartWallet, ownerEOA, chainName, successfulSteps, actionLines, actionCount, currentStepName)
		} else {
			if strings.TrimSpace(successfulSteps) != "" {
				body = fmt.Sprintf(
					"Smart wallet %s (owner %s) executed %d on-chain actions.\n\n<strong>What Executed Successfully</strong>\n%s\n\nAll steps completed on %s.",
					smartWallet, ownerEOA, actionCount, successfulSteps, chainName,
				)
			} else {
				// Fallback when no contract writes are found
				// Include workflow name and step names for better context
				stepNames := make([]string, 0)
				for _, st := range vm.ExecutionLogs {
					if st.GetSuccess() && st.GetName() != "" {
						stepNames = append(stepNames, st.GetName())
					}
				}
				stepInfo := ""
				if len(stepNames) > 0 {
					stepInfo = fmt.Sprintf(" Steps executed: %s.", strings.Join(stepNames, ", "))
				}
				body = fmt.Sprintf(
					"Smart wallet %s (owner %s) completed workflow execution for '%s'.\n\nNo on-chain contract writes were recorded. This may have been a read-only workflow or all steps were simulated.%s\n\nAll steps completed on %s.",
					smartWallet, ownerEOA, workflowName, stepInfo, chainName,
				)
			}
		}
	}

	if !failed {
		if narrative := strings.TrimSpace(buildNarrativeFromLogs(vm)); narrative != "" {
			if strings.TrimSpace(body) != "" {
				body += "\n\n" + narrative
			} else {
				body = narrative
			}
		}
	}

	return Summary{
		Subject: subject,
		Body:    body,
	}
}

func composeSingleNodeSuccessBody(vm *VM, smartWallet, ownerEOA, chainName string, successfulSteps string, actionLines []string, actionCount int, fallbackStepName string) string {
	trimmedSteps := strings.TrimSpace(successfulSteps)
	if trimmedSteps != "" {
		return fmt.Sprintf(
			"Smart wallet %s (owner %s) executed %d on-chain action(s).\n\n<strong>What Executed Successfully</strong>\n%s\n\nAll steps completed on %s.",
			displayOrUnknown(smartWallet), displayOrUnknown(ownerEOA), actionCount, trimmedSteps, chainName,
		)
	}

	if summary := buildSingleNodeRestSummary(vm, smartWallet, ownerEOA, chainName, fallbackStepName); summary != "" {
		return summary
	}

	if len(actionLines) > 0 {
		return fmt.Sprintf(
			"Smart wallet %s (owner %s) executed the node with the following activity:\n\n%s\n\nAll steps completed on %s.",
			displayOrUnknown(smartWallet),
			displayOrUnknown(ownerEOA),
			strings.Join(actionLines, "\n"),
			chainName,
		)
	}

	description := "node execution"
	if strings.TrimSpace(fallbackStepName) != "" {
		description = fmt.Sprintf("the '%s' node", safeName(fallbackStepName))
	}

	return fmt.Sprintf(
		"Smart wallet %s (owner %s) completed %s.\n\nAll steps completed on %s.",
		displayOrUnknown(smartWallet),
		displayOrUnknown(ownerEOA),
		description,
		chainName,
	)
}

func buildSingleNodeRestSummary(vm *VM, smartWallet, ownerEOA, chainName string, fallbackStepName string) string {
	step := findLastSuccessStep(vm)
	if step == nil {
		return ""
	}

	stepType := strings.ToUpper(step.GetType())
	if !strings.Contains(stepType, "REST_API") {
		return ""
	}

	status, statusText, requestURL := extractRestStepInfo(step)

	statusSummary := ""
	if status != 0 {
		statusSummary = fmt.Sprintf("HTTP %d", status)
		if strings.TrimSpace(statusText) != "" {
			statusSummary += " " + strings.TrimSpace(statusText)
		}
	} else if strings.TrimSpace(statusText) != "" {
		statusSummary = strings.TrimSpace(statusText)
	} else {
		statusSummary = "Notification provider responded"
	}

	if strings.TrimSpace(requestURL) != "" {
		provider := providerDisplayName(detectNotificationProvider(requestURL))
		if provider == "" {
			provider = shortURLHost(requestURL)
		}
		if provider != "" {
			statusSummary += fmt.Sprintf(" from %s", provider)
		}
	}

	stepName := step.GetName()
	if strings.TrimSpace(stepName) == "" {
		stepName = fallbackStepName
	}
	stepName = safeName(stepName)
	return fmt.Sprintf(
		"Smart wallet %s (owner %s) executed the '%s' REST API node.\n\n%s.\n\nAll steps completed on %s.",
		displayOrUnknown(smartWallet),
		displayOrUnknown(ownerEOA),
		stepName,
		statusSummary,
		chainName,
	)
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

func extractRestStepInfo(step *avsproto.Execution_Step) (int, string, string) {
	if step == nil {
		return 0, "", ""
	}
	rest := step.GetRestApi()
	if rest == nil || rest.Data == nil {
		return 0, "", ""
	}

	if dataMap, ok := rest.Data.AsInterface().(map[string]interface{}); ok {
		status := convertInterfaceToInt(dataMap["status"])
		statusText := asString(dataMap["statusText"])
		requestURL := asString(dataMap["url"])
		return status, statusText, requestURL
	}

	return 0, "", ""
}

func convertInterfaceToInt(val interface{}) int {
	switch v := val.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return 0
}

func providerDisplayName(provider string) string {
	switch strings.ToLower(provider) {
	case "sendgrid":
		return "SendGrid"
	case "telegram":
		return "Telegram"
	default:
		return ""
	}
}

func shortURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := parsed.Host
	if host == "" {
		return raw
	}
	path := strings.Trim(parsed.Path, "/")
	if path != "" {
		host = host + "/" + path
	}
	if len(host) > 64 {
		return host[:61] + "..."
	}
	return host
}

func displayOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

// getTotalWorkflowSteps returns the total number of steps in a workflow,
// including the trigger and all nodes. This represents the full workflow size,
// not just the executed steps so far.
// If currentNodeName is provided, notification nodes (email/telegram) are excluded
// since they are the source of the summary, not part of the workflow logic.
func getTotalWorkflowSteps(vm *VM, currentNodeName ...string) int {
	if vm == nil {
		return 0
	}

	// For single-node immediate execution (testing), return 1
	if isSingleNodeImmediate(vm) {
		return 1
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Count all nodes, excluding notification nodes (email/telegram REST API)
	nodeCount := 0
	for _, node := range vm.TaskNodes {
		if node == nil {
			continue
		}
		// Check if this is a notification node (REST API with sendgrid/telegram URL)
		if isNotificationNode(node) {
			continue
		}
		nodeCount++
	}

	// Count: 1 trigger + non-notification nodes
	totalSteps := 1 + nodeCount

	return totalSteps
}

// isNotificationNode checks if a task node is a notification endpoint (email/telegram)
func isNotificationNode(node *avsproto.TaskNode) bool {
	if node == nil {
		return false
	}
	// Check if it's a REST API node
	restAPI := node.GetRestApi()
	if restAPI == nil || restAPI.Config == nil {
		return false
	}
	// Check URL for notification providers
	url := strings.ToLower(restAPI.Config.Url)
	method := strings.ToUpper(restAPI.Config.Method)

	// Only consider POST requests as notifications (GET requests are typically data queries)
	// This helps avoid incorrectly classifying legitimate API nodes that query these services
	if method != "POST" && method != "" {
		return false
	}

	// SendGrid
	if strings.Contains(url, "sendgrid") || strings.Contains(url, "/mail/send") {
		return true
	}
	// Telegram
	if strings.Contains(url, "telegram") || strings.Contains(url, "api.telegram.org") {
		return true
	}
	return false
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

// buildStepsOverview generates a checkmark-prefixed list of successful contract write steps
func buildStepsOverview(vm *VM) string {
	if vm == nil {
		return ""
	}

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
		// Debug logging
		if vm.logger != nil {
			vm.logger.Debug("buildStepsOverview: token context", "token0Sym", token0Sym, "token1Sym", token1Sym, "routerAddr", routerAddr)
		}
	}

	// Helper function to detect contract name from address
	detectContractName := func(addr string) string {
		if addr == "" {
			return ""
		}
		// Normalize addresses for comparison
		addrLower := strings.ToLower(strings.TrimSpace(addr))
		if routerAddr != "" && strings.ToLower(strings.TrimSpace(routerAddr)) == addrLower {
			return "Uniswap V3 router"
		}
		return ""
	}

	var steps []string
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}

		t := strings.ToUpper(st.GetType())
		if !strings.Contains(t, "CONTRACT_WRITE") {
			continue
		}

		// Check if this is a simulated transaction
		// Use ExecutionContext (actual execution mode) instead of Config (node configuration)
		isSimulated := false
		if st.GetExecutionContext() != nil {
			if ctx, ok := st.GetExecutionContext().AsInterface().(map[string]interface{}); ok {
				if sim, ok := ctx["isSimulated"]; ok {
					switch v := sim.(type) {
					case bool:
						isSimulated = v
					case string:
						isSimulated = strings.EqualFold(strings.TrimSpace(v), "true")
					}
				}
			}
		}

		// Extract methodName and contractAddr from Config (still needed for description generation)
		methodName := ""
		contractAddr := ""
		if st.GetConfig() != nil {
			if cfg, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
				if addr, ok := cfg["contractAddress"].(string); ok {
					contractAddr = addr
				}
				if mcs, ok := cfg["methodCalls"].([]interface{}); ok && len(mcs) > 0 {
					if call, ok := mcs[0].(map[string]interface{}); ok {
						if mn, ok := call["methodName"].(string); ok {
							methodName = strings.ToLower(mn)
						}
					}
				}
			}
		}

		// Generate description based on method type
		description := ""
		switch methodName {
		case "approve":
			value := ""
			spender := ""
			tokenAddr := ""
			if st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
				if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
					// Check for "Approval" event (capital A) first
					if ev, ok := m["Approval"].(map[string]interface{}); ok {
						if v, ok := ev["value"].(string); ok {
							value = v
						}
						if sp, ok := ev["spender"].(string); ok {
							spender = sp
						}
						// Get token address from owner field in Approval event
						if owner, ok := ev["owner"].(string); ok && owner != "" {
							tokenAddr = owner
						}
					} else if ev, ok := m["approve"].(map[string]interface{}); ok {
						// Fallback to "approve" output data (lowercase) if event not found
						if v, ok := ev["value"].(string); ok {
							value = v
						}
						if sp, ok := ev["spender"].(string); ok {
							spender = sp
						}
						// Get token address from owner field in approve output
						if owner, ok := ev["owner"].(string); ok && owner != "" {
							tokenAddr = owner
						}
					}
				}
			}
			// Use contractAddr as token address if not found in event, but skip if it's a template variable
			if tokenAddr == "" && contractAddr != "" && !strings.HasPrefix(contractAddr, "{{") {
				tokenAddr = contractAddr
			}
			// Fallback to routerAddr if spender not found in event
			if spender == "" {
				spender = routerAddr // Use router address as default spender
			}
			// Debug logging
			if vm.logger != nil {
				vm.logger.Debug("buildStepsOverview: approve step", "contractAddr", contractAddr, "spender", spender, "value", value, "token1Sym", token1Sym, "token1Dec", token1Dec)
			}
			// Detect contract name
			contractName := detectContractName(spender)
			spenderDisplay := contractName
			if spenderDisplay == "" {
				spenderDisplay = shortHexAddr(spender)
			} else {
				spenderDisplay = spenderDisplay + " " + shortHexAddr(spender)
			}
			// Format amount with token symbol
			if value != "" && token1Sym != "" {
				formattedAmount := formatAmount(value, token1Dec)
				description = fmt.Sprintf("Approved %s %s to %s", formattedAmount, token1Sym, spenderDisplay)
			} else if value != "" {
				description = fmt.Sprintf("Approved %s to %s", value, spenderDisplay)
			} else if token1Sym != "" {
				description = fmt.Sprintf("Approved %s to %s", token1Sym, spenderDisplay)
			} else {
				description = fmt.Sprintf("Approved token to %s", spenderDisplay)
			}

		case "exactinputsingle":
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
			// Also check return value from contract write output
			if amountOut == "" && st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
				if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
					if exactInput, ok := m["exactInputSingle"].(map[string]interface{}); ok {
						if amt, ok := exactInput["amountOut"].(string); ok {
							amountOut = amt
						}
					}
				}
			}
			// Format amountOut with token symbol and decimal formatting
			if amountOut != "" {
				formattedAmount := formatAmount(amountOut, token0Dec)
				if vm.logger != nil {
					vm.logger.Info("buildStepsOverview: formatting amountOut", "amountOut", amountOut, "token0Sym", token0Sym, "token0Dec", token0Dec, "formattedAmount", formattedAmount)
				}
				if token0Sym != "" {
					description = fmt.Sprintf("Swapped for ~%s %s via Uniswap V3 (exactInputSingle)", formattedAmount, token0Sym)
				} else {
					// Fallback: try to format without symbol if we have decimals
					description = fmt.Sprintf("Swapped for ~%s via Uniswap V3 (exactInputSingle)", formattedAmount)
				}
			} else {
				description = "Swapped via Uniswap V3 (exactInputSingle)"
			}
			// Debug logging
			if vm.logger != nil {
				vm.logger.Debug("buildStepsOverview: exactInputSingle step", "amountOut", amountOut, "token0Sym", token0Sym, "token0Dec", token0Dec, "isSimulated", isSimulated)
			}

		case "transfer":
			if st.GetContractWrite() != nil && st.GetContractWrite().Data != nil {
				if m, ok := st.GetContractWrite().Data.AsInterface().(map[string]interface{}); ok {
					if ev, ok := m["Transfer"].(map[string]interface{}); ok {
						value := ""
						to := ""
						if v, ok := ev["value"].(string); ok {
							value = v
						}
						if t, ok := ev["to"].(string); ok {
							to = t
						}
						if value != "" && to != "" {
							description = fmt.Sprintf("Transferred %s to %s for settlement", value, shortHexAddr(to))
						}
					}
				}
			}
			if description == "" {
				description = fmt.Sprintf("Transferred to %s", shortHexAddr(contractAddr))
			}

		default:
			// Generic description for other methods
			description = fmt.Sprintf("Called %s on %s", methodName, shortHexAddr(contractAddr))
		}

		// Add "(Simulated)" prefix if this is a simulated transaction
		if isSimulated {
			description = "(Simulated) " + description
			if vm.logger != nil {
				vm.logger.Info("buildStepsOverview: added Simulated prefix", "methodName", methodName, "description", description)
			}
		}

		// Add checkmark prefix
		steps = append(steps, fmt.Sprintf("✓ %s", description))
	}

	return strings.Join(steps, "\n")
}

// buildFailedStepsOverview generates an X-prefixed list of failed steps with error messages
func buildFailedStepsOverview(vm *VM) string {
	if vm == nil {
		return ""
	}

	var steps []string
	for _, st := range vm.ExecutionLogs {
		if st.GetSuccess() {
			continue // Skip successful steps
		}

		name := st.GetName()
		if name == "" {
			name = st.GetId()
		}
		if name == "" {
			continue
		}

		errorMsg := st.GetError()
		if errorMsg == "" {
			errorMsg = "unknown error"
		}

		// Format: "✗ StepName: error message"
		description := fmt.Sprintf("✗ %s: %s", safeName(name), firstLine(errorMsg))
		steps = append(steps, description)
	}

	return strings.Join(steps, "\n")
}

// shortHexAddr formats an address as 0xABCD...WXYZ for compact display
func shortHexAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "0x") && len(addr) > 10 {
		return addr[:6] + "..." + addr[len(addr)-4:]
	}
	return addr
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

func findLastSuccessStep(vm *VM) *avsproto.Execution_Step {
	if vm == nil {
		return nil
	}
	steps := vm.ExecutionLogs
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].GetSuccess() {
			return steps[i]
		}
	}
	return nil
}

func findLastSuccessStepName(vm *VM) string {
	step := findLastSuccessStep(vm)
	if step == nil {
		return ""
	}
	name := step.GetName()
	if name == "" {
		name = step.GetId()
	}
	return safeName(name)
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
		"<p style=\"margin:0 0 2px 20px; font-family:monospace; font-size:0.9em\">Expression: %s %s %s</p>"+
			"<p style=\"margin:0 0 8px 20px; font-family:monospace; font-size:0.9em\">Evaluated: %s %s %s</p>",
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
