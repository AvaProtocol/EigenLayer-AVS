package trigger

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestTriggerTopicMatch(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 82)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	result, err := eventTrigger.Evaluate(event, &Check{
		TaskMetadata: &avsproto.SyncMessagesResp_TaskMetadata{
			TaskId: "123",
		},

		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "topics",
				Value: []string{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"",
					"0xc114fb059434563dc65ac8d57e7976e3eac534f4",
				},
			},
		},
	})

	if !result {
		t.Errorf("expect match, but got false: error: %v", err)
	}
}

func TestTriggerTopicNotMatch(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 82)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	result, err := eventTrigger.Evaluate(event, &Check{
		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "topics",
				Value: []string{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"",
					"abc",
				},
			},
		},
	})

	if result {
		t.Errorf("expect no match, but got true: error: %v", err)
	}
}

func TestTriggerTopicMulti(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8a863ca614db1301c80a7e0ae048df86abde4170db92084bea1abdc24feb6d55", 100)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	result, err := eventTrigger.Evaluate(event, &Check{

		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "topics",
				Value: []string{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
				},
			},
		},
	})

	if result {
		t.Errorf("expect not match, but got true %v", err)
	}

	result, err = eventTrigger.Evaluate(event, &Check{
		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "topics",
				Value: []string{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
				},
			},
			{
				Type: "topics",
				Value: []string{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"",
					"0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
				},
			},
		},
	})

	if !result {
		t.Errorf("expect match, but got false %v", err)
	}
}

func TestTriggerAddress(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8a863ca614db1301c80a7e0ae048df86abde4170db92084bea1abdc24feb6d55", 100)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	result, err := eventTrigger.Evaluate(event, &Check{
		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "address",
				Value: []string{
					"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
				},
			},
		},
	})

	if !result {
		t.Errorf("expect match, but got false %v", err)
	}

	result, err = eventTrigger.Evaluate(event, &Check{
		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "address",
				Value: []string{
					"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7237",
				},
			},
		},
	})

	if result {
		t.Errorf("expect not match, but got true %v", err)
	}
}

func TestTriggerAddressNegativeCase(t *testing.T) {
	event, err := testutil.GetEventForTx("0x786123b289e99cec4d6873e6ca08012c375f0e1147e24415e5e57bb5b9929353", 49)

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	result, err := eventTrigger.Evaluate(event, &Check{
		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "address",
				Value: []string{
					"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7237",
				},
			},
		},
	})

	if result {
		t.Errorf("expect not match, but got true %v", err)
	}
}

func TestTriggerNonTransferEvent(t *testing.T) {
	event, err := testutil.GetEventForTx("0x786123b289e99cec4d6873e6ca08012c375f0e1147e24415e5e57bb5b9929353", 48)

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	result, err := eventTrigger.Evaluate(event, &Check{
		Matcher: []*avsproto.EventCondition_Matcher{
			{
				Type: "topics",
				Value: []string{
					"0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0",
				},
			},
		},
	})

	if !result {
		t.Errorf("expect match, but got false %v", err)
	}
}

func TestTriggerExpression(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 82)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	taskMeta := &avsproto.SyncMessagesResp_TaskMetadata{
		Trigger: &avsproto.TaskTrigger{
			Name: "myEventTrigger",
		},
	}

	fmt.Println(event)

	program := `myEventTrigger.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && myEventTrigger.data.topics[2] == "0xc114fb059434563dc65ac8d57e7976e3eac534f4"`

	result, err := eventTrigger.Evaluate(event, &Check{
		Program:      program,
		TaskMetadata: taskMeta,
	})
	if !result {
		t.Errorf("expect expression to be match, but got false: error: %v", err)
	}

	program = `myEventTrigger.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && myEventTrigger.data.topics[2] == "abc"`

	result, err = eventTrigger.Evaluate(event, &Check{
		Program:      program,
		TaskMetadata: taskMeta,
	})
	if result {
		t.Errorf("expect expression to be not match, but got match: error: %v", err)
	}

	event, err = testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 81)
	program = `myEventTrigger.data.address == "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789" && myEventTrigger.data.topics[0] == "0xbb47ee3e183a558b1a2ff0874b079f3fc5478b7454eacf2bfc5af2ff5878f972"`
	result, err = eventTrigger.Evaluate(event, &Check{
		Program:      program,
		TaskMetadata: taskMeta,
	})
	if result {
		t.Errorf("expect expression to be not match, but got match: error: %v", err)
	}
}

