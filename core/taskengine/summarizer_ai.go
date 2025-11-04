package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
// to deterministic ComposeSummary on any failure. The summary is automatically
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
		vm.logger.Info("ComposeSummarySmart: starting AI summarization")
	}

	// Use a modest timeout to allow remote providers to respond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := globalSummarizer.Summarize(ctx, vm, currentStepName)
	if err != nil {
		if vm != nil && vm.logger != nil {
			vm.logger.Warn("ComposeSummarySmart: AI summarization failed, falling back to deterministic", "error", err)
		}
		return ComposeSummary(vm, currentStepName)
	}

	// Validate minimal fields; fall back if empty
	if strings.TrimSpace(s.Subject) == "" || strings.TrimSpace(s.Body) == "" {
		if vm != nil && vm.logger != nil {
			vm.logger.Warn("ComposeSummarySmart: AI summary has empty fields, falling back to deterministic")
		}
		return ComposeSummary(vm, currentStepName)
	}

	if len(strings.TrimSpace(s.Body)) < 40 {
		if vm != nil && vm.logger != nil {
			vm.logger.Warn("ComposeSummarySmart: AI summary body too short, falling back to deterministic", "bodyLength", len(s.Body))
		}
		return ComposeSummary(vm, currentStepName)
	}

	if vm != nil && vm.logger != nil {
		vm.logger.Info("ComposeSummarySmart: AI summarization successful", "subject", s.Subject, "bodyLength", len(s.Body))
	}
	return s
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
		TimeoutMs:           nonZeroOr(c.NotificationsSummary.TimeoutMs, 8000),
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

	if vm != nil && vm.logger != nil {
		vm.logger.Info("OpenAISummarizer.Summarize: building digest")
	}

	// Build compact digest with strict redaction and truncation - add timeout protection
	digestChan := make(chan string, 1)
	go func() {
		digestChan <- o.buildDigest(vm, currentStepName)
	}()

	var digest string
	select {
	case digest = <-digestChan:
		if vm != nil && vm.logger != nil {
			vm.logger.Info("OpenAISummarizer.Summarize: digest built successfully", "digestLength", len(digest))
		}
	case <-ctx.Done():
		if vm != nil && vm.logger != nil {
			vm.logger.Error("OpenAISummarizer.Summarize: buildDigest timed out", "error", ctx.Err())
		}
		return Summary{}, errors.New("digest build timeout")
	}

	// Prepare request payload for chat completions
	// Use response_format json_object to force strict JSON output
	body := map[string]interface{}{
		"model":           o.cfg.Model,
		"temperature":     o.cfg.Temperature,
		"max_tokens":      o.cfg.MaxOutputTokens,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": `You create concise, professional workflow execution summaries for on-chain operations. Summaries are used for both email and chat channels (like Telegram).

Your summary must include:
1. Workflow name and execution status (executed_steps out of total_steps)
2. Smart wallet address that executed the transactions and the owner EOA address it belongs to
3. Clear description of on-chain actions (contract address, method name, what happened)
4. For swaps: token symbols and formatted amounts (e.g., "swapped 0.10 WETH → ~300 USDC via Uniswap")
5. For approvals: token symbol, formatted amount, and spender/contract
6. If the digest indicates a failure (Failed: true), you MUST explicitly state where it failed and the root cause:
   - Include a clear line like: "Failure at {FailedStep}: {Reason}"
   - If Reason is empty, look up the first step with success=false in Steps and use its Error field (first line)
   - Keep the failure explanation short and factual, with the failed step name
7. If multiple steps failed (some branches can allow later steps to succeed, then fail again), include a short bullet list of ALL failed steps:
   - Start a new paragraph with "Failures:" then add one bullet (-) per failed step
   - Bullet format: "- {step_name_or_id}: {first_line_of_error}"
   - Use the 'failures' array from the digest when present; otherwise, derive from Steps where success=false
   - Limit to the first 8 failed steps if there are many
8. Include a bullet list of all SUCCESSFUL contractWrite operations (these change wallet state):
   - Start a new paragraph with "Writes:" then add one bullet (-) per successful write
   - Bullet format: "- {step_name_or_id}: {method} @ {contract_address}{simulated_suffix}"
   - Use the 'writes' array from the digest. If writes[i].simulated is true, append " (Simulated)" to that bullet

Use ONLY the provided data. Do not speculate. Redact secrets. Return strict JSON with keys: subject, body.

Subject format: "{workflow_name}: {status} ({executed_steps} out of {total_steps} steps)" - Keep subject under 80 characters.
Body: 1–2 short, focused paragraphs. Be concise since summaries may be used in chat channels.
- First paragraph: Smart wallet, owner EOA, and key on-chain actions in human-readable terms
- Second paragraph (if needed): Additional context or failure details

Formatting rules:
- Insert a blank line between sentences (double newline) for email readability
- Keep total body under 400 characters when possible for chat channel compatibility
- If the 'actions' array is empty or lacks recognizable actions, include: "No specific on-chain actions were recorded; this may have been a simulation or a step encountered an error."

Prefer the normalized 'actions' array when present.

IMPORTANT RULES:
- Never declare the workflow failed unless the 'Failed' flag is true in the digest.
- A Branch node is NOT a failure. It indicates which path was selected. Use 'branch_selections' and 'executed_steps/total_steps' to explain that some nodes were skipped due to branching when applicable.
- If total_steps > executed_steps, mention that some nodes were skipped due to branching/conditions.
`},
			{"role": "user", "content": digest},
		},
	}

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return Summary{}, err
	}

	if vm != nil && vm.logger != nil {
		vm.logger.Info("OpenAISummarizer.Summarize: creating HTTP request to OpenAI")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", buf)
	if err != nil {
		if vm != nil && vm.logger != nil {
			vm.logger.Error("OpenAISummarizer.Summarize: failed to create request", "error", err)
		}
		return Summary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	if vm != nil && vm.logger != nil {
		vm.logger.Info("OpenAISummarizer.Summarize: sending request to OpenAI API")
	}

	resp, err := o.hc.Do(req)

	if vm != nil && vm.logger != nil {
		if err != nil {
			vm.logger.Error("OpenAISummarizer.Summarize: HTTP request failed", "error", err)
		} else {
			vm.logger.Info("OpenAISummarizer.Summarize: received response from OpenAI", "statusCode", resp.StatusCode)
		}
	}
	if err != nil {
		return Summary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return Summary{}, errors.New("openai non-2xx response: " + string(errBody))
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

	// Get total workflow steps count BEFORE locking (this function locks internally)
	totalWorkflowSteps := getTotalWorkflowSteps(vm)

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
	executedSteps := len(steps)

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
		Workflow         string                   `json:"workflow"`
		SmartWallet      string                   `json:"smart_wallet,omitempty"`
		OwnerEOA         string                   `json:"owner_eoa,omitempty"`
		TotalSteps       int                      `json:"total_steps"`
		ExecutedSteps    int                      `json:"executed_steps"`
		SkippedSteps     int                      `json:"skipped_steps"`
		Status           string                   `json:"status"`
		Failed           bool                     `json:"failed"`
		FailedStep       string                   `json:"failed_step,omitempty"`
		Reason           string                   `json:"reason,omitempty"`
		LastStep         string                   `json:"last_step"`
		Steps            []stepDigest             `json:"steps"`
		Actions          []map[string]interface{} `json:"actions,omitempty"`
		Failures         []map[string]string      `json:"failures,omitempty"`
		Writes           []map[string]interface{} `json:"writes,omitempty"`
		BranchSelections []map[string]interface{} `json:"branch_selections,omitempty"`
	}{
		Workflow:      workflowName,
		SmartWallet:   smartWallet,
		OwnerEOA:      ownerEOA,
		TotalSteps:    totalWorkflowSteps,
		ExecutedSteps: executedSteps,
		SkippedSteps: func() int {
			if totalWorkflowSteps > executedSteps {
				return totalWorkflowSteps - executedSteps
			}
			return 0
		}(),
		Status:           "",
		Failed:           failed,
		FailedStep:       failedName,
		Reason:           firstLine(failedReason),
		LastStep:         lastName,
		Steps:            make([]stepDigest, 0, len(steps)-start),
		Actions:          make([]map[string]interface{}, 0, 4),
		Failures:         make([]map[string]string, 0, 4),
		Writes:           make([]map[string]interface{}, 0, 4),
		BranchSelections: make([]map[string]interface{}, 0, 2),
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
							// Branch details
							if strings.Contains(strings.ToUpper(st.GetType()), "BRANCH") {
								if br := st.GetBranch(); br != nil && br.GetData() != nil {
									if m, ok := br.GetData().AsInterface().(map[string]interface{}); ok {
										entry := map[string]interface{}{"step": sd.Name}
										if cid, ok := m["conditionId"].(string); ok {
											entry["condition_id"] = cid
											// infer if/else by suffix
											if idx := strings.LastIndex(cid, "."); idx >= 0 && idx+1 < len(cid) {
												switch cid[idx+1:] {
												case "0":
													entry["path"] = "if"
												case "1":
													entry["path"] = "else"
												}
											}
										}
										d.BranchSelections = append(d.BranchSelections, entry)
									}
								}
							}
							// Detect simulation flag when present on writes
							if isWrite {
								isSimulated := false
								if sim, ok := cfgMap["isSimulated"]; ok {
									switch v := sim.(type) {
									case bool:
										isSimulated = v
									case string:
										if strings.EqualFold(strings.TrimSpace(v), "true") {
											isSimulated = true
										}
									}
								}
								// If this write succeeded, record it for summary
								if st.GetSuccess() {
									d.Writes = append(d.Writes, map[string]interface{}{
										"step":      safeName(name),
										"contract":  sd.ContractAddr,
										"method":    sd.MethodName,
										"simulated": isSimulated,
									})
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
		if !st.GetSuccess() {
			// Capture all failures with compact fields for the model
			failName := safeName(name)
			failErr := firstLine(st.GetError())
			if strings.TrimSpace(failErr) == "" {
				failErr = "unknown error"
			}
			d.Failures = append(d.Failures, map[string]string{
				"step":  failName,
				"error": failErr,
			})
		}
	}

	// Set status text and notification-only flag
	if failed {
		if d.FailedStep != "" {
			d.Status = "failed at " + d.FailedStep
		} else {
			d.Status = "failed"
		}
	} else {
		d.Status = "succeeded"
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

// FormatSummaryForChannel converts an email-oriented Summary into a concise chat message
// suitable for channels like Telegram or Discord. It keeps the most important facts in a
// single short paragraph with appropriate formatting for the target channel.
func FormatSummaryForChannel(s Summary, channel string) string {
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
		// For Telegram, add bold subject line and preserve HTML-safe formatting
		// The subject is already short (under 80 chars per AI prompt)
		if subject != "" && !strings.Contains(msg, subject) {
			return "<b>" + subject + "</b>\n" + msg
		}
		return msg
	case "discord":
		// For Discord, use markdown bold
		if subject != "" && !strings.Contains(msg, subject) {
			return "**" + subject + "**\n" + msg
		}
		return msg
	default:
		return msg
	}
}
