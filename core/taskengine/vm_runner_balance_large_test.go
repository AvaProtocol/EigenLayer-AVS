package taskengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
		if r.URL.Query().Get("limit") != balanceResponseLimit || r.URL.Query().Get("cursor") != "" {
			t.Errorf("expected a single large request without a cursor, got %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		// A cursor must not cause another request. All returned rows still reach the workflow output.
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"result": tokens, "cursor": "not-followed"}); err != nil {
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
