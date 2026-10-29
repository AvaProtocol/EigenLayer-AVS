//go:build integration

package taskengine

import (
	"context"
	"crypto/ecdsa"
	cryptorand "crypto/rand"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
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
// session grant. A counting wrapper delegates every call to the production
// backend. The salt-26 runner's existing deposit must cover the whole signed
// prefund (checked after prepare, before signing), so the runner retains the
// signed gas reserve.
//
// Local .env loading and CI's TEST_PRIVATE_KEY export are shared with the other
// live tests; requireOwnerKey reads that environment value, never the controller
// key in config/test.yaml. This test additionally requires an explicit opt-in:
//
//	OWNER_NATIVE_SWEEP_E2E=1 go test -tags=integration ./core/taskengine \
//	  -run '^TestOwnerNativeSweep(With|Without)EntryPointDeposit_Sepolia$' -v -count=1 -timeout=12m
//
// Setup deploys before funding, tops the runner up to 0.02 ETH if necessary,
// and tops its deposit up to 0.001 ETH. The swept value returns to the owner.
// Run without concurrent use of the fixture or owner key.
func TestOwnerNativeSweepWithEntryPointDeposit_Sepolia(t *testing.T) {
	runOwnerNativeSweepSepolia(t, big.NewInt(fixtureSaltOwnerNativeSweep), true)
}

// A fresh salt makes the zero-deposit case repeatable: an earlier UserOp's
// unused prefund remains in its old runner's deposit and must not cover gas in
// this run. No authorization or deposit withdrawal is installed as setup.
func TestOwnerNativeSweepWithoutEntryPointDeposit_Sepolia(t *testing.T) {
	if testing.Short() || os.Getenv("OWNER_NATIVE_SWEEP_E2E") != "1" {
		t.Skip("set OWNER_NATIVE_SWEEP_E2E=1 and use -tags=integration for the live Sepolia sweep")
	}
	salt, err := cryptorand.Int(cryptorand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	salt.SetBit(salt, 127, 1) // nonzero and separate from the small fixed fixture salts
	runOwnerNativeSweepSepolia(t, salt, false)
}

// Count relay attempts without stubbing prepare, validation or submission.
// Counting events alone cannot detect a duplicate relay rejected by a bundler.
type ownerNativeLiveBackend struct {
	defaultOwnerExecutionBackend
	submits atomic.Int32
}

func (b *ownerNativeLiveBackend) Submit(ctx context.Context, sw *config.SmartWalletConfig, op *userop.UserOperationV07) (common.Hash, error) {
	b.submits.Add(1)
	return b.defaultOwnerExecutionBackend.Submit(ctx, sw, op)
}

func runOwnerNativeSweepSepolia(t *testing.T, salt *big.Int, depositCoversGas bool) {
	t.Helper()
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

	runner, err := aa.GetSenderAddressMAv2WithContext(ctx, client, owner, salt)
	require.NoError(t, err)
	require.NotNil(t, runner)
	require.NotEqual(t, owner, *runner)
	t.Logf("owner native fixture runner=%s salt=%s depositCoversGas=%v", runner.Hex(), salt, depositCoversGas)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("runner %s (owner %s, salt %s) may still hold this run's native balance or EntryPoint deposit; both stay owner-controlled and are recovered with an owner-signed UserOp from that runner",
				runner.Hex(), owner.Hex(), salt)
		}
	})

	// Check or arrange the real EntryPoint deposit before prepare, not an RPC
	// fixture that merely reports one. Never install a session grant.
	entryABI, err := abi.JSON(strings.NewReader(`[
	 {"type":"function","name":"depositTo","stateMutability":"payable","inputs":[{"name":"account","type":"address"}],"outputs":[]},
	 {"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"name":"account","type":"address"}],"outputs":[{"type":"uint256"}]}
	]`))
	require.NoError(t, err)
	entryPoint := preset.EntryPointV07()
	deposit := ownerNativeEntryPointDeposit(t, ctx, client, entryABI, *runner, nil)
	if !depositCoversGas {
		require.Zero(t, deposit.Sign(), "the fresh runner must have no EntryPoint deposit")
	}
	ensureOwnerNativeRunner(t, ctx, client, ownerKey, chainID, owner, *runner, salt)
	minimumDeposit := big.NewInt(1_000_000_000_000_000) // 0.001 ETH
	if depositCoversGas && deposit.Cmp(minimumDeposit) < 0 {
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
	if depositCoversGas {
		require.GreaterOrEqual(t, depositBefore.Cmp(minimumDeposit), 0)
	} else {
		require.Zero(t, depositBefore.Sign(), "setup must not create an EntryPoint deposit")
	}
	balanceCap, err := client.BalanceAt(ctx, *runner, nil)
	require.NoError(t, err)
	require.Positive(t, balanceCap.Sign())
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
	backend := &ownerNativeLiveBackend{}
	engine.ownerExecutionBackend = backend
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
			ChainId: chainID.Int64(), Destination: owner.Hex(), Amount: balanceCap.String(), ReserveGas: true,
		}}}}
	prepared, err := engine.PrepareOwnerWorkflow(ctx, user, trigger, []*avsproto.TaskNode{native},
		[]*avsproto.TaskEdge{{Id: "sweep", Source: "start", Target: "native"}},
		map[string]interface{}{"settings": map[string]interface{}{"runner": runner.Hex()}}, chainID.Int64())
	require.NoError(t, err, "real bundler must estimate the final near-full-balance calldata")
	op := prepared.Operation
	require.Nil(t, op.Paymaster)
	require.Empty(t, op.Signature)
	reserve := preset.MaxOwnerGasCost(op)
	require.Positive(t, reserve.Sign())
	if depositCoversGas {
		require.GreaterOrEqual(t, depositBefore.Cmp(reserve), 0,
			"deposit %s must cover the entire signed prefund %s; Sepolia fees exceed the 0.001 ETH fixture deposit, so rerun when they fall or raise minimumDeposit",
			depositBefore, reserve)
	}
	targets, values, data, err := aa.UnpackExecuteCalldata(op.CallData)
	require.NoError(t, err)
	require.Equal(t, []common.Address{owner}, targets)
	require.Len(t, values, 1)
	require.Len(t, data, 1)
	require.Empty(t, data[0])
	wantValue := new(big.Int).Sub(new(big.Int).Set(balanceCap), reserve)
	require.Positive(t, wantValue.Sign())
	require.GreaterOrEqual(t, new(big.Int).Mul(new(big.Int).Set(wantValue), big.NewInt(100)).Cmp(new(big.Int).Mul(new(big.Int).Set(balanceCap), big.NewInt(90))), 0,
		"this scenario must transfer at least 90% of the runner's native balance")
	requireOwnerNativeWei(t, wantValue, values[0], "reviewed value must equal the captured cap minus the signed reserve")
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
	require.EqualValues(t, 1, backend.submits.Load())

	receipt, event := waitOwnerNativeSweepEvent(t, ctx, client, startBlock, hash)
	require.Equal(t, uint64(types.ReceiptStatusSuccessful), receipt.Status)
	require.Equal(t, common.BytesToHash(runner.Bytes()), event.Topics[2])
	require.Equal(t, common.Hash{}, event.Topics[3], "native sweep must be self-funded")
	require.Len(t, event.Data, 128)
	requireOwnerNativeWei(t, op.Nonce, new(big.Int).SetBytes(event.Data[:32]), "event nonce must match the signed operation")
	requireOwnerNativeWei(t, big.NewInt(1), new(big.Int).SetBytes(event.Data[32:64]), "UserOp must execute successfully, not just be included")
	actualGasCost := new(big.Int).SetBytes(event.Data[64:96])
	require.Positive(t, actualGasCost.Sign())
	require.LessOrEqual(t, actualGasCost.Cmp(reserve), 0)

	recipientAfter, err := client.BalanceAt(ctx, owner, receipt.BlockNumber)
	require.NoError(t, err)
	recipientDelta := new(big.Int).Sub(recipientAfter, recipientBefore)
	// Other activity or bundle beneficiary fees can change the owner's total
	// balance. The strict proof is the single reviewed call to that owner,
	// successful execution and the exact runner/prefund outflow below.
	if recipientDelta.Cmp(wantValue) != 0 {
		t.Logf("owner balance delta %s differs from reviewed transfer %s; the owner may have other activity", recipientDelta, wantValue)
	}
	balanceAfter, err := client.BalanceAt(ctx, *runner, receipt.BlockNumber)
	require.NoError(t, err)
	depositAfter := ownerNativeEntryPointDeposit(t, ctx, client, entryABI, *runner, receipt.BlockNumber)
	wantRemaining := new(big.Int).Sub(new(big.Int).Add(new(big.Int).Set(balanceCap), depositBefore), wantValue)
	wantRemaining.Sub(wantRemaining, actualGasCost)
	requireOwnerNativeWei(t, wantRemaining, new(big.Int).Add(balanceAfter, depositAfter), "only the transfer and actual gas may leave the runner plus deposit")
	missingPrefund := new(big.Int)
	if !depositCoversGas {
		missingPrefund.Set(reserve)
	}
	wantBalanceAfter := new(big.Int).Sub(new(big.Int).Set(reserve), missingPrefund)
	wantDepositAfter := new(big.Int).Sub(new(big.Int).Add(new(big.Int).Set(depositBefore), missingPrefund), actualGasCost)
	requireOwnerNativeWei(t, wantBalanceAfter, balanceAfter, "runner pays only missing prefund plus the reviewed transfer")
	requireOwnerNativeWei(t, wantDepositAfter, depositAfter, "unused prefund is refunded to the EntryPoint deposit")

	// Repeated submission uses the durable continuation, even after inclusion
	// has advanced the nonce. It must neither reprice nor relay another op.
	repeated, err := engine.SubmitOwnerExecution(ctx, user, prepared.Execution.Id, signature)
	require.NoError(t, err)
	require.Equal(t, *submitted, *repeated)
	require.EqualValues(t, 1, backend.submits.Load(), "repeat submit must not reach the real relay again")
	raw, err := db.GetKey(ownerExecutionKey(owner, prepared.Execution.Id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(raw, &record))
	require.NotNil(t, record.Batch)
	require.Len(t, record.Batch.Calls, 1)
	requireOwnerNativeWei(t, balanceCap, record.Batch.Calls[0].Value, "stored cap must remain the originally reviewed balance")
	storedUnsigned, err := json.Marshal(record.Operation)
	require.NoError(t, err)
	require.Equal(t, unsigned, storedUnsigned, "the stored operation must remain unsigned and byte-for-byte equivalent")
	t.Logf("PASS: owner sweep userOp=%s tx=%s value=%s reserve=%s actualGasCost=%s missingPrefund=%s balanceAfter=%s depositBefore=%s depositAfter=%s relays=%d recipientDelta=%s",
		hash.Hex(), receipt.TxHash.Hex(), wantValue, reserve, actualGasCost, missingPrefund, balanceAfter, depositBefore, depositAfter, backend.submits.Load(), recipientDelta)
}

