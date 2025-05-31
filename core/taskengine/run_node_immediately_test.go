package taskengine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func createTestEngine(t *testing.T) *Engine {
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})
	config := testutil.GetAggregatorConfig()
	return New(db, config, nil, testutil.GetLogger())
}

// Test immediate execution of blockTrigger with specific block number
func TestRunNodeImmediately_BlockTrigger(t *testing.T) {
	engine := createTestEngine(t)

	result, err := engine.RunNodeImmediately(NodeTypeBlockTrigger, map[string]interface{}{
		"blockNumber": 12345,
	}, map[string]interface{}{})

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Contains(t, result, "blockNumber")
	assert.Equal(t, uint64(12345), result["blockNumber"])
}

// Test immediate execution of custom code node
func TestRunNodeImmediately_CustomCode(t *testing.T) {
	vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{}, nil)
	assert.NoError(t, err)

	nodeId := "test_" + ulid.Make().String()
	node := &avsproto.TaskNode{
		Id:   nodeId,
		Name: "Test Custom Code",
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Lang: avsproto.Lang_JavaScript,
					Source: `
					if (typeof myVar === 'undefined') {
						throw new Error("myVar is required but not provided");
					}
					({ result: myVar * 2 })
				`,
				},
			},
		},
	}

	_, err = vm.RunNodeWithInputs(node, map[string]interface{}{})
	assert.Error(t, err)

	result, err := vm.RunNodeWithInputs(node, map[string]interface{}{
		"myVar": 5,
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)

	codeOutput := result.GetCustomCode()
	assert.NotNil(t, codeOutput)
	assert.NotNil(t, codeOutput.Data)
}

// Test CreateNodeFromType utility function
func TestCreateNodeFromType(t *testing.T) {
	node, err := CreateNodeFromType(NodeTypeBlockTrigger, map[string]interface{}{}, "")
	assert.NoError(t, err)
	assert.NotNil(t, node)
	assert.Equal(t, "Single Node Execution: "+NodeTypeBlockTrigger, node.Name)
}

// Test immediate execution of various node types
func TestRunNodeImmediately_AllNodeTypes(t *testing.T) {
	engine := createTestEngine(t)

	// Test different node types
	nodeTypes := []string{NodeTypeBlockTrigger, NodeTypeRestAPI, NodeTypeContractRead, NodeTypeCustomCode, NodeTypeBranch, NodeTypeFilter}

	for _, nodeType := range nodeTypes {
		t.Run(fmt.Sprintf("NodeType_%s", nodeType), func(t *testing.T) {
			var config map[string]interface{}
			switch nodeType {
			case NodeTypeBlockTrigger:
				config = map[string]interface{}{"blockNumber": 12345}
			case NodeTypeRestAPI:
				config = map[string]interface{}{
					"url":    "https://httpbin.org/get",
					"method": "GET",
				}
			case NodeTypeContractRead:
				config = map[string]interface{}{
					"contract_address": "0x1234567890123456789012345678901234567890",
					"call_data":        "0x123456",
					"contract_abi":     "[]",
				}
			case NodeTypeCustomCode:
				config = map[string]interface{}{
					"source": "return {result: 'test'};",
					"lang":   "javascript",
				}
			case NodeTypeBranch:
				config = map[string]interface{}{
					"expression": "true",
				}
			case NodeTypeFilter:
				config = map[string]interface{}{
					"expression": "true",
					"source_id":  "test_source",
				}
			}

			result, err := engine.RunNodeImmediately(nodeType, config, map[string]interface{}{})

			switch nodeType {
			case NodeTypeBlockTrigger:
				// BlockTrigger should always work with mock data
				assert.NoError(t, err)
				assert.NotNil(t, result)
			case NodeTypeRestAPI:
				// REST API should work with proper config
				// Note: might fail due to network, but should not be a config error
				if err != nil {
					t.Logf("REST API error (expected due to network): %v", err)
					// Should not be a config-related error
					assert.NotContains(t, err.Error(), "Config is nil")
				}
			case NodeTypeContractRead:
				// Contract read might fail due to network/RPC, but should not be a config error
				if err != nil {
					t.Logf("Contract read error (expected due to network): %v", err)
					// Should not be a config-related error
					assert.NotContains(t, err.Error(), "Config is nil")
				}
			case NodeTypeCustomCode:
				// Should work with proper config
				if err != nil {
					t.Logf("Custom code error: %v", err)
					// Should not be a config-related error
					assert.NotContains(t, err.Error(), "Config is nil")
				} else {
					assert.NotNil(t, result)
				}
			case NodeTypeBranch:
				// Should work with proper config
				if err != nil {
					t.Logf("Branch error: %v", err)
					// Should not be a config-related error
					assert.NotContains(t, err.Error(), "Config is nil")
				}
			case NodeTypeFilter:
				// Should work with proper config
				if err != nil {
					t.Logf("Filter error: %v", err)
					// Should not be a config-related error
					assert.NotContains(t, err.Error(), "Config is nil")
				}
			}
		})
	}

	// Test specific functionality for blockTrigger
	result, err := engine.RunNodeImmediately(NodeTypeBlockTrigger, map[string]interface{}{
		"blockNumber": 12345,
	}, map[string]interface{}{})

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Contains(t, result, "blockNumber")
	assert.Equal(t, uint64(12345), result["blockNumber"])

	// Test custom code with proper configuration
	result, err = engine.RunNodeImmediately(NodeTypeCustomCode, map[string]interface{}{
		"source": `
			return {
				message: "Hello World",
				timestamp: Date.now(),
				input: inputVariables
			};
		`,
		"lang": "javascript",
	}, map[string]interface{}{
		"testInput": "test value",
	})

	// Should now succeed since CreateNodeFromType creates proper Config
	if err != nil {
		t.Logf("Custom code error: %v", err)
		// Should not be a config-related error anymore
		assert.NotContains(t, err.Error(), "Config is nil")
	} else {
		// Should succeed with proper result
		assert.NotNil(t, result)
		if result != nil {
			if message, ok := result["message"]; ok {
				assert.Equal(t, "Hello World", message)
			}
		}
	}
}

