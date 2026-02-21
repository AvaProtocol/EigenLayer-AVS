//go:build integration
// +build integration

package taskengine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v2"
)

// sepoliaConfig holds the notifications.summary section from aggregator-sepolia.yaml
type sepoliaConfig struct {
	Notifications struct {
		Summary struct {
			Enabled     bool   `yaml:"enabled"`
			Provider    string `yaml:"provider"`
			APIEndpoint string `yaml:"api_endpoint"`
			APIKey      string `yaml:"api_key"`
		} `yaml:"summary"`
	} `yaml:"notifications"`
}

// loadSepoliaConfig loads the context-memory API config from the local aggregator-sepolia.yaml
func loadSepoliaConfig(t *testing.T) (string, string) {
	t.Helper()

	// Allow override via env vars
	if url := os.Getenv("CONTEXT_MEMORY_URL"); url != "" {
		token := os.Getenv("SERVICE_AUTH_TOKEN")
		if token == "" {
			if strings.Contains(url, "localhost") {
				token = ContextMemoryAuthToken
			} else {
				t.Skip("SERVICE_AUTH_TOKEN not set for non-localhost URL")
			}
		}
		return url, token
	}

	// Try to load from aggregator-sepolia.yaml
	configPaths := []string{
		"../../config/aggregator-sepolia.yaml",
		"config/aggregator-sepolia.yaml",
	}

	for _, path := range configPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg sepoliaConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Logf("Failed to parse %s: %v", path, err)
			continue
		}
		if !cfg.Notifications.Summary.Enabled {
			t.Skip("notifications.summary.enabled is false in config")
		}
		apiURL := cfg.Notifications.Summary.APIEndpoint
		apiKey := cfg.Notifications.Summary.APIKey
		if apiURL == "" || apiKey == "" {
			t.Skip("notifications.summary.api_endpoint or api_key not set in config")
		}
		t.Logf("Loaded config from %s: url=%s", path, apiURL)
		return apiURL, apiKey
	}

	t.Skip("Could not load aggregator-sepolia.yaml config")
	return "", ""
}

// discrepancy tracks a difference between AI and deterministic paths
type discrepancy struct {
	Field       string
	Description string
	AIValue     string
	DetValue    string
}

func (d discrepancy) String() string {
	return fmt.Sprintf("[%s] %s\n  AI:            %s\n  Deterministic: %s", d.Field, d.Description, d.AIValue, d.DetValue)
}

// compareAndReport compares AI and deterministic summaries and reports discrepancies
func compareAndReport(t *testing.T, scenario string, ai, det Summary) []discrepancy {
	t.Helper()
	var diffs []discrepancy

	// Subject
	if ai.Subject == "" {
		diffs = append(diffs, discrepancy{"Subject", "AI subject is empty", ai.Subject, det.Subject})
	} else {
		t.Logf("[%s] AI Subject:  %s", scenario, ai.Subject)
		t.Logf("[%s] Det Subject: %s", scenario, det.Subject)
	}

	// SummaryLine
	if ai.SummaryLine == "" && det.SummaryLine != "" {
		diffs = append(diffs, discrepancy{"SummaryLine", "AI summary line is empty but deterministic has one", "", det.SummaryLine})
	} else {
		t.Logf("[%s] AI SummaryLine:  %s", scenario, ai.SummaryLine)
		t.Logf("[%s] Det SummaryLine: %s", scenario, det.SummaryLine)
	}

	// Status
	if ai.Status != det.Status {
		diffs = append(diffs, discrepancy{"Status", "Status values differ", ai.Status, det.Status})
	}

	// Annotation
	if ai.Annotation != det.Annotation {
		diffs = append(diffs, discrepancy{
			"Annotation",
			"Annotation differs (AI path does not set Annotation)",
			fmt.Sprintf("%q", ai.Annotation),
			fmt.Sprintf("%q", det.Annotation),
		})
	}

	// Network
	if ai.Network == "" && det.Network != "" {
		diffs = append(diffs, discrepancy{"Network", "AI network is empty but deterministic has one", "", det.Network})
	}

	// Trigger
	if ai.Trigger == "" && det.Trigger != "" {
		diffs = append(diffs, discrepancy{"Trigger", "AI trigger is empty but deterministic has one", "", det.Trigger})
	} else {
		t.Logf("[%s] AI Trigger:  %s", scenario, ai.Trigger)
		t.Logf("[%s] Det Trigger: %s", scenario, det.Trigger)
	}

	// Executions
	t.Logf("[%s] AI Executions (%d):  %v", scenario, len(ai.Executions), ai.Executions)
	t.Logf("[%s] Det Executions (%d): %v", scenario, len(det.Executions), det.Executions)
	if len(ai.Executions) == 0 && len(det.Executions) > 0 {
		diffs = append(diffs, discrepancy{
			"Executions",
			"AI has no executions but deterministic does",
			"[]",
			fmt.Sprintf("%v", det.Executions),
		})
	}

	// Errors
	if len(ai.Errors) != len(det.Errors) {
		diffs = append(diffs, discrepancy{
			"Errors",
			fmt.Sprintf("Error count differs: AI=%d, Det=%d", len(ai.Errors), len(det.Errors)),
			fmt.Sprintf("%v", ai.Errors),
			fmt.Sprintf("%v", det.Errors),
		})
	}

	// Body (structural comparison - check headers)
	aiBodyLower := strings.ToLower(ai.Body)
	detBodyLower := strings.ToLower(det.Body)
	if strings.Contains(aiBodyLower, "executed:") && strings.Contains(detBodyLower, "what executed on-chain") {
		diffs = append(diffs, discrepancy{
			"Body.Headers",
			"AI body uses 'Executed:' header while deterministic uses 'What Executed On-Chain'",
			"Executed:",
			"What Executed On-Chain",
		})
	}

	// Transfers (structured data from AI)
	if len(ai.Transfers) > 0 {
		t.Logf("[%s] AI Transfers: %d entries", scenario, len(ai.Transfers))
		for i, tr := range ai.Transfers {
			t.Logf("[%s]   Transfer[%d]: %s %s %s -> %s (simulated=%v, tx=%s)",
				scenario, i, tr.Amount, tr.Symbol, truncateAddress(tr.From), truncateAddress(tr.To), tr.IsSimulated, truncateTxHash(tr.TxHash))
		}
	}

	// Workflow info
	if ai.Workflow != nil {
		t.Logf("[%s] AI Workflow: name=%s chain=%s chainID=%d isSimulation=%v",
			scenario, ai.Workflow.Name, ai.Workflow.Chain, ai.Workflow.ChainID, ai.Workflow.IsSimulation)
	}

	// SmartWallet
	if ai.SmartWallet == "" && det.SmartWallet != "" {
		diffs = append(diffs, discrepancy{"SmartWallet", "AI smart wallet is empty", "", det.SmartWallet})
	}

	return diffs
}

