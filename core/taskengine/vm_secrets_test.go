package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestSecretAccessPath verifies that secrets are correctly accessible via apContext.configVars
// and that the access path hasn't changed
func TestSecretAccessPath(t *testing.T) {
	// Test secrets
	testSecrets := map[string]string{
		"api_key":        "secret_api_key_123",
		"database_url":   "postgresql://user:pass@localhost/db",
		"webhook_secret": "webhook_secret_456",
		"auth_token":     "bearer_token_789",
	}

	// Create VM with secrets
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_secrets",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), testSecrets)

	if err != nil {
		t.Fatalf("Failed to create VM with secrets: %v", err)
	}

	// Test 1: Verify apContext variable exists
	vm.mu.Lock()
	apContextValue, exists := vm.vars[APContextVarName]
	vm.mu.Unlock()

	if !exists {
		t.Errorf("apContext variable not found in VM vars")
		return
	}

	// Test 2: Verify apContext has correct structure
	apContextMap, ok := apContextValue.(map[string]map[string]string)
	if !ok {
		t.Errorf("apContext is not map[string]map[string]string, got %T", apContextValue)
		return
	}

	// Test 3: Verify configVars exists in apContext
	configVars, exists := apContextMap[ConfigVarsPath]
	if !exists {
		var keys []string
		for k := range apContextMap {
			keys = append(keys, k)
		}
		t.Errorf("configVars not found in apContext, available keys: %v", keys)
		return
	}

	// Test 4: Verify all secrets are present in configVars
	for key, expectedValue := range testSecrets {
		actualValue, exists := configVars[key]
		if !exists {
			var keys []string
			for k := range configVars {
				keys = append(keys, k)
			}
			t.Errorf("Secret key '%s' not found in configVars, available keys: %v", key, keys)
			continue
		}
		if actualValue != expectedValue {
			t.Errorf("Secret value mismatch for key '%s': expected '%s', got '%s'", key, expectedValue, actualValue)
		}
	}

	// Test 5: Verify secrets are accessible via preprocessing
	testCases := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "api_key access",
			template: "{{apContext.configVars.api_key}}",
			expected: "secret_api_key_123",
		},
		{
			name:     "database_url access",
			template: "Database: {{apContext.configVars.database_url}}",
			expected: "Database: postgresql://user:pass@localhost/db",
		},
		{
			name:     "multiple secrets",
			template: "Key: {{apContext.configVars.api_key}}, Token: {{apContext.configVars.auth_token}}",
			expected: "Key: secret_api_key_123, Token: bearer_token_789",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := vm.preprocessText(tc.template)
			if result != tc.expected {
				t.Errorf("Template preprocessing failed for %s: expected '%s', got '%s'", tc.name, tc.expected, result)
			}
		})
	}
}

// TestSecretAccessInCustomCode verifies that secrets work in custom JavaScript code
func TestSecretAccessInCustomCode(t *testing.T) {
	testSecrets := map[string]string{
		"api_key": "test_api_key_value",
		"secret":  "super_secret_value",
	}

	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_JavaScript,
			Source: "return { api_key: apContext.configVars.api_key, secret: apContext.configVars.secret, combined: apContext.configVars.api_key + '_' + apContext.configVars.secret }",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_js_secrets",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), testSecrets)

	if err != nil {
		t.Fatalf("Failed to create VM with secrets: %v", err)
	}

	processor := NewJSProcessor(vm)
	executionLog, err := processor.Execute("test_node", node)

	if err != nil {
		t.Fatalf("JavaScript execution failed: %v", err)
	}

	if !executionLog.Success {
		t.Fatalf("JavaScript execution was not successful: %s", executionLog.Error)
	}

	// Verify the output contains the expected secret values
	customCodeOutput := executionLog.GetCustomCode()
	if customCodeOutput == nil {
		t.Fatalf("No custom code output found")
	}

	outputStruct := customCodeOutput.Data.GetStructValue()
	if outputStruct == nil {
		t.Fatalf("Output data is not a struct")
	}

	// Check api_key
	if apiKeyValue := outputStruct.Fields["api_key"]; apiKeyValue != nil {
		if apiKeyValue.GetStringValue() != "test_api_key_value" {
			t.Errorf("api_key value mismatch: expected 'test_api_key_value', got '%s'", apiKeyValue.GetStringValue())
		}
	} else {
		t.Errorf("api_key not found in output")
	}

	// Check secret
	if secretValue := outputStruct.Fields["secret"]; secretValue != nil {
		if secretValue.GetStringValue() != "super_secret_value" {
			t.Errorf("secret value mismatch: expected 'super_secret_value', got '%s'", secretValue.GetStringValue())
		}
	} else {
		t.Errorf("secret not found in output")
	}

	// Check combined
	if combinedValue := outputStruct.Fields["combined"]; combinedValue != nil {
		expected := "test_api_key_value_super_secret_value"
		if combinedValue.GetStringValue() != expected {
			t.Errorf("combined value mismatch: expected '%s', got '%s'", expected, combinedValue.GetStringValue())
		}
	} else {
		t.Errorf("combined not found in output")
	}
}

// TestSecretAccessConstants verifies that the constants are correctly defined
func TestSecretAccessConstants(t *testing.T) {
	// Test that constants have expected values
	if APContextVarName != "apContext" {
		t.Errorf("APContextVarName constant incorrect: expected 'apContext', got '%s'", APContextVarName)
	}

	if ConfigVarsPath != "configVars" {
		t.Errorf("ConfigVarsPath constant incorrect: expected 'configVars', got '%s'", ConfigVarsPath)
	}

	if APContextConfigVarsPath != "apContext.configVars" {
		t.Errorf("APContextConfigVarsPath constant incorrect: expected 'apContext.configVars', got '%s'", APContextConfigVarsPath)
	}
}

// TestCollectInputsIncludesSecrets verifies that CollectInputs includes the secret path
func TestCollectInputsIncludesSecrets(t *testing.T) {
	testSecrets := map[string]string{
		"test_secret": "test_value",
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test_collect_inputs",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), testSecrets)

	if err != nil {
		t.Fatalf("Failed to create VM with secrets: %v", err)
	}

	// Add some test variables
	vm.AddVar("test_var", map[string]any{"data": "test_data"})

	inputs := vm.CollectInputs()

	// Verify that apContext.configVars is included
	if _, exists := inputs[APContextConfigVarsPath]; !exists {
		var keys []string
		for k := range inputs {
			keys = append(keys, k)
		}
		t.Errorf("apContext.configVars not found in CollectInputs output, available keys: %v", keys)
	}

	// Verify that test_var.data is included
	if _, exists := inputs["test_var.data"]; !exists {
		var keys []string
		for k := range inputs {
			keys = append(keys, k)
		}
		t.Errorf("test_var.data not found in CollectInputs output, available keys: %v", keys)
	}
}
