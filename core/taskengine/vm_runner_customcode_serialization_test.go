package taskengine

import (
	"encoding/json"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestCustomCodeNodeJSONSerialization(t *testing.T) {
	vm := &VM{
		logger:    testutil.GetLogger(),
		vars:      make(map[string]interface{}),
		TaskNodes: make(map[string]*avsproto.TaskNode),
	}
	
	nodeID := "test_node"
	vm.TaskNodes[nodeID] = &avsproto.TaskNode{
		Id:   nodeID,
		Name: "test_node",
	}

	processor := NewJSProcessor(vm)

	node := &avsproto.CustomCodeNode{
		Source: "return 'my secret is dummy_value'",
	}

	step, err := processor.Execute("test_node", node)
	if err != nil {
		t.Fatalf("Failed to execute custom code node: %v", err)
	}

	if !step.Success {
		t.Errorf("Expected step to be successful, but it failed with error: %s", step.Error)
	}

	customCode := step.GetCustomCode()
	if customCode == nil {
		t.Fatalf("Expected custom code output, but got nil")
	}

	if customCode.Data == nil {
		t.Fatalf("Expected data in custom code output, but got nil")
	}

	m := protojson.MarshalOptions{
		UseProtoNames: true,
	}
	jsonBytes, err := m.Marshal(step)
	if err != nil {
		t.Fatalf("Failed to marshal step to JSON: %v", err)
	}

	var jsonMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	customCodeMap, ok := jsonMap["custom_code"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected custom_code to be a map, got %T", jsonMap["custom_code"])
	}

	dataMap, ok := customCodeMap["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data to be a map, got %T", customCodeMap["data"])
	}

	if typeField, exists := dataMap["@type"]; exists {
		t.Errorf("Expected no @type field, but got %v", typeField)
	}

	value, ok := dataMap["string_value"].(string)
	if !ok {
		t.Fatalf("Expected string_value to be a string, got %T", dataMap["string_value"])
	}
	if value != "my secret is dummy_value" {
		t.Errorf("Expected value to be 'my secret is dummy_value', got %s", value)
	}

	execution := &avsproto.Execution{
		Id:      "test_execution",
		Success: true,
		Steps: []*avsproto.Execution_Step{
			step,
		},
	}

	jsonBytes, err = m.Marshal(execution)
	if err != nil {
		t.Fatalf("Failed to marshal execution to JSON: %v", err)
	}

	var parsedExecution avsproto.Execution
	if err := protojson.Unmarshal(jsonBytes, &parsedExecution); err != nil {
		t.Fatalf("Failed to unmarshal execution JSON: %v", err)
	}

	if len(parsedExecution.Steps) != 1 {
		t.Fatalf("Expected 1 step, got %d", len(parsedExecution.Steps))
	}

	parsedCustomCode := parsedExecution.Steps[0].GetCustomCode()
	if parsedCustomCode == nil {
		t.Fatalf("Expected custom code output in parsed execution, but got nil")
	}

	parsedValue := parsedCustomCode.Data
	
	stringValue := parsedValue.GetStringValue()
	if stringValue == "" {
		t.Fatalf("Expected a string value, but got empty string or non-string type: %v", parsedValue)
	}
	
	if stringValue != "my secret is dummy_value" {
		t.Errorf("Expected parsed value to be 'my secret is dummy_value', got %s", stringValue)
	}
}
