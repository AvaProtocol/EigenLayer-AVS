package taskengine

import (
	"encoding/json"
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestGraphlQlNodeSimpleQuery(t *testing.T) {
	node := &avsproto.GraphQLQueryNode{
		Url: "https://gateway.thegraph.com/api/10186dcf11921c7d1bc140721c69da38/subgraphs/id/Sxx812XgeKyzQPaBpR5YZWmGV5fZuBaPdh7DFhzSwiQ",
		Query: `
		{
		  lidoApprovals(where:{id: "0x000016bb61e32d436819632cc192fbc7565ba08a8e9831d67210eab974a5512343010000"}) {
		    value
			id
			owner
			spender
		  }
		}`,
	}

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "123abc",
			Name: "graphqlQuery",
			TaskType: &avsproto.TaskNode_GraphqlQuery{
				GraphqlQuery: node,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData("123abc", trigger, nil, nodes, edges)
	n, _ := NewGraphqlQueryProcessor(vm, node.Url)

	step, _, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Execute GraphQL gateway.thegraph.com") {
		t.Errorf("expected log contains request trace data but not found. Log data is: %s", step.Log)
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}

	var output struct {
		LidoApprovals []struct {
			ID      string `json:"id"`
			Owner   string `json:"owner"`
			Spender string `json:"spender"`
			Value   string `json:"value"`
		} `json:"lidoApprovals"`
	}
	err = json.Unmarshal([]byte(step.OutputData), &output)
	if err != nil {
		t.Errorf("expected the data output in json format, but failed to decode %v", err)
	}
	if len(output.LidoApprovals) == 0 {
		t.Errorf("expected non zero approval but found none. data %s", step.OutputData)
	}
	if output.LidoApprovals[0].Owner != "0xd07cb0f431a53601030262a8b8b90f946ad7514a" {
		t.Errorf("owner doesn't match. expected %s got %s", "0xd07cb0f431a53601030262a8b8b90f946ad7514a", output.LidoApprovals[0].Owner)
	}

	if output.LidoApprovals[0].Value != "115792089237316195423570985008687907853269984665640564039457584007913129639935" {
		t.Errorf("value doesn't match. expected %s got %s", "115792089237316195423570985008687907853269984665640564039457584007913129639935", output.LidoApprovals[0].Value)
	}
	if output.LidoApprovals[0].Spender != "0xcb859ea579b28e02b87a1fde08d087ab9dbe5149" {
		t.Errorf("spender doesn't match. expected %s got %s", "115792089237316195423570985008687907853269984665640564039457584007913129639935", output.LidoApprovals[0].Value)
	}
}
