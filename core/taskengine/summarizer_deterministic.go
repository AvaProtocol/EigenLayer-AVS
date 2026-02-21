package taskengine

import (
	"fmt"
	"html"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TransferInfo contains structured data for a token transfer
type TransferInfo struct {
	StepName    string `json:"stepName"`    // Step name that produced this transfer
	Type        string `json:"type"`        // Transfer type: "eth_transfer", "erc20_transfer", "contract_write"
	From        string `json:"from"`        // Sender address
	To          string `json:"to"`          // Recipient address
	RawAmount   string `json:"rawAmount"`   // Raw amount (wei/smallest unit)
	Amount      string `json:"amount"`      // Formatted amount (human-readable)
	Symbol      string `json:"symbol"`      // Token symbol (e.g., "ETH", "USDC")
	Decimals    int    `json:"decimals"`    // Token decimals
	TxHash      string `json:"txHash"`      // Transaction hash (empty if simulated)
	IsSimulated bool   `json:"isSimulated"` // Was this simulated?
}

// BalanceInfo contains structured data for a balance snapshot
type BalanceInfo struct {
	StepName         string `json:"stepName"`         // Step name that produced this balance
	TokenAddress     string `json:"tokenAddress"`     // Token contract address
	Symbol           string `json:"symbol"`           // Token symbol
	Name             string `json:"name"`             // Token name
	Balance          string `json:"balance"`          // Raw balance
	BalanceFormatted string `json:"balanceFormatted"` // Formatted balance
	Decimals         int    `json:"decimals"`         // Token decimals
}

// WorkflowInfo contains metadata about the workflow execution
type WorkflowInfo struct {
	Name         string `json:"name"`         // Workflow name
	Chain        string `json:"chain"`        // Chain name (e.g., "Sepolia")
	ChainID      int64  `json:"chainId"`      // Chain ID
	IsSimulation bool   `json:"isSimulation"` // Was this a simulation?
	RunNumber    *int64 `json:"runNumber"`    // Run number (nil for simulations)
}

// Summary represents composed notification content
// ExecutionEntry represents a single on-chain execution step with an optional transaction hash.
// When TxHash is non-empty, formatters render it as a clickable explorer link.
type ExecutionEntry struct {
	Description string // Human-readable execution description
	TxHash      string // On-chain transaction hash (empty for non-tx steps)
}

type Summary struct {
	Subject     string
	Body        string
	SummaryLine string // One-liner summary (e.g., "Your workflow 'Test Stoploss' executed 7 out of 7 total steps")
	Status      string // Execution status: "success", "partial_success", "failure"

	// Structured fields for rendering notifications
	Trigger     string           // What triggered the workflow (text description)
	TriggeredAt string           // ISO 8601 timestamp (from trigger output)
	Executions  []ExecutionEntry // On-chain operation descriptions with optional tx hashes
	Errors      []string         // Failed steps and skipped node descriptions
	SmartWallet string           // Smart wallet address that executed the workflow
	Network     string           // Chain name (e.g., "Sepolia", "Ethereum") - from body.network
	Annotation  string           // Optional italic footnote (e.g., run-node disclaimer)

	// Enhanced structured data for rich notifications (kept for potential future use)
	Transfers []TransferInfo // Transfer details from ETH_TRANSFER and CONTRACT_WRITE steps
	Balances  []BalanceInfo  // Balance snapshots from BALANCE steps
	Workflow  *WorkflowInfo  // Workflow metadata
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

	// Compose minimal HTML (improved spacing for readability)
	var hb []string
	// Summary heading and sentence
	hb = append(hb, "<div style=\"font-weight:600; margin:16px 0 8px\">Summary</div>")
	hb = append(hb, "<p style=\"margin:8px 0\">"+html.EscapeString(summaryLine)+"</p>")
	// On-chain line on separate paragraph
	if onchainLine != "" {
		hb = append(hb, "<p style=\"margin:8px 0 12px\">"+html.EscapeString(onchainLine)+"</p>")
	}

	// Add "What Went Wrong" section if there are failures
	if strings.TrimSpace(failedStepsOverview) != "" {
		hb = append(hb, "<div style=\"font-weight:600; margin:16px 0 8px\">What Went Wrong</div>")
		// Split by newlines to add each step as a separate paragraph
		steps := strings.Split(failedStepsOverview, "\n")
		for _, step := range steps {
			if strings.TrimSpace(step) != "" {
				// Escape HTML but preserve the X mark character
				escapedStep := html.EscapeString(step)
				hb = append(hb, "<p style=\"margin:8px 0\">"+escapedStep+"</p>")
			}
		}
	}

	// Add "What Executed Successfully" section if there are successful contract writes
	if strings.TrimSpace(successfulStepsOverview) != "" {
		hb = append(hb, "<div style=\"font-weight:600; margin:16px 0 8px\">What Executed Successfully</div>")
		// Split by newlines to add each step as a separate paragraph
		steps := strings.Split(successfulStepsOverview, "\n")
		for _, step := range steps {
			if strings.TrimSpace(step) != "" {
				// Escape HTML but preserve the checkmark character
				escapedStep := html.EscapeString(step)
				hb = append(hb, "<p style=\"margin:8px 0\">"+escapedStep+"</p>")
			}
		}
	}

	// Spacer before Skipped nodes (only if there are skipped nodes or branches)
	if len(skipped) > 0 || len(branches) > 0 {
		hb = append(hb, "<div style=\"height:12px\"></div>")
	}
	if len(skipped) > 0 {
		hb = append(hb, "<div style=\"font-weight:600; margin:16px 0 8px\">The below nodes were skipped due to branching conditions</div>")
		hb = append(hb, "<ul style=\"margin:0 0 16px 20px; padding:0; line-height:1.6\">")
		for _, n := range skipped {
			hb = append(hb, "<li style=\"margin:4px 0\">"+html.EscapeString(n)+"</li>")
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
			hb = append(hb, fmt.Sprintf("<div style=\"font-weight:600; margin:16px 0 8px\">%s</div>", html.EscapeString(headerText)))

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

// computeExecutionStatus determines the workflow execution status
// Returns: "success", "partial_success", or "failure"
func computeExecutionStatus(vm *VM) string {
	if vm == nil {
		return "failure"
	}

	// Check for any failed step
	hasFailed := false
	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			hasFailed = true
			break
		}
	}

	if hasFailed {
		return "failure"
	}

	// Check for skipped nodes (partial success)
	vm.mu.Lock()
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
	skippedCount := 0
	for nodeID, n := range vm.TaskNodes {
		if n == nil {
			continue
		}
		// Skip branch condition nodes
		if strings.Contains(nodeID, ".") {
			continue
		}
		// Exclude notification nodes
		if isNotificationNode(n) {
			continue
		}
		if _, ok := executed[n.Name]; !ok {
			skippedCount++
		}
	}
	vm.mu.Unlock()

	if skippedCount > 0 {
		return "partial_success"
	}

	return "success"
}

// extractTriggerInfo extracts the trigger description and timestamp from ExecutionLogs
// Returns (triggerDescription, triggeredAtISO8601)
func extractTriggerInfo(vm *VM) (triggerDesc, triggeredAt string) {
	if vm == nil {
		return "", ""
	}

	// Find the trigger step (first step with TRIGGER in type)
	for _, st := range vm.ExecutionLogs {
		stepType := strings.ToUpper(st.GetType())
		if !strings.Contains(stepType, "TRIGGER") {
			continue
		}

		// Extract timestamp from trigger output
		if st.GetStartAt() > 0 {
			triggeredAt = time.UnixMilli(st.GetStartAt()).UTC().Format(time.RFC3339)
		}

		// Build trigger description based on trigger type
		triggerDesc = buildTriggerDescription(st, vm)
		return triggerDesc, triggeredAt
	}

	return "", ""
}

// buildTriggerDescription builds a human-readable trigger description
func buildTriggerDescription(st *avsproto.Execution_Step, vm *VM) string {
	stepType := strings.ToUpper(st.GetType())
	isSimulated := vm != nil && vm.IsSimulation
	prefix := ""
	if isSimulated {
		prefix = "(Simulated) "
	}

	chainName := ""
	if vm != nil {
		chainName = resolveChainName(vm)
	}
	chainSuffix := ""
	if chainName != "" {
		chainSuffix = " on " + chainName
	}

	// Event trigger (Transfer, etc.)
	if strings.Contains(stepType, "EVENT") {
		if eventTrigger := st.GetEventTrigger(); eventTrigger != nil && eventTrigger.Data != nil {
			if data, ok := eventTrigger.Data.AsInterface().(map[string]interface{}); ok {
				// Transfer event
				if transfer, ok := data["Transfer"].(map[string]interface{}); ok {
					to := shortHexAddr(fmt.Sprintf("%v", transfer["to"]))
					value := fmt.Sprintf("%v", transfer["value"])
					// TODO: Format value with token decimals when available
					return fmt.Sprintf("%sTransfer event detected: sent %s to %s%s", prefix, value, to, chainSuffix)
				}
			}
		}
		return prefix + "Event trigger activated" + chainSuffix
	}

	// Block trigger
	if strings.Contains(stepType, "BLOCK") {
		if blockTrigger := st.GetBlockTrigger(); blockTrigger != nil && blockTrigger.Data != nil {
			if data, ok := blockTrigger.Data.AsInterface().(map[string]interface{}); ok {
				if blockNum, ok := data["blockNumber"].(float64); ok && blockNum > 0 {
					return fmt.Sprintf("%sBlock %d was mined%s", prefix, int64(blockNum), chainSuffix)
				}
			}
		}
		return prefix + "Block trigger activated" + chainSuffix
	}

	// Cron trigger
	if strings.Contains(stepType, "CRON") {
		return prefix + "Scheduled task ran" + chainSuffix
	}

	// Fixed time trigger
	if strings.Contains(stepType, "FIXED") {
		return prefix + "Scheduled time was reached" + chainSuffix
	}

	// Manual trigger
	if strings.Contains(stepType, "MANUAL") {
		return prefix + "Workflow was manually triggered" + chainSuffix
	}

	return prefix + "Workflow triggered" + chainSuffix
}

// buildExecutionsArray builds an array of on-chain execution entries from successful CONTRACT_WRITE steps
func buildExecutionsArray(vm *VM) []ExecutionEntry {
	if vm == nil {
		return nil
	}

	var executions []ExecutionEntry

	// Get token context for formatting
	vm.mu.Lock()
	var settings map[string]interface{}
	if m, ok := vm.vars["settings"].(map[string]interface{}); ok {
		settings = m
	}
	vm.mu.Unlock()

	token0Sym, token0Dec := "", 18
	token1Sym, token1Dec := "", 18
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
	}

	isSimulated := vm.IsSimulation
	prefix := ""
	if isSimulated {
		prefix = "(Simulated) "
	}

	for _, st := range vm.ExecutionLogs {
		if !st.GetSuccess() {
			continue
		}
		stepType := strings.ToUpper(st.GetType())
		if !strings.Contains(stepType, "CONTRACT_WRITE") {
			continue
		}

		// Extract method name from config
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

		// Build execution description based on method
		desc := buildContractWriteDescription(st, methodName, token0Sym, token0Dec, token1Sym, token1Dec, prefix)
		if desc != "" {
			entry := ExecutionEntry{Description: desc}
			// Only attach txHash for real (non-simulated) steps with valid tx hashes.
			// Simulated steps (Tenderly forks) produce raw BigInt values as "transactionHash"
			// which aren't real tx hashes and would generate broken explorer links.
			if !isStepSimulated(st) && st.GetMetadata() != nil {
				if meta, ok := st.GetMetadata().AsInterface().(map[string]interface{}); ok {
					if txHash, ok := meta["transactionHash"].(string); ok && isValidTxHash(txHash) {
						entry.TxHash = txHash
					}
				} else if metaArr, ok := st.GetMetadata().AsInterface().([]interface{}); ok && len(metaArr) > 0 {
					if first, ok := metaArr[0].(map[string]interface{}); ok {
						if txHash, ok := first["transactionHash"].(string); ok && isValidTxHash(txHash) {
							entry.TxHash = txHash
						}
					}
				}
			}
			executions = append(executions, entry)
		}
	}

	return executions
}

// buildContractWriteDescription builds a human-readable description for a CONTRACT_WRITE step
func buildContractWriteDescription(st *avsproto.Execution_Step, methodName string, token0Sym string, token0Dec int, token1Sym string, token1Dec int, prefix string) string {
	// Extract spender for approve
	if methodName == "approve" {
		spender := ""
		value := ""
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
		if value != "" && token1Sym != "" {
			return fmt.Sprintf("%sApproved %s %s to %s for trading", prefix, formatAmount(value, token1Dec), token1Sym, shortHexAddr(spender))
		}
		return fmt.Sprintf("%sApproved tokens to %s", prefix, shortHexAddr(spender))
	}

	// Swap operations
	if methodName == "exactinputsingle" || methodName == "exactoutputsingle" {
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
		if amountOut != "" && token0Sym != "" {
			return fmt.Sprintf("%sSwapped for ~%s %s via Uniswap V3", prefix, formatAmount(amountOut, token0Dec), token0Sym)
		}
		return prefix + "Swapped tokens via Uniswap V3"
	}

	// Generic contract write
	stepName := st.GetName()
	if stepName == "" {
		stepName = st.GetId()
	}
	if methodName != "" {
		return fmt.Sprintf("%sExecuted %s on %s", prefix, methodName, stepName)
	}
	return fmt.Sprintf("%sExecuted contract write: %s", prefix, stepName)
}

// buildErrorsArray builds an array of error descriptions from failed steps and skipped nodes
func buildErrorsArray(vm *VM, currentStepName string) []string {
	if vm == nil {
		return nil
	}

	var errors []string

	// Add failed steps
	for _, st := range vm.ExecutionLogs {
		if st.GetSuccess() {
			continue
		}
		name := st.GetName()
		if name == "" {
			name = st.GetId()
		}
		errorMsg := st.GetError()
		if errorMsg == "" {
			errorMsg = "unknown error"
		}
		errors = append(errors, fmt.Sprintf("%s: %s", safeName(name), firstLine(errorMsg)))
	}

	// Add skipped nodes
	vm.mu.Lock()
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
	// Include current step if provided
	if currentStepName != "" {
		executed[currentStepName] = struct{}{}
	}

	for nodeID, n := range vm.TaskNodes {
		if n == nil {
			continue
		}
		// Skip branch condition nodes
		if strings.Contains(nodeID, ".") {
			continue
		}
		// Exclude notification nodes from skipped count
		if isNotificationNode(n) {
			continue
		}
		if _, ok := executed[n.Name]; !ok {
			errors = append(errors, fmt.Sprintf("%s - skipped due to branch condition", safeName(n.Name)))
		}
	}
	vm.mu.Unlock()

	return errors
}

// composePlainTextBodyFromStructured composes a plain text body from the structured fields
func composePlainTextBodyFromStructured(trigger string, executions []ExecutionEntry, errors []string, status string) string {
	var sb strings.Builder

	// Trigger
	if trigger != "" {
		sb.WriteString("Trigger: ")
		sb.WriteString(trigger)
		sb.WriteString("\n\n")
	}

	// Executions
	if len(executions) > 0 {
		sb.WriteString("What Executed On-Chain:\n")
		for _, exec := range executions {
			sb.WriteString("✓ ")
			sb.WriteString(exec.Description)
			sb.WriteString("\n")
		}
	}

	// Errors (only for failure status)
	if status == "failure" && len(errors) > 0 {
		if len(executions) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("What Went Wrong:\n")
		for _, err := range errors {
			sb.WriteString("✗ ")
			sb.WriteString(err)
			sb.WriteString("\n")
		}
	}

	return strings.TrimSpace(sb.String())
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

	// Check for skipped nodes (for partial execution detection)
	// Exclude notification nodes (email/telegram) from skipped count, similar to getTotalWorkflowSteps
	vm.mu.Lock()
	skippedCount := 0
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

	// Build a set of notification node names so we can exclude them from
	// both the skipped count and the executed count (matching getTotalWorkflowSteps).
	notificationNames := make(map[string]struct{})
	for nodeID, n := range vm.TaskNodes {
		if n == nil {
			continue
		}
		// Skip branch condition nodes
		if strings.Contains(nodeID, ".") {
			continue
		}
		if isNotificationNode(n) {
			notificationNames[n.Name] = struct{}{}
			continue
		}
		if _, ok := executed[n.Name]; !ok {
			skippedCount++
		}
	}
	vm.mu.Unlock()

	// Count executed steps, excluding notification nodes so the count is
	// consistent with getTotalWorkflowSteps (which also excludes them).
	executedSteps := 0
	for _, st := range vm.ExecutionLogs {
		name := st.GetName()
		if name == "" {
			name = st.GetId()
		}
		if _, isNotif := notificationNames[name]; isNotif {
			continue
		}
		executedSteps++
	}

	// Build subject up-front to support richer failure bodies
	singleNode := isSingleNodeImmediate(vm)

	var subject string
	var summaryLine string

	// singleNode (run_node_immediately) is checked first because it also sets IsSimulation=true,
	// but should always use the "Run Node:" prefix to distinguish from multi-node simulate_task.
	if singleNode {
		// The single node is actively executing when ComposeSummary is called,
		// so count it as executed (ExecutionLogs is empty because the node
		// hasn't finished yet — it's the notification node itself).
		executedSteps = 1

		// run_node_immediately entry - single-node format (regardless of simulation mode)
		if failed {
			subject = fmt.Sprintf("Run Node: %s failed at %s", workflowName, safeName(failedName))
		} else {
			subject = fmt.Sprintf("Run Node: %s succeeded", workflowName)
		}
		// Summary line for single-node workflows
		summaryLine = fmt.Sprintf("Your workflow '%s' executed %d out of %d total steps", workflowName, executedSteps, totalWorkflowSteps)
	} else if vm.IsSimulation {
		// simulate_task entry - multi-node simulation format
		if failed {
			subject = fmt.Sprintf("Simulation: %s failed to execute", workflowName)
		} else if skippedCount > 0 {
			subject = fmt.Sprintf("Simulation: %s partially executed", workflowName)
		} else {
			subject = fmt.Sprintf("Simulation: %s successfully completed", workflowName)
		}
		// Summary line format: "Your workflow 'Test Stoploss' executed 7 out of 7 total steps"
		summaryLine = fmt.Sprintf("Your workflow '%s' executed %d out of %d total steps", workflowName, executedSteps, totalWorkflowSteps)
	} else {
		// For deployed workflows, use the original format
		subject = fmt.Sprintf("%s: succeeded (%d out of %d steps)", workflowName, executedSteps, totalWorkflowSteps)
		if failed {
			subject = fmt.Sprintf("%s: failed at %s (%d out of %d steps)", workflowName, failedName, executedSteps, totalWorkflowSteps)
		}
		// Summary line for deployed workflows
		summaryLine = fmt.Sprintf("Your workflow '%s' executed %d out of %d total steps", workflowName, executedSteps, totalWorkflowSteps)
	}

	annotation := ""
	if singleNode {
		annotation = ExampleExecutionAnnotation
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
		// Failure body: start with summary line to match context-memory API format
		// Then include what executed successfully (if any) and what didn't run
		successfulSteps := buildStepsOverview(vm)

		if strings.TrimSpace(successfulSteps) != "" {
			// Use "What Executed On-Chain" for simulations, "What Executed Successfully" for deployed workflows
			sectionHeading := "What Executed Successfully"
			if vm.IsSimulation {
				sectionHeading = "What Executed On-Chain"
			}
			// Include steps that succeeded before failure
			// Body is plain text (no HTML tags) to match context-memory API format
			body = fmt.Sprintf(
				"%s\n\nSmart wallet %s (owner %s) started workflow execution but encountered a failure.\n\n%s\n%s\n\nWhat Didn't Run\nFailed at step '%s': %s",
				summaryLine, smartWallet, ownerEOA, sectionHeading, successfulSteps, safeName(failedName), firstLine(failedReason),
			)
		} else {
			// No successful steps before failure
			// Body is plain text (no HTML tags) to match context-memory API format
			body = fmt.Sprintf(
				"%s\n\nSmart wallet %s (owner %s) started workflow execution but encountered a failure.\n\nNo on-chain contract writes were completed before the failure.\n\nWhat Didn't Run\nFailed at step '%s': %s",
				summaryLine, smartWallet, ownerEOA, safeName(failedName), firstLine(failedReason),
			)
		}
	} else {
		// Success body with What Executed On-Chain format (matching AI summarizer output)
		chainName := resolveChainName(vm)
		actionCount := countActionsInLogs(vm)

		// Build What Executed On-Chain section with checkmarks for each successful contract write
		successfulSteps := buildStepsOverview(vm)

		// Use "What Executed On-Chain" for simulations, "What Executed Successfully" for deployed workflows
		sectionHeading := "What Executed Successfully"
		if vm.IsSimulation {
			sectionHeading = "What Executed On-Chain"
		}

		if singleNode {
			body = composeSingleNodeSuccessBody(vm, smartWallet, ownerEOA, chainName, successfulSteps, actionLines, actionCount, currentStepName)
		} else {
			if strings.TrimSpace(successfulSteps) != "" {
				// Body format: plain text (no HTML tags) to match context-memory API format
				// AnalysisHtml is now generated by buildAnalysisHtmlFromStructured() in SendGridDynamicData()
				body = fmt.Sprintf(
					"%s\n%s\n\nAll steps completed on %s.",
					sectionHeading, successfulSteps, chainName,
				)
			} else {
				// Fallback when no contract writes are found
				// Body should start with summary line to match context-memory API format
				// This ensures consistency when context API is unreachable
				body = fmt.Sprintf(
					"%s\n\nNo on-chain contract writes were recorded. This may have been a read-only workflow or all steps were simulated.\n\nAll steps completed on %s.",
					summaryLine, chainName,
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

	// Compute structured fields for consistent output with context-memory API
	status := computeExecutionStatus(vm)
	trigger, triggeredAt := extractTriggerInfo(vm)
	executions := buildExecutionsArray(vm)
	errors := buildErrorsArray(vm, currentStepName)

	// If we have structured data, compose body from it for consistency
	if trigger != "" || len(executions) > 0 || len(errors) > 0 {
		structuredBody := composePlainTextBodyFromStructured(trigger, executions, errors, status)
		if structuredBody != "" {
			body = structuredBody
		}
	}

	// Fallback: when no on-chain executions were recorded and status is success/partial_success,
	// add a generic execution entry so the structured formatters always have data.
	// This is added AFTER the body composition so it doesn't override detailed bodies
	// (e.g., single-node REST API summaries), but the Executions field is still
	// populated for the channel formatters (Telegram/Discord/plain text).
	// Only show example if there are no errors to avoid confusion.
	if len(executions) == 0 && (status == "success" || status == "partial_success") && len(errors) == 0 {
		prefix := ""
		if vm.IsSimulation || isSingleNodeImmediate(vm) {
			prefix = "(Simulated) "
		}
		executions = []ExecutionEntry{{Description: prefix + ExampleExecutionMessage}}
	}

	// For single-node runs with no execution logs (the notification node itself is
	// executing), recompose the body from structured data so the client response
	// matches the formatted channel output. When ExecutionLogs exist, the body
	// already has detailed info (REST API summary, action lines, etc.) that
	// should be preserved.
	if singleNode && len(vm.ExecutionLogs) == 0 {
		if structuredBody := composePlainTextBodyFromStructured(trigger, executions, errors, status); structuredBody != "" {
			body = structuredBody
		}
	}

	return Summary{
		Subject:     subject,
		Body:        body,
		SummaryLine: summaryLine,
		Status:      status,
		Trigger:     trigger,
		TriggeredAt: triggeredAt,
		Executions:  executions,
		Errors:      errors,
		SmartWallet: smartWallet,
		Network:     resolveChainName(vm),
		Annotation:  annotation,
		// AnalysisHtml is now generated by buildAnalysisHtmlFromStructured() in SendGridDynamicData()
	}
}

func composeSingleNodeSuccessBody(vm *VM, smartWallet, ownerEOA, chainName string, successfulSteps string, actionLines []string, actionCount int, fallbackStepName string) string {
	trimmedSteps := strings.TrimSpace(successfulSteps)
	if trimmedSteps != "" {
		// Use "What Executed On-Chain" for simulations, "What Executed Successfully" for deployed workflows
		sectionHeading := "What Executed Successfully"
		if vm != nil && vm.IsSimulation {
			sectionHeading = "What Executed On-Chain"
		}
		// Body is plain text (no HTML tags) to match context-memory API format
		return fmt.Sprintf(
			"Smart wallet %s (owner %s) executed %d on-chain action(s).\n\n%s\n%s\n\nAll steps completed on %s.",
			displayOrUnknown(smartWallet), displayOrUnknown(ownerEOA), actionCount, sectionHeading, trimmedSteps, chainName,
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
		// Uses isStepSimulated() which handles both "is_simulated" (snake_case) and "isSimulated" (camelCase)
		isSimulated := isStepSimulated(st)

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

// defaultDecimalsForSymbol returns the default decimal count for a token symbol.
// WARNING: This function assumes 18 decimals for all unknown tokens, which may be incorrect.
// Many tokens use different decimal counts (e.g., USDC/USDT use 6, WBTC uses 8).
// For accurate formatting, prefer querying the actual decimal count from the blockchain
// or expanding this switch statement to include more known tokens.
func defaultDecimalsForSymbol(sym string) int {
	switch strings.ToUpper(sym) {
	case "USDC", "USDT":
		return 6
	default:
		return 18 // Assumes 18 decimals for all unknown tokens - may be incorrect
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

// TransferEventData holds parsed data from an ERC20/ETH Transfer event trigger
type TransferEventData struct {
	Direction      string // "sent" or "received"
	FromAddress    string
	ToAddress      string
	Value          string // Already formatted (e.g., "1.5")
	TokenSymbol    string // e.g., "ETH", "USDC"
	TokenName      string // e.g., "Ether", "USD Coin"
	BlockTimestamp int64  // Unix timestamp in milliseconds
	ChainName      string // e.g., "Sepolia"
}

// transferMonitorVarName is the key used for transfer event data in vm.vars
// when passed via inputVariables (e.g., single-node Telegram notification runs)
const transferMonitorVarName = "transfer_monitor"

// transferEventDataFromMap builds a TransferEventData from a map containing transfer fields.
// Returns nil if the map doesn't contain eventName == "Transfer".
func transferEventDataFromMap(data map[string]interface{}) *TransferEventData {
	eventName, _ := data["eventName"].(string)
	if eventName != "Transfer" {
		return nil
	}

	transfer := &TransferEventData{}

	if dir, ok := data["direction"].(string); ok {
		transfer.Direction = dir
	}
	if from, ok := data["fromAddress"].(string); ok {
		transfer.FromAddress = from
	}
	if to, ok := data["toAddress"].(string); ok {
		transfer.ToAddress = to
	}
	if val, ok := data["value"].(string); ok {
		transfer.Value = val
	}
	if sym, ok := data["tokenSymbol"].(string); ok {
		transfer.TokenSymbol = sym
	}
	if name, ok := data["tokenName"].(string); ok {
		transfer.TokenName = name
	}

	// Block timestamp can be int64 or float64
	if ts, ok := data["blockTimestamp"].(float64); ok {
		transfer.BlockTimestamp = int64(ts)
	} else if ts, ok := data["blockTimestamp"].(int64); ok {
		transfer.BlockTimestamp = ts
	}

	return transfer
}

// ExtractTransferEventData extracts transfer event data from the VM's execution logs
// or from vm.vars["transfer_monitor"] (for single-node runs with inputVariables).
// Returns nil if no transfer event is found.
// Thread-safe: acquires VM mutex before accessing ExecutionLogs and vars.
func ExtractTransferEventData(vm *VM) *TransferEventData {
	if vm == nil {
		return nil
	}

	// Lock mutex while iterating over ExecutionLogs and vars to prevent race conditions
	vm.mu.Lock()
	var transfer *TransferEventData

	// First, look for an event trigger step with Transfer event data in ExecutionLogs
	for _, st := range vm.ExecutionLogs {
		stepType := strings.ToUpper(st.GetType())
		if !strings.Contains(stepType, "EVENT") && !strings.Contains(stepType, "TRIGGER") {
			continue
		}

		// Check the event trigger output
		eventTrigger := st.GetEventTrigger()
		if eventTrigger == nil || eventTrigger.Data == nil {
			continue
		}

		data, ok := eventTrigger.Data.AsInterface().(map[string]interface{})
		if !ok {
			continue
		}

		transfer = transferEventDataFromMap(data)
		if transfer != nil {
			break // Found transfer event, exit loop
		}
	}

	// Fallback: check vm.vars["transfer_monitor"] for single-node runs
	// where transfer data is passed via inputVariables instead of ExecutionLogs
	if transfer == nil {
		if tmRaw, ok := vm.vars[transferMonitorVarName]; ok {
			if tmMap, ok := tmRaw.(map[string]interface{}); ok {
				if dataRaw, ok := tmMap["data"]; ok {
					if data, ok := dataRaw.(map[string]interface{}); ok {
						transfer = transferEventDataFromMap(data)
					}
				}
			}
		}
	}
	vm.mu.Unlock()

	if transfer == nil {
		return nil
	}

	// Get chain name from settings (resolveChainName acquires its own lock)
	transfer.ChainName = resolveChainName(vm)

	return transfer
}

// FormatTransferMessage formats a transfer event into an HTML notification message.
// Format: ⬆️ Sent <b>1.5 ETH</b> to <code>0x00...02</code> on <b>Sepolia</b> (<i>2026-01-20 13:03</i>)
// All user-controlled content is HTML-escaped to prevent XSS attacks
func FormatTransferMessage(data *TransferEventData) string {
	if data == nil {
		return ""
	}

	// Direction emoji and text
	var directionPrefix string
	var targetLabel string
	var targetAddress string

	if data.Direction == "sent" {
		directionPrefix = "⬆️ Sent"
		targetLabel = "to"
		targetAddress = data.ToAddress
	} else {
		directionPrefix = "⬇️ Received"
		targetLabel = "from"
		targetAddress = data.FromAddress
	}

	// Format amount with token symbol (escaped)
	amount := fmt.Sprintf("<b>%s %s</b>", html.EscapeString(data.Value), html.EscapeString(data.TokenSymbol))

	// Format address (keep full address for code tag, it's monospace and scrollable, escaped)
	addressDisplay := fmt.Sprintf("<code>%s</code>", html.EscapeString(targetAddress))

	// Format chain (escaped)
	chain := fmt.Sprintf("<b>%s</b>", html.EscapeString(data.ChainName))

	// Format timestamp
	timestamp := ""
	if data.BlockTimestamp > 0 {
		// Convert milliseconds to seconds if needed
		ts := data.BlockTimestamp
		if ts > 1e12 {
			ts = ts / 1000
		}
		t := time.Unix(ts, 0)
		timestamp = fmt.Sprintf("(<i>%s</i>)", t.Format("2006-01-02 15:04"))
	}

	// Compose message
	msg := fmt.Sprintf("%s %s %s %s on %s", directionPrefix, amount, targetLabel, addressDisplay, chain)
	if timestamp != "" {
		msg += " " + timestamp
	}

	return msg
}
