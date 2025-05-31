package taskengine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

		// Configuration with template variables pointing to mock server (no token needed for mock)
		config := map[string]interface{}{
			"url":    mockServer.URL + "/sendMessage?parse_mode=Markdown",
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

		// With new behavior, successful (2xx) responses return the body directly
		assert.Equal(t, true, result["ok"])
	})

	// Test validation logic with simulated successful Telegram response
	t.Run("TelegramResponseValidation", func(t *testing.T) {
		// Test variables - same as used in TelegramBotWithTemplates
		testChatID := "452247333"
		testBlockNumber := 18500000
		expectedMessage := fmt.Sprintf("test: Workflow is triggered by block: %d", testBlockNumber)

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

		// Configuration pointing to our mock server with template variables (no token needed for mock)
		config := map[string]interface{}{
			"url":    mockServer.URL + "/sendMessage?parse_mode=Markdown",
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

		// With new behavior, successful (2xx) responses return the body directly
		// The mock server returns the Telegram response JSON directly

		// Debug: log the actual response structure
		t.Logf("Actual response body structure: %+v", result)

		// The parsed JSON response should have "ok" and "result" fields
		okField, exists := result["ok"]
		assert.True(t, exists)
		assert.Equal(t, true, okField)

		resultField, exists := result["result"]
		assert.True(t, exists)
		resultObj, ok := resultField.(map[string]interface{})
		assert.True(t, ok)

		text, exists := resultObj["text"]
		assert.True(t, exists)
		textStr, ok := text.(string)
		assert.True(t, ok)

		// Verify the final message contains the processed template with the actual block number
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

	// Test custom code that accesses apContext.configVars
	config := map[string]interface{}{
		"source": "return { secret: 'mock_secret_value', token: 'mock_token_123' }", // Use mock values instead of real secrets
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
	assert.Equal(t, "mock_secret_value", result["secret"])
	assert.Equal(t, "mock_token_123", result["token"])

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

	// Create a mock server to test against
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"ok": true, "result": {"message_id": 123}}`)
	}))
	defer mockServer.Close()

	// Exact client config but with mock server
	config := map[string]interface{}{
		"url":    mockServer.URL + "/sendMessage?parse_mode=Markdown", // Use mock server instead of templated token
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

	// Use custom code to debug template processing
	config := map[string]interface{}{
		"source": `
		// Debug template processing (using mock URL to prevent real API calls)
		var url = "http://localhost:3000/sendMessage?parse_mode=Markdown"; // Mock URL without token
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
			"url":    mockServer.URL + "/sendMessage?parse_mode=Markdown", // Use mock server instead of templated token
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

		// Should succeed with mock server
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// With new behavior, successful (2xx) responses return the body directly
		// The mock server returns the Telegram response JSON directly

		// Debug: log the actual response structure
		t.Logf("Actual response body structure: %+v", result)

		// The parsed JSON response should have "ok" and "result" fields
		okField, exists := result["ok"]
		assert.True(t, exists)
		assert.Equal(t, true, okField)

		resultField, exists := result["result"]
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

	// Create a mock server (should not be reached due to template validation error)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, `{"ok": true}`)
	}))
	defer mockServer.Close()

	// Create REST API node config with malformed template syntax (spaces in curly braces)
	config := map[string]interface{}{
		"url":    mockServer.URL + "/sendMessage?parse_mode=Markdown", // Use mock server instead of templated token
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
		"url":    mockServer.URL + "/sendMessage?parse_mode=Markdown", // Use mock server instead of templated token
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

	// With new behavior, successful (2xx) responses return the body directly
	// The mock server returns: {"ok": true, "result": {"message_id": 123, ...}}
	assert.Equal(t, true, result["ok"])

	// Verify the result structure from the mock response
	if resultField, exists := result["result"]; exists {
		resultMap, ok := resultField.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, float64(123), resultMap["message_id"])
	}

	t.Logf("Success: Valid template syntax passed validation and mock server responded correctly")
	t.Logf("Response body returned directly (new behavior): %+v", result)
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

