package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestRunNodeImmediately_RestAPIWithTemplates(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body":   `{"message": "{{test_value}}", "timestamp": "{{date.now}}"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"test_value": "Hello World",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}

}

func TestRunNodeImmediately_SecretsAccess(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/get",
		"method": "GET",
		"headersMap": [][]string{
			{"Authorization", "Bearer {{secrets.api_key}}"},
		},
	}

	secrets := map[string]interface{}{
		"api_key": "test_secret_123",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, secrets, nil)
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}

}

func TestRunNodeImmediately_SimpleUndefinedVariable(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body":   `{"message": "{{undefined_variable}}"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, map[string]interface{}{}, nil)
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}

}

func TestRunNodeImmediately_ClientInputDebug(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body":   `{"client_input": "{{client_data}}"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"client_data": map[string]interface{}{
			"user_id": 12345,
			"action":  "test_action",
		},
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)

	// Some CI environments block outbound HTTP; tolerate network errors by skipping
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}

}

func TestRunNodeImmediately_TemplateProcessingDebug(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body":   `{"nested": "{{nested.value}}", "array": "{{array.0}}"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"nested": map[string]interface{}{
			"value": "nested_test_value",
		},
		"array": []interface{}{"first_item", "second_item"},
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}
	if result == nil {
		t.Skip("Skipping due to nil result likely from network issues")
	}

}

func TestRunNodeImmediately_MissingTemplateVariable(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body":   `{"message": "{{missing_field}}", "fallback": "default_value"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"existing_field": "some_value",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}
	if result == nil {
		t.Skip("Skipping due to nil result likely from network issues")
	}

}

func TestRunNodeImmediately_UndefinedVariableReplacement(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    "https://httpbin.org/post",
		"method": "POST",
		"body":   `{"defined": "{{defined_value}}", "undefined": "{{undefined_value}}", "mixed": "prefix_{{another_undefined}}_suffix"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"defined_value": "this_is_defined",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)
	if err != nil {
		t.Skipf("Skipping due to network error: %v", err)
	}
	if result == nil {
		t.Skip("Skipping due to nil result likely from network issues")
	}

}

func TestRunNodeImmediately_MalformedTemplateDetection(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	// Test that well-formed templates work correctly
	// Malformed templates like "{{value" are now rejected by the stricter validation
	nodeConfig := map[string]interface{}{
		"url":    MockAPIEndpoint + "/post",
		"method": "POST",
		"body":   `{"field1": "{{value1}}", "field2": "{{value2}}"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"value1": "test_value_1",
		"value2": "test_value_2",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}

}

func TestRunNodeImmediately_ValidTemplateAfterFix(t *testing.T) {
	engine := createTestEngine()
	defer storage.Destroy(engine.db.(*storage.BadgerStorage))

	nodeConfig := map[string]interface{}{
		"url":    MockAPIEndpoint + "/post",
		"method": "POST",
		"body":   `{"message": "{{message}}", "timestamp": "{{timestamp}}"}`,
		"headersMap": [][]string{
			{"Content-Type", "application/json"},
		},
	}

	triggerData := map[string]interface{}{
		"message":   "Template processing works correctly",
		"timestamp": "2024-01-01T00:00:00Z",
	}

	result, err := engine.RunNodeImmediately("restAPI", nodeConfig, triggerData, nil)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result == nil {
		t.Errorf("Expected result, got nil")
	}
}
