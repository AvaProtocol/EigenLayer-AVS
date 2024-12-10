package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestContractRead(t *testing.T) {
	n := NewContractReadProcessor(testutil.GetRpcClient())

	node := &avsproto.ContractReadNode{
		ContractAddress: "0x694AA1769357215DE4FAC081bf1f309aDC325306",
		CallData:        "0x9a6fc8f50000000000000000000000000000000000000000000000000000000000000050",
		ContractAbi:     `[{"inputs":[{"internalType":"uint80","name":"_roundId","type":"uint80"}],"name":"getRoundData","outputs":[{"internalType":"uint80","name":"roundId","type":"uint80"},{"internalType":"int256","name":"answer","type":"int256"}]]`,
		Method:          "getRoundData",
	}
	step, err := n.Execute("foo123", node)

	if err != nil {
		t.Errorf("expected contract read node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected contract read node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Execute POST webhook.site at") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}
	if !strings.Contains(step.OutputData, "This URL has no default content configured") {
		t.Errorf("expected step result contains the http endpoint response body")
	}
}
