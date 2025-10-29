package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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

// ComposeSummarySmart tries AI (if configured) with strict timeout and falls back
// to deterministic ComposeSummary on any failure.
func ComposeSummarySmart(vm *VM, currentStepName string) Summary {
	if globalSummarizer == nil {
		return ComposeSummary(vm, currentStepName)
	}

	// Use a conservative timeout even if the underlying summarizer chooses a shorter one
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if s, err := globalSummarizer.Summarize(ctx, vm, currentStepName); err == nil {
		// Validate minimal fields; fall back if empty
		if strings.TrimSpace(s.Subject) != "" && strings.TrimSpace(s.Body) != "" {
			return s
		}
	}
	return ComposeSummary(vm, currentStepName)
}

// OpenAIConfig holds runtime options for the OpenAI-based summarizer.
type OpenAIConfig struct {
	Model               string
	MaxInputTokens      int
	MaxOutputTokens     int
	Temperature         float64
	TimeoutMs           int
	BudgetUSDPerSummary float64
	APIKey              string
}

// OpenAISummarizer implements Summarizer using OpenAI chat completions with
// a strict JSON response and small bounded prompt.
type OpenAISummarizer struct {
	cfg OpenAIConfig
	hc  *http.Client
}

func NewOpenAISummarizerFromAggregatorConfig(c *config.Config) Summarizer {
	if c == nil {
		return nil
	}
	if !c.NotificationsSummary.Enabled || strings.ToLower(c.NotificationsSummary.Provider) != "openai" {
		return nil
	}
	apiKey := firstNonEmptyStr(os.Getenv("OPENAI_API_KEY"), c.MacroSecrets["openai_api_key"]) // prefer env override
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	cfg := OpenAIConfig{
		Model:               firstNonEmptyStr(c.NotificationsSummary.Model, "gpt-4o-mini"),
		MaxInputTokens:      nonZeroOr(c.NotificationsSummary.MaxInputTokens, 2000),
		MaxOutputTokens:     nonZeroOr(c.NotificationsSummary.MaxOutputTokens, 250),
		Temperature:         c.NotificationsSummary.Temperature,
		TimeoutMs:           nonZeroOr(c.NotificationsSummary.TimeoutMs, 1800),
		BudgetUSDPerSummary: c.NotificationsSummary.BudgetUSDPerSummary,
		APIKey:              apiKey,
	}
	return &OpenAISummarizer{
		cfg: cfg,
		hc:  &http.Client{Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond},
	}
}

func (o *OpenAISummarizer) Summarize(ctx context.Context, vm *VM, currentStepName string) (Summary, error) {
	if o == nil || o.hc == nil {
		return Summary{}, errors.New("summarizer not initialized")
	}

	// Build compact digest with strict redaction and truncation
	digest := o.buildDigest(vm, currentStepName)

	// Prepare request payload for chat completions
	// Use response_format json_object to force strict JSON output
	body := map[string]interface{}{
		"model":           o.cfg.Model,
		"temperature":     o.cfg.Temperature,
		"max_tokens":      o.cfg.MaxOutputTokens,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": "You create concise, accurate workflow email summaries. Do not speculate. Redact secrets. Return strict JSON with keys: subject, body."},
			{"role": "user", "content": digest},
		},
	}

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return Summary{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", buf)
	if err != nil {
		return Summary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.hc.Do(req)
	if err != nil {
		return Summary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Summary{}, errors.New("openai non-2xx response")
	}

	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Summary{}, err
	}
	if len(cr.Choices) == 0 || strings.TrimSpace(cr.Choices[0].Message.Content) == "" {
		return Summary{}, errors.New("empty ai response")
	}

	// The model is instructed to return strict JSON; parse it
	var s Summary
	if err := json.Unmarshal([]byte(cr.Choices[0].Message.Content), &s); err != nil {
		// If content contains code fences, try to strip them
		clean := stripCodeFences(cr.Choices[0].Message.Content)
		if err2 := json.Unmarshal([]byte(clean), &s); err2 != nil {
			return Summary{}, err
		}
	}
	return s, nil
}

func (o *OpenAISummarizer) buildDigest(vm *VM, currentStepName string) string {
	// The digest is compact JSON; limit the number of steps to avoid large prompts
	maxSteps := 40
	if o.cfg.MaxInputTokens > 0 && o.cfg.MaxInputTokens < 1000 {
		// Very small token limit, reduce steps further
		maxSteps = 15
	}

	workflowName := resolveWorkflowName(vm)
	failed, failedName, failedReason := findEarliestFailure(vm)
	lastName := findLastSuccessStepName(vm)
	if lastName == "" {
		lastName = safeName(currentStepName)
	}

	// Copy execution logs under lock to avoid races
	vm.mu.Lock()
	steps := make([]*avsproto.Execution_Step, len(vm.ExecutionLogs))
	copy(steps, vm.ExecutionLogs)
	vm.mu.Unlock()

	// Select tail of steps up to maxSteps for compactness
	start := 0
	if len(steps) > maxSteps {
		start = len(steps) - maxSteps
	}

	// Build digest structure
	type stepDigest struct {
		Name       string `json:"name"`
		ID         string `json:"id"`
		Success    bool   `json:"success"`
		Error      string `json:"error,omitempty"`
		StartAt    int64  `json:"start_at,omitempty"`
		EndAt      int64  `json:"end_at,omitempty"`
		DurationMs int64  `json:"duration_ms,omitempty"`
	}
	d := struct {
		Workflow   string       `json:"workflow"`
		Failed     bool         `json:"failed"`
		FailedStep string       `json:"failed_step,omitempty"`
		Reason     string       `json:"reason,omitempty"`
		LastStep   string       `json:"last_step"`
		Steps      []stepDigest `json:"steps"`
	}{
		Workflow:   workflowName,
		Failed:     failed,
		FailedStep: failedName,
		Reason:     firstLine(failedReason),
		LastStep:   lastName,
		Steps:      make([]stepDigest, 0, len(steps)-start),
	}

	for i := start; i < len(steps); i++ {
		st := steps[i]
		name := st.GetName()
		if name == "" {
			name = st.GetId()
		}
		var dur int64
		if st.GetEndAt() > 0 && st.GetStartAt() > 0 {
			dur = st.GetEndAt() - st.GetStartAt()
		}
		sd := stepDigest{
			Name:       safeName(name),
			ID:         st.GetId(),
			Success:    st.GetSuccess(),
			Error:      firstLine(st.GetError()),
			StartAt:    st.GetStartAt(),
			EndAt:      st.GetEndAt(),
			DurationMs: dur,
		}
		// Avoid including empty error field to keep prompt small
		if sd.Error == "unknown error" {
			sd.Error = ""
		}
		d.Steps = append(d.Steps, sd)
	}

	// Marshal compactly
	b := &bytes.Buffer{}
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(d)
	return strings.TrimSpace(b.String())
}

func stripCodeFences(s string) string {
	trim := strings.TrimSpace(s)
	if strings.HasPrefix(trim, "```") {
		trim = strings.TrimPrefix(trim, "```")
		// remove possible language label up to first newline
		if idx := strings.Index(trim, "\n"); idx >= 0 {
			trim = trim[idx+1:]
		}
		trim = strings.TrimSuffix(trim, "```")
	}
	return strings.TrimSpace(trim)
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func nonZeroOr(val, def int) int {
	if val > 0 {
		return val
	}
	return def
}
