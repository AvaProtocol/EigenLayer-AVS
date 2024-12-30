package trigger

import (
	"testing"

	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	"github.com/AvaProtocol/ap-avs/core/testutil"
)

func TestTriggerExpression(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 82)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000))

	program := `trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger1.data.topics[2] == "0xc114fb059434563dc65ac8d57e7976e3eac534f4"`

	result, err := eventTrigger.Evaluate(event, program)
	if !result {
		t.Errorf("expect expression to be match, but got false: error: %v", err)
	}

	program = `trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger1.data.topics[2] == "abc"`

	result, err = eventTrigger.Evaluate(event, program)
	if result {
		t.Errorf("expect expression to be not match, but got match: error: %v", err)
	}

	event, err = testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 81)
	program = `trigger1.data.address == "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789" && trigger1.data.topics[0] == "0xbb47ee3e183a558b1a2ff0874b079f3fc5478b7454eacf2bfc5af2ff5878f972"`
	result, err = eventTrigger.Evaluate(event, program)
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
	}, make(chan TriggerMetadata[EventMark], 1000))

	// USDC pair from chainlink, usually USDC price is ~99cent but never approach $1
	// for an unknow reason the decimal is 8 instead of 6
	program := `trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && bigGt(chainlinkPrice("0xA2F78ab2355fe2f984D808B5CeE7FD0A93D5270E"), toBigInt("1000000000"))`

	result, err := eventTrigger.Evaluate(event, program)
	if err != nil {
		t.Errorf("expected no error when evaluate program but got error: %s", err)
	}
	if result {
		t.Errorf("expect expression to be false, but got true: error: %v", err)
	}

	program = `trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && bigGt(chainlinkPrice("0xA2F78ab2355fe2f984D808B5CeE7FD0A93D5270E"), toBigInt("95000000"))`

	result, err = eventTrigger.Evaluate(event, program)
	if err != nil {
		t.Errorf("expected no error when evaluate program but got error: %s", err)
	}
	if !result {
		t.Errorf("expect expression to be false, but got true: error: %v", err)
	}
}
