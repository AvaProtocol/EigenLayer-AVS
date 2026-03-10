package taskengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Test GraphQL node processing against a local mock server
func TestGraphlQlNodeSimpleQuery(t *testing.T) {
	// Mock GraphQL server that returns a response matching The Graph subgraph format
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"data": {
				"markets": [
					{
						"id": "0x794a61358d6845594f94dc1db02a252b5b4814ad-0x82af49447d8a07e3bd95bd0d56f35241523fbab1-0xa97684ead0e402dc232d5a977953df7ecbab3cdb",
						"name": "Aave Arbitrum WETH",
						"inputToken": {
							"symbol": "WETH",
							"decimals": 18
						},
						"totalValueLockedUSD": "150000000.50"
					},
					{
						"id": "0x794a61358d6845594f94dc1db02a252b5b4814ad-0xaf88d065e77c8cc2239327c5edb3a432268e5831-0xa97684ead0e402dc232d5a977953df7ecbab3cdb",
						"name": "Aave Arbitrum USDC",
						"inputToken": {
							"symbol": "USDC",
							"decimals": 6
						},
						"totalValueLockedUSD": "200000000.25"
					}
				]
			}
		}`)
	}))
	defer mockServer.Close()

	node := &avsproto.GraphQLQueryNode{
		Config: &avsproto.GraphQLQueryNode_Config{
			Url: mockServer.URL,
			Query: `
          query AaveMarkets {
            markets(first: 2, orderBy: totalValueLockedUSD, orderDirection: desc) {
              id
              name
              inputToken {
                symbol
                decimals
              }
              totalValueLockedUSD
            }
          }
			`,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
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
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	n, _ := NewGraphqlQueryProcessor(vm)

	step, _, err := n.Execute("123abc", node)
	if err != nil {
		t.Fatalf("expected graphql node to run successfully but got error: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected graphql node to run successfully but failed")
	}

	if !strings.Contains(step.Log, "Executing 'graphqlQuery'") {
		t.Errorf("expected log contains request trace data but not found. Log data is: %s", step.Log)
	}

	if step.Error != "" {
		t.Errorf("expected no error but got: %s", step.Error)
	}

	graphqlResult := step.GetGraphql()
	if graphqlResult == nil || graphqlResult.Data == nil {
		t.Fatal("expected graphql data but got nil")
	}

	var output struct {
		Markets []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			InputToken struct {
				Symbol   string `json:"symbol"`
				Decimals int    `json:"decimals"`
			} `json:"inputToken"`
			TotalValueLockedUSD string `json:"totalValueLockedUSD"`
		} `json:"markets"`
	}

	dataMap := gow.ValueToMap(graphqlResult.Data)
	if dataMap == nil {
		t.Fatal("expected graphql data map but got nil")
	}

	jsonBytes, err := json.Marshal(dataMap)
	if err != nil {
		t.Fatalf("failed to marshal data map: %v", err)
	}

	err = json.Unmarshal(jsonBytes, &output)
	if err != nil {
		t.Fatalf("expected the data output in json format, but failed to decode: %v", err)
	}

	if len(output.Markets) != 2 {
		t.Errorf("expected 2 markets but found %d", len(output.Markets))
	}

	if output.Markets[0].Name != "Aave Arbitrum WETH" {
		t.Errorf("name doesn't match. expected %s got %s", "Aave Arbitrum WETH", output.Markets[0].Name)
	}

	if output.Markets[0].InputToken.Symbol != "WETH" {
		t.Errorf("symbol doesn't match. expected %s got %s", "WETH", output.Markets[0].InputToken.Symbol)
	}

	if output.Markets[1].Name != "Aave Arbitrum USDC" {
		t.Errorf("name doesn't match. expected %s got %s", "Aave Arbitrum USDC", output.Markets[1].Name)
	}

	if output.Markets[1].InputToken.Decimals != 6 {
		t.Errorf("decimals doesn't match. expected %d got %d", 6, output.Markets[1].InputToken.Decimals)
	}
}