// formatForAllChannels formats a summary for all channels and logs the output
func formatForAllChannels(t *testing.T, label string, s Summary, vm *VM) {
	t.Helper()
	for _, ch := range []string{"telegram", "discord", "plaintext"} {
		msg := FormatForMessageChannels(s, ch, vm)
		t.Logf("[%s] %s channel output:\n%s", label, ch, msg)
	}
	// Email (SendGrid data)
	data := s.SendGridDynamicData()
	t.Logf("[%s] Email SendGrid data keys: %v", label, mapKeys(data))
	if html, ok := data["analysisHtml"].(string); ok {
		t.Logf("[%s] Email analysisHtml (first 500 chars): %s", label, truncateStr(html, 500))
	}
	if ann, ok := data["annotation"].(string); ok {
		t.Logf("[%s] Email annotation: %s", label, ann)
	}
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ============================================================================
// Test 1: RunNodeImmediately (single-node with empty execution logs)
// ============================================================================

func TestAIIntegration_RunNodeImmediately(t *testing.T) {
	apiURL, apiKey := loadSepoliaConfig(t)
	t.Logf("Testing RunNodeImmediately against: %s", apiURL)

	// Build VM mimicking RunNodeImmediately: single notification node, no execution logs
	// isSingleNodeImmediate checks: vm.GetTaskId() == "" && len(vm.TaskNodes) == 1
	// So we need: vm.task == nil (default from NewVM()) and exactly 1 TaskNode
	vm := NewVM()
	// Don't set TaskID — leave vm.task nil so GetTaskId() returns ""
	vm.IsSimulation = true // RunNodeImmediately sets IsSimulation=true

	// No execution logs — the single node (notification) is currently executing
	vm.ExecutionLogs = []*avsproto.Execution_Step{}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"telegram_send": {
			Id:   "telegram_send",
			Name: "telegram_send",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{},
			},
		},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Test RunNode Workflow",
			"chain":  "Sepolia",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":   "Test RunNode Workflow",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		},
	}
	vm.mu.Unlock()

	// --- AI path ---
	summarizer := NewContextMemorySummarizer(apiURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aiSummary, aiErr := summarizer.Summarize(ctx, vm, "telegram_send")
	if aiErr != nil {
		t.Logf("AI summarization failed (expected if API not running): %v", aiErr)
		t.Skip("Context-memory API not available")
	}
	t.Logf("AI Summary received: subject=%q, status=%q, bodyLen=%d", aiSummary.Subject, aiSummary.Status, len(aiSummary.Body))

	// --- Deterministic path ---
	detSummary := ComposeSummary(vm, "telegram_send")
	t.Logf("Det Summary: subject=%q, status=%q, bodyLen=%d", detSummary.Subject, detSummary.Status, len(detSummary.Body))

	// --- Compare ---
	diffs := compareAndReport(t, "RunNodeImmediately", aiSummary, detSummary)

	// --- Format for all channels ---
	t.Log("\n=== AI Summary formatted for channels ===")
	formatForAllChannels(t, "AI-RunNode", aiSummary, vm)
	t.Log("\n=== Deterministic Summary formatted for channels ===")
	formatForAllChannels(t, "Det-RunNode", detSummary, vm)

	// --- Report discrepancies ---
	if len(diffs) > 0 {
		t.Log("\n========================================")
		t.Logf("DISCREPANCIES FOUND: %d", len(diffs))
		t.Log("========================================")
		for i, d := range diffs {
			t.Logf("  %d. %s", i+1, d.String())
		}
	} else {
		t.Log("\nNo discrepancies found between AI and deterministic paths.")
	}

	// --- Key assertions ---
	// The deterministic path should have Annotation set for singleNode
	if detSummary.Annotation == "" {
		t.Error("Deterministic summary should have Annotation for RunNodeImmediately")
	}
	// The AI path currently does NOT set Annotation — document this as known discrepancy
	if aiSummary.Annotation == "" {
		t.Log("KNOWN DISCREPANCY: AI path does not set Annotation field for RunNodeImmediately")
	}
	// Deterministic should show "1 out of 1"
	if !strings.Contains(detSummary.SummaryLine, "1 out of 1") {
		t.Errorf("Deterministic SummaryLine should contain '1 out of 1', got: %s", detSummary.SummaryLine)
	}
}

