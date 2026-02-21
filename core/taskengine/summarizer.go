package taskengine

import (
	"context"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// Constants for example execution messages shown when no executions are available
const (
	// ExampleExecutionMessage is the base message for example executions
	ExampleExecutionMessage = "On-chain transaction successfully completed"
	// ExampleExecutionAnnotation is the annotation text explaining that this is an example
	ExampleExecutionAnnotation = "This is an example. Actual execution details will appear when the workflow is simulated or triggered by a real event."
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

// isValidTxHash checks if a string is a valid Ethereum transaction hash (0x + 64 hex chars).
// This filters out fake hashes like raw BigInt values from Tenderly simulation IDs.
func isValidTxHash(hash string) bool {
	if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
		return false
	}
	for _, c := range hash[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isStepSimulated checks whether an execution step was run in simulation mode
// by reading the ExecutionContext attached to the step.
// Handles both naming conventions: "is_simulated" (snake_case from engine.go/node_utils.go)
// and "isSimulated" (camelCase from execution_providers.go).
func isStepSimulated(st *avsproto.Execution_Step) bool {
	if st.GetExecutionContext() == nil {
		return false
	}
	ctx, ok := st.GetExecutionContext().AsInterface().(map[string]interface{})
	if !ok {
		return false
	}
	for _, key := range []string{"is_simulated", "isSimulated"} {
		if sim, exists := ctx[key]; exists {
			switch v := sim.(type) {
			case bool:
				if v {
					return true
				}
			case string:
				if strings.EqualFold(strings.TrimSpace(v), "true") {
					return true
				}
			}
		}
	}
	return false
}

// buildTxExplorerURL constructs a full block explorer transaction URL from a Summary and tx hash.
// It resolves chainID from Workflow.ChainID first, then falls back to reverse-mapping the Network name.
// Returns "" if the txHash is not a valid Ethereum transaction hash or the chain is unknown.
func buildTxExplorerURL(s Summary, txHash string) string {
	if !isValidTxHash(txHash) {
		return ""
	}
	var chainID int64
	if s.Workflow != nil {
		chainID = s.Workflow.ChainID
	}
	// Fallback: reverse-map the network display name to a chain ID
	if chainID == 0 {
		chainID = mapNameToChainID(s.Network)
	}
	baseURL := getBlockExplorerURL(chainID)
	if baseURL == "" {
		return ""
	}
	return baseURL + "/tx/" + txHash
}

// getBlockExplorerURL returns the block explorer base URL for a chain ID.
// Returns an empty string for unknown chains — callers must not assume a default.
func getBlockExplorerURL(chainID int64) string {
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
	default:
		return ""
	}
}

// mapNameToChainID reverse-maps a display chain name to a numeric chain ID.
// Used as a fallback when chainID is not directly available (e.g. deterministic summarizer path).
func mapNameToChainID(name string) int64 {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mainnet", "ethereum":
		return 1
	case "sepolia":
		return 11155111
	case "polygon":
		return 137
	case "arbitrum", "arbitrum one":
		return 42161
	case "optimism":
		return 10
	case "base":
		return 8453
	case "base-sepolia", "base sepolia":
		return 84532
	case "bsc", "binance smart chain", "bnb chain":
		return 56
	case "avalanche":
		return 43114
	default:
		return 0
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
