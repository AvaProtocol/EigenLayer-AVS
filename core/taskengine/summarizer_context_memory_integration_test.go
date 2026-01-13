//go:build integration
// +build integration

package taskengine

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/go-resty/resty/v2"
)

// ExecutionSummaryResponse matches the TypeScript ExecutionSummaryResponse interface
type ExecutionSummaryResponse struct {
	Status        string   `json:"status"`        // 'success' | 'partial_success' | 'failure'
	BranchSummary string   `json:"branchSummary"` // Plain text summary
	SkippedNodes  []string `json:"skippedNodes"`  // Array of skipped node names
	TotalSteps    int      `json:"totalSteps"`    // Total steps in workflow
	ExecutedSteps int      `json:"executedSteps"` // Number of steps executed
	SkippedSteps  int      `json:"skippedSteps"`  // Number of skipped steps
}

// SummarizeRequest matches the TypeScript SummarizeRequest interface
type SummarizeRequest struct {
	OwnerEOA        string                 `json:"ownerEOA"`
	Name            string                 `json:"name"`
	SmartWallet     string                 `json:"smartWallet"`
	Steps           []StepDigest           `json:"steps"`
	ChainName       string                 `json:"chainName,omitempty"`
	Nodes           []NodeDefinition       `json:"nodes,omitempty"`
	Edges           []EdgeDefinition       `json:"edges,omitempty"`
	Settings        map[string]interface{} `json:"settings,omitempty"`
	CurrentNodeName string                 `json:"currentNodeName,omitempty"`
}

type StepDigest struct {
	Name            string                 `json:"name"`
	ID              string                 `json:"id"`
	Type            string                 `json:"type"`
	Success         bool                   `json:"success"`
	Error           string                 `json:"error,omitempty"`
	ContractAddress string                 `json:"contractAddress,omitempty"`
	MethodName      string                 `json:"methodName,omitempty"`
	MethodParams    map[string]interface{} `json:"methodParams,omitempty"`
	OutputData      interface{}            `json:"outputData,omitempty"`
	Metadata        interface{}            `json:"metadata,omitempty"`
	StepDescription string                 `json:"stepDescription,omitempty"`
}

type NodeDefinition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type EdgeDefinition struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
}

// getContextMemoryURL returns the base URL for context-memory API
// Uses CONTEXT_MEMORY_URL env var if set, otherwise defaults to production URL from source code
func getContextMemoryURL() string {
	if url := os.Getenv("CONTEXT_MEMORY_URL"); url != "" {
		return url
	}
	return ContextAPIURL
}

// baseURL shared across tests to avoid redeclaration issues
var baseURL string

