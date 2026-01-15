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
}

type contextMemoryStepDigest struct {
	Name             string                      `json:"name"`
	ID               string                      `json:"id"`
	Type             string                      `json:"type"`
	Success          bool                        `json:"success"`
	Error            string                      `json:"error,omitempty"`
	ContractAddress  string                      `json:"contractAddress,omitempty"`
	MethodName       string                      `json:"methodName,omitempty"`
	MethodParams     map[string]interface{}      `json:"methodParams,omitempty"`
	OutputData       interface{}                 `json:"outputData,omitempty"`
	Metadata         interface{}                 `json:"metadata,omitempty"`
	StepDescription  string                      `json:"stepDescription,omitempty"`
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

// SummarizeResponse matches the TypeScript SummarizeResponse
type contextMemorySummarizeResponse struct {
	Subject           string   `json:"subject"`
	Summary           string   `json:"summary"`      // One-liner summary
	AnalysisHtml      string   `json:"analysisHtml"` // Pre-formatted HTML with ✓ symbols
	Body              string   `json:"body"`         // Plain text body (for backward compatibility)
	StatusHtml        string   `json:"statusHtml"`   // Status badge HTML (green/yellow/red badge with icon)
	Status            string   `json:"status"`       // Execution status: "success", "partial_success", "failure"
	PromptVersion     string   `json:"promptVersion"`
	Cached            bool     `json:"cached,omitempty"`
	BranchSummaryHtml string   `json:"branchSummaryHtml,omitempty"` // HTML formatted branch summary (when nodes are skipped)
	SkippedNodes      []string `json:"skippedNodes,omitempty"`      // List of skipped node names
}

func (c *ContextMemorySummarizer) Summarize(ctx context.Context, vm *VM, currentStepName string) (Summary, error) {
	if c == nil || c.httpClient == nil {
		return Summary{}, fmt.Errorf("summarizer not initialized")
	}

	// Build request from VM
	req, err := c.buildRequest(vm, currentStepName)
	if err != nil {
		return Summary{}, fmt.Errorf("failed to build request: %w", err)
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
		// Log INFO level message when context-memory API is not available (so it's visible in logs)
		if vm != nil && vm.logger != nil {
			vm.logger.Info("Context-memory API not available: HTTP request failed", "error", err, "url", c.baseURL+"/api/summarize")
		}
		return Summary{}, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		// Log INFO level message when context-memory API returns error (so it's visible in logs)
		if vm != nil && vm.logger != nil {
			vm.logger.Info("Context-memory API not available: non-2xx response", "status_code", resp.StatusCode, "response_body", string(body), "url", c.baseURL+"/api/summarize")
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
		vm.logger.Info("Context-memory API: received successful response", "subject", apiResp.Subject, "status", apiResp.Status)
	}

	return Summary{
		Subject:           apiResp.Subject,
		Body:              apiResp.Body,
		SummaryLine:       apiResp.Summary,
		AnalysisHtml:      apiResp.AnalysisHtml,
		StatusHtml:        apiResp.StatusHtml,
		Status:            apiResp.Status,
		BranchSummaryHtml: apiResp.BranchSummaryHtml,
		SkippedNodes:      apiResp.SkippedNodes,
	}, nil
}

func (c *ContextMemorySummarizer) buildRequest(vm *VM, currentStepName string) (*contextMemorySummarizeRequest, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Extract workflow context
	var ownerEOA, smartWallet, workflowName, chainName string
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

	// Convert execution logs to steps
	steps := make([]contextMemoryStepDigest, 0, len(vm.ExecutionLogs))
	for _, log := range vm.ExecutionLogs {
		step := contextMemoryStepDigest{
			Name:    log.GetName(),
			ID:      log.GetId(),
			Type:    log.GetType(),
			Success: log.GetSuccess(),
		}
		if log.GetError() != "" {
			step.Error = log.GetError()
		}
		// Extract contract details from node config (available in log.Config)
		// This includes contractAddress and methodParams for CONTRACT_READ and CONTRACT_WRITE nodes
		if configProto := log.GetConfig(); configProto != nil {
			if configMap, ok := configProto.AsInterface().(map[string]interface{}); ok {
				// Extract contractAddress
				if contractAddr, ok := configMap["contractAddress"].(string); ok {
					step.ContractAddress = contractAddr
				}

				// Extract methodCalls[0].methodName and methodParams
				if methodCalls, ok := configMap["methodCalls"].([]interface{}); ok && len(methodCalls) > 0 {
					if firstCall, ok := methodCalls[0].(map[string]interface{}); ok {
						if methodName, ok := firstCall["methodName"].(string); ok {
							step.MethodName = methodName
						}
						if methodParams, ok := firstCall["methodParams"].([]interface{}); ok && len(methodParams) > 0 {
							step.MethodParams = make(map[string]interface{})
							for i, param := range methodParams {
								step.MethodParams[fmt.Sprintf("param_%d", i)] = param
							}
						}
					}
				}
			}
		}

		// If contractAddress is a template variable, try to extract resolved address from metadata
		// The metadata contains receipt data with the actual "to" address used in the transaction
		resolvedContractAddress := step.ContractAddress
		if strings.Contains(step.ContractAddress, "{{") || step.ContractAddress == "" {
			resolvedContractAddress = extractResolvedContractAddress(log)
		}

		// Look up token metadata for the contract address (for ERC20 tokens only)
		// Use the resolved address if available
		if resolvedContractAddress != "" && !strings.Contains(resolvedContractAddress, "{{") && isERC20Method(step.MethodName) {
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

		// Extract output data
		if contractRead := log.GetContractRead(); contractRead != nil && contractRead.Data != nil {
			step.OutputData = contractRead.Data.AsInterface()
		}
		if contractWrite := log.GetContractWrite(); contractWrite != nil && contractWrite.Data != nil {
			step.OutputData = contractWrite.Data.AsInterface()
		}
		if log.GetMetadata() != nil {
			step.Metadata = log.GetMetadata().AsInterface()
		}
		// Extract ExecutionContext (actual execution mode: is_simulated, provider, chain_id)
		if log.GetExecutionContext() != nil {
			if ctxInterface := log.GetExecutionContext().AsInterface(); ctxInterface != nil {
				step.ExecutionContext = ctxInterface
			}
			// Note: If AsInterface() returns nil, we silently skip setting ExecutionContext
			// This can happen if the ExecutionContext contains unsupported data types
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
		if step.TokenMetadata != nil && step.ContractAddress != "" {
			addr := strings.ToLower(step.ContractAddress)
			if !strings.Contains(addr, "{{") { // Skip template variables
				tokenMetadataMap[addr] = step.TokenMetadata
			}
		}
	}

	// 2. Collect from settings.uniswapv3_pool.tokens (input, output, base, quote)
	if pool, ok := settings["uniswapv3_pool"].(map[string]interface{}); ok {
		if tokens, ok := pool["tokens"].(map[string]interface{}); ok {
			tokenService := GetTokenEnrichmentService()
			for _, tokenAddr := range tokens {
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
	}, nil
}

// isERC20Method returns true if the method name suggests this is an ERC20 token interaction
// This is used to determine whether to fetch token metadata for a contract address
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
	if log == nil {
		return ""
	}

	// Try to extract from metadata (contains receipt data with "to" field)
	if log.GetMetadata() != nil {
		metadataInterface := log.GetMetadata().AsInterface()

		// Metadata is an array of method results for contract_write
		if resultsArray, ok := metadataInterface.([]interface{}); ok {
			for _, result := range resultsArray {
				if resultMap, ok := result.(map[string]interface{}); ok {
					// Check receipt for "to" field (the actual contract address)
					if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
						if to, hasTo := receipt["to"].(string); hasTo && common.IsHexAddress(to) {
							return to
						}
					}
					// Also check logs for event addresses (for ERC20 events like Approval)
					if receipt, hasReceipt := resultMap["receipt"].(map[string]interface{}); hasReceipt {
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
	}

	return ""
}
