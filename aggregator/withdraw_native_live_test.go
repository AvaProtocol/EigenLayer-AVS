//go:build integration
// +build integration

package aggregator

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// L6 — native withdraw REST to a listed recipient vs an unlisted one.
// Own salt (25) so it does not share entities with the taskengine A4 suite.
//
//	OWNER_EOA, TEST_PRIVATE_KEY, config/test.yaml
//	go test -tags=integration ./aggregator -run TestExecuteWithdraw_NativeGrant_Sepolia -v -count=1
func TestExecuteWithdraw_NativeGrant_Sepolia(t *testing.T) {
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err)

	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	chainID, err := client.ChainID(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(11155111), chainID.Int64())

	ownerHex := strings.TrimSpace(os.Getenv("OWNER_EOA"))
	require.NotEmpty(t, ownerHex)
	owner := common.HexToAddress(ownerHex)
	raw := os.Getenv("TEST_PRIVATE_KEY")
	require.NotEmpty(t, raw)
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		raw = raw[2:]
	}
	ownerKey, err := crypto.HexToECDSA(raw)
	require.NoError(t, err)
	require.Equal(t, owner, crypto.PubkeyToAddress(ownerKey.PublicKey))

	require.NoError(t, aa.SetFactoryAddressForConfig(cfg.SmartWallet))
	const salt = int64(25)
	runner, err := aa.GetSenderAddressMAv2(client, owner, big.NewInt(salt))
	require.NoError(t, err)
	t.Logf("L6 runner %s (salt %d)", runner.Hex(), salt)

	code, err := client.CodeAt(context.Background(), *runner, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code,
		"runner %s is not deployed; deploy with scripts/fixture_wallet -salt %d -deploy -fund 0.03",
		runner.Hex(), salt)
	bal, err := client.BalanceAt(context.Background(), *runner, nil)
	require.NoError(t, err)
	need := big.NewInt(20_000_000_000_000_000) // 0.02 ETH
	require.True(t, bal.Cmp(need) >= 0,
		"runner %s holds %s wei, need %s; fund with scripts/fixture_wallet -salt %d -fund 0.03",
		runner.Hex(), bal, need, salt)

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	engine := taskengine.New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(engine.Stop)
	engine.InstallSessionResolver()
	t.Cleanup(func() { preset.SetSessionResolver(nil) })
	taskengine.RegisterChainStateReader(11155111, taskengine.NewDirectChainStateReader(client, 11155111))
	t.Cleanup(taskengine.ClearChainStateReaderRegistry)

	factory, err := aa.EffectiveFactory(cfg.SmartWallet)
	require.NoError(t, err)
	require.NoError(t, taskengine.StoreWallet(db, chainID.Int64(), owner, &model.SmartWallet{
		Owner: &owner, Address: runner, Factory: &factory, Salt: big.NewInt(salt),
	}))
	user := &model.User{Address: owner, SmartAccountAddress: runner}

	rec := owner
	perms := taskengine.SessionPermissions{
		NativeRecipients: []*common.Address{&rec},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "30000000000000000", GrantedCap: "30000000000000000"},
		ValidUntilMs:     time.Now().Add(7 * 24 * time.Hour).UnixMilli(),
	}
	in := taskengine.SessionPolicyInput{Wallet: *runner, ChainID: chainID.Int64(), AgentLabel: "WithdrawLive", Permissions: perms}
	prepared, err := engine.PrepareSessionPolicy(user, in)
	require.NoError(t, err)
	sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey)
	require.NoError(t, err)
	sig[64] += 27
	_, _, err = engine.SubmitSessionPolicy(user, in, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, sig)
	require.NoError(t, err)

	// Production reader path: pooled ChainStateReader + smartWalletRpc.
	// Do not inject withdrawNativeReader — that is the F1 hole A4 must prove.
	server := &RpcServer{
		config:         cfg,
		engine:         engine,
		db:             db,
		smartWalletRpc: client,
		ethrpc:         client,
		chainID:        chainID,
	}

	bob := common.HexToAddress("0x000000000000000000000000000000000000b0b0")
	_, err = server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress:   bob.Hex(),
		Amount:             "1000000000000",
		Token:              "ETH",
		SmartWalletAddress: runner.Hex(),
		ChainId:            chainID.Int64(),
	})
	require.Error(t, err, "L6: unlisted withdraw must fail")
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), taskengine.SessionPolicyRecipientNotAllowedCode,
		"L6 unlisted must be RECIPIENT_NOT_ALLOWED (grant resolved), not NATIVE_NOT_ALLOWED (grant missing): %v", err)
	t.Log("L6 PASS: unlisted withdraw refused with RECIPIENT_NOT_ALLOWED")

	listed, err := server.ExecuteWithdraw(context.Background(), user, &avsproto.WithdrawFundsReq{
		RecipientAddress:   owner.Hex(),
		Amount:             "1000000000000",
		Token:              "ETH",
		SmartWalletAddress: runner.Hex(),
		ChainId:            chainID.Int64(),
	})
	require.NoError(t, err, "L6: listed withdraw RPC error")
	require.NotNil(t, listed)
	require.True(t, listed.Success, "L6: listed withdraw must succeed: %s", listed.Message)
	t.Logf("L6 PASS: listed withdraw mined userOp=%s", listed.UserOpHash)
}
