package taskengine

import (
	"context"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// Summarizer defines an interface for generating human-readable summaries
// from the current VM execution context. Implementations must be resilient
// and return concise content suitable for email/IM notifications.
type Summarizer interface {
	Summarize(ctx context.Context, vm *VM, currentStepName string) (Summary, error)
}

var globalSummarizer Summarizer

// SetSummarizer sets the global summarizer implementation used by ComposeSummarySmart.
// Pass nil to disable AI summarization and use deterministic fallback only.
func SetSummarizer(s Summarizer) {
	globalSummarizer = s
}

// ComposeSummarySmart tries the configured summarizer (context-memory API) with strict timeout
// and falls back to deterministic ComposeSummary on any failure. The summary is automatically
// formatted for the appropriate channel (email or chat) by the REST API runner
// when used in notification nodes.
func ComposeSummarySmart(vm *VM, currentStepName string) Summary {
	if globalSummarizer == nil {
		if vm != nil && vm.logger != nil {
			vm.logger.Info("ComposeSummarySmart: no global summarizer, using deterministic")
		}
		return ComposeSummary(vm, currentStepName)
	}

	if vm != nil && vm.logger != nil {
		vm.logger.Info("ComposeSummarySmart: starting summarization via context-memory API")
	}

	// Use a generous timeout to allow AI summarization to complete
	// The context-memory API may take 10-20 seconds for complex workflows
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := globalSummarizer.Summarize(ctx, vm, currentStepName)
	if err != nil {
		if vm != nil && vm.logger != nil {
			vm.logger.Info("Context-memory API not available: summarization failed, falling back to deterministic", "error", err)
		}
		return ComposeSummary(vm, currentStepName)
	}

	// Validate minimal fields; fall back if empty
	if strings.TrimSpace(s.Subject) == "" || strings.TrimSpace(s.Body) == "" {
		if vm != nil && vm.logger != nil {
			vm.logger.Warn("ComposeSummarySmart: summary has empty fields, falling back to deterministic")
		}
		return ComposeSummary(vm, currentStepName)
	}

	if len(strings.TrimSpace(s.Body)) < 40 {
		if vm != nil && vm.logger != nil {
			vm.logger.Warn("ComposeSummarySmart: summary body too short, falling back to deterministic", "bodyLength", len(s.Body))
		}
		return ComposeSummary(vm, currentStepName)
	}

	if vm != nil && vm.logger != nil {
		vm.logger.Info("ComposeSummarySmart: summarization successful", "subject", s.Subject, "bodyLength", len(s.Body))
	}
	return s
}

// NewContextMemorySummarizerFromAggregatorConfig creates a ContextMemorySummarizer from aggregator config
// Reads context_api_endpoint and context_api_key from macros.secrets
func NewContextMemorySummarizerFromAggregatorConfig(c *config.Config) Summarizer {
	if c == nil {
		return nil
	}
	if !c.NotificationsSummary.Enabled || strings.ToLower(c.NotificationsSummary.Provider) != "context-memory" {
		return nil
	}
	// Read from macros.secrets instead of notifications.summary
	if c.MacroSecrets == nil {
		return nil
	}
	baseURL := c.MacroSecrets["context_api_endpoint"]
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	authToken := c.MacroSecrets["context_api_key"]
	if strings.TrimSpace(authToken) == "" {
		return nil
	}
	return NewContextMemorySummarizer(baseURL, authToken)
}

// FormatForMessageChannels converts a Summary into a concise chat message
// suitable for messaging channels like Telegram or Discord. It prioritizes
// AI-generated structured fields (Trigger, Executions, Errors) when available.
//
// Note: Email is handled separately via SendGridDynamicData(), not this function.
//
// Fallback order:
// 1. AI-generated structured data from context-memory API
// 2. Transfer event detection (for simple transfer notifications without API)
// 3. Plain text body (legacy)
func FormatForMessageChannels(s Summary, channel string, vm *VM) string {
	// Prioritize AI-generated structured format (from context-memory API)
	if len(s.Executions) > 0 || len(s.Errors) > 0 || s.Trigger != "" {
		switch strings.ToLower(channel) {
		case "telegram":
			return formatTelegramFromStructured(s)
		case "discord":
			return formatDiscordFromStructured(s)
		default:
			return formatPlainTextFromStructured(s)
		}
	}

	// Fallback: Check for transfer event data (when API not available)
	if vm != nil {
		if transferData := ExtractTransferEventData(vm); transferData != nil {
			return FormatTransferMessage(transferData)
		}
	}

	// Legacy fallback: use plain text body
	return formatChannelFromBody(s, channel)
}

// formatTelegramFromStructured formats Summary into Telegram HTML using AI-generated strings
// Uses the API response fields directly without composing additional text
// Format: Subject (bold) as header, Smart Wallet, then trigger, executions, errors
func formatTelegramFromStructured(s Summary) string {
	var sb strings.Builder

	// Subject as header (bold for Telegram HTML)
	if s.Subject != "" {
		sb.WriteString("<b>")
		sb.WriteString(s.Subject)
		sb.WriteString("</b>\n")
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

	// Trigger (AI-generated text) with timestamp
	if s.Trigger != "" {
		sb.WriteString(s.Trigger)
		// Append timestamp in format (2026-01-20 12:36)
		if s.TriggeredAt != "" {
			if ts := formatTimestampShort(s.TriggeredAt); ts != "" {
				sb.WriteString(" (")
				sb.WriteString(ts)
				sb.WriteString(")")
			}
		}
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

	return strings.TrimSpace(sb.String())
}

// formatTimestampShort formats an ISO 8601 timestamp to "2006-01-02 15:04" format
func formatTimestampShort(isoTimestamp string) string {
	if isoTimestamp == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		// Try parsing without timezone
		t, err = time.Parse("2006-01-02T15:04:05", isoTimestamp)
		if err != nil {
			return ""
		}
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// formatDiscordFromStructured formats Summary into Discord markdown using AI-generated strings
// Uses the API response fields directly without composing additional text
// Format: Subject (bold) as header, then trigger, executions, errors
func formatDiscordFromStructured(s Summary) string {
	var sb strings.Builder

	// Subject as header (bold for Discord markdown)
	if s.Subject != "" {
		sb.WriteString("**")
		sb.WriteString(s.Subject)
		sb.WriteString("**\n\n")
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

	return strings.TrimSpace(sb.String())
}

// formatPlainTextFromStructured formats Summary into plain text using AI-generated strings
// Uses the API response fields directly without composing additional text
// Format: Subject as header, then trigger, executions, errors
func formatPlainTextFromStructured(s Summary) string {
	var sb strings.Builder

	// Subject as header
	if s.Subject != "" {
		sb.WriteString(s.Subject)
		sb.WriteString("\n\n")
	}

	// Trigger (AI-generated text)
	if s.Trigger != "" {
		sb.WriteString(s.Trigger)
		sb.WriteString("\n\n")
	}

	// Executions (AI-generated descriptions)
	for _, exec := range s.Executions {
		sb.WriteString("- ")
		sb.WriteString(exec)
		sb.WriteString("\n")
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

	return strings.TrimSpace(sb.String())
}

// formatChannelFromBody is the legacy formatter using plain text body
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
