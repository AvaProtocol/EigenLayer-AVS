package taskengine

import (
	"context"
	"html"
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
// Reads api_endpoint and api_key from notifications.summary section
func NewContextMemorySummarizerFromAggregatorConfig(c *config.Config) Summarizer {
	if c == nil {
		return nil
	}
	if !c.NotificationsSummary.Enabled || strings.ToLower(c.NotificationsSummary.Provider) != "context-memory" {
		return nil
	}
	baseURL := c.NotificationsSummary.APIEndpoint
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	authToken := c.NotificationsSummary.APIKey
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
// Thread-safety: This function reads from the Summary struct and VM object.
// The Summary struct should not be modified after creation. The VM object is
// accessed read-only (for transfer event extraction). If the Summary or VM
// may be accessed concurrently, ensure proper synchronization at the caller level.
//
// Fallback order:
// 1. AI-generated structured data from context-memory API
// 2. Transfer event detection (for simple transfer notifications without API)
// 3. Plain text body (legacy)
func FormatForMessageChannels(s Summary, channel string, vm *VM) string {
	// Prioritize AI-generated structured format (from context-memory API)
	// Check for new PRD format (transfers/workflow) or legacy format (executions/errors/trigger)
	hasStructuredData := len(s.Transfers) > 0 || s.Workflow != nil || len(s.Executions) > 0 || len(s.Errors) > 0 || s.Trigger != ""
	if hasStructuredData {
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

	// Fallback: For single-node executions without meaningful data, show example message
	if vm != nil && isSingleNodeImmediate(vm) {
		return formatSingleNodeExampleMessage(vm, channel)
	}

	// Legacy fallback: use plain text body
	return formatChannelFromBody(s, channel)
}

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

// formatSubjectWithBoldName formats the subject with bold only around the workflow name
// Subject patterns:
//   - "Simulation: {name} successfully completed"
//   - "Run #N: {name} successfully completed"
//   - "{name} successfully completed"
//   - And similar patterns for "failed to execute" and "partially executed"
func formatSubjectWithBoldName(subject string) string {
	// Suffixes to look for
	suffixes := []string{
		" successfully completed",
		" failed to execute",
		" partially executed",
	}

	// Prefixes to look for
	prefixes := []string{
		"Simulation: ",
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

	// Build the formatted string: prefix + <b>name</b> + suffix
	var sb strings.Builder
	if prefix != "" {
		sb.WriteString(html.EscapeString(prefix))
	}
	sb.WriteString("<b>")
	sb.WriteString(html.EscapeString(name))
	sb.WriteString("</b>")
	sb.WriteString(html.EscapeString(suffix))

	return sb.String()
}

// formatTimestampShort formats an ISO 8601 timestamp to "2006-01-02 15:04" format
// logger is optional and used for debug logging when timestamp parsing fails
func formatTimestampShort(isoTimestamp string, logger interface{ Debug(string, ...interface{}) }) string {
	if isoTimestamp == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		// Try parsing without timezone
		t, err = time.Parse("2006-01-02T15:04:05", isoTimestamp)
		if err != nil {
			if logger != nil {
				logger.Debug("Failed to parse timestamp in both RFC3339 and simplified format", "timestamp", isoTimestamp, "error", err)
			}
			return ""
		}
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// truncateAddress truncates an Ethereum address to format "0x5d814...434f"
func truncateAddress(addr string) string {
	if len(addr) < 12 {
		return addr
	}
	return addr[:7] + "..." + addr[len(addr)-4:]
}

// truncateTxHash truncates a transaction hash to format "0x1234...cdef"
func truncateTxHash(hash string) string {
	if len(hash) < 14 {
		return hash
	}
	return hash[:6] + "..." + hash[len(hash)-4:]
}

// getBlockExplorerURL returns the block explorer URL for a chain (by name or ID)
func getBlockExplorerURL(chainName string, chainID int64) string {
	// First try by chain ID (more reliable)
	switch chainID {
	case 1:
		return "https://etherscan.io"
	case 11155111:
		return "https://sepolia.etherscan.io"
	case 137:
		return "https://polygonscan.com"
	case 42161:
		return "https://arbiscan.io"
	case 10:
		return "https://optimistic.etherscan.io"
	case 8453:
		return "https://basescan.org"
	case 84532:
		return "https://sepolia.basescan.org"
	case 56:
		return "https://bscscan.com"
	case 43114:
		return "https://snowtrace.io"
	}

	// Fallback to chain name if chain ID not provided or unknown
	switch strings.ToLower(chainName) {
	case "mainnet", "ethereum":
		return "https://etherscan.io"
	case "sepolia":
		return "https://sepolia.etherscan.io"
	case "polygon":
		return "https://polygonscan.com"
	case "arbitrum", "arbitrum one":
		return "https://arbiscan.io"
	case "optimism":
		return "https://optimistic.etherscan.io"
	case "base":
		return "https://basescan.org"
	case "base-sepolia", "base sepolia":
		return "https://sepolia.basescan.org"
	case "bsc", "binance smart chain":
		return "https://bscscan.com"
	case "avalanche":
		return "https://snowtrace.io"
	default:
		return "https://etherscan.io"
	}
}

// getChainDisplayName returns a display-friendly chain name from chain ID
// This provides a local fallback when the API doesn't return a chain name
func getChainDisplayName(chainID int64) string {
	switch chainID {
	case 1:
		return "Ethereum"
	case 11155111:
		return "Sepolia"
	case 137:
		return "Polygon"
	case 80001:
		return "Polygon Mumbai"
	case 42161:
		return "Arbitrum One"
	case 421614:
		return "Arbitrum Sepolia"
	case 10:
		return "Optimism"
	case 11155420:
		return "Optimism Sepolia"
	case 8453:
		return "Base"
	case 84532:
		return "Base Sepolia"
	case 56:
		return "BNB Chain"
	case 97:
		return "BNB Testnet"
	case 43114:
		return "Avalanche"
	case 43113:
		return "Avalanche Fuji"
	default:
		return ""
	}
}

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

	return strings.TrimSpace(sb.String())
}

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

// formatSingleNodeExampleMessage creates an example message for single-node executions
// when no meaningful execution data (like transfer_monitor) is available.
// This helps users understand what the notification format will look like.
func formatSingleNodeExampleMessage(vm *VM, channel string) string {
	workflowName := resolveWorkflowName(vm)
	chainName := resolveChainName(vm)

	switch strings.ToLower(channel) {
	case "telegram":
		return formatTelegramExampleMessage(workflowName, chainName)
	case "discord":
		return formatDiscordExampleMessage(workflowName, chainName)
	default:
		return formatPlainTextExampleMessage(workflowName, chainName)
	}
}

func formatTelegramExampleMessage(workflowName, chainName string) string {
	var sb strings.Builder

	// Status line with emoji and workflow name
	sb.WriteString("✅ <b>")
	sb.WriteString(html.EscapeString(workflowName))
	sb.WriteString("</b> completed\n\n")

	// Network
	sb.WriteString("<b>Network:</b> ")
	sb.WriteString(html.EscapeString(chainName))
	sb.WriteString("\n\n")

	// Executed section with example
	sb.WriteString("<b>Executed:</b>\n")
	sb.WriteString("• (Simulated) On-chain transaction successfully completed\n\n")

	// Example notice
	sb.WriteString("<i>This is an example. Actual execution details will appear when the workflow is simulated or triggered by a real event.</i>")

	return sb.String()
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

func formatPlainTextExampleMessage(workflowName, chainName string) string {
	var sb strings.Builder

	sb.WriteString(workflowName)
	sb.WriteString(" completed\n\n")

	sb.WriteString("Network: ")
	sb.WriteString(chainName)
	sb.WriteString("\n\n")

	sb.WriteString("Executed:\n")
	sb.WriteString("• (Simulated) On-chain transaction successfully completed\n\n")

	sb.WriteString("This is an example. Actual execution details will appear when the workflow is simulated or triggered by a real event.")

	return sb.String()
}