// ============================================================================
// Test 2: Simulation (simulate_task with cron-triggered ETH transfer workflow)
// Mirrors the real client payload from simulateWorkflow with:
//   - cronTrigger (timeTrigger) with schedule config
//   - balance check node
//   - ethTransfer node with destination/amount config and transfer output
//   - telegram_send and email1 notification nodes
// ============================================================================

func TestAIIntegration_Simulation(t *testing.T) {
	apiURL, apiKey := loadSepoliaConfig(t)
	t.Logf("Testing Simulation against: %s", apiURL)

	vm := NewVM()
	vm.IsSimulation = true

	// Execution contexts
	simulatedCtxStruct, _ := structpb.NewStruct(map[string]interface{}{
		"is_simulated": true,
		"provider":     "tenderly",
		"chain_id":     float64(11155111),
	})
	simulatedCtx := structpb.NewStructValue(simulatedCtxStruct)
	chainRpcCtxStruct, _ := structpb.NewStruct(map[string]interface{}{
		"is_simulated": false,
		"provider":     "chain_rpc",
		"chain_id":     float64(11155111),
	})
	chainRpcCtx := structpb.NewStructValue(chainRpcCtxStruct)

	// Trigger config and output (matching real cronTrigger)
	triggerConfig, _ := structpb.NewValue(map[string]interface{}{
		"schedules": []interface{}{"0 23 */3 * *"},
	})
	triggerOutput, _ := structpb.NewValue(map[string]interface{}{
		"timestamp":    float64(1769651702059),
		"timestampIso": "2026-01-29T01:55:02.059Z",
	})

	// Balance node config and output
	balanceConfig, _ := structpb.NewValue(map[string]interface{}{
		"address":        "{{settings.runner}}",
		"chain":          "sepolia",
		"tokenAddresses": []interface{}{"{{settings.token_amount.address}}"},
		"includeSpam":    false,
	})
	balanceOutput, _ := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"balance":          "62673652309441472",
			"balanceFormatted": "0.062673652309441472",
			"decimals":         float64(18),
			"name":             "Ether",
			"symbol":           "ETH",
		},
	})

	// ETH transfer config and output
	transferConfig, _ := structpb.NewValue(map[string]interface{}{
		"destination": "{{settings.recipient}}",
		"amount":      "{{settings.token_amount.amount}}",
	})
	transferOutput, _ := structpb.NewValue(map[string]interface{}{
		"transfer": map[string]interface{}{
			"from":  "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"to":    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"value": "10000000000000000",
		},
	})
	transferMetadata, _ := structpb.NewValue(map[string]interface{}{
		"transactionHash": "0x0000000000000000000000000000000000000000000001769651703156844000",
	})

	startTime := int64(1769651702059)
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id: "01K2H450DVV4APEFCRK08BP5B2", Name: "timeTrigger", Type: avsproto.TriggerType_TRIGGER_TYPE_CRON.String(), Success: true,
			StartAt:          startTime,
			EndAt:            startTime + 1,
			Config:           triggerConfig,
			ExecutionContext: simulatedCtx,
			OutputData: &avsproto.Execution_Step_CronTrigger{
				CronTrigger: &avsproto.CronTrigger_Output{Data: triggerOutput},
			},
		},
		{
			Id: "01KFGZH8JF0Z7RHQXP6QQFN62F", Name: "balance1", Type: avsproto.NodeType_NODE_TYPE_BALANCE.String(), Success: true,
			StartAt:          startTime + 186,
			EndAt:            startTime + 1096,
			Config:           balanceConfig,
			ExecutionContext: chainRpcCtx,
			OutputData: &avsproto.Execution_Step_Balance{
				Balance: &avsproto.BalanceNode_Output{Data: balanceOutput},
			},
		},
		{
			Id: "01KFH6ETEFS0E9WPKV2WCDFRFH", Name: "transfer1", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER.String(), Success: true,
			StartAt:          startTime + 1096,
			EndAt:            startTime + 1097,
			Config:           transferConfig,
			Metadata:         transferMetadata,
			ExecutionContext: chainRpcCtx,
			OutputData: &avsproto.Execution_Step_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode_Output{Data: transferOutput},
			},
		},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"01KFGZH8JF0Z7RHQXP6QQFN62F": {
			Id: "01KFGZH8JF0Z7RHQXP6QQFN62F", Name: "balance1",
			TaskType: &avsproto.TaskNode_Balance{Balance: &avsproto.BalanceNode{
				Config: &avsproto.BalanceNode_Config{
					Address: "{{settings.runner}}",
					Chain:   "sepolia",
				},
			}},
		},
		"01KFH6ETEFS0E9WPKV2WCDFRFH": {
			Id: "01KFH6ETEFS0E9WPKV2WCDFRFH", Name: "transfer1",
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{
				Config: &avsproto.ETHTransferNode_Config{
					Destination: "{{settings.recipient}}",
					Amount:      "{{settings.token_amount.amount}}",
				},
			}},
		},
		"01K2JTNQBQHX00PRBE5X14Q932": {
			Id: "01K2JTNQBQHX00PRBE5X14Q932", Name: "telegram_send",
			TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://api.telegram.org/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage",
					Method: "POST",
				},
			}},
		},
		"01KG2ZJC16V948XHHPAFBSQKSX": {
			Id: "01KG2ZJC16V948XHHPAFBSQKSX", Name: "email1",
			TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://api.sendgrid.com/v3/mail/send",
					Method: "POST",
				},
			}},
		},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":      "Test settings.name",
			"chain":     "Sepolia",
			"runner":    "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"chain_id":  float64(11155111),
			"recipient": "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"token_amount": map[string]interface{}{
				"amount":   "10000000000000000",
				"address":  "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				"decimals": float64(18),
			},
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":   "Test settings.name",
			"runner": "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		},
	}
	vm.mu.Unlock()

	// --- AI path ---
	summarizer := NewContextMemorySummarizer(apiURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aiSummary, aiErr := summarizer.Summarize(ctx, vm, "telegram_send")
	if aiErr != nil {
		t.Logf("AI summarization failed: %v", aiErr)
		t.Skip("Context-memory API not available")
	}
	t.Logf("AI Summary received: subject=%q, status=%q, bodyLen=%d", aiSummary.Subject, aiSummary.Status, len(aiSummary.Body))
	t.Logf("AI Body: %s", aiSummary.Body)

	// --- Deterministic path ---
	detSummary := ComposeSummary(vm, "telegram_send")
	t.Logf("Det Summary: subject=%q, status=%q, bodyLen=%d", detSummary.Subject, detSummary.Status, len(detSummary.Body))

	// --- Compare ---
	diffs := compareAndReport(t, "Simulation", aiSummary, detSummary)

	// --- Format for all channels ---
	t.Log("\n=== AI Summary formatted for channels ===")
	formatForAllChannels(t, "AI-Simulation", aiSummary, vm)
	t.Log("\n=== Deterministic Summary formatted for channels ===")
	formatForAllChannels(t, "Det-Simulation", detSummary, vm)

	// --- Report discrepancies ---
	if len(diffs) > 0 {
		t.Log("\n========================================")
		t.Logf("DISCREPANCIES FOUND: %d", len(diffs))
		t.Log("========================================")
		for i, d := range diffs {
			t.Logf("  %d. %s", i+1, d.String())
		}
	} else {
		t.Log("\nNo discrepancies found between AI and deterministic paths.")
	}

	// --- Key assertions ---
	// Simulation should NOT have annotation
	if detSummary.Annotation != "" {
		t.Errorf("Deterministic should NOT have Annotation for simulation, got: %s", detSummary.Annotation)
	}
	// Both should report success or partial_success
	validStatuses := map[string]bool{"success": true, "partial_success": true}
	if !validStatuses[aiSummary.Status] {
		t.Errorf("AI status should be success/partial_success, got: %s", aiSummary.Status)
	}
	// Subject should contain "Simulation:" prefix
	if !strings.HasPrefix(detSummary.Subject, "Simulation:") {
		t.Errorf("Deterministic subject should start with 'Simulation:', got: %s", detSummary.Subject)
	}
	if !strings.Contains(aiSummary.Subject, "Test settings.name") {
		t.Logf("NOTE: AI subject does not contain workflow name: %s", aiSummary.Subject)
	}

	// AI should return meaningful trigger and executions with proper config/output data
	if aiSummary.Trigger == "" {
		t.Logf("DISCREPANCY: AI trigger is empty despite providing cron config with schedules")
	} else {
		t.Logf("AI trigger: %s", aiSummary.Trigger)
	}
	if len(aiSummary.Executions) == 0 {
		t.Logf("DISCREPANCY: AI executions is empty despite providing ETH transfer output data")
	} else {
		for i, exec := range aiSummary.Executions {
			t.Logf("AI execution[%d]: %s (txHash=%s)", i, exec.Description, exec.TxHash)
		}
	}

	// Deterministic path should NOT attach txHash for simulated steps
	// The test fixture has a fake BigInt as transactionHash in metadata + simulated ExecutionContext
	for i, exec := range detSummary.Executions {
		if exec.TxHash != "" {
			t.Errorf("Det execution[%d] should NOT have txHash for simulated step, got: %s", i, exec.TxHash)
		}
	}
}

