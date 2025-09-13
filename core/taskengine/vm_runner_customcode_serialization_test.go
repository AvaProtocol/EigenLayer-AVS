package taskengine

import (
	"encoding/json"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestCustomCodeNodeJSONSerialization(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	nodeID := "test_node"
	vm.mu.Lock()
	vm.TaskNodes[nodeID] = &avsproto.TaskNode{
		Id:   nodeID,
		Name: "test_node",
	}
	vm.mu.Unlock()

	processor := NewJSProcessor(vm)

	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Source: "return 'my secret is dummy_value'",
		},
	}

	step, err := processor.Execute("test_node", node)
	if err != nil {
		t.Fatalf("Failed to execute custom code node: %v", err)
	}

	if !step.Success {
		t.Errorf("Expected step to be successful, but it failed with error: %s", step.Error)
	}

	customCodeOutputProto := step.GetCustomCode()
	if customCodeOutputProto == nil {
		t.Fatalf("Expected custom code output, but got nil")
	}

	if customCodeOutputProto.Data == nil {
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
		t.Fatalf("Failed to unmarshal JSON into map: %v", err)
	}

	customCodeJsonPart, ok := jsonMap["custom_code"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'custom_code' field to be a map, got %T", jsonMap["custom_code"])
	}

	dataFieldJson, dataExists := customCodeJsonPart["data"]
	if !dataExists {
		t.Fatalf("Expected 'data' field within 'custom_code' JSON part")
	}

	actualStringData, okStr := dataFieldJson.(string)
	if !okStr {
		t.Fatalf("Expected 'data' field in JSON to be a string (representing StringValue), got %T (value: %v)", dataFieldJson, dataFieldJson)
	}

	if actualStringData != "my secret is dummy_value" {
		t.Errorf("Expected JSON data string to be 'my secret is dummy_value', got '%s'", actualStringData)
	}

	execution := &avsproto.Execution{
		Id:      "test_execution",
		Success: true,
		Index:   0, // Test execution
		Steps: []*avsproto.Execution_Step{
			step,
		},
	}

	jsonBytesExecution, err := m.Marshal(execution)
	if err != nil {
		t.Fatalf("Failed to marshal execution to JSON: %v", err)
	}

	var parsedExecution avsproto.Execution
	if err := protojson.Unmarshal(jsonBytesExecution, &parsedExecution); err != nil {
		t.Fatalf("Failed to unmarshal execution JSON: %v", err)
	}

	if len(parsedExecution.Steps) != 1 {
		t.Fatalf("Expected 1 step in parsed execution, got %d", len(parsedExecution.Steps))
	}

	parsedCustomCodeOutput := parsedExecution.Steps[0].GetCustomCode()
	if parsedCustomCodeOutput == nil {
		t.Fatalf("Expected custom code output in parsed execution, but got nil")
	}

	parsedProtoValue := parsedCustomCodeOutput.Data
	if parsedProtoValue == nil {
		t.Fatalf("Parsed custom code data (*structpb.Value) is nil")
	}

	stringValueKind, okStringKind := parsedProtoValue.GetKind().(*structpb.Value_StringValue)
	if !okStringKind {
		t.Fatalf("Expected parsed data to be of kind StringValue, got %T for value %v", parsedProtoValue.GetKind(), parsedProtoValue)
	}

	if stringValueKind.StringValue != "my secret is dummy_value" {
		t.Errorf("Expected parsed StringValue to be 'my secret is dummy_value', got '%s'", stringValueKind.StringValue)
	}
}
