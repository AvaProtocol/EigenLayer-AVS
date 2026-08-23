package aggregator

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ExecuteWithdraw composes a real reason on the failure path, and the REST
// handler renders it — but the adapter used to drop it, so every failed
// withdraw reached the caller as a bare {"status":"failed"} with no cause.
// That is not cosmetic: the underlying send errors are classified
// client-fixable, so they never page either, and the failure was invisible
// from both the API and Sentry at once.
func TestWithdrawResultCarriesMessageAndSubmittedAt(t *testing.T) {
	resp := &avsproto.WithdrawFundsResp{
		Success:         false,
		Status:          "failed",
		Message:         "failed to send withdrawal transaction: no session authorization",
		SubmittedAt:     1766000000,
		UserOpHash:      "0xuserop",
		TransactionHash: "0xtx",
	}

	got := withdrawResultFrom(resp)

	if got.Message != resp.GetMessage() {
		t.Fatalf("message dropped: got %q, want %q", got.Message, resp.GetMessage())
	}
	if got.SubmittedAt != resp.GetSubmittedAt() {
		t.Fatalf("submittedAt dropped: got %d, want %d", got.SubmittedAt, resp.GetSubmittedAt())
	}
	if got.Status != "failed" {
		t.Fatalf("status: got %q", got.Status)
	}
}