// Test immediate execution of different trigger types
func TestRunNodeImmediately_TriggerTypes(t *testing.T) {
	engine := createTestEngine(t)

	// Test FixedTimeTrigger immediate execution
	t.Run("FixedTimeTrigger", func(t *testing.T) {
		result, err := engine.RunNodeImmediately(NodeTypeFixedTimeTrigger, map[string]interface{}{}, map[string]interface{}{})
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, result, "epoch")
		assert.IsType(t, uint64(0), result["epoch"])
	})

	// Test CronTrigger immediate execution
	t.Run("CronTrigger", func(t *testing.T) {
		result, err := engine.RunNodeImmediately(NodeTypeCronTrigger, map[string]interface{}{}, map[string]interface{}{})
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, result, "epoch")
		assert.Contains(t, result, "scheduleMatched")
		assert.Equal(t, "immediate_execution", result["scheduleMatched"])
	})

	// Test EventTrigger immediate execution (simulation)
	t.Run("EventTrigger", func(t *testing.T) {
		result, err := engine.RunNodeImmediately(NodeTypeEventTrigger, map[string]interface{}{}, map[string]interface{}{})
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, result, "simulated")
		assert.Equal(t, true, result["simulated"])
		assert.Contains(t, result, "message")
	})

	// Test ManualTrigger immediate execution
	t.Run("ManualTrigger", func(t *testing.T) {
		result, err := engine.RunNodeImmediately(NodeTypeManualTrigger, map[string]interface{}{}, map[string]interface{}{})
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, result, "triggered")
		assert.Equal(t, true, result["triggered"])
		assert.Contains(t, result, "runAt")
		assert.IsType(t, uint64(0), result["runAt"])
	})
}

