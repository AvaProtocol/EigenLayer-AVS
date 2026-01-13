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
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SummarizeRequest matches the TypeScript interface for /api/summarize
type contextMemorySummarizeRequest struct {
	OwnerEOA        string                    `json:"ownerEOA"`
	Name            string                    `json:"name"`
	SmartWallet     string                    `json:"smartWallet"`
	Steps           []contextMemoryStepDigest `json:"steps"`
	ChainName       string                    `json:"chainName,omitempty"`
	Nodes           []contextMemoryNodeDef    `json:"nodes,omitempty"`
	Edges           []contextMemoryEdgeDef    `json:"edges,omitempty"`
	Settings        map[string]interface{}    `json:"settings,omitempty"`
	CurrentNodeName string                    `json:"currentNodeName,omitempty"`
}

type contextMemoryStepDigest struct {
	Name             string                 `json:"name"`
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	Success          bool                   `json:"success"`
	Error            string                 `json:"error,omitempty"`
	ContractAddress  string                 `json:"contractAddress,omitempty"`
	MethodName       string                 `json:"methodName,omitempty"`
	MethodParams     map[string]interface{} `json:"methodParams,omitempty"`
	OutputData       interface{}            `json:"outputData,omitempty"`
	Metadata         interface{}            `json:"metadata,omitempty"`
	StepDescription  string                 `json:"stepDescription,omitempty"`
	ExecutionContext interface{}            `json:"executionContext,omitempty"` // Actual execution mode (is_simulated, provider, chain_id)
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
	Subject       string `json:"subject"`
	Summary       string `json:"summary"`      // One-liner summary
	AnalysisHtml  string `json:"analysisHtml"` // Pre-formatted HTML with ✓ symbols
	Body          string `json:"body"`         // Plain text body (for backward compatibility)
	StatusHtml    string `json:"statusHtml"`   // Status badge HTML (green/yellow/red badge with icon)
	Status        string `json:"status"`       // Execution status: "success", "partial_success", "failure"
	PromptVersion string `json:"promptVersion"`
	Cached        bool   `json:"cached,omitempty"`
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

	// Send request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Summary{}, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return Summary{}, fmt.Errorf("non-2xx response (%d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp contextMemorySummarizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return Summary{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return Summary{
		Subject:      apiResp.Subject,
		Body:         apiResp.Body,
		SummaryLine:  apiResp.Summary,
		AnalysisHtml: apiResp.AnalysisHtml,
		StatusHtml:   apiResp.StatusHtml,
		Status:       apiResp.Status,
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
		// Extract contract call info from ContractRead or ContractWrite
		if contractRead := log.GetContractRead(); contractRead != nil {
			// ContractRead doesn't have contract address/method in the output
			// These would be in the node config, but we'll skip for now
		}
		if contractWrite := log.GetContractWrite(); contractWrite != nil {
			// ContractWrite output doesn't contain contract address/method directly
			// These would be in the node config, but we'll skip for now
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
	}, nil
}
