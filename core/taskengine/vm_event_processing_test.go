package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestEvaluateEvent(t *testing.T) {
	// JSON data for transfer event
	transferEventData := map[string]interface{}{
		"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"value":            "1500000",
		"tokenName":        "TestToken",
		"tokenSymbol":      "TEST",
		"tokenDecimals":    18,
		"transactionHash":  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		"blockNumber":      7212417,
		"blockTimestamp":   1625097600000,
		"fromAddress":      "0x0000000000000000000000000000000000000000",
		"toAddress":        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"valueFormatted":   "1.5",
		"transactionIndex": 0,
		"logIndex":         98,
	}

	// Convert to google.protobuf.Value
	protoValue, err := structpb.NewValue(transferEventData)
	if err != nil {
		t.Fatalf("Failed to create protobuf value: %v", err)
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			Data: protoValue,
		},
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, triggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm == nil {
		t.Fatal("vm is nil")
	}

	triggerName, err := vm.GetTriggerNameAsVar()
	if err != nil {
		t.Fatalf("failed to get trigger name: %v", err)
	}

	if vm.vars[triggerName] == nil {
		t.Errorf("expected trigger data to be available at key '%s'", triggerName)
	}
}

func TestEvaluateEventEvmLog(t *testing.T) {
	// JSON data for general EVM log event
	evmLogEventData := map[string]interface{}{
		"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"topics":           []interface{}{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x0000000000000000000000000000000000000000000000000000000000000000", "0x0000000000000000000000001c7d4b196cb0c7b01d743fbc6116a902379c7238"},
		"data":             "0x0000000000000000000000000000000000000000000000000000000000016e36",
		"blockNumber":      7212417,
		"transactionHash":  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		"transactionIndex": 0,
		"blockHash":        "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"logIndex":         98,
		"removed":          false,
	}

	// Convert to google.protobuf.Value
	protoValue, err := structpb.NewValue(evmLogEventData)
	if err != nil {
		t.Fatalf("Failed to create protobuf value: %v", err)
	}

	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			Data: protoValue,
		},
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, triggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm == nil {
		t.Fatal("vm is nil")
	}

	triggerName, err := vm.GetTriggerNameAsVar()
	if err != nil {
		t.Fatalf("failed to get trigger name: %v", err)
	}

	if vm.vars[triggerName] == nil {
		t.Errorf("expected trigger data to be available at key '%s'", triggerName)
	}
}

func TestEventTriggerDataAccessibility(t *testing.T) {
	// Test with enriched transfer event data
	transferEventData := map[string]interface{}{
		"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"value":            "1500000",
		"tokenName":        "TestToken",
		"tokenSymbol":      "TEST",
		"tokenDecimals":    18,
		"transactionHash":  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		"blockNumber":      7212417,
		"blockTimestamp":   1625097600000,
		"fromAddress":      "0x0000000000000000000000000000000000000000",
		"toAddress":        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"valueFormatted":   "1.5",
		"transactionIndex": 0,
		"logIndex":         98,
	}

	// Convert to google.protobuf.Value
	transferProtoValue, err := structpb.NewValue(transferEventData)
	if err != nil {
		t.Fatalf("Failed to create protobuf value: %v", err)
	}

	transferTriggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			Data: transferProtoValue,
		},
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, transferTriggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm == nil {
		t.Fatal("vm is nil")
	}

	triggerName, err := vm.GetTriggerNameAsVar()
	if err != nil {
		t.Fatalf("failed to get trigger name: %v", err)
	}

	if vm.vars[triggerName] == nil {
		t.Errorf("expected trigger data to be available at key '%s'", triggerName)
	}

	// Test with basic EVM log event data
	evmLogEventData := map[string]interface{}{
		"address":          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"topics":           []interface{}{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
		"data":             "0x0000000000000000000000000000000000000000000000000000000000016e36",
		"blockNumber":      7212417,
		"transactionHash":  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		"transactionIndex": 0,
		"blockHash":        "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"logIndex":         98,
		"removed":          false,
	}

	// Convert to google.protobuf.Value
	evmLogProtoValue, err := structpb.NewValue(evmLogEventData)
	if err != nil {
		t.Fatalf("Failed to create protobuf value: %v", err)
	}

	evmLogTriggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			Data: evmLogProtoValue,
		},
	}

	vm2, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid2",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger2",
				Name: "test_trigger2",
			},
		},
	}, evmLogTriggerData, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm2 == nil {
		t.Fatal("vm2 is nil")
	}

	triggerName2, err := vm2.GetTriggerNameAsVar()
	if err != nil {
		t.Fatalf("failed to get trigger name: %v", err)
	}

	if vm2.vars[triggerName2] == nil {
		t.Errorf("expected trigger data to be available at key '%s'", triggerName2)
	}
}