func TestContextMemoryExecutionSummary_SuccessfulWorkflow(t *testing.T) {
	baseURL = getContextMemoryURL()
	authToken := getAuthTokenOrSkip(t, baseURL)
	t.Logf("Testing against: %s", baseURL)

	client := resty.New()
	client.SetTimeout(30 * time.Second)

	// Build request from VM-like structure
	request := SummarizeRequest{
		OwnerEOA:    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		Name:        "Test Workflow",
		SmartWallet: "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		ChainName:   "Sepolia",
		Steps: []StepDigest{
			{
				Name:    "balance1",
				ID:      "step1",
				Type:    "balance",
				Success: true,
			},
			{
				Name:            "approve_token1",
				ID:              "step2",
				Type:            "contractWrite",
				Success:         true,
				ContractAddress: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
				MethodName:      "approve",
				StepDescription: "Approved 1000 USDC to Uniswap V3 router",
			},
			{
				Name:    "get_quote",
				ID:      "step3",
				Type:    "contractRead",
				Success: true,
			},
		},
		Nodes: []NodeDefinition{
			{ID: "node1", Name: "balance1"},
			{ID: "node2", Name: "approve_token1"},
			{ID: "node3", Name: "get_quote"},
			{ID: "node4", Name: "run_swap"},
		},
		Edges: []EdgeDefinition{
			{ID: "edge1", Source: "node1", Target: "node2"},
			{ID: "edge2", Source: "node2", Target: "node3"},
			{ID: "edge3", Source: "node3", Target: "node4"},
		},
	}

	var response ExecutionSummaryResponse
	url := baseURL + "/api/execution-summary"

	// Log request details for debugging
	t.Logf("Making POST request to: %s", url)
	t.Logf("Request body (first 500 chars): %s", truncateString(mustMarshalJSON(request), 500))

	resp, err := client.R().
		SetHeader("Authorization", "Bearer "+authToken).
		SetHeader("Content-Type", "application/json").
		SetBody(request).
		SetResult(&response).
		Post(url)

	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Logf("Response status: %d", resp.StatusCode())
		t.Logf("Response headers: %v", resp.Header())
		t.Logf("Full response body: %s", string(resp.Body()))
		t.Fatalf("Expected status 200, got %d. Response body: %s", resp.StatusCode(), string(resp.Body()))
	}

	t.Logf("Success! Response: status=%s, executedSteps=%d, totalSteps=%d",
		response.Status, response.ExecutedSteps, response.TotalSteps)

	// Validate response structure
	if response.Status == "" {
		t.Error("Response status should not be empty")
	}
	if response.Status != "success" && response.Status != "partial_success" && response.Status != "failure" {
		t.Errorf("Invalid status value: %s (expected 'success', 'partial_success', or 'failure')", response.Status)
	}

	// branchSummary should always be present (even if empty string)
	if response.BranchSummary == "" {
		// Empty string is valid when there are no skipped nodes
		t.Log("branchSummary is empty (expected for successful workflow)")
	}

	// skippedNodes should always be present (even if empty array)
	if response.SkippedNodes == nil {
		t.Error("skippedNodes should not be nil")
	}

	// Validate metrics
	if response.ExecutedSteps != 3 {
		t.Errorf("Expected executedSteps=3, got %d", response.ExecutedSteps)
	}
	if response.TotalSteps < response.ExecutedSteps {
		t.Errorf("totalSteps (%d) should be >= executedSteps (%d)", response.TotalSteps, response.ExecutedSteps)
	}
	if response.SkippedSteps < 0 {
		t.Errorf("skippedSteps should be >= 0, got %d", response.SkippedSteps)
	}

	// For this test, we expect 1 skipped node (run_swap)
	if response.SkippedSteps != 1 {
		t.Errorf("Expected skippedSteps=1 (run_swap was skipped), got %d", response.SkippedSteps)
	}
	if len(response.SkippedNodes) != 1 {
		t.Errorf("Expected 1 skipped node, got %d: %v", len(response.SkippedNodes), response.SkippedNodes)
	}
	if len(response.SkippedNodes) > 0 && response.SkippedNodes[0] != "run_swap" {
		t.Errorf("Expected skipped node 'run_swap', got %s", response.SkippedNodes[0])
	}

	t.Logf("Response: status=%s, executedSteps=%d, totalSteps=%d, skippedSteps=%d, skippedNodes=%v",
		response.Status, response.ExecutedSteps, response.TotalSteps, response.SkippedSteps, response.SkippedNodes)
}

