package aggregator

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
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
		Amount:           "1",
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

// resolveSmartWalletConfigForChain returns (r.config.SmartWallet, nil) — no
// error — in single-chain mode, so a gateway started without a smart wallet
// config hands ExecuteWithdraw a nil swCfg. The native-ETH refusal reads that
// config, and SmartWalletConfig.AccountProviderName has no nil receiver guard,
// so the refusal must not run before the nil check.
func TestExecuteWithdraw_NilSmartWalletConfigDoesNotPanic(t *testing.T) {
	server := &RpcServer{config: &config.Config{Logger: logger.NewNoOpLogger()}}
	user := &model.User{Address: common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")}

	_, err := server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress: "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
		Amount:           "1",
		Token:            "ETH",
	})
	if err == nil {
		t.Fatal("expected an error when no smart wallet config is configured")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal for a missing smart wallet config, got %s: %v", status.Code(err), err)
	}
}

type stubWithdrawReader struct {
	code []byte
	fee  *big.Int
}

func (s stubWithdrawReader) CodeAt(context.Context, common.Address) ([]byte, error) {
	return s.code, nil
}
func (s stubWithdrawReader) MaxFeePerGas(context.Context) (*big.Int, error) {
	return s.fee, nil
}

func TestExecuteWithdraw_CoveringGrantPassesPreflight(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	policy := &model.SessionPolicy{
		ID: "01coveringgrantaaaaaaaaaaa", Owner: &owner, Runner: &wallet,
		ChainID: 11155111, EntityID: 1, SessionSigner: &signer,
		Status:           model.SessionPolicyPending,
		NativeRecipients: []*common.Address{&owner},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "100000000000000000", GrantedCap: "100000000000000000"},
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
			CarrierNonce:   big.NewInt(1),
			Deadline:       1785541743,
			OwnerSignature: make([]byte, 65),
		},
	}
	if err := taskengine.StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
	}

	server := withdrawTestServer(t, "")
	server.db = db
	server.withdrawNativeReader = stubWithdrawReader{
		code: nil,
		fee:  big.NewInt(2_000_000_000),
	}

	err := server.preflightNativeWithdraw(
		&model.User{Address: owner},
		server.config.SmartWallet,
		&avsproto.WithdrawFundsReq{
			RecipientAddress:   owner.Hex(),
			Amount:             "1000000000000000",
			Token:              "ETH",
			SmartWalletAddress: wallet.Hex(),
		},
		big.NewInt(1_000_000_000_000_000),
		false,
	)
	if err != nil {
		t.Fatalf("covering grant with injected reader must pass preflight, got %v", err)
	}

	// Without a reader the same grant fail-closes — the F1 hole.
	server.withdrawNativeReader = nil
	err = server.preflightNativeWithdraw(
		&model.User{Address: owner},
		server.config.SmartWallet,
		&avsproto.WithdrawFundsReq{
			RecipientAddress:   owner.Hex(),
			Amount:             "1000000000000000",
			Token:              "ETH",
			SmartWalletAddress: wallet.Hex(),
		},
		big.NewInt(1_000_000_000_000_000),
		false,
	)
	if err == nil {
		t.Fatal("covering grant without a reader must fail closed")
	}
	if !strings.Contains(err.Error(), taskengine.SessionPolicyRecipientNotEOACode) &&
		!strings.Contains(err.Error(), taskengine.SessionPolicyNativeCapExceededCode) {
		t.Fatalf("expected EOA or cap fail-closed, got %v", err)
	}
}

func TestDerivationSaltForWallet(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x16b4b3624CC88AAafE3Df54955d19AEB2840670C")
	factory := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	const chainID int64 = 11155111
	if err := taskengine.StoreWallet(db, chainID, owner, &model.SmartWallet{
		Owner: &owner, Address: &wallet, Factory: &factory, Salt: big.NewInt(25),
	}); err != nil {
		t.Fatal(err)
	}

	server := &RpcServer{db: db, config: &config.Config{Logger: logger.NewNoOpLogger()}}
	salt, err := server.derivationSaltForWallet(chainID, owner, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if salt == nil || salt.Int64() != 25 {
		t.Fatalf("stored salt = %v, want 25", salt)
	}

	_, err = server.derivationSaltForWallet(chainID, owner, common.HexToAddress("0x000000000000000000000000000000000000dEaD"))
	if err == nil {
		t.Fatal("missing wallet must error, not fall back to salt 0")
	}
	if !strings.Contains(err.Error(), "looking up derivation salt") {
		t.Fatalf("missing wallet: %v", err)
	}

	nilSaltWallet := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	if err := taskengine.StoreWallet(db, chainID, owner, &model.SmartWallet{
		Owner: &owner, Address: &nilSaltWallet, Factory: &factory, Salt: nil,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = server.derivationSaltForWallet(chainID, owner, nilSaltWallet)
	if err == nil {
		t.Fatal("nil Salt must error, not fall back to salt 0")
	}
	if !strings.Contains(err.Error(), "no derivation salt recorded") {
		t.Fatalf("nil Salt: %v", err)
	}

	noDB := &RpcServer{config: &config.Config{Logger: logger.NewNoOpLogger()}}
	_, err = noDB.derivationSaltForWallet(chainID, owner, wallet)
	if err == nil {
		t.Fatal("no storage must error, not assume salt 0")
	}
}
