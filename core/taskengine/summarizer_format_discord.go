package taskengine

import "strings"

// formatDiscordFromStructured formats Summary into Discord markdown using AI-generated strings
// Uses the API response fields directly without composing additional text
// Format: Subject (bold) as header, Smart Wallet, then trigger, executions, errors
// Note: Discord uses markdown (not HTML), so XSS is not a concern
func formatDiscordFromStructured(s Summary) string {
	var sb strings.Builder

	// Subject as header (bold for Discord markdown)
	if s.Subject != "" {
		sb.WriteString("**")
		sb.WriteString(s.Subject)
		sb.WriteString("**\n")
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
	}

	// Executions (AI-generated descriptions)
	if len(s.Executions) > 0 {
		if s.Trigger != "" {
			sb.WriteString("\n\n")
		}
		for _, exec := range s.Executions {
			sb.WriteString("• ")
			sb.WriteString(exec)
			sb.WriteString("\n")
		}
	}

	// Errors - only show if status is "failure" (not partial_success)
	if s.Status == "failure" && len(s.Errors) > 0 {
		if len(s.Executions) > 0 || s.Trigger != "" {
			sb.WriteString("\n")
		}
		for _, err := range s.Errors {
			sb.WriteString("• ")
			sb.WriteString(err)
			sb.WriteString("\n")
		}
	}

	if s.Annotation != "" {
		sb.WriteString("\n*")
		sb.WriteString(s.Annotation)
		sb.WriteString("*")
	}

	return strings.TrimSpace(sb.String())
}

func formatDiscordExampleMessage(workflowName, chainName string) string {
	var sb strings.Builder

	// Status line with emoji and workflow name
	sb.WriteString("✅ **")
	sb.WriteString(workflowName)
	sb.WriteString("** completed\n\n")

	// Network
	sb.WriteString("**Network:** ")
	sb.WriteString(chainName)
	sb.WriteString("\n\n")

	// Executed section with example
	sb.WriteString("**Executed:**\n")
	sb.WriteString("• (Simulated) On-chain transaction successfully completed\n\n")

	// Example notice
	sb.WriteString("*This is an example. Actual execution details will appear when the workflow is simulated or triggered by a real event.*")

	return sb.String()
}
