package aggregator

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// withdrawTestServer is the smallest RpcServer that can reach the native-ETH
// refusal: single-chain mode (nil chainRegistry), a smart wallet config, and a
// logger. It deliberately has no chain RPC and no chain-state reader — the
// refusal must land before any of that is needed, which is also why the
// validation block sits ahead of chain-reader resolution in ExecuteWithdraw.
func withdrawTestServer(t *testing.T, accountProvider string) *RpcServer {
	t.Helper()
	return &RpcServer{
		config: &config.Config{
			Logger: logger.NewNoOpLogger(),
			SmartWallet: &config.SmartWalletConfig{
				ChainID:         11155111,
				AccountProvider: accountProvider,
			},
		},
	}
}

func TestExecuteWithdraw_RefusesNativeETHOnModularAccountV2(t *testing.T) {
	server := withdrawTestServer(t, "") // empty defaults to modular_account_v2
	user := &model.User{Address: common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")}

	_, err := server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress:   "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
		Amount:             "1000000000000000",
		Token:              "ETH",
		SmartWalletAddress: "0x209eb31c199bEB4c386eF83CF442DE1a00667a1F",
	})
	if err == nil {
		t.Fatal("expected a native ETH withdraw to be refused")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected a gRPC status error, got %T", err)
	}
	// InvalidArgument is what the problem middleware maps to 400; anything
	// else would surface this client-fixable refusal as a 500.
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
	if !strings.Contains(st.Message(), taskengine.SessionPolicyNativeNotAllowedCode) {
		t.Fatalf("expected the %s marker (the REST layer keys the problem code off it), got %q",
			taskengine.SessionPolicyNativeNotAllowedCode, st.Message())
	}
}

// Lowercase "eth" is the same request; the calldata builder upper-cases before
// comparing, so the refusal has to match on the same terms or a lowercase
// token would slip through to the bundler as AA23.
func TestExecuteWithdraw_NativeRefusalIsCaseInsensitive(t *testing.T) {
	server := withdrawTestServer(t, "")
	user := &model.User{Address: common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")}

	_, err := server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress: "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
		Amount:           "max",
		Token:            "eth",
	})
	if err == nil || !strings.Contains(err.Error(), taskengine.SessionPolicyNativeNotAllowedCode) {
		t.Fatalf("lowercase \"eth\" must be refused too, got %v", err)
	}
}

// An ERC-20 withdraw carries a real transfer selector, so the native refusal
// must not fire for it. It fails later for want of a chain reader — that it
// gets that far is the assertion.
func TestExecuteWithdraw_ERC20NotRefusedAsNative(t *testing.T) {
	server := withdrawTestServer(t, "")
	user := &model.User{Address: common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")}

	_, err := server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress: "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
		Amount:           "10000",
		Token:            "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
	})
	if err != nil && strings.Contains(err.Error(), taskengine.SessionPolicyNativeNotAllowedCode) {
		t.Fatalf("ERC-20 withdraw must not hit the native refusal, got %v", err)
	}
}

// Bad input still reports the input problem: the native refusal must not
// shadow the recipient check, or a typo'd address would come back as an
// unrelated session-policy error.
func TestExecuteWithdraw_InvalidRecipientTakesPrecedence(t *testing.T) {
	server := withdrawTestServer(t, "")
	user := &model.User{Address: common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")}

	_, err := server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress: "not-an-address",
		Amount:           "1",
		Token:            "ETH",
	})
	if err == nil {
		t.Fatal("expected an error for a malformed recipient")
	}
	if strings.Contains(err.Error(), taskengine.SessionPolicyNativeNotAllowedCode) {
		t.Fatalf("recipient validation should win over the native refusal, got %v", err)
	}
}
