package taskengine

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// formatValueConcise renders an arbitrary value as a short, human-readable string for
// branch-condition logging (used by vm_runner_branch.go). Long strings/objects are truncated.
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

// globalSummarizer holds the Studio /api/notify payload builder (Path B). It is the only
// summarizer path: the gateway forwards raw execution data and Studio summarizes + sends.
var globalSummarizer *ContextMemorySummarizer

// SetSummarizer sets the global summarizer used to build /api/notify payloads.
// Pass nil to disable (notification nodes then forward their body unchanged).
func SetSummarizer(s *ContextMemorySummarizer) {
	globalSummarizer = s
}

// NewContextMemorySummarizerFromAggregatorConfig builds the workflow summarizer from the
// aggregator's notifications.summary config (api_endpoint + api_key).
//
// Returns (nil, nil) when summarization is disabled — a valid "off" mode in which notification
// nodes forward their body unchanged. Returns (nil, error) when summarization is ENABLED but
// misconfigured (unsupported provider, or empty endpoint/key) so the daemon fails fast at
// startup instead of silently degrading. There is no hardcoded endpoint default: the origin
// must come from config (avs-infra: ${SUMMARIZER_API_URL} / ${SUMMARIZER_API_KEY}).
func NewContextMemorySummarizerFromAggregatorConfig(c *config.Config) (*ContextMemorySummarizer, error) {
	if c == nil || !c.NotificationsSummary.Enabled {
		return nil, nil // Not enabled — notification nodes forward their body unchanged.
	}
	if strings.ToLower(c.NotificationsSummary.Provider) != "context-memory" {
		// NOTE: "context-memory" is a legacy provider identifier — the endpoint now points at
		// Studio's /api/summarize, not the retired context-memory service. The string is kept
		// for config back-compat; don't be misled by the name.
		return nil, fmt.Errorf("notifications.summary.enabled is true but provider %q is unsupported (expected \"context-memory\" — a legacy identifier; the endpoint now points at Studio)", c.NotificationsSummary.Provider)
	}
	baseURL := strings.TrimSpace(c.NotificationsSummary.APIEndpoint)
	if baseURL == "" {
		return nil, fmt.Errorf("notifications.summary.enabled is true but api_endpoint is empty (set SUMMARIZER_API_URL to the Studio origin, e.g. https://app.avaprotocol.org)")
	}
	authToken := strings.TrimSpace(c.NotificationsSummary.APIKey)
	if authToken == "" {
		return nil, fmt.Errorf("notifications.summary.enabled is true but api_key is empty (set SUMMARIZER_API_KEY)")
	}
	return &ContextMemorySummarizer{
		baseURL:    baseURL,
		authToken:  authToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}
