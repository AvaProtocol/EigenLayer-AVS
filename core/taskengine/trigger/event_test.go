package trigger

import (
	"testing"

	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/expr-lang/expr"
)

func TestChainlinkLatestAnswer(t *testing.T) {
	event, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 82)

	if err != nil {
		t.Errorf("expect no error but got one: %v", err)
	}

	eventTrigger := NewEventTrigger(&RpcOption{
		RpcURL:   testutil.GetTestRPCURL(),
		WsRpcURL: testutil.GetTestRPCURL(),
	}, make(chan TriggerMetadata[EventMark], 1000))

	envs := macros.GetEnvs(map[string]interface{}{
		"trigger1": map[string]interface{}{
			"data": map[string]interface{}{
				"topics": []string{
					"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"0xabcdef",
					"0xc114fb059434563dc65ac8d57e7976e3eac534f4",
				},
			},
		},
	})

	program, err := expr.Compile(`
		trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger1.data.topics[2] == "0xc114fb059434563dc65ac8d57e7976e3eac534f4"
      `, expr.Env(envs), expr.AsBool())

	if err != nil {
		panic(err)
	}

	result, err := eventTrigger.Evaluate(event, program)
	if !result {
		t.Errorf("expect expression to be match, but got false: error: %v", err)
	}

	program, err = expr.Compile(`
		(trigger1.data.topics[0] == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" && trigger1.data.topics[2] == "abc")
      `)

	result, err = eventTrigger.Evaluate(event, program)
	if result {
		t.Errorf("expect expression to be not match, but got match: error: %v", err)
	}
}