// Keep setup self-contained and repeatable. RawTransact estimates gas rather
// than using the fixture CLI's fixed funding limit, and deployment completes
// before funding the runner. Both setup transactions are paid by the owner.
func ensureOwnerNativeRunner(t *testing.T, ctx context.Context, client *ethclient.Client, ownerKey *ecdsa.PrivateKey, chainID *big.Int, owner, runner common.Address, salt *big.Int) {
	t.Helper()
	code, err := client.CodeAt(ctx, runner, nil)
	require.NoError(t, err)
	if len(code) == 0 {
		factory, initCode, err := aa.GetInitCodeMAv2(owner, salt)
		require.NoError(t, err)
		auth, err := bind.NewKeyedTransactorWithChainID(ownerKey, chainID)
		require.NoError(t, err)
		auth.Context = ctx
		contract := bind.NewBoundContract(factory, abi.ABI{}, client, client, client)
		tx, err := contract.RawTransact(auth, initCode)
		require.NoError(t, err, "deploying the owner-controlled fixture")
		receipt, err := bind.WaitMined(ctx, client, tx)
		require.NoError(t, err)
		require.Equal(t, uint64(types.ReceiptStatusSuccessful), receipt.Status, "fixture deployment reverted")
		code, err = client.CodeAt(ctx, runner, nil)
		require.NoError(t, err)
		require.NotEmpty(t, code)
	}
	balance, err := client.BalanceAt(ctx, runner, nil)
	require.NoError(t, err)
	minimumBalance := big.NewInt(20_000_000_000_000_000) // 0.02 ETH, swept back to owner
	if balance.Cmp(minimumBalance) < 0 {
		auth, err := bind.NewKeyedTransactorWithChainID(ownerKey, chainID)
		require.NoError(t, err)
		auth.Context = ctx
		auth.Value = new(big.Int).Sub(minimumBalance, balance)
		contract := bind.NewBoundContract(runner, abi.ABI{}, client, client, client)
		tx, err := contract.RawTransact(auth, nil)
		require.NoError(t, err, "funding the owner-controlled fixture")
		receipt, err := bind.WaitMined(ctx, client, tx)
		require.NoError(t, err)
		require.Equal(t, uint64(types.ReceiptStatusSuccessful), receipt.Status, "fixture funding reverted")
	}
}

// Compare amounts by value. require.Equal falls back to reflect.DeepEqual, which
// also compares big.Int's internal limbs: a nil and an empty slice are both zero
// but unequal, so such a check passes or fails by how each value was built.
func requireOwnerNativeWei(t *testing.T, want, got *big.Int, msg string) {
	t.Helper()
	require.NotNil(t, got, msg)
	require.Zero(t, want.Cmp(got), "%s: want %s, got %s", msg, want, got)
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