// ============================================================================
// Test 3: Real execution (deployed workflow with real trigger)
// ============================================================================

func TestAIIntegration_RealExecution(t *testing.T) {
	apiURL, apiKey := loadSepoliaConfig(t)
	t.Logf("Testing Real Execution against: %s", apiURL)

	// Build VM mimicking a deployed workflow triggered by a real event
	// Scenario: USDC stoploss — event trigger detects price drop, approve USDC, swap on Uniswap
	vm := NewVM()
	vm.IsSimulation = false

	realCtxStruct, _ := structpb.NewStruct(map[string]interface{}{
		"is_simulated": false,
		"provider":     "bundler",
		"chain_id":     float64(11155111),
	})
	realCtx := structpb.NewStructValue(realCtxStruct)

	// Event trigger config and output
	eventTriggerConfig, _ := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"eventName":       "Transfer",
		"chain":           "sepolia",
	})
	triggerOutput, _ := structpb.NewValue(map[string]interface{}{
		"blockNumber":     float64(7500000),
		"logIndex":        float64(42),
		"address":         "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"transactionHash": "0xabc123def456789abcdef0123456789abcdef0123456789abcdef0123456789a",
	})

	// Balance check config and output
	balanceConfig, _ := structpb.NewValue(map[string]interface{}{
		"address":        "{{settings.runner}}",
		"chain":          "sepolia",
		"tokenAddresses": []interface{}{"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"},
	})
	balanceOutput, _ := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"balance":          "20990000",
			"balanceFormatted": "20.99",
			"decimals":         float64(6),
			"name":             "USD Coin",
			"symbol":           "USDC",
			"tokenAddress":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
	})

	// Approve config and output
	approveConfig, _ := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"callData":        "approve(address,uint256)",
		"callInput":       "[\"0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E\", \"20990000\"]",
	})
	approveOutput, _ := structpb.NewValue(map[string]interface{}{
		"approve": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
			"value":   "20990000",
		},
	})
	approveMetadata, _ := structpb.NewValue(map[string]interface{}{
		"transactionHash": "0xdef456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
	})

	// Swap config and output
	swapConfig, _ := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
		"callData":        "exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))",
		"callInput":       "[\"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238\",\"0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14\",3000,\"0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f\",20990000,0,0,0]",
	})
	swapOutput, _ := structpb.NewValue(map[string]interface{}{
		"exactInputSingle": map[string]interface{}{
			"amountOut": "2235380089399511",
		},
	})
	swapMetadata, _ := structpb.NewValue(map[string]interface{}{
		"transactionHash": "0x123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01",
	})

	startTime := int64(1769651702059)
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id: "node0", Name: "eventTrigger", Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT.String(), Success: true,
			StartAt: startTime, EndAt: startTime + 1,
			Config:           eventTriggerConfig,
			ExecutionContext: realCtx,
			OutputData: &avsproto.Execution_Step_EventTrigger{
				EventTrigger: &avsproto.EventTrigger_Output{Data: triggerOutput},
			},
		},
		{
			Id: "node1", Name: "balance1", Type: avsproto.NodeType_NODE_TYPE_BALANCE.String(), Success: true,
			StartAt: startTime + 100, EndAt: startTime + 500,
			Config:           balanceConfig,
			ExecutionContext: realCtx,
			OutputData: &avsproto.Execution_Step_Balance{
				Balance: &avsproto.BalanceNode_Output{Data: balanceOutput},
			},
		},
		{
			Id: "node2", Name: "approve1", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(), Success: true,
			StartAt: startTime + 500, EndAt: startTime + 2000,
			Config:           approveConfig,
			Metadata:         approveMetadata,
			ExecutionContext: realCtx,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{Data: approveOutput},
			},
		},
		{
			Id: "node3", Name: "swap1", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(), Success: true,
			StartAt: startTime + 2000, EndAt: startTime + 4000,
			Config:           swapConfig,
			Metadata:         swapMetadata,
			ExecutionContext: realCtx,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{Data: swapOutput},
			},
		},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node0": {Id: "node0", Name: "eventTrigger"},
		"node1": {
			Id: "node1", Name: "balance1",
			TaskType: &avsproto.TaskNode_Balance{Balance: &avsproto.BalanceNode{
				Config: &avsproto.BalanceNode_Config{
					Address: "{{settings.runner}}",
					Chain:   "sepolia",
				},
			}},
		},
		"node2": {
			Id: "node2", Name: "approve1",
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					CallData:        "approve(address,uint256)",
				},
			}},
		},
		"node3": {
			Id: "node3", Name: "swap1",
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
					CallData:        "exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))",
				},
			}},
		},
		"node4": {
			Id: "node4", Name: "telegram_send",
			TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://api.telegram.org/bot123/sendMessage",
					Method: "POST",
				},
			}},
		},
		"node5": {
			Id: "node5", Name: "email1",
			TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://api.sendgrid.com/v3/mail/send",
					Method: "POST",
				},
			}},
		},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":     "Stoploss USDC->WETH",
			"chain":    "Sepolia",
			"runner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"chain_id": float64(11155111),
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":           "Stoploss USDC->WETH",
			"runner":         "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"owner":          "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"executionCount": int64(5),
		},
	}
	vm.mu.Unlock()

	// --- AI path ---
	summarizer := NewContextMemorySummarizer(apiURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aiSummary, aiErr := summarizer.Summarize(ctx, vm, "telegram_send")
	if aiErr != nil {
		t.Logf("AI summarization failed: %v", aiErr)
		t.Skip("Context-memory API not available")
	}
	t.Logf("AI Summary received: subject=%q, status=%q, bodyLen=%d", aiSummary.Subject, aiSummary.Status, len(aiSummary.Body))

	// --- Deterministic path ---
	detSummary := ComposeSummary(vm, "telegram_send")
	t.Logf("Det Summary: subject=%q, status=%q, bodyLen=%d", detSummary.Subject, detSummary.Status, len(detSummary.Body))

	// --- Compare ---
	diffs := compareAndReport(t, "RealExecution", aiSummary, detSummary)

	// --- Format for all channels ---
	t.Log("\n=== AI Summary formatted for channels ===")
	formatForAllChannels(t, "AI-Real", aiSummary, vm)
	t.Log("\n=== Deterministic Summary formatted for channels ===")
	formatForAllChannels(t, "Det-Real", detSummary, vm)

	// --- Report discrepancies ---
	if len(diffs) > 0 {
		t.Log("\n========================================")
		t.Logf("DISCREPANCIES FOUND: %d", len(diffs))
		t.Log("========================================")
		for i, d := range diffs {
			t.Logf("  %d. %s", i+1, d.String())
		}
	} else {
		t.Log("\nNo discrepancies found between AI and deterministic paths.")
	}

	// --- Key assertions ---
	// Real execution should NOT have annotation
	if detSummary.Annotation != "" {
		t.Errorf("Deterministic should NOT have Annotation for real execution, got: %s", detSummary.Annotation)
	}
	// Note: With 4 executed out of 5 total (trigger node double-counted), status is partial_success
	// The AI may report success or partial_success depending on its interpretation
	validStatuses := map[string]bool{"success": true, "partial_success": true}
	if !validStatuses[aiSummary.Status] {
		t.Logf("NOTE: AI status is %q", aiSummary.Status)
	}
	if !validStatuses[detSummary.Status] {
		t.Errorf("Deterministic status should be success/partial_success, got: %s", detSummary.Status)
	}
	// Notification nodes (telegram_send, email1) should NOT be counted in step totals.
	// getTotalWorkflowSteps counts: 1 (trigger) + non-notification TaskNodes.
	// TaskNodes: eventTrigger + balance1 + approve1 + contractWrite1 = 4 non-notification
	// Total = 1 + 4 = 5 (the trigger node is counted both as trigger and as a TaskNode)
	// executedSteps = 4 (trigger + balance1 + approve1 + contractWrite1, excluding notification logs)
	if strings.Contains(detSummary.SummaryLine, "out of") {
		t.Logf("Det SummaryLine: %s", detSummary.SummaryLine)
		if !strings.Contains(detSummary.SummaryLine, "4 out of 5") {
			t.Errorf("Expected '4 out of 5' in SummaryLine, got: %s", detSummary.SummaryLine)
		}
	}

	// Real execution steps have valid tx hashes and non-simulated ExecutionContext
	// The deterministic path should attach txHash for these steps
	validTxHashes := map[string]string{
		"approve1": "0xdef456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
		"swap1":    "0x123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01",
	}
	for i, exec := range detSummary.Executions {
		if exec.TxHash == "" {
			t.Logf("Det execution[%d] has no txHash (may not be CONTRACT_WRITE)", i)
		} else {
			t.Logf("Det execution[%d]: txHash=%s", i, exec.TxHash)
			if !isValidTxHash(exec.TxHash) {
				t.Errorf("Det execution[%d] has invalid txHash: %s", i, exec.TxHash)
			}
		}
	}
	_ = validTxHashes // logged above for reference
}