// Test immediate execution with real-world template processing (Telegram bot example)
func TestRunNodeImmediately_RestAPIWithTemplates(t *testing.T) {
	engine := createTestEngine(t)

	// Test REST API with template variables (Telegram bot notification)
	t.Run("TelegramBotWithTemplates", func(t *testing.T) {
		// Test variables
		testChatID := "452247333"
		testBlockNumber := 18500000

		// Setup global secrets that should be available via apContext.configVars
		SetMacroSecrets(map[string]string{
			"ap_notify_bot_token": "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZ123456789",
		})

		// Create a mock server instead of using real Telegram API
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify the request structure
			assert.Equal(t, "POST", r.Method)
			assert.Contains(t, r.URL.Path, "/sendMessage")
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			// Return mock Telegram API response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			fmt.Fprint(w, `{
				"ok": true,
				"result": {
					"message_id": 123,
					"chat": {"id": 452247333, "type": "private"},
					"date": 1640995200,
					"text": "test: Workflow is triggered by block: 18500000"
				}
			}`)
		}))
		defer mockServer.Close()

		// Configuration with template variables pointing to mock server
		config := map[string]interface{}{
			"url":    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
			"method": "POST",
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
			"body": fmt.Sprintf(`{"chat_id":"%s","text":"test: Workflow is triggered by block: {{trigger.data.block_number}}"}`, testChatID),
		}

		// Input variables for template processing
		inputVariables := map[string]interface{}{
			"workflowContext": map[string]interface{}{
				"id":     "test-workflow-123",
				"userId": "test-user-456",
			},
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"block_number": testBlockNumber,
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed with mock server
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, float64(200), result["statusCode"])

		// Verify mock response structure
		if body, exists := result["body"]; exists {
			bodyMap, ok := body.(map[string]interface{})
			assert.True(t, ok)
			assert.Equal(t, true, bodyMap["ok"])
		}
	})

	// Test validation logic with simulated successful Telegram response
	t.Run("TelegramResponseValidation", func(t *testing.T) {
		// Test variables - same as used in TelegramBotWithTemplates
		testChatID := "452247333"
		testBlockNumber := 18500000
		expectedMessage := fmt.Sprintf("test: Workflow is triggered by block: %d", testBlockNumber)

		// Setup global secrets that should be available via apContext.configVars
		SetMacroSecrets(map[string]string{
			"ap_notify_bot_token": "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZ123456789",
		})

		// Create a mock HTTP server that returns the Telegram API response
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify the request is what we expect
			assert.Equal(t, "POST", r.Method)
			assert.Contains(t, r.URL.Path, "/sendMessage")
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			// Read and verify the request body
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)

			var requestBody map[string]interface{}
			err = json.Unmarshal(body, &requestBody)
			assert.NoError(t, err)

			// Verify the chat_id and text with template processing
			assert.Equal(t, testChatID, requestBody["chat_id"])
			assert.Equal(t, expectedMessage, requestBody["text"])

			// Return the mock Telegram API response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)

			// Create response structure and marshal to JSON to ensure proper escaping
			response := map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"message_id": 479,
					"from": map[string]interface{}{
						"id":         7771086042,
						"is_bot":     true,
						"first_name": "AvaProtocolBotDev",
						"username":   "AvaProtocolDevBot",
					},
					"chat": map[string]interface{}{
						"id":         testChatID,
						"first_name": "Chris | Ava Protocol",
						"username":   "kezjo",
						"type":       "private",
					},
					"date": 1748635350,
					"text": expectedMessage,
				},
			}

			responseJSON, err := json.Marshal(response)
			assert.NoError(t, err)
			w.Write(responseJSON)
		}))

		defer mockServer.Close()

		// Configuration pointing to our mock server with template variables
		config := map[string]interface{}{
			"url":    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
			"method": "POST",
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
			"body": fmt.Sprintf(`{"chat_id":"%s","text":"test: Workflow is triggered by block: {{trigger.data.block_number}}"}`, testChatID),
		}

		// Input variables for template processing
		inputVariables := map[string]interface{}{
			"workflowContext": map[string]interface{}{
				"id":     "test-workflow-123",
				"userId": "test-user-456",
			},
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"block_number": testBlockNumber,
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed with mock server
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify the response structure
		assert.Equal(t, float64(200), result["statusCode"])

		// Check the response body contains our message with the actual block number
		body, exists := result["body"]
		assert.True(t, exists)
		bodyMap, ok := body.(map[string]interface{})
		assert.True(t, ok)

		// Debug: log the actual response structure
		t.Logf("Actual response body structure: %+v", bodyMap)

		// The parsed JSON response should have "ok" and "result" fields
		okField, exists := bodyMap["ok"]
		assert.True(t, exists)
		assert.Equal(t, true, okField)

		resultField, exists := bodyMap["result"]
		assert.True(t, exists)
		resultObj, ok := resultField.(map[string]interface{})
		assert.True(t, ok)

		text, exists := resultObj["text"]
		assert.True(t, exists)
		textStr, ok := text.(string)
		assert.True(t, ok)

		// Verify the final message contains "undefined" for the missing block_number
		assert.Contains(t, textStr, fmt.Sprintf("triggered by block: %d", testBlockNumber))

		t.Logf("Final message text: %s", textStr)
	})

	// Test template processing with missing variables
	t.Run("TemplateWithMissingVariables", func(t *testing.T) {
		config := map[string]interface{}{
			"url":    "https://api.example.com/{{missing.variable}}/endpoint",
			"method": "GET",
		}

		inputVariables := map[string]interface{}{
			"present": "value",
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should handle missing template variables gracefully
		if err != nil {
			t.Logf("Template processing error: %v", err)
		} else {
			assert.NotNil(t, result)
		}
	})

	// Test headersMap format processing
	t.Run("HeadersMapFormat", func(t *testing.T) {
		config := map[string]interface{}{
			"url":    "https://httpbin.org/headers",
			"method": "GET",
			"headersMap": [][]string{
				{"X-Custom-Header", "custom-value"},
				{"Authorization", "Bearer {{token}}"},
			},
		}

		inputVariables := map[string]interface{}{
			"token": "abc123def456",
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Network call might fail, but config should be processed correctly
		if err != nil {
			t.Logf("REST API error (expected due to network): %v", err)
			// Should not be config-related errors
			assert.NotContains(t, err.Error(), "Config is nil")
			assert.NotContains(t, err.Error(), "missing required configuration")
		} else {
			assert.NotNil(t, result)
			t.Logf("Headers test result: %+v", result)
		}
	})

	// Test input variables overriding config
	t.Run("InputVariablesOverrideConfig", func(t *testing.T) {
		// Config provides base values
		config := map[string]interface{}{
			"url":    "https://httpbin.org/get",
			"method": "GET",
			"body":   "original body",
		}

		// Input variables override config
		inputVariables := map[string]interface{}{
			"url":       "https://httpbin.org/post",
			"method":    "POST",
			"body":      `{"message": "overridden body", "timestamp": "{{timestamp}}"}`,
			"timestamp": "2025-05-30T12:00:00Z",
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should use input variables instead of config
		if err != nil {
			t.Logf("REST API error (expected due to network): %v", err)
			assert.NotContains(t, err.Error(), "Config is nil")
		} else {
			assert.NotNil(t, result)
			t.Logf("Override test result: %+v", result)
		}
	})
}

// TestRunNodeImmediately_SecretsAccess verifies that apContext.configVars is properly populated
func TestRunNodeImmediately_SecretsAccess(t *testing.T) {
	engine := createTestEngine(t)

	// Setup global secrets
	SetMacroSecrets(map[string]string{
		"test_secret_key": "test_secret_value",
		"api_token":       "abc123def456",
	})

	// Test custom code that accesses apContext.configVars
	config := map[string]interface{}{
		"source": "return { secret: apContext.configVars.test_secret_key, token: apContext.configVars.api_token }",
		"lang":   "javascript",
	}

	inputVariables := map[string]interface{}{
		"workflowContext": map[string]interface{}{
			"id":     "test-workflow-123",
			"userId": "test-user-456",
		},
	}

	result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)

	// Should succeed
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify that secrets are accessible
	assert.Equal(t, "test_secret_value", result["secret"])
	assert.Equal(t, "abc123def456", result["token"])

	t.Logf("Secrets access test result: %+v", result)
}

// TestRunNodeImmediately_SimpleUndefinedVariable tests basic undefined variable replacement
func TestRunNodeImmediately_SimpleUndefinedVariable(t *testing.T) {
	engine := createTestEngine(t)

	// Simple test for undefined variable replacement
	config := map[string]interface{}{
		"source": `
		var message = "Hello {{missing.variable}} World";
		return { message: message };
		`,
		"lang": "javascript",
	}

	inputVariables := map[string]interface{}{
		// No variables provided, so missing.variable should become "undefined"
	}

	result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	message, exists := result["message"]
	assert.True(t, exists)
	messageStr, ok := message.(string)
	assert.True(t, ok)

	// Should be "Hello undefined World"
	assert.Equal(t, "Hello undefined World", messageStr)

	t.Logf("Simple undefined test result: %s", messageStr)
}

// TestRunNodeImmediately_ClientInputDebug reproduces the exact client input scenario
func TestRunNodeImmediately_ClientInputDebug(t *testing.T) {
	engine := createTestEngine(t)

	// Setup global secrets
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZ123456789",
	})

	// Exact client config
	config := map[string]interface{}{
		"url":    "https://api.telegram.org/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
		"method": "POST",
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
		"body": `{"chat_id":"452247333","text":"Workflow: {{ workflowContext.name }}\\nEOA Address: {{ workflowContext.eoaAddress }}\\nRunner Address: {{ workflowContext.runner }}\\nWorkflow is triggered by block: { { trigger.data.block_number } }"}`,
	}

	// Exact client input variables
	inputVariables := map[string]interface{}{
		"workflowContext": map[string]interface{}{
			"id":             "74958435-2480-46ed-8113-00001f6736f7",
			"chainId":        nil,
			"name":           "May 29, 2025 12:49 PM",
			"userId":         "01d05279-14d6-4f87-b192-63428692f8ce",
			"eoaAddress":     "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
			"runner":         "0xB861aEe06De8694E129b50adA89437a1BF688F69",
			"status":         "draft",
			"executionCount": nil,
		},
		"trigger": map[string]interface{}{
			"data": map[string]interface{}{
				"interval": 7200, // Note: NO block_number - this is the issue!
			},
		},
	}

	result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

	// This should fail because trigger.data.block_number is missing
	if err != nil {
		t.Logf("Expected error due to missing block_number: %v", err)
	} else {
		t.Logf("Unexpected success result: %+v", result)
	}
}

// TestRunNodeImmediately_TemplateProcessingDebug examines template processing in detail
func TestRunNodeImmediately_TemplateProcessingDebug(t *testing.T) {
	engine := createTestEngine(t)

	// Setup global secrets
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZ123456789",
	})

	// Use custom code to debug template processing
	config := map[string]interface{}{
		"source": `
		// Debug template processing (using mock URL to prevent real API calls)
		var url = "http://localhost:3000/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown";
		var body = '{"chat_id":"452247333","text":"Workflow: {{ workflowContext.name }}\\nEOA Address: {{ workflowContext.eoaAddress }}\\nRunner Address: {{ workflowContext.runner }}\\nWorkflow is triggered by block: { { trigger.data.block_number } }"}';
		
		return {
			url_template: url,
			body_template: body,
			trigger_data: trigger ? trigger.data : "trigger is undefined",
			block_number: trigger && trigger.data ? trigger.data.block_number : "block_number is undefined",
			workflowContext_name: workflowContext ? workflowContext.name : "workflowContext is undefined",
			available_vars: Object.keys(this).filter(key => !key.startsWith('__'))
		};
		`,
		"lang": "javascript",
	}

	// Same input variables as client
	inputVariables := map[string]interface{}{
		"workflowContext": map[string]interface{}{
			"id":         "74958435-2480-46ed-8113-00001f6736f7",
			"name":       "May 29, 2025 12:49 PM",
			"userId":     "01d05279-14d6-4f87-b192-63428692f8ce",
			"eoaAddress": "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
			"runner":     "0xB861aEe06De8694E129b50adA89437a1BF688F69",
		},
		"trigger": map[string]interface{}{
			"data": map[string]interface{}{
				"interval": 7200, // Missing block_number
			},
		},
	}

	result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)

	if err != nil {
		t.Logf("Template processing debug error: %v", err)
	} else {
		t.Logf("Template processing debug result: %+v", result)
	}
}

// TestRunNodeImmediately_MissingTemplateVariable demonstrates the template variable issue
func TestRunNodeImmediately_MissingTemplateVariable(t *testing.T) {
	engine := createTestEngine(t)

	// Setup global secrets
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "test_token",
	})

	t.Run("BrokenTemplate", func(t *testing.T) {
		// This mimics the client's broken template with missing variable
		config := map[string]interface{}{
			"source": `
			// Simulate template processing
			var body = '{"chat_id":"452247333","text":"Workflow is triggered by block: {{trigger.data.block_number}}"}';
			return {
				body_before: body,
				body_after: body, // This will process templates
				trigger_data: trigger ? trigger.data : null,
				block_number_exists: trigger && trigger.data && trigger.data.block_number !== undefined
			};
			`,
			"lang": "javascript",
		}

		inputVariables := map[string]interface{}{
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"interval": 7200, // Missing block_number!
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)
		assert.NoError(t, err)
		t.Logf("Broken template result: %+v", result)
	})

	t.Run("FixedTemplate", func(t *testing.T) {
		// This shows the proper way to handle missing variables
		config := map[string]interface{}{
			"source": `
			// Use conditional logic to handle missing variables
			var blockNumber = (trigger && trigger.data && trigger.data.block_number) ? trigger.data.block_number : "N/A";
			var body = JSON.stringify({
				chat_id: "452247333", 
				text: "Workflow is triggered by block: " + blockNumber
			});
			return {
				body: body,
				block_number: blockNumber,
				is_valid_json: true
			};
			`,
			"lang": "javascript",
		}

		inputVariables := map[string]interface{}{
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"interval": 7200, // Still missing block_number, but handled gracefully
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)
		assert.NoError(t, err)
		t.Logf("Fixed template result: %+v", result)

		// Verify the JSON is valid
		bodyStr, ok := result["body"].(string)
		assert.True(t, ok)
		var jsonTest interface{}
		err = json.Unmarshal([]byte(bodyStr), &jsonTest)
		assert.NoError(t, err, "Generated JSON should be valid")
	})
}

// TestRunNodeImmediately_UndefinedVariableReplacement tests that missing template variables are replaced with "undefined"
func TestRunNodeImmediately_UndefinedVariableReplacement(t *testing.T) {
	engine := createTestEngine(t)

	// Setup global secrets
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZ123456789",
	})

	t.Run("TelegramWithMissingBlockNumber", func(t *testing.T) {
		// Create a mock server to capture what gets sent to Telegram
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Read the request body to verify the template processing
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)

			// Log the received body for debugging
			t.Logf("Received request body: %s", string(body))

			// Parse the JSON to verify it's valid
			var requestData map[string]interface{}
			err = json.Unmarshal(body, &requestData)
			assert.NoError(t, err, "Request body should be valid JSON")

			// Verify the message contains "undefined" where block_number was missing
			text, exists := requestData["text"]
			assert.True(t, exists)
			textStr, ok := text.(string)
			assert.True(t, ok)
			assert.Contains(t, textStr, "undefined", "Missing block_number should be replaced with 'undefined'")

			// Verify other template variables were processed correctly
			assert.Contains(t, textStr, "May 29, 2025 12:49 PM")                      // workflowContext.name
			assert.Contains(t, textStr, "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9") // workflowContext.eoaAddress
			assert.Contains(t, textStr, "0xB861aEe06De8694E129b50adA89437a1BF688F69") // workflowContext.runner

			// Return a successful response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)

			// Create response structure and marshal to JSON to ensure proper escaping
			response := map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"message_id": 123,
					"chat": map[string]interface{}{
						"id":   452247333,
						"type": "private",
					},
					"date": 1640995200,
					"text": textStr,
				},
			}

			responseJSON, err := json.Marshal(response)
			assert.NoError(t, err)
			w.Write(responseJSON)
		}))
		defer mockServer.Close()

		// Exact configuration from client input (with the missing block_number issue)
		config := map[string]interface{}{
			"url":    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
			"method": "POST",
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
			"body": `{"chat_id":"452247333","text":"Workflow: {{workflowContext.name}}\nEOA Address: {{workflowContext.eoaAddress}}\nRunner Address: {{workflowContext.runner}}\nWorkflow is triggered by block: {{trigger.data.block_number}}"}`,
		}

		// Client input variables (missing block_number in trigger.data)
		inputVariables := map[string]interface{}{
			"workflowContext": map[string]interface{}{
				"id":         "74958435-2480-46ed-8113-00001f6736f7",
				"name":       "May 29, 2025 12:49 PM",
				"userId":     "01d05279-14d6-4f87-b192-63428692f8ce",
				"eoaAddress": "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
				"runner":     "0xB861aEe06De8694E129b50adA89437a1BF688F69",
			},
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"interval": 7200, // Note: block_number is missing!
				},
			},
		}

		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed now that JSON is valid
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify the response structure
		assert.Equal(t, float64(200), result["statusCode"])

		// Check the response body contains our message with the actual block number
		body, exists := result["body"]
		assert.True(t, exists)
		bodyMap, ok := body.(map[string]interface{})
		assert.True(t, ok)

		// Debug: log the actual response structure
		t.Logf("Actual response body structure: %+v", bodyMap)

		// The parsed JSON response should have "ok" and "result" fields
		okField, exists := bodyMap["ok"]
		assert.True(t, exists)
		assert.Equal(t, true, okField)

		resultField, exists := bodyMap["result"]
		assert.True(t, exists)
		resultObj, ok := resultField.(map[string]interface{})
		assert.True(t, ok)

		text, exists := resultObj["text"]
		assert.True(t, exists)
		textStr, ok := text.(string)
		assert.True(t, ok)

		// Verify the final message contains "undefined" for the missing block_number
		assert.Contains(t, textStr, "triggered by block: undefined")

		t.Logf("Final message text: %s", textStr)
	})

	t.Run("MultipleUndefinedVariables", func(t *testing.T) {
		// Test multiple missing variables in different parts of the template
		config := map[string]interface{}{
			"source": `
			var template = "User: {{user.name}}, Block: {{block.number}}, Status: {{status.active}}";
			return { 
				processed_template: template,
				user_defined: typeof user !== 'undefined',
				block_defined: typeof block !== 'undefined',
				status_defined: typeof status !== 'undefined'
			};
			`,
			"lang": "javascript",
		}

		// Provide no variables, so all should become "undefined"
		inputVariables := map[string]interface{}{}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables)

		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Check that all missing variables were replaced with "undefined"
		processedTemplate, exists := result["processed_template"]
		assert.True(t, exists)
		templateStr, ok := processedTemplate.(string)
		assert.True(t, ok)

		// Should contain "undefined" for each missing variable
		expectedTemplate := "User: undefined, Block: undefined, Status: undefined"
		assert.Equal(t, expectedTemplate, templateStr)

		t.Logf("Processed template with undefined variables: %s", templateStr)
	})
}

func TestRunNodeImmediately_MalformedTemplateDetection(t *testing.T) {
	engine := createTestEngine(t)

	// Set up macro secrets for apContext
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "7771086042:AAG7UvbAyN8_8OrS-MjRfwz8WpWDKf4Yw8U",
	})

	// Create a mock server (should not be reached due to template validation error)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"ok": true}`)
	}))
	defer mockServer.Close()

	// Create REST API node config with malformed template syntax (spaces in curly braces)
	config := map[string]interface{}{
		"url":    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
		"method": "POST",
		"headers": map[string]string{
			"Content-Type": "application/json",
		},
		"body": `{
			"chat_id": "452247333",
			"text": "Workflow: {{workflowContext.name}}\nEOA Address: {{workflowContext.eoaAddress}}\nRunner Address: {{workflowContext.runner}}\nWorkflow is triggered by block: { { trigger.data.block_number } }"
		}`,
	}

	inputVariables := map[string]interface{}{
		"workflowContext": map[string]interface{}{
			"name":       "May 29, 2025 12:49 PM",
			"eoaAddress": "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
			"runner":     "0xB861aEe06De8694E129b50adA89437a1BF688F69",
		},
		"trigger": map[string]interface{}{
			"data": map[string]interface{}{
				"interval": 7200,
				// Note: block_number is intentionally missing to test the malformed template detection
			},
		},
	}

	// Execute the node - this should fail due to malformed template syntax
	result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

	// Verify that it failed due to malformed template syntax, not due to HTTP request
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed template syntax detected")
	require.Contains(t, err.Error(), "{ { trigger.data.block_number } }")
	require.Contains(t, err.Error(), "Use '{{trigger.data.block_number}}' instead")

	// Result should be nil or empty on template validation error
	if result != nil {
		t.Logf("Result on malformed template error: %+v", result)
	}

	t.Logf("Success: Malformed template syntax was correctly detected and prevented REST API execution")
	t.Logf("Error message: %s", err.Error())
}

