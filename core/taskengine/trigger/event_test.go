package trigger

import (
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
)

func TestChainlinkLatestAnswer(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 82)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMark[string], 1000))

	result, err := eventTrigger.Evaluate(event, `
		(trigger.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger.data.topics[2] == "0xc114fb059434563dc65ac8d57e7976e3eac534f4")
		  && 
          (
            ( trigger.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" &&
              bigCmp(
                trigger.data.data | toBigInt(),
                "5000000" | toBigInt()
              ) > 0
            ) ||
            ( trigger.data.address == ("0x779877a7b0d9e8603169ddbd7836e478b4624789" | lower()) &&
              bigCmp(
                priceChainlink("0xc59E3633BAAC79493d908e63626716e204A45EdF"),
                toBigInt("5000000")
              ) > 0
            )
          )
      `)
	if !result {
		t.Errorf("expect expression to be match, but got false: error: %v", err)
	}

	result, err = eventTrigger.Evaluate(event, `
		(trigger.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger.data.topics[2] == "0xc114fb059434563dc65ac8d57e7976e3eac534f4")
		  && 
          (
            ( trigger.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" &&
              bigCmp(
                toBigInt(trigger.data.data),
                toBigInt("95000000")
              ) > 0
            ) ||
            ( trigger.data.address == "0x779877a7b0d9e8603169ddbd7836e478b4624789" &&
              bigCmp(
                priceChainlink("0xc59E3633BAAC79493d908e63626716e204A45EdF"),
                toBigInt("5000000")
              ) > 0
            )
          )
      `)
	if result {
		t.Errorf("expect expression to be not match, but got match: error: %v", err)
	}
}
