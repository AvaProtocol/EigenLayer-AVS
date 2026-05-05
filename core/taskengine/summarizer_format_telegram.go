package taskengine

import (
	"fmt"
	"html"
	"strings"
)

// formatTelegramFromStructured formats Summary into Telegram HTML using structured data
// Uses the PRD format: emoji + subject, network, time, executions list, footer
// All user-controlled content is HTML-escaped to prevent XSS attacks
func formatTelegramFromStructured(s Summary) string {
	var sb strings.Builder

	// Get status emoji - API returns subject WITHOUT emoji, aggregator prepends it
	statusEmoji := getStatusEmoji(s)

	// Subject as header with emoji prefix - only bold the workflow name
	if s.Subject != "" {
		sb.WriteString(statusEmoji)
		if statusEmoji != "" {
			sb.WriteString(" ")
		}
		sb.WriteString(formatSubjectWithBoldName(s.Subject))
		sb.WriteString("\n")
	}

	// Skipped note (success runs with branch-skipped steps) — surfaces why the
	// header shows ⚠️ instead of ✅.
	if s.SkippedNote != "" {
		sb.WriteString("<i>")
		sb.WriteString(html.EscapeString(s.SkippedNote))
		sb.WriteString("</i>\n")
	}

	// Network: use body.network field, fallback to workflow.chain or derive from chainID
	network := s.Network
	if network == "" && s.Workflow != nil {
		network = s.Workflow.Chain
		if network == "" && s.Workflow.ChainID > 0 {
			network = getChainDisplayName(s.Workflow.ChainID)
		}
	}

	// Time / Runner / Cost — metadata block. Network is folded into the Runner
	// line ("Runner: 0x… on Sepolia") to save a line, since chain is contextual
	// to the wallet that ran the workflow. When Runner is absent we keep
	// Network as a standalone line so chain context isn't lost.
	// Cost follows Runner so the "who and what it cost" pair stays adjacent.
	// For simulations the Cost line is a placeholder ("⛽ (cost estimated at
	// deploy)") — actual gas numbers only appear for deployed runs with real
	// receipts. Runner addresses are intentionally NOT <code>-wrapped (hex
	// addresses don't trigger Telegram auto-linking; the wrap adds noise).
	hasRunner := s.Runner != nil && s.Runner.SmartWallet != ""
	costLine := formatTelegramCostLine(s)
	if network != "" || s.TriggeredAt != "" || hasRunner || costLine != "" {
		sb.WriteString("\n")
		if s.TriggeredAt != "" {
			sb.WriteString("<b>Time:</b> ")
			sb.WriteString(html.EscapeString(formatTimestampHumanReadable(s.TriggeredAt)))
			sb.WriteString("\n")
		}
		switch {
		case hasRunner && network != "":
			sb.WriteString("<b>Runner:</b> ")
			sb.WriteString(html.EscapeString(truncateAddress(s.Runner.SmartWallet)))
			sb.WriteString(" on ")
			sb.WriteString(html.EscapeString(network))
			sb.WriteString("\n")
		case hasRunner:
			sb.WriteString("<b>Runner:</b> ")
			sb.WriteString(html.EscapeString(truncateAddress(s.Runner.SmartWallet)))
			sb.WriteString("\n")
		case network != "":
			sb.WriteString("<b>Network:</b> ")
			sb.WriteString(html.EscapeString(network))
			sb.WriteString("\n")
		}
		if costLine != "" {
			sb.WriteString(costLine)
		}
	}

	// Trigger section
	if s.Trigger != "" {
		sb.WriteString("\n<b>Trigger:</b> ")
		sb.WriteString(html.EscapeString(s.Trigger))
		sb.WriteString("\n")
	}

	// Display executions with "Executed:" header (PRD format)
	if len(s.Executions) > 0 {
		sb.WriteString("\n<b>Executed:</b>\n")
		for _, exec := range s.Executions {
			sb.WriteString("• ")
			sb.WriteString(formatBackticksForChannel(exec.Description, "telegram"))
			sb.WriteString("\n")
			if exec.TxHash != "" {
				if explorerURL := buildTxExplorerURL(s, exec.TxHash); explorerURL != "" {
					sb.WriteString("  Transaction: <a href=\"")
					sb.WriteString(html.EscapeString(explorerURL))
					sb.WriteString("\">")
					sb.WriteString(html.EscapeString(truncateTxHash(exec.TxHash)))
					sb.WriteString("</a>\n")
				}
			}
		}
	}

	// Errors section — heading depends on status. On a successful run the only
	// entries here are branch-skipped nodes, so calling it "What Went Wrong"
	// misleads the reader; use "Skipped Steps" instead.
	if len(s.Errors) > 0 {
		heading := "What Went Wrong"
		if s.Status == "success" {
			heading = "Skipped Steps"
		}
		sb.WriteString(fmt.Sprintf("\n<b>%s:</b>\n", heading))
		for _, err := range s.Errors {
			sb.WriteString("• ")
			sb.WriteString(formatBackticksForChannel(err, "telegram"))
			sb.WriteString("\n")
		}
	}

	if s.Annotation != "" {
		sb.WriteString("\n<i>")
		sb.WriteString(html.EscapeString(s.Annotation))
		sb.WriteString("</i>")
	}

	return strings.TrimSpace(sb.String())
}

