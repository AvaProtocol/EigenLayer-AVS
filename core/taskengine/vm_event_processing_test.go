package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestEvaluateEvent(t *testing.T) {
	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000",
					TokenName:        "TestToken",
					TokenSymbol:      "TEST",
					TokenDecimals:    18,
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					BlockNumber:      7212417,
					BlockTimestamp:   1625097600000,
					FromAddress:      "0x0000000000000000000000000000000000000000",
					ToAddress:        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					ValueFormatted:   "1.5",
					TransactionIndex: 0,
					LogIndex:         98,
				},
			},
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
	triggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_EvmLog{
				EvmLog: &avsproto.Evm_Log{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Topics:           []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x0000000000000000000000000000000000000000000000000000000000000000", "0x0000000000000000000000001c7d4b196cb0c7b01d743fbc6116a902379c7238"},
					Data:             "0x0000000000000000000000000000000000000000000000000000000000016e36",
					BlockNumber:      7212417,
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					TransactionIndex: 0,
					BlockHash:        "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					Index:            98,
					Removed:          false,
				},
			},
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

func TestEventTriggerOneofExclusivity(t *testing.T) {
	transferLogTriggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: &avsproto.EventTrigger_TransferLogOutput{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Value:            "1500000",
					TokenName:        "TestToken",
					TokenSymbol:      "TEST",
					TokenDecimals:    18,
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					BlockNumber:      7212417,
					BlockTimestamp:   1625097600000,
					FromAddress:      "0x0000000000000000000000000000000000000000",
					ToAddress:        "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					ValueFormatted:   "1.5",
					TransactionIndex: 0,
					LogIndex:         98,
				},
			},
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
	}, transferLogTriggerData, testutil.GetTestSmartWalletConfig(), nil)

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

	evmLogTriggerData := &TriggerData{
		Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT,
		Output: &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_EvmLog{
				EvmLog: &avsproto.Evm_Log{
					Address:          "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					Topics:           []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
					Data:             "0x0000000000000000000000000000000000000000000000000000000000016e36",
					BlockNumber:      7212417,
					TransactionHash:  "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
					TransactionIndex: 0,
					BlockHash:        "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					Index:            98,
					Removed:          false,
				},
			},
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
