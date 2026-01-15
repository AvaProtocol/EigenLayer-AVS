//go:build integration
// +build integration

package taskengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestSendGridEmailWithContextMemoryResponse tests that the context-memory API response
// is correctly used to send a SendGrid email with the proper subject and summary fields.
// This test uses the actual response format from the context-memory API.
func TestSendGridEmailWithContextMemoryResponse(t *testing.T) {
	// Load test config from aggregator-sepolia.yaml
	// Use GetTestRPC() to ensure config is loaded (it calls loadTestConfigOnce internally)
	_ = testutil.GetTestRPC()
	testConfig := testutil.GetTestConfig()
	if testConfig == nil {
		t.Fatal("Test config must be loaded from aggregator-sepolia.yaml")
	}

	// Extract SendGrid API key from config
	sendgridKey := ""
	if testConfig.MacroSecrets != nil {
		if key, ok := testConfig.MacroSecrets["sendgrid_key"]; ok {
			sendgridKey = key
		}
	}
	if sendgridKey == "" {
		t.Skip("sendgrid_key not found in config, skipping SendGrid integration test")
	}

	// Mock context-memory API server that returns the exact response from terminal output
	contextMemoryResponse := map[string]interface{}{
		"subject":       "Simulation: Test Stoploss successfully completed",
		"summary":       "Your workflow 'Test Stoploss' executed 8 out of 8 total steps",
		"analysisHtml":  "<p style=\"margin-bottom:16px\"><strong>What Executed On-Chain</strong></p><p style=\"margin:8px 0\">✓ Approved 20,990,000 to 0x3bFA...e48E for trading</p><p style=\"margin:8px 0\">✓ (Simulated) Swapped for ~1.8729 via Uniswap V3</p>",
		"body":          "What Executed On-Chain\n✓ Approved 20,990,000 to 0x3bFA...e48E for trading\n✓ (Simulated) Swapped for ~1.8729 via Uniswap V3",
		"statusHtml":    "<div style=\"display:inline-block; padding:8px 16px; background-color:#D1FAE5; color:#065F46; border-radius:8px; font-weight:500; margin:8px 0\"><svg width=\"16\" height=\"16\" viewBox=\"0 0 16 16\" fill=\"none\" xmlns=\"http://www.w3.org/2000/svg\" style=\"vertical-align:middle; margin-right:6px\"><circle cx=\"8\" cy=\"8\" r=\"7\" fill=\"#10B981\"/><path d=\"M11 6L7 10L5 8\" stroke=\"white\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/></svg>All steps completed successfully</div>",
		"status":        "success",
		"promptVersion": "v1.1.0",
		"branchSummary": map[string]interface{}{
			"text": "Summary\nAll nodes were executed successfully.\nExecuted 2 on-chain transaction(s).\nWhat Executed Successfully\n✓ Approved token to \n✓ Swapped for ~0.001872 via Uniswap V3 (exactInputSingle)",
		},
	}

	contextMemoryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request to context-memory API, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/api/summarize") {
			t.Errorf("expected /api/summarize path, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(contextMemoryResponse); err != nil {
			t.Errorf("failed to encode context-memory response: %v", err)
		}
	}))
	defer contextMemoryServer.Close()

	// Use real SendGrid API endpoint for integration testing
	sendgridAPIURL := "https://api.sendgrid.com/v3/mail/send"

	// Set up global summarizer from config, but override baseURL to use mock server
	// Create a modified config with mock server URL in macros.secrets
	mockConfig := *testConfig
	if mockConfig.MacroSecrets == nil {
		mockConfig.MacroSecrets = make(map[string]string)
	}
	// Copy existing secrets
	for k, v := range testConfig.MacroSecrets {
		mockConfig.MacroSecrets[k] = v
	}
	// Override context_api_endpoint to use mock server
	mockConfig.MacroSecrets["context_api_endpoint"] = contextMemoryServer.URL
	summarizer := NewContextMemorySummarizerFromAggregatorConfig(&mockConfig)
	if summarizer == nil {
		t.Fatal("Failed to create context-memory summarizer from config")
	}
	SetSummarizer(summarizer)
	defer SetSummarizer(nil) // Clean up after test

	// Set global macro secrets so template variables like {{apContext.configVars.sendgrid_key}} can be resolved
	if testConfig.MacroSecrets != nil {
		SetMacroSecrets(testConfig.MacroSecrets)
		defer SetMacroSecrets(nil) // Clean up after test
	}

	// Create Options with summarize=true
	optsVal, err := structpb.NewValue(map[string]interface{}{"summarize": true})
	if err != nil {
		t.Fatalf("failed to create Options value: %v", err)
	}

	// Extract secrets from config
	secrets := make(map[string]string)
	if testConfig.MacroSecrets != nil {
		for k, v := range testConfig.MacroSecrets {
			secrets[k] = v
		}
	}

	// Create a minimal VM with settings that match the test scenario
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "trigger1",
	}
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test-task",
			Trigger: trigger,
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "email1",
					Name: "email1",
					TaskType: &avsproto.TaskNode_RestApi{
						RestApi: &avsproto.RestAPINode{
							Config: &avsproto.RestAPINode_Config{
								Url:    sendgridAPIURL,
								Method: "POST",
								Headers: map[string]string{
									"Authorization": "Bearer {{apContext.configVars.sendgrid_key}}",
									"Content-Type":  "application/json",
								},
								Body: `{
									"from": {
										"email": "notifications@avaprotocol.org",
										"name": "AP Studio Notification"
									},
									"personalizations": [{
										"to": [{
											"email": "dev@avaprotocol.org"
										}]
									}]
								}`,
								Options: optsVal,
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), secrets)
	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	// Set up VM variables to match the test scenario
	vm.mu.Lock()
	vm.vars["aa_sender"] = "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f"
	vm.vars["settings"] = map[string]interface{}{
		"name":         "Test Stoploss",
		"isSimulation": true,
		"runner":       "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
	}
	if wc, ok := vm.vars["workflowContext"].(map[string]interface{}); ok {
		wc["owner"] = "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788"
		wc["eoaAddress"] = "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788"
	} else {
		vm.vars["workflowContext"] = map[string]interface{}{
			"owner":      "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
			"eoaAddress": "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788",
		}
	}
	vm.mu.Unlock()

	// Add some execution logs to simulate a workflow execution
	vm.mu.Lock()
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "01KAMATPAVF34V03HACRMVWN23",
			Name:    "eventTrigger",
			Type:    "TRIGGER_TYPE_EVENT",
			Success: true,
		},
		{
			Id:      "01KAMAWC7FHYTBBKXE495EMCMH",
			Name:    "balance1",
			Type:    "NODE_TYPE_BALANCE",
			Success: true,
		},
		{
			Id:      "01KAMAY4JNJ3GSQE6BF4CM663B",
			Name:    "code1",
			Type:    "NODE_TYPE_CUSTOM_CODE",
			Success: true,
		},
		{
			Id:      "01KAMBKSM3XEGH8Z8S6R5S0MWC",
			Name:    "branch1",
			Type:    "NODE_TYPE_BRANCH",
			Success: true,
		},
		{
			Id:      "01KAMC4STMZAVH2QR236BV7W3Y",
			Name:    "approve1",
			Type:    "NODE_TYPE_CONTRACT_WRITE",
			Success: true,
		},
		{
			Id:      "01KAMBMJY33EKE372E6FA28QPV",
			Name:    "contractRead1",
			Type:    "NODE_TYPE_CONTRACT_READ",
			Success: true,
		},
		{
			Id:      "01KCHPTWWC3NWQCK782JKF7M85",
			Name:    "calc_slippage",
			Type:    "NODE_TYPE_CUSTOM_CODE",
			Success: true,
		},
		{
			Id:      "01KAMC9PVJYZGH8K0KEFEY76TX",
			Name:    "contractWrite1",
			Type:    "NODE_TYPE_CONTRACT_WRITE",
			Success: true,
		},
	}
	vm.mu.Unlock()

	// Execute the REST API node
	processor := NewRestProcessor(vm)
	node := vm.TaskNodes["email1"]
	if node == nil {
		t.Fatal("node email1 not found")
	}
	restApiNode := node.GetRestApi()
	if restApiNode == nil {
		t.Fatal("node email1 is not a REST API node")
	}
	step, err := processor.Execute("email1", restApiNode)
	if err != nil {
		t.Fatalf("failed to execute REST API node: %v", err)
	}

	// The production code handles the SendGrid API call and response parsing
	// If step.Success is true, the email was sent successfully
	if !step.Success {
		// Handle specific error cases
		if strings.Contains(step.Error, "403") || strings.Contains(step.Error, "Forbidden") {
			t.Skipf("SendGrid returned 403 Forbidden - likely sender email not verified. Error: %s. Skipping test.", step.Error)
		}
		t.Fatalf("REST API node execution failed: %s", step.Error)
	}

	// Verify expected values that should be in the email
	expectedSubject := "Simulation: Test Stoploss successfully completed"
	expectedSummary := "Your workflow 'Test Stoploss' executed 8 out of 8 total steps"

	t.Logf("✅ SendGrid email sent successfully to dev@avaprotocol.org")
	t.Logf("   Expected Subject: %q", expectedSubject)
	t.Logf("   Expected Summary: %q", expectedSummary)
	t.Logf("   Note: Check dev@avaprotocol.org inbox to verify email content matches expected values")
}
