package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func createTestEngine() *Engine {
	logger := testutil.GetLogger()
	db := testutil.TestMustDB()
	return &Engine{
		logger: logger,
		db:     db,
	}
}

func TestTaskRunLogicAndTemplateVariables(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	testCases := []struct {
		name         string
		nodeType     avsproto.NodeType
		templateData map[string]interface{}
		expectError  bool
		description  string
	}{
		{
			name:     "REST API with complex template",
			nodeType: avsproto.NodeType_NODE_TYPE_REST_API,
			templateData: map[string]interface{}{
				"user": map[string]interface{}{
					"id":   12345,
					"name": "test_user",
					"preferences": map[string]interface{}{
						"theme": "dark",
						"lang":  "en",
					},
				},
				"transaction": map[string]interface{}{
					"hash":   "0x1234567890abcdef",
					"amount": "1.5",
					"token":  "ETH",
				},
			},
			expectError: false,
			description: "Should handle complex nested template variables",
		},
		{
			name:     "Custom code with template variables",
			nodeType: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			templateData: map[string]interface{}{
				"input_value": 42,
				"multiplier":  2,
			},
			expectError: false,
			description: "Should process template variables in custom code",
		},
		{
			name:     "Template with missing nested field",
			nodeType: avsproto.NodeType_NODE_TYPE_REST_API,
			templateData: map[string]interface{}{
				"user": map[string]interface{}{
					"id": 12345,
				},
			},
			expectError: false,
			description: "Should handle missing nested template fields gracefully",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeType := ""
			nodeConfig := make(map[string]interface{})

			switch tc.nodeType {
			case avsproto.NodeType_NODE_TYPE_REST_API:
				nodeType = "restAPI"
				nodeConfig = map[string]interface{}{
					"url":    "https://httpbin.org/post",
					"method": "POST",
					"body": `{
						"user_id": "{{trigger.user.id}}",
						"user_name": "{{trigger.user.name}}",
						"theme": "{{trigger.user.preferences.theme}}",
						"transaction_hash": "{{trigger.transaction.hash}}",
						"amount": "{{trigger.transaction.amount}}",
						"missing_field": "{{trigger.user.missing.field}}"
					}`,
					"headersMap": [][]string{
						{"Content-Type", "application/json"},
					},
				}
			case avsproto.NodeType_NODE_TYPE_CUSTOM_CODE:
				nodeType = "customCode"
				nodeConfig = map[string]interface{}{
					"source": `
						function main() {
							const inputValue = {{trigger.input_value}};
							const multiplier = {{trigger.multiplier}};
							return {
								result: inputValue * multiplier,
								message: "Calculation completed"
							};
						}
					`,
				}
			}

			result, err := engine.RunNodeImmediately(nodeType, nodeConfig, tc.templateData)

			if tc.expectError && err == nil {
				t.Errorf("Expected error for %s, but got none", tc.name)
			}

			if !tc.expectError && err != nil {
				t.Errorf("Expected no error for %s, got: %v", tc.name, err)
			}

			if result == nil && !tc.expectError {
				t.Errorf("Expected result for %s, got nil", tc.name)
			}

			t.Logf("✅ %s: %s", tc.name, tc.description)
		})
	}
}

func TestConvertToSnakeCase(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	testCases := []struct {
		input    string
		expected string
	}{
		{"camelCase", "camel_case"},
		{"PascalCase", "pascal_case"},
		{"simpleword", "simpleword"},
		{"UPPERCASE", "uppercase"},
		{"mixedCASEExample", "mixed_case_example"},
		{"", ""},
		{"a", "a"},
		{"A", "a"},
		{"aB", "a_b"},
		{"AB", "ab"},
		{"testHTTPSConnection", "test_https_connection"},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := convertToSnakeCase(tc.input)
			if result != tc.expected {
				t.Errorf("convertToSnakeCase(%q) = %q, expected %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestSmartTriggerDataFallback(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body": `{
			"camelCase": "{{trigger.camelCaseValue}}",
			"snake_case": "{{trigger.snake_case_value}}",
			"fallback_camel": "{{trigger.fallbackCamelValue}}",
			"fallback_snake": "{{trigger.fallback_snake_value}}"
		}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"camelCaseValue":     "camel_value",
		"snake_case_value":   "snake_value",
		"fallbackCamelValue": "fallback_camel_value",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}
}
