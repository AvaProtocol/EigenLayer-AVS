package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
)

const (
	// ContextAPIURL is the production URL for the context-api service
	ContextAPIURL = "https://context-api.avaprotocol.org"
)

// ContextMemorySummarizer implements Summarizer using the context-memory API
type ContextMemorySummarizer struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
}

// NewContextMemorySummarizer creates a new summarizer that calls context-memory API
// baseURL defaults to ContextAPIURL (production) if empty
func NewContextMemorySummarizer(baseURL, authToken string) Summarizer {
	if baseURL == "" {
		baseURL = ContextAPIURL
	}
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
	Summary     string                        `json:"summary"`     // One-line execution summary
	Status      string                        `json:"status"`      // "success", "partial_success", "failure"
	Network     string                        `json:"network"`     // Chain name (e.g., "Sepolia", "Ethereum")
	Trigger     string                        `json:"trigger"`     // What triggered the workflow (text description)
	TriggeredAt string                        `json:"triggeredAt"` // ISO 8601 timestamp (from trigger output)
	Executions  []contextMemoryExecutionEntry `json:"executions"`  // On-chain operation descriptions with optional tx hashes
	Errors      []string                      `json:"errors"`      // Failed steps and skipped node descriptions

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

	// Build request from VM
	req, err := c.buildRequest(vm, currentStepName)
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

	// Convert API response workflow to Summary workflow
	var workflow *WorkflowInfo
	if apiResp.Body.Workflow != nil {
		workflow = &WorkflowInfo{
			Name:         apiResp.Body.Workflow.Name,
			Chain:        apiResp.Body.Workflow.Chain,
			ChainID:      apiResp.Body.Workflow.ChainID,
			IsSimulation: apiResp.Body.Workflow.IsSimulation,
			RunNumber:    apiResp.Body.Workflow.RunNumber,
		}
	}

	// Convert API execution entries to Summary execution entries
	var executions []ExecutionEntry
	for _, e := range apiResp.Body.Executions {
		executions = append(executions, ExecutionEntry{
			Description: e.Description,
			TxHash:      e.TxHash,
		})
	}

	return Summary{
		Subject:     apiResp.Subject,
		Body:        composePlainTextBodyFromAPI(apiResp.Body),
		SummaryLine: apiResp.Body.Summary,
		Status:      apiResp.Body.Status,
		Network:     apiResp.Body.Network,
		Trigger:     apiResp.Body.Trigger,
		TriggeredAt: apiResp.Body.TriggeredAt,
		Executions:  executions,
		Errors:      apiResp.Body.Errors,
		SmartWallet: req.SmartWallet,
		Transfers:   transfers,
		Balances:    balances,
		Workflow:    workflow,
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

func (c *ContextMemorySummarizer) buildRequest(vm *VM, currentStepName string) (*contextMemorySummarizeRequest, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Extract workflow context
	var ownerEOA, smartWallet, workflowName, chainName string
	var executionCount int64
	if wfCtx, ok := vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
		if owner, ok := wfCtx["owner"].(string); ok {
			ownerEOA = owner
		}
		if runner, ok := wfCtx["runner"].(string); ok {
			smartWallet = runner
		}
		if eoa, ok := wfCtx["eoaAddress"].(string); ok && eoa != "" && ownerEOA == "" {
			ownerEOA = eoa
		}
		// Extract execution count for API request
		if count, ok := wfCtx["executionCount"].(int64); ok {
			executionCount = count
		} else if count, ok := wfCtx["executionCount"].(uint64); ok {
			// Overflow check: cap at MaxInt64 to prevent negative values
			if count > math.MaxInt64 {
				executionCount = math.MaxInt64
				if vm != nil && vm.logger != nil {
					vm.logger.Warn("executionCount exceeded MaxInt64, capped to avoid overflow", "count", count, "capped_to", executionCount)
				}
			} else {
				executionCount = int64(count)
			}
		}
	}
	// Prioritize settings.name for workflow name (most accurate source)
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if name, ok := settings["name"].(string); ok && strings.TrimSpace(name) != "" {
			workflowName = name
		}
		if chain, ok := settings["chain"].(string); ok {
			chainName = chain
		}
		if runner, ok := settings["runner"].(string); ok && smartWallet == "" {
			smartWallet = runner
		}
	}

	// Fallback to TaskOwner if available (for single node executions)
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

			// If contractAddress is a template variable, try to extract resolved address from metadata
			resolvedContractAddress := contractAddress
			if strings.Contains(contractAddress, "{{") || contractAddress == "" {
				resolvedContractAddress = extractResolvedContractAddress(log)
			}

			// Look up token metadata for the contract address (for ERC20 tokens only)
			if resolvedContractAddress != "" && !strings.Contains(resolvedContractAddress, "{{") && common.IsHexAddress(resolvedContractAddress) && isERC20Method(methodName) {
				if tokenService := GetTokenEnrichmentService(); tokenService != nil {
					if metadata, err := tokenService.GetTokenMetadata(resolvedContractAddress); err == nil && metadata != nil {
						step.TokenMetadata = &contextMemoryTokenMetadata{
							Symbol:   metadata.Symbol,
							Decimals: metadata.Decimals,
							Name:     metadata.Name,
						}
					}
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
			tokenService := GetTokenEnrichmentService()
			for tokenKey, tokenAddr := range tokens {
				if addr, ok := tokenAddr.(string); ok && common.IsHexAddress(addr) {
					addrLower := strings.ToLower(addr)
					// Skip if already have metadata for this address
					if _, exists := tokenMetadataMap[addrLower]; !exists {
						if tokenService != nil {
							if metadata, err := tokenService.GetTokenMetadata(addr); err == nil && metadata != nil {
								tokenMetadataMap[addrLower] = &contextMemoryTokenMetadata{
									Symbol:   metadata.Symbol,
									Decimals: metadata.Decimals,
									Name:     metadata.Name,
								}
							} else if err != nil && vm != nil && vm.logger != nil {
								vm.logger.Debug("Failed to fetch token metadata", "address", addr, "tokenKey", tokenKey, "error", err)
							}
						}
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
		ChainName:       chainName,
		Nodes:           nodes,
		Edges:           edges,
		Settings:        settings,
		CurrentNodeName: currentStepName,
		TokenMetadata:   tokenMetadataMap,
		RunNumber:       executionCount, // 1-based for real executions; ignored when isSimulation is true
	}, nil
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
