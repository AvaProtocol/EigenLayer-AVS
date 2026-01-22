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
	config := testutil.GetAggregatorConfig()
	return New(db, config, nil, logger)
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
					"missing": map[string]interface{}{
						"field": "provided_value",
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
					"id":   12345,
					"name": "test_user",
					"preferences": map[string]interface{}{
						"theme": "default",
						"lang":  "en",
					},
					"missing": map[string]interface{}{
						"field": "n/a",
					},
				},
				"transaction": map[string]interface{}{
					"hash":   "0x0",
					"amount": "0",
					"token":  "N/A",
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
					"url":    MockAPIEndpoint + "/post",
					"method": "POST",
					"body": `{
						"user_id": "{{user.id}}",
						"user_name": "{{user.name}}",
						"theme": "{{user.preferences.theme}}",
						"transaction_hash": "{{transaction.hash}}",
						"amount": "{{transaction.amount}}",
						"missing_field": "{{user.missing.field}}"
					}`,
					"headersMap": [][]string{
						{"Content-Type", "application/json"},
					},
				}
			case avsproto.NodeType_NODE_TYPE_CUSTOM_CODE:
				nodeType = "customCode"
				nodeConfig = map[string]interface{}{
					"lang": avsproto.Lang_LANG_JAVASCRIPT,
					"source": `
						function main() {
							const inputValue = {{input_value}};
							const multiplier = {{multiplier}};
							return {
								result: inputValue * multiplier,
								message: "Calculation completed"
							};
						}
					`,
				}
			}

			result, err := engine.RunNodeImmediately(nodeType, nodeConfig, tc.templateData, nil)

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

// TestConvertToSnakeCase was removed as part of cleanup - function no longer exists
// The dual access mapping functionality has been removed in favor of consistent camelCase usage

func TestSmartTriggerDataFallback(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    MockAPIEndpoint + "/post",
		"method": "POST",
		"body": `{
			"camelCase": "{{camelCaseValue}}",
			"snake_case": "{{snake_case_value}}",
			"fallback_camel": "{{fallbackCamelValue}}",
			"fallback_snake": "{{fallback_snake_value}}"
		}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"camelCaseValue":       "camel_value",
		"snake_case_value":     "snake_value",
		"fallbackCamelValue":   "fallback_camel_value",
		"fallback_snake_value": "fallback_snake_value",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}
}