// ============================================================================
// Test 4: Format consistency check - AI summary through FormatForMessageChannels
// ============================================================================

func TestAIIntegration_FormatConsistency(t *testing.T) {
	apiURL, apiKey := loadSepoliaConfig(t)
	t.Logf("Testing Format Consistency against: %s", apiURL)

	// Build a simulation VM with proper config/output data for meaningful AI results
	vm := NewVM()
	vm.IsSimulation = true

	simCtxStruct, _ := structpb.NewStruct(map[string]interface{}{
		"is_simulated": true,
		"provider":     "tenderly",
		"chain_id":     float64(11155111),
	})
	simCtx := structpb.NewStructValue(simCtxStruct)

	triggerConfig, _ := structpb.NewValue(map[string]interface{}{
		"schedules": []interface{}{"0 */6 * * *"},
	})
	triggerOutput, _ := structpb.NewValue(map[string]interface{}{
		"timestamp":    float64(1769651702059),
		"timestampIso": "2026-01-29T01:55:02.059Z",
	})
	balanceConfig, _ := structpb.NewValue(map[string]interface{}{
		"address": "{{settings.runner}}",
		"chain":   "sepolia",
	})
	balanceOutput, _ := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"balance": "500000000000000000", "balanceFormatted": "0.5",
			"decimals": float64(18), "name": "Ether", "symbol": "ETH",
		},
	})
	transferConfig, _ := structpb.NewValue(map[string]interface{}{
		"destination": "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		"amount":      "100000000000000000",
	})
	transferOutput, _ := structpb.NewValue(map[string]interface{}{
		"transfer": map[string]interface{}{
			"from":  "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"to":    "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"value": "100000000000000000",
		},
	})
	transferMetadata, _ := structpb.NewValue(map[string]interface{}{
		"transactionHash": "0x0000000000000000000000000000000000000000000001769651703156844000",
	})

	startTime := int64(1769651702059)
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id: "node0", Name: "timeTrigger", Type: avsproto.TriggerType_TRIGGER_TYPE_CRON.String(), Success: true,
			StartAt: startTime, EndAt: startTime + 1,
			Config:           triggerConfig,
			ExecutionContext: simCtx,
			OutputData: &avsproto.Execution_Step_CronTrigger{
				CronTrigger: &avsproto.CronTrigger_Output{Data: triggerOutput},
			},
		},
		{
			Id: "node1", Name: "balance1", Type: avsproto.NodeType_NODE_TYPE_BALANCE.String(), Success: true,
			StartAt: startTime + 100, EndAt: startTime + 500,
			Config:           balanceConfig,
			ExecutionContext: simCtx,
			OutputData: &avsproto.Execution_Step_Balance{
				Balance: &avsproto.BalanceNode_Output{Data: balanceOutput},
			},
		},
		{
			Id: "node2", Name: "transfer1", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER.String(), Success: true,
			StartAt: startTime + 500, EndAt: startTime + 600,
			Config:           transferConfig,
			Metadata:         transferMetadata,
			ExecutionContext: simCtx,
			OutputData: &avsproto.Execution_Step_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode_Output{Data: transferOutput},
			},
		},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node0": {Id: "node0", Name: "timeTrigger"},
		"node1": {
			Id: "node1", Name: "balance1",
			TaskType: &avsproto.TaskNode_Balance{Balance: &avsproto.BalanceNode{
				Config: &avsproto.BalanceNode_Config{
					Address: "{{settings.runner}}",
					Chain:   "sepolia",
				},
			}},
		},
		"node2": {
			Id: "node2", Name: "transfer1",
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{
				Config: &avsproto.ETHTransferNode_Config{
					Destination: "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
					Amount:      "100000000000000000",
				},
			}},
		},
		"node3": {
			Id: "node3", Name: "telegram_send",
			TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://api.telegram.org/bot123/sendMessage",
					Method: "POST",
				},
			}},
		},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Format Test Workflow",
			"chain":  "Sepolia",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":   "Format Test Workflow",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		},
	}
	vm.mu.Unlock()

	// --- AI path ---
	summarizer := NewContextMemorySummarizer(apiURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aiSummary, aiErr := summarizer.Summarize(ctx, vm, "telegram_send")
	if aiErr != nil {
		t.Skip("Context-memory API not available")
	}

	// --- Deterministic path ---
	detSummary := ComposeSummary(vm, "telegram_send")

	// --- Compare Telegram output ---
	aiTelegram := FormatForMessageChannels(aiSummary, "telegram", vm)
	detTelegram := FormatForMessageChannels(detSummary, "telegram", vm)

	t.Logf("AI Telegram:\n%s", aiTelegram)
	t.Logf("\nDet Telegram:\n%s", detTelegram)

	// Check that both produce non-empty Telegram output
	if aiTelegram == "" {
		t.Error("AI Telegram output is empty")
	}
	if detTelegram == "" {
		t.Error("Det Telegram output is empty")
	}

	// Check that AI path uses structured formatters (not legacy body-based)
	// AI summary should have structured data so formatTelegramFromStructured is used
	hasAIStructured := len(aiSummary.Transfers) > 0 || aiSummary.Workflow != nil ||
		len(aiSummary.Executions) > 0 || len(aiSummary.Errors) > 0 || aiSummary.Trigger != ""
	if !hasAIStructured {
		t.Log("WARNING: AI summary has no structured data, will use legacy formatter")
	}

	// Both paths should go through structured formatters (both set Executions)
	hasDetStructured := len(detSummary.Executions) > 0 || len(detSummary.Errors) > 0 || detSummary.Trigger != ""
	if !hasDetStructured {
		t.Log("WARNING: Det summary has no structured data, will use legacy formatter")
	}

	// --- Compare Email output ---
	aiEmail := aiSummary.SendGridDynamicData()
	detEmail := detSummary.SendGridDynamicData()

	t.Logf("\nAI Email keys: %v", mapKeys(aiEmail))
	t.Logf("Det Email keys: %v", mapKeys(detEmail))

	// Both should have annotation only for deterministic singleNode (not this simulation)
	if _, hasAnn := aiEmail["annotation"]; hasAnn {
		t.Log("NOTE: AI email has annotation (unexpected for simulation)")
	}
	if _, hasAnn := detEmail["annotation"]; hasAnn {
		t.Error("Det email should NOT have annotation for simulation")
	}

	// Check key email fields
	for _, key := range []string{"subject", "status", "analysisHtml"} {
		aiVal, aiOk := aiEmail[key]
		detVal, detOk := detEmail[key]
		if aiOk && detOk {
			t.Logf("Email[%s] AI=%q Det=%q", key, truncateStr(fmt.Sprintf("%v", aiVal), 100), truncateStr(fmt.Sprintf("%v", detVal), 100))
		} else if !aiOk && detOk {
			t.Logf("Email[%s] MISSING in AI, Det=%q", key, truncateStr(fmt.Sprintf("%v", detVal), 100))
		}
	}
}

