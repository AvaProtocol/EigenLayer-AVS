package taskengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
)

func TestBalanceNode_LargeSingleResponse(t *testing.T) {
	const count = 1024
	const exactBalance = "90071992547409931234567890"
	tokens := make([]MoralisTokenBalance, count)
	for i := range tokens {
		tokens[i] = MoralisTokenBalance{TokenAddress: fmt.Sprintf("0x%040x", i+1), Symbol: fmt.Sprintf("TOKEN%d", i), Decimals: 18, Balance: exactBalance}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("limit") != fmt.Sprint(balanceResponseLimit) || r.URL.Query().Get("cursor") != "" {
			t.Errorf("expected a single large request without a cursor, got %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		// An explicit final-page marker proves completeness; all returned rows reach the output.
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"result": tokens, "cursor": nil}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	previousURL := testMoralisAPIBaseURL
	testMoralisAPIBaseURL = server.URL
	t.Cleanup(func() { testMoralisAPIBaseURL = previousURL })
	config := &avsproto.BalanceNode_Config{Address: "0x1111111111111111111111111111111111111111", Chain: "sepolia"}
	vm, balance := setupBalanceVM(t, config)
	node := &avsproto.TaskNode{Id: "holdings", Name: "holdings", Type: avsproto.NodeType_NODE_TYPE_BALANCE, TaskType: &avsproto.TaskNode_Balance{Balance: balance}}
	step, err := vm.RunNodeWithInputs(node, nil)
	if err != nil || !step.GetSuccess() {
		t.Fatalf("balance node failed: %v, %s", err, step.GetError())
	}
	rows := step.GetBalance().GetData().GetListValue().GetValues()
	if len(rows) != count || requests != 1 {
		t.Fatalf("got %d rows in %d requests; want %d rows in one request", len(rows), requests, count)
	}
	for _, row := range rows {
		if row.GetStructValue().GetFields()["balance"].GetStringValue() != exactBalance {
			t.Fatal("raw token amount lost precision")
		}
	}
}

func TestBalanceNode_RejectsIncompleteResponsesWithoutPagination(t *testing.T) {
	tokens := make([]MoralisTokenBalance, balanceResponseLimit)
	for i := range tokens {
		tokens[i] = MoralisTokenBalance{TokenAddress: fmt.Sprintf("0x%040x", i+1), Balance: "1", Decimals: 18}
	}
	for _, tc := range []struct {
		name string
		body interface{}
		want int
	}{
		{"cursor at cap", map[string]interface{}{"result": tokens, "cursor": "more"}, -1},
		{"cursor before cap", map[string]interface{}{"result": tokens[:1], "cursor": "more"}, -1},
		{"array at cap", tokens, -1},
		{"wrapper without completion marker at cap", map[string]interface{}{"result": tokens}, -1},
		{"explicit final page at cap", map[string]interface{}{"result": tokens, "cursor": nil}, balanceResponseLimit},
		{"empty final cursor at cap", map[string]interface{}{"result": tokens, "cursor": ""}, balanceResponseLimit},
		{"short legacy array", tokens[:1], 1},
		{"complete empty array", []MoralisTokenBalance{}, 0},
		{"missing token list", map[string]interface{}{"cursor": nil}, -1},
		{"null response", nil, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				require.Empty(t, r.URL.Query().Get("cursor"))
				require.NoError(t, json.NewEncoder(w).Encode(tc.body))
			}))
			defer server.Close()
			previousURL := testMoralisAPIBaseURL
			testMoralisAPIBaseURL = server.URL
			t.Cleanup(func() { testMoralisAPIBaseURL = previousURL })
			vm, balance := setupBalanceVM(t, &avsproto.BalanceNode_Config{Address: "0x1111111111111111111111111111111111111111", Chain: "sepolia"})
			node := &avsproto.TaskNode{Id: "holdings", Name: "holdings", Type: avsproto.NodeType_NODE_TYPE_BALANCE, TaskType: &avsproto.TaskNode_Balance{Balance: balance}}
			step, err := vm.RunNodeWithInputs(node, nil)
			require.Equal(t, 1, requests, "never follow provider cursors")
			if tc.want < 0 {
				require.Error(t, err)
				require.False(t, step.GetSuccess())
				return
			}
			require.NoError(t, err)
			require.True(t, step.GetSuccess())
			require.Len(t, step.GetBalance().GetData().GetListValue().GetValues(), tc.want)
		})
	}
}

func TestBalanceNode_FailedRequestedLookupNeverBecomesZero(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncated=%t", truncated), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Query().Get("token_addresses[0]") != "" {
					if truncated {
						require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"result": []MoralisTokenBalance{}, "cursor": "more"}))
					} else {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
					}
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode([]MoralisTokenBalance{{NativeToken: true, Balance: "1000000000000000000", Decimals: 18}}))
			}))
			defer server.Close()
			previousURL := testMoralisAPIBaseURL
			testMoralisAPIBaseURL = server.URL
			t.Cleanup(func() { testMoralisAPIBaseURL = previousURL })
			vm, balance := setupBalanceVM(t, &avsproto.BalanceNode_Config{Address: "0x1111111111111111111111111111111111111111", Chain: "sepolia", TokenAddresses: []string{"0x2222222222222222222222222222222222222222"}})
			node := &avsproto.TaskNode{Id: "holdings", Name: "holdings", Type: avsproto.NodeType_NODE_TYPE_BALANCE, TaskType: &avsproto.TaskNode_Balance{Balance: balance}}
			step, err := vm.RunNodeWithInputs(node, nil)
			require.ErrorContains(t, err, "fetching requested token balances")
			require.False(t, step.GetSuccess())
			require.Equal(t, 2, requests)
		})
	}
}