func TestTriggerWithContractReadBindingInExpression(t *testing.T) {
	// This event is transfering usdc
	event, err := testutil.GetEventForTx("0x4bb728dfbe58d7c641c02a214cac6156a0d6a0fe648cb27a7de229a3160e91b1", 145)

	macros.SetRpc(testutil.GetTestRPCURL())
	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000),
		testutil.GetLogger())

	taskMeta := &avsproto.SyncMessagesResp_TaskMetadata{
		Trigger: &avsproto.TaskTrigger{
			Name: "myEventTrigger",
		},
	}

	// USDC pair from chainlink, usually USDC price is ~99cent but never approach $1
	// for an unknow reason the decimal is 8 instead of 6
	program := `myEventTrigger.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && bigGt(chainlinkPrice("0xA2F78ab2355fe2f984D808B5CeE7FD0A93D5270E"), toBigInt("1000000000"))`

	result, err := eventTrigger.Evaluate(event, &Check{
		Program:      program,
		TaskMetadata: taskMeta,
	})
	if err != nil {
		t.Errorf("expected no error when evaluate program but got error: %s", err)
	}
	if result {
		t.Errorf("expect expression to be false, but got true: error: %v", err)
	}

	program = `myEventTrigger.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && bigGt(chainlinkPrice("0xA2F78ab2355fe2f984D808B5CeE7FD0A93D5270E"), toBigInt("95000000"))`

	result, err = eventTrigger.Evaluate(event, &Check{
		Program:      program,
		TaskMetadata: taskMeta,
	})
	if err != nil {
		t.Errorf("expected no error when evaluate program but got error: %s", err)
	}
	if !result {
		t.Errorf("expect expression to be false, but got true: error: %v", err)
	}
}

// Fix issue: https://github.com/AvaProtocol/EigenLayer-AVS/issues/149
// operator crash on: received new event trigger	{"id": "01JMBH85WVK1ZNMHBA9JWN45Y4", "type": "name:\"eventTrigger\" event:{expression:\"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef&&0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788\"} id:\"01JMBH7G6BCAKRBKRYN55ETX6M\""
// The expression in event required a statement that strictly result to true. there were a crash when blindly convert to true. This test ensure our program won't crash and that will
// Before this test and its implementation the program crash
// panic: interface conversion: interface {} is float64, not bool [recovered]
//
//	panic: interface conversion: interface {} is float64, not bool
//
// goroutine 67 [running]:
// testing.tRunner.func1.2({0x103aa1b20, 0x1400003de30})
//
//	/usr/local/go/src/testing/testing.go:1632 +0x1bc
//
// testing.tRunner.func1()
//
//	/usr/local/go/src/testing/testing.go:1635 +0x334
//
// panic({0x103aa1b20?, 0x1400003de30?})
//
//	/usr/local/go/src/runtime/panic.go:785 +0x124
//
// github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger.(*EventTrigger).Evaluate(0x140000ba3e0?, 0x140004f1600, 0x140001e9f18)
//
//	/Users/vinh/Sites/oak/eigenlayer/EigenLayer-AVS/core/taskengine/trigger/event.go:185 +0x704
//
// github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger.TestTriggerEventExpressionWontCrashOnInvalidInput(0x140004e6b60)
//
//	/Users/vinh/Sites/oak/eigenlayer/EigenLayer-AVS/core/taskengine/trigger/event_test.go:330 +0x1b0
//
// testing.tRunner(0x140004e6b60, 0x103bd8e70)
//
//	/usr/local/go/src/testing/testing.go:1690 +0xe4
//
// created by testing.(*T).Run in goroutine 1
//
//	/usr/local/go/src/testing/testing.go:1743 +0x314
//
// exit status 2
// FAIL	github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger	3.929s
func TestTriggerEventExpressionWontCrashOnInvalidInput(t *testing.T) {
	tests := []struct {
		name          string
		expression    string
		expectedError string
	}{
		{
			name:          "Invalid Hex String",
			expression:    "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
			expectedError: "the expression `0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef` didn't return a boolean but 1.0038928713678618e+77",
		},
		{
			name:          "Invalid Numeric String",
			expression:    "123",
			expectedError: "the expression `123` didn't return a boolean but 123",
		},
		{
			name:          "invalid javascript expression",
			expression:    "**P{P%^&*()_}{$%}",
			expectedError: "SyntaxError: SyntaxError: (anonymous): Line 1:1 Unexpected token **",
		},
		{
			name:          "blank expression",
			expression:    "",
			expectedError: "invalid event trigger check: both matcher or expression are missing or empty",
		},
		{
			name:          "space only expression",
			expression:    "    ",
			expectedError: "the expression `    ` didn't return a boolean but <nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := testutil.GetEventForTx("0x4bb728dfbe58d7c641c02a214cac6156a0d6a0fe648cb27a7de229a3160e91b1", 145)
			if err != nil {
				t.Fatalf("failed to get event: %v", err)
			}

			eventTrigger := NewEventTrigger(&RpcOption{
				RpcURL:   testutil.GetTestRPCURL(),
				WsRpcURL: testutil.GetTestRPCURL(),
			}, make(chan TriggerMetadata[EventMark], 1000),
				testutil.GetLogger())

			result, err := eventTrigger.Evaluate(event, &Check{
				Program: tt.expression,
			})

			if result {
				t.Errorf("expected the evaluation to return false but got true")
			}

			if err == nil || err.Error() != tt.expectedError {
				t.Errorf("expected error: %s, got: %v", tt.expectedError, err)
			}
		})
	}
}
