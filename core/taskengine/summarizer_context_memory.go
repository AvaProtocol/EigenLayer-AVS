package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
)

// ContextMemorySummarizer implements Summarizer by posting to the workflow-summary
// endpoint (Studio's /api/summarize; the standalone context-memory service is deprecated).
type ContextMemorySummarizer struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
}

// NewContextMemorySummarizer creates a summarizer that posts to {baseURL}/api/summarize.
// baseURL MUST be a bare origin (no "/api/summarize" path and no trailing slash) — the
// client appends the path itself; including it would double the path. No default is
// applied: the origin must be supplied explicitly (production wiring fails fast at
// startup when it is missing, see NewContextMemorySummarizerFromAggregatorConfig).
func NewContextMemorySummarizer(baseURL, authToken string) Summarizer {
	return &ContextMemorySummarizer{
		baseURL:    baseURL,
		authToken:  authToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SummarizeRequest matches the TypeScript interface for /api/summarize
type contextMemorySummarizeRequest struct {
	OwnerEOA        string                                 `json:"ownerEOA"`
	Name            string                                 `json:"name"`
	SmartWallet     string                                 `json:"smartWallet"`
	Steps           []contextMemoryStepDigest              `json:"steps"`
	Status          string                                 `json:"status"`         // "success" | "failed" | "error" — aggregator's execution verdict
	ExecutionError  string                                 `json:"executionError"` // Empty string on success; non-empty on failed/error
	ChainName       string                                 `json:"chainName,omitempty"`
	Nodes           []contextMemoryNodeDef                 `json:"nodes,omitempty"`
	Edges           []contextMemoryEdgeDef                 `json:"edges,omitempty"`
	Settings        map[string]interface{}                 `json:"settings,omitempty"`
	CurrentNodeName string                                 `json:"currentNodeName,omitempty"`
	TokenMetadata   map[string]*contextMemoryTokenMetadata `json:"tokenMetadata,omitempty"` // All tokens involved, keyed by address (lowercase)
	RunNumber       int64                                  `json:"runNumber,omitempty"`     // 1-based run number for real executions; ignored when isSimulation is true
}

type contextMemoryStepDigest struct {
	Name             string                      `json:"name"`
	ID               string                      `json:"id"`
	Type             string                      `json:"type"`
	Success          bool                        `json:"success"`
	Error            string                      `json:"error,omitempty"`
	Config           interface{}                 `json:"config,omitempty"`     // Full config (trigger or node) - unified field for all types
	OutputData       interface{}                 `json:"outputData,omitempty"` // Full output (all 15 types)
	Metadata         interface{}                 `json:"metadata,omitempty"`
	ExecutionContext interface{}                 `json:"executionContext,omitempty"` // Actual execution mode (is_simulated, provider, chain_id)
	TokenMetadata    *contextMemoryTokenMetadata `json:"tokenMetadata,omitempty"`    // Token info for the contract (symbol, decimals)
}

// contextMemoryTokenMetadata contains ERC20 token information for formatting
type contextMemoryTokenMetadata struct {
	Symbol   string `json:"symbol"`
	Decimals uint32 `json:"decimals"`
	Name     string `json:"name,omitempty"`
}

type contextMemoryNodeDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type contextMemoryEdgeDef struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
}

// contextMemoryTransferInfo matches the API response structure for transfers
type contextMemoryTransferInfo struct {
	StepName    string `json:"stepName"`
	Type        string `json:"type"`
	From        string `json:"from"`
	To          string `json:"to"`
	RawAmount   string `json:"rawAmount"`
	Amount      string `json:"amount"`
	Symbol      string `json:"symbol"`
	Decimals    int    `json:"decimals"`
	TxHash      string `json:"txHash"`
	IsSimulated bool   `json:"isSimulated"`
}

// contextMemoryBalanceInfo matches the API response structure for balances
type contextMemoryBalanceInfo struct {
	StepName         string `json:"stepName"`
	TokenAddress     string `json:"tokenAddress"`
	Symbol           string `json:"symbol"`
	Name             string `json:"name"`
	Balance          string `json:"balance"`
	BalanceFormatted string `json:"balanceFormatted"`
	Decimals         int    `json:"decimals"`
}

// contextMemoryWorkflowInfo matches the API response structure for workflow metadata
type contextMemoryWorkflowInfo struct {
	Name         string `json:"name"`
	Chain        string `json:"chain"`
	ChainID      int64  `json:"chainId"`
	IsSimulation bool   `json:"isSimulation"`
	RunNumber    *int64 `json:"runNumber"`
}

// contextMemoryExecutionEntry matches the API response structure for execution entries.
// Each entry has a description and an optional txHash for on-chain transactions.
type contextMemoryExecutionEntry struct {
	Description string `json:"description"`
	TxHash      string `json:"txHash,omitempty"`
}

// contextMemorySummarizeBody contains the structured workflow execution summary
// The aggregator is responsible for rendering this into email HTML or Telegram format
type contextMemorySummarizeBody struct {
	Summary       string                        `json:"summary"`               // One-line execution summary (no skip-note suffix)
	Status        string                        `json:"status"`                // "success" | "failed" | "error"
	Network       string                        `json:"network"`               // Chain name (e.g., "Sepolia", "Ethereum")
	Trigger       string                        `json:"trigger"`               // What triggered the workflow (text description)
	TriggeredAt   string                        `json:"triggeredAt"`           // ISO 8601 timestamp (from trigger output)
	Executions    []contextMemoryExecutionEntry `json:"executions"`            // On-chain operations only (no BRANCH entries)
	Errors        []string                      `json:"errors"`                // Failed steps and skipped node descriptions
	SkippedNote   string                        `json:"skippedNote,omitempty"` // "1 node was skipped by Branch condition." — present only when status=success && skippedSteps>0
	ExecutedSteps int                           `json:"executedSteps"`         // Number of steps that actually executed
	TotalSteps    int                           `json:"totalSteps"`            // Executed + skipped-by-branch
	SkippedSteps  int                           `json:"skippedSteps"`          // 0 when nothing was skipped

	// Enhanced structured data for rich notifications (kept for potential future use)
	Transfers []contextMemoryTransferInfo `json:"transfers,omitempty"` // Transfer details
	Balances  []contextMemoryBalanceInfo  `json:"balances,omitempty"`  // Balance snapshots
	Workflow  *contextMemoryWorkflowInfo  `json:"workflow,omitempty"`  // Workflow metadata
}

// SummarizeResponse matches the TypeScript SummarizeResponse
type contextMemorySummarizeResponse struct {
	Subject       string                     `json:"subject"`
	Body          contextMemorySummarizeBody `json:"body"`
	PromptVersion string                     `json:"promptVersion"`
	Cached        bool                       `json:"cached,omitempty"`
}

func (c *ContextMemorySummarizer) Summarize(ctx context.Context, vm *VM, currentStepName string) (Summary, error) {
	if c == nil || c.httpClient == nil {
		return Summary{}, fmt.Errorf("summarizer not initialized")
	}

	// Compute execution verdict BEFORE buildRequest acquires vm.mu — AnalyzeExecutionResult
	// takes the same lock and sync.Mutex is not reentrant.
	//
	// Empty ExecutionLogs is the single-node RunNodeImmediately case: the only
	// step is the notification node currently running and therefore not in
	// ExecutionLogs yet. AnalyzeExecutionResult treats that as "no execution
	// steps found" / ExecutionFailed, which would emit a bogus status=failed
	// to context-memory. Mirror the deterministic path and treat empty logs as
	// success — nothing has failed yet.
	var status, executionError string
	if len(vm.ExecutionLogs) == 0 {
		status = "success"
	} else {
		var resultStatus ExecutionResultStatus
		executionError, _, resultStatus = vm.AnalyzeExecutionResult()
		status = mapExecutionStatusToAPIString(resultStatus)
	}

	// Build request from VM
	req, err := c.buildRequest(vm, currentStepName, status, executionError)
	if err != nil {
		// Include the specific validation error to help with debugging
		return Summary{}, fmt.Errorf("failed to build request (validation error): %w", err)
	}

	// Marshal request
	reqBody, err := json.Marshal(req)
	if err != nil {
		return Summary{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/summarize", bytes.NewBuffer(reqBody))
	if err != nil {
		return Summary{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	// Log request details
	if vm != nil && vm.logger != nil {
		vm.logger.Info("Context-memory API: sending request", "url", c.baseURL+"/api/summarize", "request_size", len(reqBody))
	}

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Log DEBUG level for fallback operations (reduces log clutter in production)
		if vm != nil && vm.logger != nil {
			vm.logger.Debug("Context-memory API not available: HTTP request failed", "error", err, "url", c.baseURL+"/api/summarize")
		}
		return Summary{}, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		// Log DEBUG level for fallback operations (reduces log clutter in production)
		if vm != nil && vm.logger != nil {
			vm.logger.Debug("Context-memory API not available: non-2xx response", "status_code", resp.StatusCode, "response_body", string(body), "url", c.baseURL+"/api/summarize")
		}
		return Summary{}, fmt.Errorf("non-2xx response (%d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp contextMemorySummarizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		if vm != nil && vm.logger != nil {
			vm.logger.Info("Context-memory API: failed to decode response", "error", err)
		}
		return Summary{}, fmt.Errorf("failed to decode response: %w", err)
	}

	// Log successful response
	if vm != nil && vm.logger != nil {
		vm.logger.Info("Context-memory API: received successful response", "subject", apiResp.Subject, "status", apiResp.Body.Status)
	}

	// Convert API response transfers to Summary transfers
	var transfers []TransferInfo
	if len(apiResp.Body.Transfers) > 0 {
		transfers = make([]TransferInfo, len(apiResp.Body.Transfers))
		for i, t := range apiResp.Body.Transfers {
			transfers[i] = TransferInfo{
				StepName:    t.StepName,
				Type:        t.Type,
				From:        t.From,
				To:          t.To,
				RawAmount:   t.RawAmount,
				Amount:      t.Amount,
				Symbol:      t.Symbol,
				Decimals:    t.Decimals,
				TxHash:      t.TxHash,
				IsSimulated: t.IsSimulated,
			}
		}
	}

	// Convert API response balances to Summary balances
	var balances []BalanceInfo
	if len(apiResp.Body.Balances) > 0 {
		balances = make([]BalanceInfo, len(apiResp.Body.Balances))
		for i, b := range apiResp.Body.Balances {
			balances[i] = BalanceInfo{
				StepName:         b.StepName,
				TokenAddress:     b.TokenAddress,
				Symbol:           b.Symbol,
				Name:             b.Name,
				Balance:          b.Balance,
				BalanceFormatted: b.BalanceFormatted,
				Decimals:         b.Decimals,
			}
		}
	}

	// Workflow metadata: the aggregator owns IsSimulation since it ran the
	// workflow — the API response's value (if any) is informational only.
	// Always populate so renderers can rely on the flag without nil-checking
	// against API response shape drift.
	workflow := &WorkflowInfo{IsSimulation: vm.IsSimulation}
	if apiResp.Body.Workflow != nil {
		workflow.Name = apiResp.Body.Workflow.Name
		workflow.Chain = apiResp.Body.Workflow.Chain
		workflow.ChainID = apiResp.Body.Workflow.ChainID
		workflow.RunNumber = apiResp.Body.Workflow.RunNumber
	}

	// Convert API execution entries to Summary execution entries
	// Validate txHash at ingestion: only keep hashes that look like real
	// Ethereum tx hashes (0x + 64 hex chars). This filters out Tenderly
	// simulation IDs and other non-hash values the API may return.
	var executions []ExecutionEntry
	for _, e := range apiResp.Body.Executions {
		entry := ExecutionEntry{Description: e.Description}
		if isValidTxHash(e.TxHash) {
			entry.TxHash = e.TxHash
		}
		executions = append(executions, entry)
	}

	// Runner / Fees are aggregator-local (not from the API response). Both helpers
	// read VM state directly so notifications surface the same fee numbers as
	// EstimateFees() and the persisted Execution.Fee. See PRD:
	// docs/changes/20260501-summary-runner-and-fees-sections.md.
	return Summary{
		Subject:       apiResp.Subject,
		Body:          composePlainTextBodyFromAPI(apiResp.Body),
		SummaryLine:   apiResp.Body.Summary,
		Status:        apiResp.Body.Status,
		Network:       apiResp.Body.Network,
		Trigger:       apiResp.Body.Trigger,
		TriggeredAt:   apiResp.Body.TriggeredAt,
		Executions:    executions,
		Errors:        apiResp.Body.Errors,
		SkippedNote:   apiResp.Body.SkippedNote,
		ExecutedSteps: apiResp.Body.ExecutedSteps,
		TotalSteps:    apiResp.Body.TotalSteps,
		SkippedSteps:  apiResp.Body.SkippedSteps,
		SmartWallet:   req.SmartWallet,
		Transfers:     transfers,
		Balances:      balances,
		Workflow:      workflow,
		Runner:        buildRunnerFromVM(vm),
		Fees:          buildFeesFromVM(vm),
	}, nil
}

// composePlainTextBodyFromAPI creates a plain text body from the structured API response
// This is used for backward compatibility with channels that expect plain text
// NOTE: Does NOT include summary line - that's in the separate SummaryLine field
func composePlainTextBodyFromAPI(body contextMemorySummarizeBody) string {
	var sb strings.Builder

	// Trigger (don't include summary - it's in SummaryLine field)
	if body.Trigger != "" {
		sb.WriteString("Trigger: ")
		sb.WriteString(body.Trigger)
		sb.WriteString("\n\n")
	}

	// Executions
	if len(body.Executions) > 0 {
		sb.WriteString("Executed:\n")
		for _, exec := range body.Executions {
			sb.WriteString("- ")
			sb.WriteString(exec.Description)
			sb.WriteString("\n")
		}
		if len(body.Errors) > 0 {
			sb.WriteString("\n")
		}
	}

	// Errors
	if len(body.Errors) > 0 {
		sb.WriteString("Issues:\n")
		for _, err := range body.Errors {
			sb.WriteString("- ")
			sb.WriteString(err)
			sb.WriteString("\n")
		}
	}

	return strings.TrimSpace(sb.String())
}

func (c *ContextMemorySummarizer) buildRequest(vm *VM, currentStepName, status, executionError string) (*contextMemorySummarizeRequest, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Read task fields directly from vm.task.
	var ownerEOA, smartWallet, workflowName, chainName string
	var executionCount int64

	if vm.task != nil && vm.task.Task != nil {
		ownerEOA = vm.task.Owner
		smartWallet = vm.task.SmartWalletAddress
		workflowName = vm.task.Name
		executionCount = int64(vm.task.ExecutionCount)
	}

	// Fallback: read chain from settings (chain is not on Task protobuf)
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if chain, ok := settings["chain"].(string); ok {
			chainName = chain
		}
		// Fallback for single-node executions (RunNodeImmediately) where vm.task may be nil
		if workflowName == "" {
			if name, ok := settings["name"].(string); ok && strings.TrimSpace(name) != "" {
				workflowName = name
			}
		}
		if smartWallet == "" {
			if runner, ok := settings["runner"].(string); ok && strings.TrimSpace(runner) != "" {
				smartWallet = runner
			}
		}
	}

	// Fallback to TaskOwner if available
	if ownerEOA == "" && vm.TaskOwner != (common.Address{}) {
		ownerEOA = vm.TaskOwner.Hex()
	}

	// Validate that workflow name is set (required - no fallbacks)
	if workflowName == "" {
		return nil, fmt.Errorf("workflow name is required in settings.name")
	}

	// Get trigger definition for config extraction
	var trigger *avsproto.TaskTrigger
	if vm.task != nil && vm.task.Task != nil {
		trigger = vm.task.Task.Trigger
	}

	// Workflow-level chain ID — routes token metadata lookups to the correct
	// chain's TokenEnrichmentService. Persisted task chain wins; the engine's
	// resolved smart-wallet chain is the next-best signal; the SDK-provided
	// settings.chain_id is the last fallback (e.g. RunNodeImmediately where
	// vm.task is nil). vm.mu is already held — do not call chainIDFromVM,
	// which re-locks.
	var workflowChainID uint64
	if vm.task != nil && vm.task.Task != nil && vm.task.Task.ChainId > 0 {
		workflowChainID = uint64(vm.task.Task.ChainId)
	}
	if workflowChainID == 0 && vm.smartWalletConfig != nil && vm.smartWalletConfig.ChainID > 0 {
		workflowChainID = uint64(vm.smartWalletConfig.ChainID)
	}
	if workflowChainID == 0 {
		if rawSettings, ok := vm.vars["settings"].(map[string]interface{}); ok {
			workflowChainID = chainIDFromSettingsValue(rawSettings["chain_id"])
		}
	}

	// Convert execution logs to steps using the new extraction functions
	steps := make([]contextMemoryStepDigest, 0, len(vm.ExecutionLogs))
	for _, log := range vm.ExecutionLogs {
		step := contextMemoryStepDigest{
			Name:             log.GetName(),
			ID:               log.GetId(),
			Type:             log.GetType(),
			Success:          log.GetSuccess(),
			Config:           ExtractStepConfig(log, vm.TaskNodes, trigger), // Full config via extraction function
			OutputData:       ExtractStepOutput(log),                        // All 15 output types via extraction function
			Metadata:         nil,
			ExecutionContext: nil,
		}
		if log.GetError() != "" {
			step.Error = log.GetError()
		}

		// Extract Metadata
		if log.GetMetadata() != nil {
			step.Metadata = log.GetMetadata().AsInterface()
		}

		// Extract ExecutionContext (actual execution mode: is_simulated, provider, chain_id)
		if log.GetExecutionContext() != nil {
			if ctxInterface := log.GetExecutionContext().AsInterface(); ctxInterface != nil {
				step.ExecutionContext = ctxInterface
			}
		}

		// Token metadata lookup for ERC20 contract interactions
		// Extract contract address and method name from config for token lookup
		if configMap, ok := step.Config.(map[string]interface{}); ok {
			contractAddress := ""
			methodName := ""

			// Extract contractAddress from config
			if addr, ok := configMap["contractAddress"].(string); ok {
				contractAddress = addr
			}

			// Extract methodName from methodCalls[0] if present
			if methodCalls, ok := configMap["methodCalls"].([]interface{}); ok && len(methodCalls) > 0 {
				if firstCall, ok := methodCalls[0].(map[string]interface{}); ok {
					if name, ok := firstCall["methodName"].(string); ok {
						methodName = name
					}
				}
			}

			// Fallback for LOOP steps: check runner.config for contractAddress and methodName
			if contractAddress == "" && methodName == "" {
				if runner, ok := configMap["runner"].(map[string]interface{}); ok {
					if runnerConfig, ok := runner["config"].(map[string]interface{}); ok {
						if addr, ok := runnerConfig["contractAddress"].(string); ok {
							contractAddress = addr
						}
						if methodCalls, ok := runnerConfig["methodCalls"].([]interface{}); ok && len(methodCalls) > 0 {
							if firstCall, ok := methodCalls[0].(map[string]interface{}); ok {
								if name, ok := firstCall["methodName"].(string); ok {
									methodName = name
								}
							}
						}
					}
				}
			}

			// If contractAddress is a template variable, try to extract resolved address from metadata
			resolvedContractAddress := contractAddress
			if strings.Contains(contractAddress, "{{") || contractAddress == "" {
				resolvedContractAddress = extractResolvedContractAddress(log)

				// Fallback for LOOP steps: step-level metadata is nil, but the
				// iteration output may contain metadata with the resolved address.
				// Loop output is []interface{} where each element has a "metadata" key.
				if resolvedContractAddress == "" {
					resolvedContractAddress = extractResolvedContractAddressFromLoopOutput(log)
				}
			}

			// Look up token metadata for the contract address (for ERC20 tokens only).
			// Resolve via the chain-keyed registry so a Sepolia USDC address is looked up
			// against the Sepolia whitelist/RPC, not whichever chain the gateway happens
			// to have as its default (chains[0] = Ethereum mainnet in production). The
			// step's execution context is the authoritative chain source — it records
			// the chain a step actually ran against — with the workflow chain as fallback.
			// resolveTokenServiceForChain falls back to the legacy global when no chain
			// service is registered (single-chain mode, or tests that bypass the registry).
			if resolvedContractAddress != "" && !strings.Contains(resolvedContractAddress, "{{") && common.IsHexAddress(resolvedContractAddress) && isERC20Method(methodName) {
				stepChainID := chainIDFromExecutionContext(step.ExecutionContext)
				if stepChainID == 0 {
					stepChainID = workflowChainID
				}
				// Route through resolveTokenMetadataWithCatalog so the per-step
				// metadata picks up the cross-chain catalog fallback when the
				// bound TokenEnrichmentService returns UNKNOWN. Without this
				// the request payload ships {Symbol:"UNKNOWN", Decimals:18} at
				// the step level for cross-chain dev scenarios, which is
				// harmless today (resolveTokenMeta on the TS side resolves
				// via the top-level tokenMetadata map first) but is the kind
				// of staleness that masks real bugs in the long run.
				// vm is dereferenced unconditionally at the top of
				// buildRequest (vm.mu.Lock()), so it is non-nil here.
				if meta := resolveTokenMetadataWithCatalog(resolveTokenServiceForChain(stepChainID), resolvedContractAddress, stepChainID, vm.logger); meta != nil {
					step.TokenMetadata = meta
				}
			}
		}

		steps = append(steps, step)
	}

	// Convert TaskNodes to nodes
	nodes := make([]contextMemoryNodeDef, 0, len(vm.TaskNodes))
	for nodeID, node := range vm.TaskNodes {
		if node == nil {
			continue
		}
		// Skip branch condition pseudo-nodes
		if len(nodeID) > 0 && nodeID[0] == '.' {
			continue
		}
		nodes = append(nodes, contextMemoryNodeDef{
			ID:   nodeID,
			Name: node.GetName(),
		})
	}

	// Convert edges (if available)
	edges := make([]contextMemoryEdgeDef, 0)
	if vm.task != nil && vm.task.Task != nil && vm.task.Task.Edges != nil {
		for _, edge := range vm.task.Task.Edges {
			edges = append(edges, contextMemoryEdgeDef{
				ID:     edge.GetId(),
				Source: edge.GetSource(),
				Target: edge.GetTarget(),
			})
		}
	}

	var settings map[string]interface{}
	if s, ok := vm.vars["settings"].(map[string]interface{}); ok {
		// Make a copy to avoid modifying the original
		settings = make(map[string]interface{})
		for k, v := range s {
			settings[k] = v
		}
	} else {
		settings = make(map[string]interface{})
	}

	// Always include isSimulation flag from VM
	settings["isSimulation"] = vm.IsSimulation

	// Collect all token metadata into a request-level map (keyed by lowercase address)
	tokenMetadataMap := make(map[string]*contextMemoryTokenMetadata)

	// 1. Collect from per-step tokenMetadata (already populated above)
	for _, step := range steps {
		if step.TokenMetadata != nil {
			// Extract contractAddress from step.Config
			contractAddress := ""
			if configMap, ok := step.Config.(map[string]interface{}); ok {
				if addr, ok := configMap["contractAddress"].(string); ok {
					contractAddress = addr
				}
			}

			// Use resolved address from metadata if contractAddress is a template variable
			// This ensures we use the actual contract address that was used in the transaction
			resolvedAddr := contractAddress
			if strings.Contains(contractAddress, "{{") || contractAddress == "" {
				// Extract resolved address from step.Metadata (same data we used earlier)
				resolvedAddr = extractResolvedContractAddressFromMetadata(step.Metadata)
			}

			// Use resolved address as key, fallback to contractAddress if resolution failed
			addr := resolvedAddr
			if addr == "" {
				addr = contractAddress
			}

			// Only add to map if we have a valid Ethereum address (not a template variable)
			if addr != "" && !strings.Contains(addr, "{{") && common.IsHexAddress(addr) {
				tokenMetadataMap[strings.ToLower(addr)] = step.TokenMetadata
			}
		}
	}

	// 2. Collect from settings.uniswapv3_pool.tokens (input, output, base, quote)
	if pool, ok := settings["uniswapv3_pool"].(map[string]interface{}); ok {
		if tokens, ok := pool["tokens"].(map[string]interface{}); ok {
			tokenService := resolveTokenServiceForChain(workflowChainID)
			var lg sdklogging.Logger
			if vm != nil {
				lg = vm.logger
			}
			for tokenKey, tokenAddr := range tokens {
				if addr, ok := tokenAddr.(string); ok && common.IsHexAddress(addr) {
					addrLower := strings.ToLower(addr)
					if _, exists := tokenMetadataMap[addrLower]; !exists {
						if meta := resolveTokenMetadataWithCatalog(tokenService, addr, workflowChainID, lg); meta != nil {
							tokenMetadataMap[addrLower] = meta
						} else if lg != nil {
							lg.Debug("Failed to fetch token metadata", "address", addr, "tokenKey", tokenKey)
						}
					}
				}
			}
		}
	}

	// 3. Collect from settings.tokens — explicit list of token addresses declared by the workflow.
	// This is the preferred way for workflows to declare which tokens they interact with,
	// ensuring context-memory always has the metadata needed for decimal formatting.
	if tokens, ok := settings["tokens"].([]interface{}); ok {
		tokenService := resolveTokenServiceForChain(workflowChainID)
		var lg sdklogging.Logger
		if vm != nil {
			lg = vm.logger
		}
		for _, t := range tokens {
			if addr, ok := t.(string); ok && common.IsHexAddress(addr) {
				addrLower := strings.ToLower(addr)
				if _, exists := tokenMetadataMap[addrLower]; !exists {
					if meta := resolveTokenMetadataWithCatalog(tokenService, addr, workflowChainID, lg); meta != nil {
						tokenMetadataMap[addrLower] = meta
					} else if lg != nil {
						lg.Debug("Failed to fetch token metadata from settings.tokens", "address", addr)
					}
				}
			}
		}
	}

	return &contextMemorySummarizeRequest{
		OwnerEOA:        ownerEOA,
		Name:            workflowName,
		SmartWallet:     smartWallet,
		Steps:           steps,
		Status:          status,
		ExecutionError:  executionError,
		ChainName:       chainName,
		Nodes:           nodes,
		Edges:           edges,
		Settings:        settings,
		CurrentNodeName: currentStepName,
		TokenMetadata:   tokenMetadataMap,
		RunNumber:       executionCount, // 1-based for real executions; ignored when isSimulation is true
	}, nil
}

// resolveTokenServiceForChain prefers the per-chain registered service so
// gateway mode picks the right whitelist/RPC, but falls back to the legacy
// global service when none is registered for the chain. The fallback covers:
//   - single-chain mode (registry has one entry whose chain matches by virtue
//     of being the only one — and SetTokenEnrichmentService registers it)
//   - tests that bypass the registry by calling SetTokenEnrichmentService
//     directly with a service that has chainID=0
//   - gateway mode for chains where init failed (RPC dial error)
func resolveTokenServiceForChain(chainID uint64) *TokenEnrichmentService {
	if svc := GetTokenEnrichmentServiceForChain(chainID); svc != nil {
		return svc
	}
	return GetTokenEnrichmentService()
}

// resolveTokenMetadataWithCatalog returns the API-shaped token metadata for an
// address, preferring the per-chain TokenEnrichmentService and falling through
// to the cross-chain catalog when the service either has no entry or surfaces
// the {Symbol: "UNKNOWN", Decimals: 18} fallback from fetchTokenMetadataFromRPC.
//
// This mirrors the catalog fallback enrichTransferEventShared and the
// simulation injector both use — the workflow's declared chain might not be
// hosted by the gateway, so the bound services per-chain whitelist can miss
// addresses that are otherwise well-known on a different chain. Without this
// fallback, the request to context-memory ships UNKNOWN entries that take
// precedence over the catalog inside resolveTokenMeta, leaving execution-step
// descriptions formatted at the wrong decimal scale.
//
// Returns nil when neither the bound service nor the catalog can resolve the
// address. Catalog hits return the catalog's name when the bound service
// didn't have anything resolvable, otherwise the catalog symbol/decimals win
// over the bound services UNKNOWN-flavoured fallback.
func resolveTokenMetadataWithCatalog(tokenService *TokenEnrichmentService, address string, workflowChainID uint64, logger sdklogging.Logger) *contextMemoryTokenMetadata {
	var (
		metadata *TokenMetadata
	)
	if tokenService != nil {
		if md, err := tokenService.GetTokenMetadata(address); err == nil && md != nil {
			metadata = md
		} else if err != nil && logger != nil {
			logger.Debug("resolveTokenMetadataWithCatalog: bound service lookup failed", "address", address, "error", err)
		}
	}
	if isUnknownTokenMetadata(metadata) {
		if catalogHit := LookupTokenInCatalog(workflowChainID, address, logger); catalogHit != nil {
			metadata = catalogHit
		}
	}
	if metadata == nil {
		return nil
	}
	return &contextMemoryTokenMetadata{
		Symbol:   metadata.Symbol,
		Decimals: metadata.Decimals,
		Name:     metadata.Name,
	}
}

// chainIDFromSettingsValue parses a chain_id value from a settings map. The
// concrete type depends on how the value arrived: JSON-decoded settings give
// float64; protobuf-derived settings give int64; SDK string passthroughs give
// string. Returns 0 when absent or unparseable.
func chainIDFromSettingsValue(raw interface{}) uint64 {
	switch v := raw.(type) {
	case nil:
		return 0
	case uint64:
		return v
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case int:
		if v > 0 {
			return uint64(v)
		}
	case float64:
		if v > 0 {
			return uint64(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return uint64(n)
		}
	case string:
		if n, err := parsePositiveUint(v); err == nil {
			return n
		}
	}
	return 0
}

// chainIDFromExecutionContext extracts chain_id from a step's ExecutionContext.
// The argument is the protobuf Value's AsInterface() — always a
// map[string]interface{} when present. Step context is authoritative for
// cross-chain workflows where a node may have run against a different chain
// than the workflow's default.
func chainIDFromExecutionContext(ec interface{}) uint64 {
	if ec == nil {
		return 0
	}
	m, ok := ec.(map[string]interface{})
	if !ok {
		return 0
	}
	return chainIDFromSettingsValue(m["chain_id"])
}

// parsePositiveUint is a tiny strconv.ParseUint replacement that returns an
// error for non-positive values. Inlined to avoid pulling strconv into the
// summarizer's import block for a single call site.
func parsePositiveUint(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty string")
	}
	var out uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit in %q", s)
		}
		out = out*10 + uint64(r-'0')
	}
	if out == 0 {
		return 0, fmt.Errorf("non-positive: %q", s)
	}
	return out, nil
}

// isERC20Method returns true if the method name suggests this is an ERC20 token interaction
// This is used to determine whether to fetch token metadata for a contract address
// Note: Uses case-insensitive comparison, so method names in the array can be in any case
func isERC20Method(methodName string) bool {
	if methodName == "" {
		return false
	}
	// Common ERC20 methods that interact with token contracts
	erc20Methods := []string{
		"approve",
		"transfer",
		"transferFrom",
		"allowance",
		"balanceOf",
		"totalSupply",
		"name",
		"symbol",
		"decimals",
		// Also include common token-related methods in DeFi protocols
		"deposit",  // WETH
		"withdraw", // WETH
		"mint",
		"burn",
	}
	lowerMethod := strings.ToLower(methodName)
	for _, m := range erc20Methods {
		if strings.ToLower(m) == lowerMethod {
			return true
		}
	}
	return false
}

// extractResolvedContractAddress extracts the actual contract address from execution metadata
// This is useful when the config contains a template variable like {{settings.token}}
// but the metadata contains the resolved address from the actual transaction
func extractResolvedContractAddress(log *avsproto.Execution_Step) string {
	if log == nil || log.GetMetadata() == nil {
		return ""
	}
	return extractResolvedContractAddressFromMetadata(log.GetMetadata().AsInterface())
}

// extractResolvedContractAddressFromMetadata extracts the actual contract address from metadata interface
// This is a helper for extracting resolved addresses when we only have the metadata interface (not the log)
func extractResolvedContractAddressFromMetadata(metadataInterface interface{}) string {
	if metadataInterface == nil {
		return ""
	}

	// Metadata is an array of method results for contract_write
	if resultsArray, ok := metadataInterface.([]interface{}); ok {
		for _, result := range resultsArray {
			if resultMap, ok := result.(map[string]interface{}); ok {
				// Check receipt for both "to" field and event log addresses
				if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
					// First, check for "to" field (the actual contract address)
					if to, hasTo := receipt["to"].(string); hasTo && common.IsHexAddress(to) {
						return to
					}
					// Also check logs for event addresses (for ERC20 events like Approval)
					if logs, hasLogs := receipt["logs"].([]interface{}); hasLogs && len(logs) > 0 {
						// Find the first log with a valid address (likely the token contract)
						for _, logEntry := range logs {
							if logMap, ok := logEntry.(map[string]interface{}); ok {
								if addr, hasAddr := logMap["address"].(string); hasAddr && common.IsHexAddress(addr) {
									return addr
								}
							}
						}
					}
				}
			}
		}
	}

	return ""
}

// extractResolvedContractAddressFromLoopOutput extracts the resolved contract address
// from a LOOP step's output data. Loop iterations store metadata.contractAddress
// (propagated from the iteration's receipt.to during execution).
func extractResolvedContractAddressFromLoopOutput(log *avsproto.Execution_Step) string {
	if log == nil {
		return ""
	}

	// Loop output is stored as the Loop output type
	loopOutput := log.GetLoop()
	if loopOutput == nil || loopOutput.Data == nil {
		return ""
	}

	// Loop output data is an array of iteration results
	outputInterface := loopOutput.Data.AsInterface()
	iterations, ok := outputInterface.([]interface{})
	if !ok || len(iterations) == 0 {
		return ""
	}

	// Check the first successful iteration for metadata.contractAddress
	for _, iter := range iterations {
		iterMap, ok := iter.(map[string]interface{})
		if !ok {
			continue
		}
		if meta, ok := iterMap["metadata"].(map[string]interface{}); ok {
			if addr, ok := meta["contractAddress"].(string); ok && common.IsHexAddress(addr) {
				return addr
			}
		}
	}

	return ""
}