func TestContextMemoryExecutionSummary_FailedWorkflow(t *testing.T) {
	baseURL = getContextMemoryURL()
	authToken := getAuthTokenOrSkip(t, baseURL)

	client := resty.New()
	client.SetTimeout(30 * time.Second)

	request := SummarizeRequest{
		OwnerEOA:    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		Name:        "Failed Workflow",
		SmartWallet: "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		ChainName:   "Sepolia",
		Steps: []StepDigest{
			{
				Name:    "approve_token",
				ID:      "step1",
				Type:    "contractWrite",
				Success: true,
			},
			{
				Name:    "swap_tokens",
				ID:      "step2",
				Type:    "contractWrite",
				Success: false,
				Error:   "Insufficient gas",
			},
		},
		Nodes: []NodeDefinition{
			{ID: "node1", Name: "approve_token"},
			{ID: "node2", Name: "swap_tokens"},
		},
	}

	var response ExecutionSummaryResponse
	url := baseURL + "/api/execution-summary"

	// Log request details for debugging
	t.Logf("Making POST request to: %s", url)
	t.Logf("Request body (first 500 chars): %s", truncateString(mustMarshalJSON(request), 500))

	resp, err := client.R().
		SetHeader("Authorization", "Bearer "+authToken).
		SetHeader("Content-Type", "application/json").
		SetBody(request).
		SetResult(&response).
		Post(url)

	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Logf("Response status: %d", resp.StatusCode())
		t.Logf("Response headers: %v", resp.Header())
		t.Logf("Full response body: %s", string(resp.Body()))
		t.Fatalf("Expected status 200, got %d. Response body: %s", resp.StatusCode(), string(resp.Body()))
	}

	t.Logf("Success! Response: status=%s, executedSteps=%d, totalSteps=%d",
		response.Status, response.ExecutedSteps, response.TotalSteps)

	// Validate failure status
	if response.Status != "failure" {
		t.Errorf("Expected status='failure', got %s", response.Status)
	}

	// Validate metrics
	if response.ExecutedSteps != 2 {
		t.Errorf("Expected executedSteps=2, got %d", response.ExecutedSteps)
	}

	t.Logf("Failed workflow response: status=%s, executedSteps=%d", response.Status, response.ExecutedSteps)
}

func TestContextMemoryExecutionSummary_CompleteWorkflow(t *testing.T) {
	baseURL = getContextMemoryURL()
	authToken := getAuthTokenOrSkip(t, baseURL)

	client := resty.New()
	client.SetTimeout(30 * time.Second)

	request := SummarizeRequest{
		OwnerEOA:    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		Name:        "Complete Workflow",
		SmartWallet: "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		ChainName:   "Sepolia",
		Steps: []StepDigest{
			{
				Name:    "balance1",
				ID:      "step1",
				Type:    "balance",
				Success: true,
			},
			{
				Name:    "approve_token1",
				ID:      "step2",
				Type:    "contractWrite",
				Success: true,
			},
			{
				Name:    "get_quote",
				ID:      "step3",
				Type:    "contractRead",
				Success: true,
			},
			{
				Name:    "run_swap",
				ID:      "step4",
				Type:    "contractWrite",
				Success: true,
			},
		},
		Nodes: []NodeDefinition{
			{ID: "node1", Name: "balance1"},
			{ID: "node2", Name: "approve_token1"},
			{ID: "node3", Name: "get_quote"},
			{ID: "node4", Name: "run_swap"},
		},
	}

	var response ExecutionSummaryResponse
	url := baseURL + "/api/execution-summary"

	// Log request details for debugging
	t.Logf("Making POST request to: %s", url)
	t.Logf("Request body (first 500 chars): %s", truncateString(mustMarshalJSON(request), 500))

	resp, err := client.R().
		SetHeader("Authorization", "Bearer "+authToken).
		SetHeader("Content-Type", "application/json").
		SetBody(request).
		SetResult(&response).
		Post(url)

	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Logf("Response status: %d", resp.StatusCode())
		t.Logf("Response headers: %v", resp.Header())
		t.Logf("Full response body: %s", string(resp.Body()))
		t.Fatalf("Expected status 200, got %d. Response body: %s", resp.StatusCode(), string(resp.Body()))
	}

	t.Logf("Success! Response: status=%s, executedSteps=%d, totalSteps=%d",
		response.Status, response.ExecutedSteps, response.TotalSteps)

	// Validate success status
	if response.Status != "success" {
		t.Errorf("Expected status='success', got %s", response.Status)
	}

	// All nodes executed, no skipped nodes
	if response.SkippedSteps != 0 {
		t.Errorf("Expected skippedSteps=0, got %d", response.SkippedSteps)
	}
	if len(response.SkippedNodes) != 0 {
		t.Errorf("Expected no skipped nodes, got %v", response.SkippedNodes)
	}
	if response.ExecutedSteps != 4 {
		t.Errorf("Expected executedSteps=4, got %d", response.ExecutedSteps)
	}
	if response.TotalSteps != 4 {
		t.Errorf("Expected totalSteps=4, got %d", response.TotalSteps)
	}

	t.Logf("Complete workflow response: status=%s, executedSteps=%d, totalSteps=%d",
		response.Status, response.ExecutedSteps, response.TotalSteps)
}

