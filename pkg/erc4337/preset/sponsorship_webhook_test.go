package preset

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// stubAlchemySponsorship is a tiny JSON-RPC server that records the first
// alchemy_requestGasAndPaymasterAndData params for assertion.
type stubAlchemySponsorship struct {
	t      *testing.T
	params map[string]interface{}
	srv    *httptest.Server
}

func startStubAlchemy(t *testing.T) *stubAlchemySponsorship {
	t.Helper()
	s := &stubAlchemySponsorship{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int               `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Method == "alchemy_requestGasAndPaymasterAndData" && len(body.Params) > 0 {
			if err := json.Unmarshal(body.Params[0], &s.params); err != nil {
				t.Errorf("unmarshal params: %v", err)
			}
		}
		// Minimal v0.7-shaped success response.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      body.ID,
			"result": map[string]string{
				"paymaster":                     "0x2cc0c7981D846b9F2a16276556f6e8cb52BfB633",
				"paymasterData":                 "0x",
				"paymasterVerificationGasLimit": "0x186a0",
				"paymasterPostOpGasLimit":       "0x0",
				"callGasLimit":                  "0x5208",
				"verificationGasLimit":          "0x186a0",
				"preVerificationGas":            "0xc350",
				"maxFeePerGas":                  "0x1",
				"maxPriorityFeePerGas":          "0x1",
			},
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func sponsorshipTestOp() *userop.UserOperationV07 {
	return &userop.UserOperationV07{
		Sender:               common.HexToAddress("0x1"),
		Nonce:                big.NewInt(0),
		CallData:             []byte{0x01},
		CallGasLimit:         big.NewInt(1),
		VerificationGasLimit: big.NewInt(1),
		PreVerificationGas:   big.NewInt(1),
		MaxFeePerGas:         big.NewInt(0),
		MaxPriorityFeePerGas: big.NewInt(0),
		Signature:            dummySignatureV07(),
	}
}

func TestRequestSponsorshipV07_IncludesWebhookDataWhenSet(t *testing.T) {
	stub := startStubAlchemy(t)
	client, err := rpc.DialHTTP(stub.srv.URL)
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}
	t.Cleanup(client.Close)

	op := sponsorshipTestOp()
	err = RequestSponsorshipV07(context.Background(), client, op, EntryPointV07(), SponsorshipRequestV07{
		PolicyID:    "bf905871-55d7-4197-a020-605302f4bc87",
		WebhookData: "super-secret",
	})
	if err != nil {
		t.Fatalf("RequestSponsorshipV07: %v", err)
	}
	if stub.params == nil {
		t.Fatal("stub did not capture params")
	}
	got, _ := stub.params["webhookData"].(string)
	if got != "super-secret" {
		t.Fatalf("webhookData = %q, want super-secret", got)
	}
	if _, ok := stub.params["policyId"]; !ok {
		t.Fatal("policyId missing from params")
	}
}

func TestRequestSponsorshipV07_OmitsWebhookDataWhenEmpty(t *testing.T) {
	stub := startStubAlchemy(t)
	client, err := rpc.DialHTTP(stub.srv.URL)
	if err != nil {
		t.Fatalf("dial stub: %v", err)
	}
	t.Cleanup(client.Close)

	op := sponsorshipTestOp()
	err = RequestSponsorshipV07(context.Background(), client, op, EntryPointV07(), SponsorshipRequestV07{
		PolicyID: "bf905871-55d7-4197-a020-605302f4bc87",
		// WebhookData empty
	})
	if err != nil {
		t.Fatalf("RequestSponsorshipV07: %v", err)
	}
	if stub.params == nil {
		t.Fatal("stub did not capture params")
	}
	if _, ok := stub.params["webhookData"]; ok {
		t.Fatalf("webhookData should be omitted when empty, got %v", stub.params["webhookData"])
	}
	// Sanity: request still had required fields
	if !strings.HasPrefix(stub.params["entryPoint"].(string), "0x") {
		t.Fatalf("entryPoint unexpected: %v", stub.params["entryPoint"])
	}
}
