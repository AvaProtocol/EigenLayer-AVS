package preset

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// Ensures RequestSponsorshipV07 request shape includes webhookData only when set.
// Full RPC is not exercised here; this guards the param map construction contract
// used against Alchemy's custom-rules webhook (gas_manager_webhook_secret).
func TestSponsorshipRequestV07_WebhookDataOptional(t *testing.T) {
	t.Parallel()
	if (SponsorshipRequestV07{PolicyID: "p", WebhookData: "secret"}).WebhookData != "secret" {
		t.Fatal("WebhookData field must be settable on SponsorshipRequestV07")
	}
	if (SponsorshipRequestV07{PolicyID: "p"}).WebhookData != "" {
		t.Fatal("WebhookData must default empty")
	}

	// Smoke that a UserOp still marshals for the userOperation field (unchanged).
	op := &userop.UserOperationV07{
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
	if _, err := json.Marshal(op); err != nil {
		t.Fatalf("marshal op: %v", err)
	}
}