func TestContextMemoryExecutionSummary_FromVM(t *testing.T) {
	baseURL = getContextMemoryURL()
	authToken := getAuthTokenOrSkip(t, baseURL)
	// Create a VM similar to TestComposeSummarySmart_WithRealWorkflowState
	vm := NewVM()
	vm.TaskID = "01K6H8R583M8WFXM2Z4APP7JTN"

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "trigger", Name: "eventTrigger", Type: "eventTrigger", Success: true},
		{Id: "step1", Name: "balance1", Type: "balance", Success: true},
		{Id: "step2", Name: "branch1", Type: "branch", Success: true},
		{Id: "step3", Name: "email_report", Type: "restApi", Success: true},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node1": {Id: "node1", Name: "balance1"},
		"node2": {Id: "node2", Name: "branch1"},
		"node3": {Id: "node3", Name: "approve_token1"},
		"node4": {Id: "node4", Name: "get_quote"},
		"node5": {Id: "node5", Name: "run_swap"},
		"node6": {Id: "node6", Name: "email_report"},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Test template",
			"chain":  "Sepolia",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":   "Test template",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		},
	}
	vm.mu.Unlock()

	// Convert VM to API request
	// Note: buildSummarizeRequestFromVM locks vm.mu, so we call it before creating the client
	request := buildSummarizeRequestFromVM(vm)

	baseURL = getContextMemoryURL()
	t.Logf("Testing against: %s", baseURL)

	client := resty.New()
	client.SetTimeout(30 * time.Second)

	var response ExecutionSummaryResponse
	url := baseURL + "/api/execution-summary"

	// Log request details for debugging
	t.Logf("Making POST request to: %s", url)
	t.Logf("Request body (first 500 chars): %s", truncateString(mustMarshalJSON(request), 500))

	resp, err := client.R().
		SetHeader("Authorization", "Bearer "+authToken).
		SetHeader("Content-Type", "application/json").
		SetBody(request).
		SetResult(&response).
		Post(url)

	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Logf("Response status: %d", resp.StatusCode())
		t.Logf("Response headers: %v", resp.Header())
		t.Logf("Full response body: %s", string(resp.Body()))
		t.Fatalf("Expected status 200, got %d. Response body: %s", resp.StatusCode(), string(resp.Body()))
	}

	t.Logf("Success! Response: status=%s, executedSteps=%d, totalSteps=%d",
		response.Status, response.ExecutedSteps, response.TotalSteps)

	// Validate response
	if response.Status == "" {
		t.Error("Response status should not be empty")
	}

	// Should have 4 executed steps (trigger + 3 nodes)
	if response.ExecutedSteps != 4 {
		t.Errorf("Expected executedSteps=4, got %d", response.ExecutedSteps)
	}

	// Should have 3 skipped nodes (approve_token1, get_quote, run_swap)
	if response.SkippedSteps != 3 {
		t.Errorf("Expected skippedSteps=3, got %d", response.SkippedSteps)
	}
	if response.TotalSteps != 7 {
		t.Errorf("Expected totalSteps=7 (4 executed + 3 skipped), got %d", response.TotalSteps)
	}

	// Validate skipped nodes
	expectedSkipped := []string{"approve_token1", "get_quote", "run_swap"}
	if len(response.SkippedNodes) != len(expectedSkipped) {
		t.Errorf("Expected %d skipped nodes, got %d: %v", len(expectedSkipped), len(response.SkippedNodes), response.SkippedNodes)
	}

	// branchSummary should contain text about skipped nodes
	if response.BranchSummary == "" {
		t.Error("branchSummary should not be empty when there are skipped nodes")
	}
	if !strings.Contains(response.BranchSummary, "skipped") {
		t.Errorf("branchSummary should mention skipped nodes, got: %s", response.BranchSummary)
	}

	t.Logf("VM-based test response: status=%s, executedSteps=%d, totalSteps=%d, skippedSteps=%d, skippedNodes=%v",
		response.Status, response.ExecutedSteps, response.TotalSteps, response.SkippedSteps, response.SkippedNodes)
	t.Logf("branchSummary: %s", response.BranchSummary)
}

