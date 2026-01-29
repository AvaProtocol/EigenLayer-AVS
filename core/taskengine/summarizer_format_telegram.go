package taskengine

import (
	"html"
	"strings"
)

// formatTelegramFromStructured formats Summary into Telegram HTML using structured data
// Uses the PRD format: emoji + subject, network, time, executions list, footer
// All user-controlled content is HTML-escaped to prevent XSS attacks
func formatTelegramFromStructured(s Summary) string {
	var sb strings.Builder

	// Get status emoji - API returns subject WITHOUT emoji, aggregator prepends it
	statusEmoji := getStatusEmoji(s.Status)

	// Subject as header with emoji prefix - only bold the workflow name
	if s.Subject != "" {
		sb.WriteString(statusEmoji)
		if statusEmoji != "" {
			sb.WriteString(" ")
		}
		sb.WriteString(formatSubjectWithBoldName(s.Subject))
		sb.WriteString("\n")
	}

	// Network: use body.network field, fallback to workflow.chain or derive from chainID
	network := s.Network
	if network == "" && s.Workflow != nil {
		network = s.Workflow.Chain
		if network == "" && s.Workflow.ChainID > 0 {
			network = getChainDisplayName(s.Workflow.ChainID)
		}
	}

	// Network and Time section
	if network != "" || s.TriggeredAt != "" {
		sb.WriteString("\n")
		if network != "" {
			sb.WriteString("<b>Network:</b> ")
			sb.WriteString(html.EscapeString(network))
			sb.WriteString("\n")
		}
		if s.TriggeredAt != "" {
			sb.WriteString("<b>Time:</b> ")
			sb.WriteString(html.EscapeString(s.TriggeredAt))
			sb.WriteString("\n")
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
			sb.WriteString(html.EscapeString(exec))
			sb.WriteString("\n")
		}
	} else if s.Status == "failure" && len(s.Errors) > 0 {
		// Error display for failed workflows
		sb.WriteString("\n")
		for _, err := range s.Errors {
			sb.WriteString("<b>Error:</b> ")
			sb.WriteString(html.EscapeString(err))
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

// getStatusEmoji returns the emoji for a given status
func getStatusEmoji(status string) string {
	switch status {
	case "success":
		return "✅"
	case "failure":
		return "❌"
	case "partial_success":
		return "⚠️"
	default:
		return ""
	}
}

// formatSubjectWithBoldName formats the subject with <code> tags only around the workflow name
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

	// Build the formatted string: prefix + <code>name</code> + suffix
	// Using <code> prevents Telegram from auto-linking names that contain dots
	var sb strings.Builder
	if prefix != "" {
		sb.WriteString(html.EscapeString(prefix))
	}
	sb.WriteString("<code>")
	sb.WriteString(html.EscapeString(name))
	sb.WriteString("</code>")
	sb.WriteString(html.EscapeString(suffix))

	return sb.String()
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
