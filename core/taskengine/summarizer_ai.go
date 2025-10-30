package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

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

	// Use a modest timeout to allow remote providers to respond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	apiKey := firstNonEmptyStr(c.MacroSecrets["openai_api_key"], os.Getenv("OPENAI_API_KEY")) // prefer config secret; fallback to env
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
			{"role": "system", "content": `You create concise, professional workflow execution email summaries for on-chain operations. Your summary must include:

1. Workflow name and total steps executed
2. Smart wallet address that executed the transactions and the owner EOA address it belongs to
3. Detailed explanation of what smart contract calls were made (contract address, method name, what action was performed)
4. For swaps: explain token amounts (e.g., "spent X token1 for Y token0")
5. For approvals: explain what token was approved and to which contract

Be specific about on-chain state changes. Do not speculate. Redact any secrets. Return strict JSON with keys: subject, body.

Subject format: "{workflow_name}: {status} ({N} steps)"
Body format: A clear paragraph explaining the workflow execution, smart wallet operations, and on-chain changes.

Formatting rules:
- Insert a blank line between sentences (double newline) to increase readability.
- If the provided 'actions' array is empty or there are no recognizable on-chain actions, include a sentence:
  "No specific on-chain actions were recorded; this may have been a simulation or a step encountered an error."

Note: The input includes a normalized 'actions' array summarizing key on-chain actions (approve/quote/swap). Prefer these for accuracy.`},
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

	// Post-process body to ensure double newlines between sentences
	s.Body = ensureDoubleNewlines(s.Body)
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

	// Extract smart wallet and owner info
	vm.mu.Lock()
	smartWallet := ""
	ownerEOA := ""

	// Try aa_sender first (smart wallet address)
	if aaSender, ok := vm.vars["aa_sender"].(string); ok && aaSender != "" {
		smartWallet = aaSender
	}

	// Try workflowContext.runner (also smart wallet)
	if wfCtx, ok := vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
		if runner, ok := wfCtx["runner"].(string); ok && runner != "" && smartWallet == "" {
			smartWallet = runner
		}
		if owner, ok := wfCtx["owner"].(string); ok && owner != "" {
			ownerEOA = owner
		}
		if eoa, ok := wfCtx["eoaAddress"].(string); ok && eoa != "" && ownerEOA == "" {
			ownerEOA = eoa
		}
	}

	// Also check settings.runner for single-node executions
	if smartWallet == "" {
		if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
			if runner, ok := settings["runner"].(string); ok && strings.TrimSpace(runner) != "" {
				smartWallet = runner
			}
		}
	}

	// Fallback to TaskOwner if available
	if ownerEOA == "" && vm.TaskOwner != (common.Address{}) {
		ownerEOA = vm.TaskOwner.Hex()
	}

	steps := make([]*avsproto.Execution_Step, len(vm.ExecutionLogs))
	copy(steps, vm.ExecutionLogs)
	vm.mu.Unlock()

	// Select tail of steps up to maxSteps for compactness
	start := 0
	if len(steps) > maxSteps {
		start = len(steps) - maxSteps
	}

	// Build digest structure with contract call details
	type stepDigest struct {
		Name         string                 `json:"name"`
		ID           string                 `json:"id"`
		Type         string                 `json:"type"`
		Success      bool                   `json:"success"`
		Error        string                 `json:"error,omitempty"`
		StartAt      int64                  `json:"start_at,omitempty"`
		EndAt        int64                  `json:"end_at,omitempty"`
		DurationMs   int64                  `json:"duration_ms,omitempty"`
		ContractAddr string                 `json:"contract_address,omitempty"`
		MethodName   string                 `json:"method_name,omitempty"`
		MethodParams map[string]interface{} `json:"method_params,omitempty"`
		OutputData   interface{}            `json:"output_data,omitempty"`
		Metadata     interface{}            `json:"metadata,omitempty"`
	}
	d := struct {
		Workflow    string                   `json:"workflow"`
		SmartWallet string                   `json:"smart_wallet,omitempty"`
		OwnerEOA    string                   `json:"owner_eoa,omitempty"`
		TotalSteps  int                      `json:"total_steps"`
		Failed      bool                     `json:"failed"`
		FailedStep  string                   `json:"failed_step,omitempty"`
		Reason      string                   `json:"reason,omitempty"`
		LastStep    string                   `json:"last_step"`
		Steps       []stepDigest             `json:"steps"`
		Actions     []map[string]interface{} `json:"actions,omitempty"`
	}{
		Workflow:    workflowName,
		SmartWallet: smartWallet,
		OwnerEOA:    ownerEOA,
		TotalSteps:  len(steps),
		Failed:      failed,
		FailedStep:  failedName,
		Reason:      firstLine(failedReason),
		LastStep:    lastName,
		Steps:       make([]stepDigest, 0, len(steps)-start),
		Actions:     make([]map[string]interface{}, 0, 4),
	}

	// If this is a single-node immediate VM, reflect at least one in-flight step
	// even if the execution log hasn't been appended yet.
	if d.TotalSteps == 0 && isSingleNodeImmediate(vm) {
		d.TotalSteps = 1
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
			Type:       st.GetType(),
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

		// Extract contract call details for contractWrite and contractRead
		t := strings.ToUpper(st.GetType())
		isWrite := strings.Contains(t, "CONTRACT_WRITE")
		isRead := strings.Contains(t, "CONTRACT_READ")
		if isWrite || isRead {
			// Extract contract address from config
			if st.GetConfig() != nil {
				if cfgMap, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
					if addr, ok := cfgMap["contractAddress"].(string); ok && addr != "" {
						sd.ContractAddr = addr
					}
					// Extract method calls
					if methodCalls, ok := cfgMap["methodCalls"].([]interface{}); ok && len(methodCalls) > 0 {
						if firstCall, ok := methodCalls[0].(map[string]interface{}); ok {
							if mName, ok := firstCall["methodName"].(string); ok {
								sd.MethodName = mName
							}
							if mParams, ok := firstCall["methodParams"].([]interface{}); ok && len(mParams) > 0 {
								sd.MethodParams = map[string]interface{}{
									"params": mParams,
								}
							}
						}
					}
				}
			}

			// Extract output data
			switch {
			case st.GetContractWrite() != nil && st.GetContractWrite().Data != nil:
				sd.OutputData = st.GetContractWrite().Data.AsInterface()
				if st.Metadata != nil {
					sd.Metadata = st.Metadata.AsInterface()
				}
			case st.GetContractRead() != nil && st.GetContractRead().Data != nil:
				sd.OutputData = st.GetContractRead().Data.AsInterface()
				if st.Metadata != nil {
					sd.Metadata = st.Metadata.AsInterface()
				}
			}
			// Build normalized actions for the model
			lowerMethod := strings.ToLower(sd.MethodName)
			switch lowerMethod {
			case "approve":
				action := map[string]interface{}{
					"type":     "approve",
					"contract": map[string]interface{}{"address": sd.ContractAddr},
				}
				if sd.MethodParams != nil {
					action["params"] = sd.MethodParams
				}
				if out, ok := sd.OutputData.(map[string]interface{}); ok {
					if ev, ok2 := out["Approval"].(map[string]interface{}); ok2 {
						if v, ok3 := ev["value"]; ok3 {
							action["amount"] = v
						}
					}
				}
				d.Actions = append(d.Actions, action)
			case "quoteexactinputsingle":
				action := map[string]interface{}{
					"type":     "quote",
					"contract": map[string]interface{}{"address": sd.ContractAddr},
				}
				if out, ok := sd.OutputData.(map[string]interface{}); ok {
					if q, ok2 := out["quoteExactInputSingle"].(map[string]interface{}); ok2 {
						if v, ok3 := q["amountOut"]; ok3 {
							action["amountOut"] = v
						}
					}
				}
				d.Actions = append(d.Actions, action)
			case "exactinputsingle":
				action := map[string]interface{}{
					"type":     "swap",
					"contract": map[string]interface{}{"address": sd.ContractAddr},
				}
				if isWrite && sd.Metadata != nil {
					if arr, ok := sd.Metadata.([]interface{}); ok && len(arr) > 0 {
						if first, ok := arr[0].(map[string]interface{}); ok {
							if val, ok := first["value"].(map[string]interface{}); ok {
								if amt, ok := val["amountOut"].(string); ok && amt != "" {
									action["amountOut"] = amt
								}
							} else if amtStr, ok := first["value"].(string); ok && amtStr != "" {
								action["amountOut"] = amtStr
							}
						}
					}
				}
				if isRead && sd.OutputData != nil {
					if out, ok := sd.OutputData.(map[string]interface{}); ok {
						if q, ok2 := out["exactInputSingle"].(map[string]interface{}); ok2 {
							if v, ok3 := q["amountOut"]; ok3 {
								action["amountOut"] = v
							}
						}
					}
				}
				d.Actions = append(d.Actions, action)
			}
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

// ensureDoubleNewlines inserts a blank line between sentences.
// It replaces a single space after sentence terminators with two newlines
// while preserving existing double newlines.
func ensureDoubleNewlines(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	// Normalize Windows newlines
	t := strings.ReplaceAll(text, "\r\n", "\n")
	// Don't break existing double newlines
	// First, collapse any triple+ newlines to double
	t = regexp.MustCompile("\n{3,}").ReplaceAllString(t, "\n\n")
	// Insert double newlines after typical sentence endings if not already followed by a newline
	t = regexp.MustCompile(`([.!?])\s+(\S)`).ReplaceAllString(t, "$1\n\n$2")
	return t
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