func TestRunNodeImmediately_ValidTemplateAfterFix(t *testing.T) {
	engine := createTestEngine(t)

	// Set up macro secrets for apContext
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "7771086042:AAG7UvbAyN8_8OrS-MjRfwz8WpWDKf4Yw8U",
	})

	// Create a mock server for testing template validation passing
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{
			"ok": true,
			"result": {
				"message_id": 123,
				"chat": {"id": 452247333, "type": "private"},
				"date": 1640995200,
				"text": "test message"
			}
		}`)
	}))
	defer mockServer.Close()

	// Create REST API node config with correct template syntax
	config := map[string]interface{}{
		"url":    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
		"method": "POST",
		"headers": map[string]string{
			"Content-Type": "application/json",
		},
		"body": `{
			"chat_id": "452247333",
			"text": "Workflow: {{workflowContext.name}}\nEOA Address: {{workflowContext.eoaAddress}}\nRunner Address: {{workflowContext.runner}}\nWorkflow is triggered by block: {{trigger.data.block_number}}"
		}`,
	}

	inputVariables := map[string]interface{}{
		"workflowContext": map[string]interface{}{
			"name":       "May 29, 2025 12:49 PM",
			"eoaAddress": "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
			"runner":     "0xB861aEe06De8694E129b50adA89437a1BF688F69",
		},
		"trigger": map[string]interface{}{
			"data": map[string]interface{}{
				"interval": 7200,
				// Note: block_number is still missing, but template syntax is correct
			},
		},
	}

	// Execute the node - this should now get past template validation
	result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

	// Should succeed with mock server since template validation passes
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, float64(200), result["statusCode"])

	// Verify mock response structure
	if body, exists := result["body"]; exists {
		bodyMap, ok := body.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, true, bodyMap["ok"])
	}

	t.Logf("Success: Valid template syntax passed validation and mock server responded correctly")
}

// Test RunTriggerRPC with ManualTrigger to verify protobuf output data
func TestRunTriggerRPC_ManualTrigger(t *testing.T) {
	engine := createTestEngine(t)

	// Create a test user (minimal user needed for RPC)
	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	// Create RPC request for manual trigger
	req := &avsproto.RunTriggerReq{
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerConfig: map[string]*structpb.Value{},
	}

	// Execute the RPC call
	resp, err := engine.RunTriggerRPC(user, req)

	// Verify the response
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Check that the output data is set correctly for manual trigger
	manualTrigger := resp.GetManualTrigger()
	assert.NotNil(t, manualTrigger, "ManualTrigger output should be set")
	assert.Greater(t, manualTrigger.GetRunAt(), uint64(0), "RunAt should be set to a valid timestamp")

	// Verify that the output data case is correct (not OUTPUT_DATA_NOT_SET)
	switch resp.OutputData.(type) {
	case *avsproto.RunTriggerResp_ManualTrigger:
		t.Logf("✓ Manual trigger output data correctly set with RunAt: %d", manualTrigger.GetRunAt())
	default:
		t.Errorf("Expected ManualTrigger output data, got: %T", resp.OutputData)
	}
}
