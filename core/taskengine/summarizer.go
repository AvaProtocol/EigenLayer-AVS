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