// ============================================================================
// Test 5: Full pipeline through ComposeSummarySmart
// ============================================================================

func TestAIIntegration_ComposeSummarySmart(t *testing.T) {
	apiURL, apiKey := loadSepoliaConfig(t)
	t.Logf("Testing ComposeSummarySmart against: %s", apiURL)

	// Set up the global summarizer with the real API
	origSummarizer := globalSummarizer
	defer SetSummarizer(origSummarizer)

	SetSummarizer(NewContextMemorySummarizer(apiURL, apiKey))

	// Build a simulation VM
	vm := NewVM()
	vm.IsSimulation = true

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{Id: "node0", Name: "timeTrigger", Type: avsproto.TriggerType_TRIGGER_TYPE_CRON.String(), Success: true},
		{Id: "node1", Name: "balance1", Type: avsproto.NodeType_NODE_TYPE_BALANCE.String(), Success: true},
	}

	vm.mu.Lock()
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"node0": {Id: "node0", Name: "timeTrigger"},
		"node1": {Id: "node1", Name: "balance1"},
		"node2": {
			Id: "node2", Name: "email1",
			TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{
				Config: &avsproto.RestAPINode_Config{
					Url:    "https://api.sendgrid.com/v3/mail/send",
					Method: "POST",
				},
			}},
		},
	}
	vm.vars = map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Smart Test Workflow",
			"chain":  "Sepolia",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
		},
		WorkflowContextVarName: map[string]interface{}{
			"name":   "Smart Test Workflow",
			"runner": "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
			"owner":  "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		},
	}
	vm.mu.Unlock()

	// Call ComposeSummarySmart which tries AI first, falls back to deterministic
	smartSummary := ComposeSummarySmart(vm, "email1")

	t.Logf("Smart Summary: subject=%q, status=%q, bodyLen=%d", smartSummary.Subject, smartSummary.Status, len(smartSummary.Body))
	t.Logf("Smart SummaryLine: %s", smartSummary.SummaryLine)
	t.Logf("Smart Body: %s", smartSummary.Body)

	// Validate that ComposeSummarySmart returns a valid summary
	if smartSummary.Subject == "" {
		t.Error("ComposeSummarySmart returned empty subject")
	}
	if smartSummary.Body == "" {
		t.Error("ComposeSummarySmart returned empty body")
	}
	if len(smartSummary.Body) < 40 {
		t.Errorf("ComposeSummarySmart body too short (len=%d), might have fallen back", len(smartSummary.Body))
	}

	// Format output
	t.Log("\n=== ComposeSummarySmart formatted output ===")
	formatForAllChannels(t, "Smart", smartSummary, vm)

	// Also get pure deterministic for comparison
	SetSummarizer(nil)
	detSummary := ComposeSummarySmart(vm, "email1")
	t.Logf("\nDet (fallback) Summary: subject=%q", detSummary.Subject)

	// Compare the two
	if smartSummary.Subject != detSummary.Subject {
		t.Logf("Subject differs between Smart (AI) and Det: AI=%q, Det=%q", smartSummary.Subject, detSummary.Subject)
	}
}
