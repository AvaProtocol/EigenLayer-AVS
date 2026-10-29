//go:build integration

package taskengine

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestOwnerNativeSweepWithEntryPointDeposit_Sepolia exercises the production
// prepare/sign/submit path against a real bundler and EntryPoint, without a
// session grant or an injected ownerExecutionBackend. It sweeps the dedicated
// salt-26 runner to its owner, retaining the signed maximum gas reserve.
//
// Local .env loading and CI's TEST_PRIVATE_KEY export are shared with the other
// live tests; requireOwnerKey reads that environment value, never the controller
// key in config/test.yaml. This test additionally requires an explicit opt-in:
//
//	go run ./scripts/fixture_wallet -salt 26 -deploy
//	go run ./scripts/fixture_wallet -salt 26 -fund 0.02
//	OWNER_NATIVE_SWEEP_E2E=1 go test -tags=integration ./core/taskengine \
//	  -run '^TestOwnerNativeSweepWithEntryPointDeposit_Sepolia$' -v -count=1 -timeout=6m
//
// The owner must also cover a deposit top-up of at most 0.001 Sepolia ETH plus
// that transaction's gas. Existing deposits are retained; this is not a deposit
// withdrawal test. Run this fixture on its own, without concurrent wallet use.
func TestOwnerNativeSweepWithEntryPointDeposit_Sepolia(t *testing.T) {
	if testing.Short() || os.Getenv("OWNER_NATIVE_SWEEP_E2E") != "1" {
		t.Skip("set OWNER_NATIVE_SWEEP_E2E=1 and use -tags=integration for the live Sepolia sweep")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ownerKey := requireOwnerKey(t)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	if configured := strings.TrimSpace(os.Getenv("OWNER_EOA")); configured != "" {
		require.True(t, common.IsHexAddress(configured), "OWNER_EOA must be a valid address")
		require.Equal(t, common.HexToAddress(configured), owner, "TEST_PRIVATE_KEY must belong to OWNER_EOA")
	}
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "config/test.yaml must load")
	require.NotNil(t, cfg.SmartWallet)
	client, err := ethclient.DialContext(ctx, cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	chainID, err := client.ChainID(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(11155111), chainID.Int64(), "this test only spends Sepolia ETH")

	salt := big.NewInt(fixtureSaltOwnerNativeSweep)
	runner, err := aa.GetSenderAddressMAv2WithContext(ctx, client, owner, salt)
	require.NoError(t, err)
	require.NotNil(t, runner)
	require.NotEqual(t, owner, *runner)
	code, err := client.CodeAt(ctx, *runner, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code, "deploy the dedicated runner with scripts/fixture_wallet -salt 26 -deploy, then fund it separately with -salt 26 -fund 0.02")

	// Arrange a real, nonzero EntryPoint deposit before prepare, not an RPC
	// fixture that merely reports one. Never install a session grant.
	entryABI, err := abi.JSON(strings.NewReader(`[
	 {"type":"function","name":"depositTo","stateMutability":"payable","inputs":[{"name":"account","type":"address"}],"outputs":[]},
	 {"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"name":"account","type":"address"}],"outputs":[{"type":"uint256"}]}
	]`))
	require.NoError(t, err)
	entryPoint := preset.EntryPointV07()
	deposit := ownerNativeEntryPointDeposit(t, ctx, client, entryABI, *runner, nil)
	minimumDeposit := big.NewInt(1_000_000_000_000_000) // 0.001 ETH
	if deposit.Cmp(minimumDeposit) < 0 {
		auth, err := bind.NewKeyedTransactorWithChainID(ownerKey, chainID)
		require.NoError(t, err)
		auth.Context = ctx
		auth.Value = new(big.Int).Sub(minimumDeposit, deposit)
		entry := bind.NewBoundContract(entryPoint, entryABI, client, client, client)
		tx, err := entry.Transact(auth, "depositTo", *runner)
		require.NoError(t, err, "fund the owner's EOA for the EntryPoint deposit top-up")
		receipt, err := bind.WaitMined(ctx, client, tx)
		require.NoError(t, err)
		require.Equal(t, uint64(types.ReceiptStatusSuccessful), receipt.Status, "depositTo reverted")
	}
	depositBefore := ownerNativeEntryPointDeposit(t, ctx, client, entryABI, *runner, nil)
	require.GreaterOrEqual(t, depositBefore.Cmp(minimumDeposit), 0)
	cap, err := client.BalanceAt(ctx, *runner, nil)
	require.NoError(t, err)
	require.Positive(t, cap.Sign(), "fund the dedicated salt-26 runner before running this sweep")
	recipientBefore, err := client.BalanceAt(ctx, owner, nil)
	require.NoError(t, err)
	startBlock, err := client.BlockNumber(ctx)
	require.NoError(t, err)

	// Enable only this in-process owner execution, explicitly self-funded even
	// when the developer's config names a Gas Manager policy.
	cfg.OwnerExecutionEnabled, cfg.DisableGasSponsorship = true, true
	cfg.SmartWallet.DisableGasSponsorship = true
	for _, chain := range cfg.Chains {
		if chain.SmartWallet != nil {
			chain.SmartWallet.DisableGasSponsorship = true
		}
	}
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(engine.Stop)
	require.Nil(t, engine.ownerExecutionBackend, "exercise the default production backend")
	factory := aa.MAv2FactoryAddress()
	require.NoError(t, StoreWallet(db, chainID.Int64(), owner, &model.SmartWallet{
		Owner: &owner, Address: runner, Factory: &factory, Salt: salt,
	}))
	user := &model.User{Address: owner, SmartAccountAddress: runner, ChainID: chainID.Int64()}
	trigger := &avsproto.TaskTrigger{Id: "start", Name: "start", Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{Manual: &avsproto.ManualTrigger{Config: &avsproto.ManualTrigger_Config{
			Lang: avsproto.Lang_LANG_JSON, Data: structpb.NewStructValue(&structpb.Struct{}),
		}}}}
	native := &avsproto.TaskNode{Id: "native", Name: "native", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
		TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{
			ChainId: chainID.Int64(), Destination: owner.Hex(), Amount: cap.String(), ReserveGas: true,
		}}}}
	prepared, err := engine.PrepareOwnerWorkflow(ctx, user, trigger, []*avsproto.TaskNode{native},
		[]*avsproto.TaskEdge{{Id: "sweep", Source: "start", Target: "native"}},
		map[string]interface{}{"settings": map[string]interface{}{"runner": runner.Hex()}}, chainID.Int64())
	require.NoError(t, err, "real bundler must estimate the final near-full-balance calldata with an existing deposit")
	op := prepared.Operation
	require.Nil(t, op.Paymaster)
	require.Empty(t, op.Signature)
	reserve := preset.MaxOwnerGasCost(op)
	require.Positive(t, reserve.Sign())
	targets, values, data, err := aa.UnpackExecuteCalldata(op.CallData)
	require.NoError(t, err)
	require.Equal(t, []common.Address{owner}, targets)
	require.Len(t, values, 1)
	require.Empty(t, data[0])
	wantValue := new(big.Int).Sub(new(big.Int).Set(cap), reserve)
	require.Positive(t, wantValue.Sign())
	require.GreaterOrEqual(t, new(big.Int).Mul(new(big.Int).Set(wantValue), big.NewInt(100)).Cmp(new(big.Int).Mul(new(big.Int).Set(cap), big.NewInt(90))), 0,
		"fund the salt-26 runner so this scenario transfers at least 90%% of its native balance")
	require.Equal(t, wantValue, values[0], "reserve must come from the captured cap, not the existing deposit")
	unsigned, err := json.Marshal((*storedOwnerOperation)(op))
	require.NoError(t, err)
	hash, err := op.GetUserOpHash(entryPoint, chainID)
	require.NoError(t, err)
	require.Equal(t, hash.Hex(), prepared.UserOpHash)

	// Match the advertised wallet personal_sign recipe: raw 32-byte hash,
	// EIP-191 prefix once. Submit accepts only the resulting signature.
	signature, err := crypto.Sign(accounts.TextHash(hash.Bytes()), ownerKey)
	require.NoError(t, err)
	signature[64] += 27
	submitted, err := engine.SubmitOwnerExecution(ctx, user, prepared.Execution.Id, signature)
	require.NoError(t, err)
	require.Equal(t, prepared.UserOpHash, submitted.UserOpHash)
	require.Equal(t, ownerSubmissionSubmitted, submitted.SubmissionState)

	receipt, event := waitOwnerNativeSweepEvent(t, ctx, client, startBlock, hash)
	require.Equal(t, uint64(types.ReceiptStatusSuccessful), receipt.Status)
	require.Equal(t, common.BytesToHash(runner.Bytes()), event.Topics[2])
	require.Equal(t, common.Hash{}, event.Topics[3], "native sweep must be self-funded")
	require.Len(t, event.Data, 128)
	require.Equal(t, op.Nonce, new(big.Int).SetBytes(event.Data[:32]))
	require.Equal(t, big.NewInt(1), new(big.Int).SetBytes(event.Data[32:64]), "UserOp must execute successfully, not just be included")
	actualGasCost := new(big.Int).SetBytes(event.Data[64:96])
	require.Positive(t, actualGasCost.Sign())
	require.LessOrEqual(t, actualGasCost.Cmp(reserve), 0)

	recipientAfter, err := client.BalanceAt(ctx, owner, receipt.BlockNumber)
	require.NoError(t, err)
	require.Equal(t, wantValue, new(big.Int).Sub(recipientAfter, recipientBefore), "recipient must get the exact reviewed amount")
	balanceAfter, err := client.BalanceAt(ctx, *runner, receipt.BlockNumber)
	require.NoError(t, err)
	depositAfter := ownerNativeEntryPointDeposit(t, ctx, client, entryABI, *runner, receipt.BlockNumber)
	wantRemaining := new(big.Int).Sub(new(big.Int).Add(new(big.Int).Set(cap), depositBefore), wantValue)
	wantRemaining.Sub(wantRemaining, actualGasCost)
	require.Equal(t, wantRemaining, new(big.Int).Add(balanceAfter, depositAfter), "only the transfer and actual gas may leave the runner plus deposit")
	minimumRemainingDeposit := new(big.Int).Sub(new(big.Int).Set(depositBefore), actualGasCost)
	if minimumRemainingDeposit.Sign() > 0 {
		require.GreaterOrEqual(t, depositAfter.Cmp(minimumRemainingDeposit), 0, "pre-existing deposit is not swept to the recipient")
	}

	// Repeated submission uses the durable continuation, even after inclusion
	// has advanced the nonce. It must neither reprice nor relay another op.
	repeated, err := engine.SubmitOwnerExecution(ctx, user, prepared.Execution.Id, signature)
	require.NoError(t, err)
	require.Equal(t, *submitted, *repeated)
	raw, err := db.GetKey(ownerExecutionKey(owner, prepared.Execution.Id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(raw, &record))
	require.NotNil(t, record.Batch)
	require.Len(t, record.Batch.Calls, 1)
	require.Equal(t, cap, record.Batch.Calls[0].Value, "stored cap must remain the originally reviewed balance")
	storedUnsigned, err := json.Marshal(record.Operation)
	require.NoError(t, err)
	require.Equal(t, unsigned, storedUnsigned, "the stored operation must remain unsigned and byte-for-byte equivalent")
	t.Logf("PASS: owner sweep userOp=%s tx=%s value=%s reserve=%s actualGasCost=%s depositBefore=%s depositAfter=%s",
		hash.Hex(), receipt.TxHash.Hex(), wantValue, reserve, actualGasCost, depositBefore, depositAfter)
}

