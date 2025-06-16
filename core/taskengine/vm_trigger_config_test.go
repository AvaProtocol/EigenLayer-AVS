package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestEventTriggerConfigExposure(t *testing.T) {
	// Create an EventTrigger with config that matches the SDK structure
	eventTrigger := &avsproto.EventTrigger{
		Config: &avsproto.EventTrigger_Config{
			Queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"}, // USDC
					Topics: []*avsproto.EventTrigger_Topics{
						{
							Values: []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"}, // Transfer signature
						},
						{
							Values: []string{"0x000000000000000000000000fE66125343Aabda4A330DA667431eC1Acb7BbDA9"}, // FROM address
						},
						{
							Values: []string{}, // Any TO address
						},
					},
					MaxEventsPerBlock: func() *uint32 { v := uint32(10); return &v }(),
				},
				{
					Addresses: []string{"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"}, // USDC
					Topics: []*avsproto.EventTrigger_Topics{
						{
							Values: []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"}, // Transfer signature
						},
						{
							Values: []string{}, // Any FROM address
						},
						{
							Values: []string{"0x000000000000000000000000fE66125343Aabda4A330DA667431eC1Acb7BbDA9"}, // TO address
						},
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "event_trigger_test",
		Name: "test_event_trigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: eventTrigger,
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "test_task_config",
			Trigger: trigger,
		},
	}

	// Create VM with the task
	vm, err := NewVMWithData(task, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	// Test 1: Verify triggerConfig variable exists
	vm.mu.Lock()
	triggerConfigValue, exists := vm.vars["triggerConfig"]
	vm.mu.Unlock()

	if !exists {
		t.Errorf("triggerConfig variable not found in VM vars")
		return
	}

	// Test 2: Verify triggerConfig has correct structure
	triggerConfigMap, ok := triggerConfigValue.(map[string]interface{})
	if !ok {
		t.Errorf("triggerConfig is not map[string]interface{}, got %T", triggerConfigValue)
		return
	}

	// Test 3: Verify basic trigger metadata
	if triggerConfigMap["id"] != "event_trigger_test" {
		t.Errorf("Expected trigger id 'event_trigger_test', got %v", triggerConfigMap["id"])
	}

	if triggerConfigMap["name"] != "test_event_trigger" {
		t.Errorf("Expected trigger name 'test_event_trigger', got %v", triggerConfigMap["name"])
	}

	if triggerConfigMap["type"] != "TRIGGER_TYPE_EVENT" {
		t.Errorf("Expected trigger type 'TRIGGER_TYPE_EVENT', got %v", triggerConfigMap["type"])
	}

	// Test 4: Verify EventTrigger-specific data structure
	dataValue, exists := triggerConfigMap["data"]
	if !exists {
		t.Errorf("triggerConfig.data not found")
		return
	}

	dataMap, ok := dataValue.(map[string]interface{})
	if !ok {
		t.Errorf("triggerConfig.data is not map[string]interface{}, got %T", dataValue)
		return
	}

	queriesValue, exists := dataMap["queries"]
	if !exists {
		t.Errorf("triggerConfig.data.queries not found")
		return
	}

	queries, ok := queriesValue.([]interface{})
	if !ok {
		t.Errorf("triggerConfig.data.queries is not []interface{}, got %T", queriesValue)
		return
	}

	// Test 5: Verify first query structure
	if len(queries) != 2 {
		t.Errorf("Expected 2 queries, got %d", len(queries))
		return
	}

	firstQuery, ok := queries[0].(map[string]interface{})
	if !ok {
		t.Errorf("First query is not map[string]interface{}, got %T", queries[0])
		return
	}

	// Check addresses
	addresses, exists := firstQuery["addresses"]
	if !exists {
		t.Errorf("First query addresses not found")
		return
	}

	addressSlice, ok := addresses.([]string)
	if !ok {
		t.Errorf("First query addresses is not []string, got %T", addresses)
		return
	}

	if len(addressSlice) != 1 || addressSlice[0] != "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48" {
		t.Errorf("Expected USDC address, got %v", addressSlice)
	}

	// Check topics structure
	topics, exists := firstQuery["topics"]
	if !exists {
		t.Errorf("First query topics not found")
		return
	}

	topicsSlice, ok := topics.([]interface{})
	if !ok {
		t.Errorf("First query topics is not []interface{}, got %T", topics)
		return
	}

	if len(topicsSlice) != 3 {
		t.Errorf("Expected 3 topics, got %d", len(topicsSlice))
		return
	}

	// Check first topic (Transfer signature)
	firstTopic, ok := topicsSlice[0].(map[string]interface{})
	if !ok {
		t.Errorf("First topic is not map[string]interface{}, got %T", topicsSlice[0])
		return
	}

	values, exists := firstTopic["values"]
	if !exists {
		t.Errorf("First topic values not found")
		return
	}

	valuesSlice, ok := values.([]string)
	if !ok {
		t.Errorf("First topic values is not []string, got %T", values)
		return
	}

	if len(valuesSlice) != 1 || valuesSlice[0] != "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
		t.Errorf("Expected Transfer signature, got %v", valuesSlice)
	}

	// Check maxEventsPerBlock
	maxEvents, exists := firstQuery["maxEventsPerBlock"]
	if !exists {
		t.Errorf("First query maxEventsPerBlock not found")
		return
	}

	maxEventsValue, ok := maxEvents.(uint32)
	if !ok {
		t.Errorf("maxEventsPerBlock is not uint32, got %T", maxEvents)
		return
	}

	if maxEventsValue != 10 {
		t.Errorf("Expected maxEventsPerBlock 10, got %d", maxEventsValue)
	}

	t.Logf("✅ EventTrigger config successfully exposed with correct structure")
}

func TestTriggerConfigAccessInJavaScript(t *testing.T) {
	// Create EventTrigger with USDC contract address
	eventTrigger := &avsproto.EventTrigger{
		Config: &avsproto.EventTrigger_Config{
			Queries: []*avsproto.EventTrigger_Query{
				{
					Addresses: []string{"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"}, // USDC
					Topics: []*avsproto.EventTrigger_Topics{
						{
							Values: []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
						},
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "js_test_trigger",
		Name: "jsTestTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: eventTrigger,
		},
	}

	// Create a CustomCode node that accesses triggerConfig
	customCodeNode := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang: avsproto.Lang_JavaScript,
			Source: `
				// Test accessing trigger config
				const triggerInfo = {
					id: triggerConfig.id,
					name: triggerConfig.name,
					type: triggerConfig.type,
					queryCount: triggerConfig.data.queries.length,
					firstAddress: triggerConfig.data.queries[0].addresses[0],
					isUSDC: triggerConfig.data.queries[0].addresses[0] === "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
				};
				return triggerInfo;
			`,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "test_js_node",
			Name: "testJSNode",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: customCodeNode,
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "test_js_node",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "js_test_task",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	vm, err := NewVMWithData(task, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("Failed to create VM: %v", err)
	}

	vm.WithLogger(testutil.GetLogger())

	err = vm.Compile()
	if err != nil {
		t.Fatalf("Failed to compile VM: %v", err)
	}

	err = vm.Run()
	if err != nil {
		t.Fatalf("Failed to run VM: %v", err)
	}

	// Check execution results
	if len(vm.ExecutionLogs) != 1 {
		t.Fatalf("Expected 1 execution log, got %d", len(vm.ExecutionLogs))
	}

	step := vm.ExecutionLogs[0]
	if !step.Success {
		t.Fatalf("JavaScript execution failed: %s", step.Error)
	}

	t.Logf("✅ JavaScript successfully accessed triggerConfig")
	t.Logf("Execution log: %s", step.Log)
}