// getStatusEmoji returns the emoji for the summary's status.
// A success run with branch-skipped steps renders ⚠️ (warn) rather than ✅,
// matching the yellow badge on email.
func getStatusEmoji(s Summary) string {
	switch s.Status {
	case "success":
		if s.SkippedSteps > 0 {
			return "⚠️"
		}
		return "✅"
	case "failed", "error":
		return "❌"
	default:
		return ""
	}
}

// formatSubjectWithBoldName formats the subject with <code> tags around the
// prefix + workflow name (everything except the trailing status suffix).
// Subject patterns:
//   - "Simulation: {name} successfully completed"
//   - "Run Node: {name} succeeded"
//   - "Run #N: {name} successfully completed"
//   - "{name} successfully completed"
//   - And similar patterns for "failed to execute" and "partially executed"
func formatSubjectWithBoldName(subject string) string {
	// Suffixes to look for (ordered from most specific to least)
	suffixes := []string{
		" successfully completed",
		" failed to execute",
		" partially executed",
		" succeeded",
	}

	// Prefixes to look for
	prefixes := []string{
		"Simulation: ",
		"Run Node: ",
	}

	// Check for "Run #N: " prefix pattern
	runPrefix := ""
	if strings.HasPrefix(subject, "Run #") {
		// Find the ": " after "Run #N"
		if idx := strings.Index(subject, ": "); idx > 0 {
			runPrefix = subject[:idx+2]
		}
	}

	// Find which suffix matches
	var suffix string
	var nameEnd int
	for _, s := range suffixes {
		if strings.HasSuffix(subject, s) {
			suffix = s
			nameEnd = len(subject) - len(s)
			break
		}
	}

	// Check for deployed workflow format: "{name}: succeeded (...)" or "{name}: failed at ..."
	// Must be checked before the generic " failed at " pattern to avoid splitting at the wrong point.
	if suffix == "" {
		for _, marker := range []string{": succeeded (", ": failed at "} {
			if idx := strings.Index(subject, marker); idx > 0 {
				suffix = subject[idx:]
				nameEnd = idx
				break
			}
		}
	}

	// Check for "failed at <stepName>" suffix (Run Node failure format)
	if suffix == "" && strings.Contains(subject, " failed at ") {
		idx := strings.LastIndex(subject, " failed at ")
		if idx > 0 {
			suffix = subject[idx:]
			nameEnd = idx
		}
	}

	// If no suffix found, just escape and return
	if suffix == "" {
		return html.EscapeString(subject)
	}

	// Find the prefix and extract the name
	var prefix string
	nameStart := 0

	if runPrefix != "" {
		prefix = runPrefix
		nameStart = len(runPrefix)
	} else {
		for _, p := range prefixes {
			if strings.HasPrefix(subject, p) {
				prefix = p
				nameStart = len(p)
				break
			}
		}
	}

	// Extract the workflow name
	name := subject[nameStart:nameEnd]

	// Build the formatted string: <code>prefix + name</code> + suffix
	// Using <code> prevents Telegram from auto-linking names that contain dots
	var sb strings.Builder
	sb.WriteString("<code>")
	if prefix != "" {
		sb.WriteString(html.EscapeString(prefix))
	}
	sb.WriteString(html.EscapeString(name))
	sb.WriteString("</code>")
	sb.WriteString(html.EscapeString(suffix))

	return sb.String()
}

// formatTelegramCostLine renders a single Cost line from Summary.Fees.Total.
// Format: "⛽ <b>Cost:</b> 0.000003 ETH ($0.01), 1.2 USDC ($1.20)" — native
// token first, comma-separated, USD parenthetical per token. Unpriceable
// tokens render as "$?". For simulations the line collapses to the static
// "⛽ (see cost estimate before deploy)" placeholder. Returns "" when there's
// nothing to render.
func formatTelegramCostLine(s Summary) string {
	if s.Workflow != nil && s.Workflow.IsSimulation {
		return "⛽ <i>(see cost estimate before deploy)</i>\n"
	}
	if s.Fees == nil || len(s.Fees.Total) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Fees.Total))
	for _, t := range s.Fees.Total {
		if t == nil || t.Amount == "" || t.Amount == "0" {
			continue
		}
		// USD-unit entries are the platform fee — render as "$X platform fee"
		// (the dollar amount is already canonical; no need for the parenthetical).
		if t.Unit == "USD" {
			parts = append(parts, fmt.Sprintf("$%s platform fee", html.EscapeString(t.Amount)))
			continue
		}
		usd := "$?"
		if t.USD != "" {
			usd = "$" + t.USD
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", html.EscapeString(t.Amount), html.EscapeString(t.Unit), usd))
	}
	if len(parts) == 0 {
		return ""
	}
	return "⛽ <b>Cost:</b> " + strings.Join(parts, ", ") + "\n"
}

func formatTelegramExampleMessage(workflowName, chainName string) string {
	var sb strings.Builder

	// Status line with emoji and workflow name (code-wrapped to prevent auto-linking)
	sb.WriteString("✅ <code>")
	sb.WriteString(html.EscapeString(workflowName))
	sb.WriteString("</code> completed\n\n")

	// Network
	sb.WriteString("<b>Network:</b> ")
	sb.WriteString(html.EscapeString(chainName))
	sb.WriteString("\n\n")

	// Executed section with example
	sb.WriteString("<b>Executed:</b>\n")
	sb.WriteString("• (Simulated) ")
	sb.WriteString(ExampleExecutionMessage)
	sb.WriteString("\n\n")

	// Example notice
	sb.WriteString("<i>")
	sb.WriteString(ExampleExecutionAnnotation)
	sb.WriteString("</i>")

	return sb.String()
}