func ownerNativeEntryPointDeposit(t *testing.T, ctx context.Context, client *ethclient.Client, entryABI abi.ABI, runner common.Address, block *big.Int) *big.Int {
	t.Helper()
	data, err := entryABI.Pack("balanceOf", runner)
	require.NoError(t, err)
	entryPoint := preset.EntryPointV07()
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &entryPoint, Data: data}, block)
	require.NoError(t, err)
	require.Len(t, out, 32)
	return new(big.Int).SetBytes(out)
}

func waitOwnerNativeSweepEvent(t *testing.T, ctx context.Context, client *ethclient.Client, from uint64, hash common.Hash) (*types.Receipt, types.Log) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	query := ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(from), Addresses: []common.Address{preset.EntryPointV07()},
		Topics: [][]common.Hash{{crypto.Keccak256Hash([]byte("UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)"))}, {hash}}}
	for {
		logs, err := client.FilterLogs(ctx, query)
		require.NoError(t, err)
		if len(logs) > 0 {
			require.Len(t, logs, 1)
			require.Len(t, logs[0].Topics, 4)
			receipt, err := client.TransactionReceipt(ctx, logs[0].TxHash)
			require.NoError(t, err)
			return receipt, logs[0]
		}
		select {
		case <-ctx.Done():
			t.Fatalf("owner native sweep %s was not included: %v", hash.Hex(), ctx.Err())
		case <-ticker.C:
		}
	}
}
