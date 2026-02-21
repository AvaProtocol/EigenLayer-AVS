package taskengine

import "strings"

// formatPlainTextFromStructured formats Summary into plain text using AI-generated strings
// Uses the API response fields directly without composing additional text
// Format: Subject as header, Smart Wallet, then trigger, executions, errors
func formatPlainTextFromStructured(s Summary) string {
	var sb strings.Builder

	// Subject as header
	if s.Subject != "" {
		sb.WriteString(s.Subject)
		sb.WriteString("\n")
	}

	// Smart Wallet line (right beneath title)
	if s.SmartWallet != "" {
		sb.WriteString("Smart Wallet: ")
		sb.WriteString(s.SmartWallet)
		sb.WriteString("\n")
	}

	// Add blank line before trigger if we have subject or smart wallet
	if s.Subject != "" || s.SmartWallet != "" {
		sb.WriteString("\n")
	}

	// Trigger (AI-generated text)
	if s.Trigger != "" {
		sb.WriteString(s.Trigger)
		sb.WriteString("\n\n")
	}

	// Executions (AI-generated descriptions)
	if len(s.Executions) > 0 {
		for _, exec := range s.Executions {
			sb.WriteString("- ")
			sb.WriteString(exec.Description)
			if exec.TxHash != "" {
				sb.WriteString(" (")
				sb.WriteString(buildTxExplorerURL(s, exec.TxHash))
				sb.WriteString(")")
			}
			sb.WriteString("\n")
		}
	}

	// Errors - only show if status is "failure" (not partial_success)
	if s.Status == "failure" && len(s.Errors) > 0 {
		if len(s.Executions) > 0 {
			sb.WriteString("\n")
		}
		for _, err := range s.Errors {
			sb.WriteString("- ")
			sb.WriteString(err)
			sb.WriteString("\n")
		}
	}

	if s.Annotation != "" {
		sb.WriteString("\n")
		sb.WriteString(s.Annotation)
	}

	return strings.TrimSpace(sb.String())
}

func formatPlainTextExampleMessage(workflowName, chainName string) string {
	var sb strings.Builder

	sb.WriteString(workflowName)
	sb.WriteString(" completed\n\n")

	sb.WriteString("Network: ")
	sb.WriteString(chainName)
	sb.WriteString("\n\n")

	sb.WriteString("Executed:\n")
	sb.WriteString("• (Simulated) ")
	sb.WriteString(ExampleExecutionMessage)
	sb.WriteString("\n\n")

	sb.WriteString(ExampleExecutionAnnotation)

	return sb.String()
}

// formatChannelFromBody is the legacy formatter using plain text body.
// Used as a fallback when no structured data is available.
func formatChannelFromBody(s Summary, channel string) string {
	body := strings.TrimSpace(s.Body)
	subject := strings.TrimSpace(s.Subject)
	if body == "" {
		return subject
	}
	// Extract the first sentence or up to ~200 chars, whichever comes first.
	maxLen := 220
	msg := body
	// Split on blank lines first (since email body uses double newlines).
	parts := strings.SplitN(body, "\n\n", 2)
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		msg = strings.TrimSpace(parts[0])
	}
	// Hard cap length for Telegram-style brevity.
	if len(msg) > maxLen {
		msg = msg[:maxLen]
		// avoid cutting in the middle of a word
		if idx := strings.LastIndex(msg, " "); idx > 0 {
			msg = msg[:idx]
		}
		msg += "…"
	}
	// Channel-specific formatting
	switch strings.ToLower(channel) {
	case "telegram":
		if subject != "" && !strings.Contains(msg, subject) {
			return "<b>" + subject + "</b>\n" + msg
		}
		return msg
	case "discord":
		if subject != "" && !strings.Contains(msg, subject) {
			return "**" + subject + "**\n" + msg
		}
		return msg
	default:
		return msg
	}
}