// buildSummarizeRequestFromVM converts a VM to a SummarizeRequest
// This mimics what the aggregator will do when integrating with context-memory
func buildSummarizeRequestFromVM(vm *VM) SummarizeRequest {
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
		if name, ok := wfCtx["name"].(string); ok {
			workflowName = name
		}
	}
	if settings, ok := vm.vars["settings"].(map[string]interface{}); ok {
		if name, ok := settings["name"].(string); ok && workflowName == "" {
			workflowName = name
		}
		if chain, ok := settings["chain"].(string); ok {
			chainName = chain
		}
		if runner, ok := settings["runner"].(string); ok && smartWallet == "" {
			smartWallet = runner
		}
	}

	// Convert execution logs to steps
	steps := make([]StepDigest, 0, len(vm.ExecutionLogs))
	for _, log := range vm.ExecutionLogs {
		step := StepDigest{
			Name:    log.GetName(),
			ID:      log.GetId(),
			Type:    log.GetType(),
			Success: log.GetSuccess(),
		}
		if log.GetError() != "" {
			step.Error = log.GetError()
		}
		steps = append(steps, step)
	}

	// Convert TaskNodes to nodes
	nodes := make([]NodeDefinition, 0, len(vm.TaskNodes))
	for nodeID, node := range vm.TaskNodes {
		if node == nil {
			continue
		}
		// Skip branch condition pseudo-nodes (IDs starting with '.')
		if len(nodeID) > 0 && nodeID[0] == '.' {
			continue
		}
		nodes = append(nodes, NodeDefinition{
			ID:   nodeID,
			Name: node.GetName(),
		})
	}

	// Convert edges (if available)
	// Note: vm.task is a *model.Task which contains the protobuf Task
	edges := make([]EdgeDefinition, 0)
	if vm.task != nil && vm.task.Task != nil && vm.task.Task.Edges != nil {
		for _, edge := range vm.task.Task.Edges {
			edges = append(edges, EdgeDefinition{
				ID:     edge.GetId(),
				Source: edge.GetSource(),
				Target: edge.GetTarget(),
			})
		}
	}

	return SummarizeRequest{
		OwnerEOA:    ownerEOA,
		Name:        workflowName,
		SmartWallet: smartWallet,
		Steps:       steps,
		ChainName:   chainName,
		Nodes:       nodes,
		Edges:       edges,
	}
}

// getAuthTokenOrSkip returns the auth token for context-api requests
// Accepts SERVICE_AUTH_TOKEN env var override, or uses default local token for localhost URLs
func getAuthTokenOrSkip(t *testing.T, baseURL string) string {
	t.Helper()
	// Check for SERVICE_AUTH_TOKEN override first (works for any URL)
	if authToken := os.Getenv("SERVICE_AUTH_TOKEN"); authToken != "" && strings.TrimSpace(authToken) != "" {
		return authToken
	}
	// For localhost URLs, use default local token if SERVICE_AUTH_TOKEN not set
	if strings.Contains(baseURL, "localhost") {
		t.Logf("SERVICE_AUTH_TOKEN not set, using default local token")
		return ContextMemoryAuthToken
	}
	// For production URLs, require SERVICE_AUTH_TOKEN to be set
	t.Skip("SERVICE_AUTH_TOKEN not set, skipping integration test")
	return ""
}

// Helper functions for debugging
func mustMarshalJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(data)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
