package worker

import (
	"context"
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestServer_GetTokenBalance_ZeroAddress: the worker rejects a zero-address
// token before dialing the chain, so a Server with no wired worker is enough
// to exercise the guard. Without it, balanceOf on the zero address fails with
// the opaque "no contract code at given address" (Sentry EIGENLAYER-AVS-1J)
// on the worker-routed withdraw path.
func TestServer_GetTokenBalance_ZeroAddress(t *testing.T) {
	s := &Server{}
	req := &avsproto.WorkerGetTokenBalanceReq{
		TokenAddress: "0x0000000000000000000000000000000000000000",
		OwnerAddress: "0x1234567890123456789012345678901234567890",
	}
	if _, err := s.GetTokenBalance(context.Background(), req); err == nil {
		t.Fatalf("expected zero-address token to be rejected")
	} else if !strings.Contains(err.Error(), "zero address") {
		t.Fatalf("error should mention zero address, got: %v", err)
	}
}