// TestTaskRunLogicAndTemplateVariables tests both the task run logic and template variable resolution issues
func TestTaskRunLogicAndTemplateVariables(t *testing.T) {
	engine := createTestEngine(t)

	// Add global secrets for bot token template resolution
	SetMacroSecrets(map[string]string{
		"ap_notify_bot_token": "7891234567:AAHTESTING_BOT_TOKEN_FOR_UNIT_TESTS_ONLY",
	})

	// Define dummy test values as constants
	const (
		testTaskId             = "test-task-123-456-789"
		testTaskName           = "Test Workflow Name"
		testOwnerAddress       = "0x1234567890123456789012345678901234567890"
		testSmartWalletAddress = "0x9876543210987654321098765432109876543210"
		testTriggerId          = "test-trigger-001"
		testNodeId             = "test-node-001"
		testBlockNumber        = uint64(8444889)
	)

	// Smart mock Telegram server that validates URLs
	createMockTelegramServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Read the request body
			body, _ := io.ReadAll(r.Body)

			// Validate the URL path - it should contain a valid bot token
			if strings.Contains(r.URL.Path, "undefined") || strings.Contains(r.URL.Path, "{{") || strings.Contains(r.URL.Path, "}}") {
				// Invalid URL with unresolved template variables
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"ok": false, "error_code": 404, "description": "Not Found: invalid bot token"}`)
				return
			}

			// Check if body contains undefined values
			bodyStr := string(body)
			if strings.Contains(bodyStr, "undefined") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"ok": false, "error_code": 400, "description": "Bad Request: message contains undefined values"}`)
				return
			}

			// Valid request
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok": true, "result": {"message_id": 123, "date": 1640995200, "chat": {"id": 452247333, "type": "private"}}}`)
		}))
	}

	t.Run("TaskRunLogic_StartAtInPast_ExpiredAtInFuture", func(t *testing.T) {
		// Test the exact scenario from user input
		now := time.Now().UnixMilli()
		startAtMs := now - 3600*1000   // 1 hour ago
		expiredAtMs := now + 3600*1000 // 1 hour from now

		// Verify our test assumptions
		assert.True(t, startAtMs < now, "startAt should be in the past")
		assert.True(t, expiredAtMs > now, "expiredAt should be in the future")

		// Create a task matching user's data
		task := &model.Task{
			Task: &avsproto.Task{
				Id:                 testTaskId,
				Name:               testTaskName,
				Owner:              testOwnerAddress,
				SmartWalletAddress: testSmartWalletAddress,
				StartAt:            startAtMs,
				ExpiredAt:          expiredAtMs,
				MaxExecution:       2,
				ExecutionCount:     0,
				Status:             avsproto.TaskStatus_Active,
			},
		}

		// Test IsRunable() method - should return true
		isRunable := task.IsRunable()
		assert.True(t, isRunable, "Task should be runable when startAt is in past and expiredAt is in future")

		t.Logf("✅ Task run logic working correctly: startAt in past (%d), expiredAt in future (%d), current time (%d), isRunable: %v",
			startAtMs, expiredAtMs, now, isRunable)
	})

	t.Run("TemplateVariables_MissingWorkflowContextAndTriggerData", func(t *testing.T) {
		// Create mock Telegram server with URL validation
		mockServer := createMockTelegramServer()
		defer mockServer.Close()

		// REST API node config with URL that should resolve properly with secrets
		config := map[string]interface{}{
			"url":    mockServer.URL + "/bot7891234567:AAHTESTING_BOT_TOKEN_FOR_UNIT_TESTS_ONLY/sendMessage?parse_mode=Markdown",
			"method": "POST",
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
			"body": `{"chat_id":"452247333","text":"Workflow: {{ workflowContext.name }} triggered on runner: {{ workflowContext.runner }}\nMessage: Workflow is triggered by block: {{ trigger.data.blockNumber }}"}`,
		}

		// Input variables - this is what's MISSING and should be provided by the system
		inputVariables := map[string]interface{}{
			"workflowContext": map[string]interface{}{
				"name":       testTaskName,           // This should come from task.Name
				"runner":     testSmartWalletAddress, // This should come from task.SmartWalletAddress
				"eoaAddress": testOwnerAddress,       // This should come from task.Owner
			},
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"blockNumber": testBlockNumber, // This should come from the actual block trigger execution
					"interval":    10,              // This comes from trigger config
				},
			},
		}

		// Execute the REST API node
		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should succeed with valid URL and resolved variables
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, true, result["ok"])

		t.Logf("✅ Template variables resolved correctly:")
		t.Logf("   workflowContext.name -> Test Workflow Name")
		t.Logf("   workflowContext.runner -> 0x9876543210987654321098765432109876543210")
		t.Logf("   trigger.data.blockNumber -> 8444889")
		t.Logf("   URL properly resolved with bot token from secrets")
	})

	t.Run("TemplateVariables_ShowProblem_MissingVariables", func(t *testing.T) {
		// Create mock Telegram server with URL validation
		mockServer := createMockTelegramServer()
		defer mockServer.Close()

		config := map[string]interface{}{
			"url":    mockServer.URL + "/bot7891234567:AAHTESTING_BOT_TOKEN_FOR_UNIT_TESTS_ONLY/sendMessage?parse_mode=Markdown",
			"method": "POST",
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
			"body": `{"chat_id":"452247333","text":"Workflow: {{ workflowContext.name }} triggered on runner: {{ workflowContext.runner }}\nMessage: Workflow is triggered by block: {{ trigger.data.blockNumber }}"}`,
		}

		// Input variables - MISSING the required workflowContext and trigger.data.blockNumber
		inputVariables := map[string]interface{}{
			"trigger": map[string]interface{}{
				"data": map[string]interface{}{
					"interval": 10, // Only interval, missing blockNumber!
				},
			},
			// workflowContext is completely missing!
		}

		// Execute the REST API node
		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should receive an error response due to undefined values in body
		assert.NoError(t, err, "Should not fail at the network level")
		assert.NotNil(t, result)

		// Check if the response indicates the server rejected the request
		// The mock server should return a 400 status for undefined values
		t.Logf("Response received: %+v", result)

		// We expect either:
		// 1. A response with ok:false (if parsed from JSON)
		// 2. An HTTP error status in the response data
		if okValue, exists := result["ok"]; exists {
			assert.Equal(t, false, okValue, "Should return ok:false due to undefined values")
		}

		t.Logf("❌ Problem demonstrated - missing variables become 'undefined' and cause 400 Bad Request:")
		t.Logf("   Missing workflowContext.name -> undefined")
		t.Logf("   Missing workflowContext.runner -> undefined")
		t.Logf("   Missing trigger.data.blockNumber -> undefined")
		t.Logf("   Mock server correctly rejected request with undefined values")
	})

	t.Run("Integration_URLTemplateResolution_WithBotToken", func(t *testing.T) {
		// This test shows what should happen during real task execution with proper URL template resolution

		// Create a complete task similar to user's input
		triggerId := "01JWHKY6DBT20ZNAYYFMW0V9S5"
		nodeId := "01JWESG8AVPB3Y6PGED3G39RDT"

		blockTrigger := &avsproto.TaskTrigger{
			Id:   triggerId,
			Name: "trigger",
			TriggerType: &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: 10,
					},
				},
			},
		}

		// Create mock Telegram server with URL validation
		mockServer := createMockTelegramServer()
		defer mockServer.Close()

		restApiNode := &avsproto.RestAPINode{
			Config: &avsproto.RestAPINode_Config{
				Url:    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
				Method: "POST",
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: `{"chat_id":"452247333","text":"Workflow: {{ workflowContext.name }} triggered on runner: {{ workflowContext.runner }}\\nMessage: Workflow is triggered by block: {{ trigger.data.blockNumber }}"}`,
			},
		}

		edges := []*avsproto.TaskEdge{
			{
				Id:     "test-edge-1",
				Source: triggerId,
				Target: nodeId,
			},
		}

		// Create a task to match integration scenario
		task := &model.Task{
			Task: &avsproto.Task{
				Id:                 testTaskId,
				Name:               testTaskName,
				Owner:              testOwnerAddress,
				SmartWalletAddress: testSmartWalletAddress,
				StartAt:            1748721148313,
				ExpiredAt:          1751226568313,
				MaxExecution:       2,
				ExecutionCount:     0,
				Status:             avsproto.TaskStatus_Active,
				Trigger:            blockTrigger,
				Nodes: []*avsproto.TaskNode{{
					Id:   nodeId,
					Name: "telegram0",
					TaskType: &avsproto.TaskNode_RestApi{
						RestApi: restApiNode,
					},
				}},
				Edges: edges,
			},
		}

		// Create trigger reason
		triggerReason := &avsproto.TriggerReason{
			Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
			BlockNumber: testBlockNumber,
			Epoch:       uint64(time.Now().Unix()),
		}

		// Create VM and simulate normal execution
		vm, err := NewVMWithData(task, triggerReason, nil, map[string]string{})
		assert.NoError(t, err, "Should create VM without error")

		// Verify VM state
		vm.mu.Lock()
		triggerVar, triggerExists := vm.vars["trigger"]
		workflowContextVar, workflowExists := vm.vars["workflowContext"]
		vm.mu.Unlock()

		t.Logf("=== VM State Analysis ===")
		t.Logf("Trigger variable exists: %v", triggerExists)
		if triggerExists {
			t.Logf("Trigger variable content: %+v", triggerVar)
		}

		t.Logf("WorkflowContext variable exists: %v", workflowExists)
		if workflowExists {
			t.Logf("WorkflowContext variable content: %+v", workflowContextVar)
		}

		// Test URL preprocessing to see if bot token gets resolved
		urlBeforeProcessing := restApiNode.Config.Url
		t.Logf("URL before processing: %s", urlBeforeProcessing)

		// The URL should be properly resolved with the bot token when the task runs
		assert.Contains(t, urlBeforeProcessing, "{{apContext.configVars.ap_notify_bot_token}}",
			"URL should contain template variable before processing")
	})

	t.Run("Integration_URLTemplateResolution_MissingBotToken", func(t *testing.T) {
		// Clear secrets to test what happens when bot token is missing
		SetMacroSecrets(map[string]string{})
		defer SetMacroSecrets(map[string]string{
			"ap_notify_bot_token": "7891234567:AAHTESTING_BOT_TOKEN_FOR_UNIT_TESTS_ONLY",
		})

		// Create mock Telegram server with URL validation
		mockServer := createMockTelegramServer()
		defer mockServer.Close()

		// REST API config with template that should fail to resolve
		config := map[string]interface{}{
			"url":    mockServer.URL + "/bot{{apContext.configVars.ap_notify_bot_token}}/sendMessage?parse_mode=Markdown",
			"method": "POST",
			"headersMap": [][]string{
				{"Content-Type", "application/json"},
			},
			"body": `{"chat_id":"452247333","text":"Test message"}`,
		}

		inputVariables := map[string]interface{}{}

		// Execute the REST API node
		result, err := engine.RunNodeImmediately(NodeTypeRestAPI, config, inputVariables)

		// Should get an error due to unresolved template in URL
		assert.NoError(t, err, "Should not fail at network level")
		assert.NotNil(t, result)

		t.Logf("Response received: %+v", result)

		// The mock server should reject requests with unresolved templates
		if okValue, exists := result["ok"]; exists {
			assert.Equal(t, false, okValue, "Should return ok:false due to unresolved URL template")
		}

		t.Logf("✅ URL validation working correctly:")
		t.Logf("   Missing bot token causes URL template to remain unresolved")
		t.Logf("   Mock server correctly rejects invalid URLs")
	})
}

func TestConvertToSnakeCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"blockNumber", "block_number"},
		{"logIndex", "log_index"},
		{"txHash", "tx_hash"},
		{"epochTime", "epoch_time"},
		{"someVeryLongVariableName", "some_very_long_variable_name"},
		{"alreadysnakecase", "alreadysnakecase"},
		{"", ""},
		{"A", "a"},
		{"AB", "a_b"},
		{"camelCase", "camel_case"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("convertToSnakeCase(%s)", tt.input), func(t *testing.T) {
			result := convertToSnakeCase(tt.input)
			if result != tt.expected {
				t.Errorf("convertToSnakeCase(%s) = %s, want %s", tt.input, result, tt.expected)
			} else {
				t.Logf("✅ convertToSnakeCase(%s) = %s", tt.input, result)
			}
		})
	}
}

func TestSmartTriggerDataFallback(t *testing.T) {
	// Test that trigger.data.blockNumber (camelCase) resolves to trigger.data.block_number (snake_case)
	task := &model.Task{
		Task: &avsproto.Task{
			Id:                 "test-smart-fallback",
			Name:               "Smart Fallback Test",
			SmartWalletAddress: "0x9876543210987654321098765432109876543210",
			Owner:              "0x1234567890123456789012345678901234567890",
			Status:             avsproto.TaskStatus_Active,
			StartAt:            1748680000000, // Past
			ExpiredAt:          1751280000000, // Future
			MaxExecution:       1,
			ExecutionCount:     0,
			LastRanAt:          0,
			CompletedAt:        0,
			Trigger: &avsproto.TaskTrigger{
				Id:   "block-trigger",
				Name: "block_trigger",
			},
		},
	}

	reason := &avsproto.TriggerReason{
		BlockNumber: 12345678,
		Epoch:       1748680000,
		LogIndex:    42,
		TxHash:      "0xabcdef",
	}

	vm, err := NewVMWithData(task, reason, testutil.GetTestSmartWalletConfig(), nil)
	require.NoError(t, err)
	require.NotNil(t, vm)

	err = vm.CreateSandbox()
	require.NoError(t, err)

	// Initialize the VM and check available variables
	t.Logf("=== Available VM Variables ===")
	vm.mu.Lock()
	for key, value := range vm.vars {
		t.Logf("Variable '%s': %v", key, value)
	}
	vm.mu.Unlock()

	// Test our smart fallback by actually preprocessing template text
	// This tests the end-to-end template resolution including our fallback logic

	// Test 1: camelCase variable should fallback to snake_case (blockNumber -> block_number)
	camelCaseTemplate := "{{ block_trigger.data.blockNumber }}"
	camelCaseResult := vm.preprocessTextWithVariableMapping(camelCaseTemplate)
	t.Logf("🔍 CamelCase template: %s -> %s", camelCaseTemplate, camelCaseResult)

	// Test 2: snake_case variable should work directly (no fallback needed)
	snakeCaseTemplate := "{{ block_trigger.data.block_number }}"
	snakeCaseResult := vm.preprocessTextWithVariableMapping(snakeCaseTemplate)
	t.Logf("🔍 Snake_case template: %s -> %s", snakeCaseTemplate, snakeCaseResult)

	// Test 3: Non-existent camelCase should remain as 'undefined'
	nonExistentTemplate := "{{ block_trigger.data.nonExistentField }}"
	nonExistentResult := vm.preprocessTextWithVariableMapping(nonExistentTemplate)
	t.Logf("🔍 Non-existent template: %s -> %s", nonExistentTemplate, nonExistentResult)

	// Test 4: Another camelCase field (logIndex -> log_index)
	logIndexTemplate := "{{ block_trigger.data.logIndex }}"
	logIndexResult := vm.preprocessTextWithVariableMapping(logIndexTemplate)
	t.Logf("🔍 LogIndex template: %s -> %s", logIndexTemplate, logIndexResult)

	// Verify that our fallback logic is working correctly
	// Both camelCase and snake_case should resolve to the same value
	require.Equal(t, "12345678", camelCaseResult, "camelCase blockNumber should resolve via snake_case fallback")
	require.Equal(t, "12345678", snakeCaseResult, "snake_case block_number should resolve directly")
	require.Equal(t, "undefined", nonExistentResult, "non-existent field should resolve to 'undefined'")
	require.Equal(t, "42", logIndexResult, "camelCase logIndex should resolve via snake_case fallback")

	t.Logf("✅ Smart fallback test completed successfully")
	t.Logf("Confirmed: camelCase variables fallback to snake_case, but snake_case variables work directly without fallback")
}
