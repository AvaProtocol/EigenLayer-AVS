//go:build integration

package taskengine

import (
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Real token send -> VM native MAX simulation/run -> owner recovery. Each case
// uses an isolated testnet wallet, at most 0.02 ETH initial funding, 100 USDC
// base units and (deposit case only) 0.001 ETH in the owner's EntryPoint deposit.
// The two short-limit cases use an OWNER operation after the token send to set
// the exact remaining limit; this is fixture setup, not simulated chain state.
func TestSelfFundedNativeMax_Sepolia(t *testing.T) {
	if testing.Short() || os.Getenv("NATIVE_MAX_LIVE") != "1" {
		t.Skip("set NATIVE_MAX_LIVE=1 for bounded Sepolia native MAX canaries")
	}
	t.Setenv("WALLET_BUDGET_FEASIBILITY", "1")
	for _, tc := range []struct {
		name      string
		remaining int64
		deposit   bool
	}{
		{name: "full_sweep"},
		{name: "short_permission", remaining: 1_000_000_000_000_000},
		{name: "permission_below_fee", remaining: 1},
		{name: "deposit_funded_fee", deposit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBudgetSpike(t, true)
			sessionKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			f.sessionKey = sessionKey
			const entity = uint32(701)
			cap := big.NewInt(30_000_000_000_000_000)
			until := time.Now().Add(time.Hour).UnixMilli()
			token := common.HexToAddress(SEPOLIA_USDC)
			permissions := SessionPermissions{
				AllowedActions:   []model.AllowedAction{{Target: &token, Selectors: []string{selectorApprove, selectorTransfer}}},
				SpendCap:         &model.ERC20SpendCap{Token: &token, Amount: "500"},
				NativeRecipients: []*common.Address{&f.owner}, NativeSpendCap: &model.NativeSpendCap{Amount: cap.String()},
				ValidUntilMs: until,
			}
			install := f.install(t, entity, big.NewInt(500), uint64(until/1000), []aa.AllowlistInput{{Target: f.owner}}, cap)
			installHash, err := f.ownerBatch(t, []aa.Call{f.call(f.wallet, install)})
			require.NoError(t, err)
			ownerSignature, err := crypto.Sign(accounts.TextHash(installHash.Bytes()), f.ownerKey)
			require.NoError(t, err)
			ownerSignature[64] += 27
			db, err := storage.NewWithPath(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			factory := aa.MAv2FactoryAddress()
			require.NoError(t, storeWalletFixture(db, testPolicyChain, f.owner, &model.SmartWallet{Owner: &f.owner, Address: &f.wallet, Factory: &factory, Salt: f.salt}))
			signer := crypto.PubkeyToAddress(sessionKey.PublicKey)
			policy := &model.SessionPolicy{ID: "live-native-max", Owner: &f.owner, Runner: &f.wallet, ChainID: testPolicyChain,
				EntityID: entity, SessionSigner: &signer, Status: model.SessionPolicyActive, ValidUntil: until,
				Grant: &model.SessionGrantAuthorization{InstallCall: install, AppliedAt: time.Now().UnixMilli(),
					OwnerSignature: ownerSignature, CarrierNonce: big.NewInt(0), Deadline: uint64(until / 1000),
					AppliedUserOpHash: installHash.Hex(), RequiresExecuteUserOp: true}}
			attachDeclaredPermissions(policy, permissions)
			require.NoError(t, StoreSessionPolicy(db, policy))
			preset.SetSessionResolver(func(chain int64, owner, wallet common.Address) (*preset.SessionAuthorization, error) {
				require.EqualValues(t, testPolicyChain, chain)
				require.Equal(t, f.owner, owner)
				require.Equal(t, f.wallet, wallet)
				return &preset.SessionAuthorization{EntityID: entity, SignerKey: sessionKey, WrapExecuteUserOp: true}, nil
			})
			t.Cleanup(func() { preset.SetSessionResolver(nil) })
			previousRPC := rpcConn
			rpcConn = f.chain
			t.Cleanup(func() { rpcConn = previousRPC })

			// A real preceding token transfer, under the same session hooks.
			fundNativeMaxToken(t, f, token)
			beforeToken := f.tokenBalance(t, token)
			require.Equal(t, "100", beforeToken.String())
			_, err = f.session(t, entity, []aa.Call{f.call(token, budgetTokenCall("transfer(address,uint256)", f.owner, big.NewInt(100)))}, nil)
			require.NoError(t, err)
			require.Zero(t, f.tokenBalance(t, token).Sign())
			if tc.remaining != 0 {
				module := aa.NativeTokenLimitModuleAddress()
				arguments, err := aa.PackNativeTokenLimitInstallData(entity, big.NewInt(tc.remaining))
				require.NoError(t, err)
				data := append(crypto.Keccak256([]byte("updateLimits(uint32,uint256)"))[:4], arguments...)
				_, err = f.ownerBatch(t, []aa.Call{f.call(module, data)})
				require.NoError(t, err)
				remaining, err := aa.ReadNativeSpendLimit(f.ctx, f.chain, entity, f.wallet)
				require.NoError(t, err)
				require.Equal(t, big.NewInt(tc.remaining), remaining)
			}
			if tc.deposit {
				fundNativeMaxDeposit(t, f)
			}
			vm, err := NewVMWithData(nil, nil, f.sw, nil)
			require.NoError(t, err)
			vm.TaskOwner = f.owner
			vm.WithDb(db).WithContext(f.ctx).WithLogger(testutil.GetLogger())
			node := &avsproto.TaskNode{Id: "native-max", Name: "nativeMax", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
				TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{
					Destination: f.owner.Hex(), Amount: "MAX", ChainId: testPolicyChain,
				}}}}
			inputs := map[string]interface{}{"aa_sender": f.wallet.Hex(), "aa_salt": new(big.Int).Set(f.salt)}
			balance, err := f.chain.BalanceAt(f.ctx, f.wallet, nil)
			require.NoError(t, err)
			ownerBefore, err := f.chain.BalanceAt(f.ctx, f.owner, nil)
			require.NoError(t, err)
			vm.SetSimulation(true)
			simulated, simErr := vm.RunNodeWithInputs(node, inputs)
			vm.SetSimulation(false)
			step, runErr := vm.RunNodeWithInputs(node, inputs)
			if tc.remaining == 1 {
				require.Error(t, simErr)
				require.Error(t, runErr)
				require.Contains(t, step.Error, preset.NativeCapBelowFeeCode)
				unchanged, err := f.chain.BalanceAt(f.ctx, f.wallet, nil)
				require.NoError(t, err)
				require.Equal(t, balance, unchanged, "the refused native step must send nothing")
				t.Logf("native MAX refused: wallet=%s salt=%s code=%s token send included; native balance unchanged=%s", f.wallet, f.salt, preset.NativeCapBelowFeeCode, unchanged)
				return
			}
			require.NoError(t, simErr)
			require.NoError(t, runErr)
			require.True(t, step.Success, step.Error)
			transfer, metadata := stepMaps(t, step)
			value, ok := new(big.Int).SetString(transfer["value"].(string), 10)
			require.True(t, ok)
			ownerAfter, err := f.chain.BalanceAt(f.ctx, f.owner, nil)
			require.NoError(t, err)
			require.Equal(t, value, new(big.Int).Sub(ownerAfter, ownerBefore), "recipient must receive the reported amount")
			left, err := f.chain.BalanceAt(f.ctx, f.wallet, nil)
			require.NoError(t, err)
			require.Contains(t, metadata, "gasReserve")
			if tc.remaining != 0 {
				require.Positive(t, left.Sign())
				require.Equal(t, preset.NativeReserveLimitedByPermission, metadata["limitedBy"])
				require.Equal(t, left.String(), metadata["leftInWallet"])
			} else {
				require.Zero(t, left.Sign())
				require.NotContains(t, metadata, "leftInWallet")
			}
			if tc.deposit {
				require.Equal(t, balance, value, "the deposit-funded operation must send the entire wallet balance")
				require.Equal(t, "0", metadata["gasReserve"])
			}
			simTransfer, _ := stepMaps(t, simulated)
			estimated, ok := new(big.Int).SetString(simTransfer["value"].(string), 10)
			require.True(t, ok)
			difference := new(big.Int).Abs(new(big.Int).Sub(value, estimated))
			fee, ok := new(big.Int).SetString(simulated.TotalGasCost, 10)
			require.True(t, ok)
			t.Logf("MAX estimate comparison: value=%s estimated=%s difference=%s estimatedMaxGasCost=%s", value, estimated, difference, fee)
			require.LessOrEqual(t, difference.Cmp(new(big.Int).Mul(fee, big.NewInt(2))), 0, "simulation differs by more than two estimated network fees")
			deposit, err := preset.EntryPointDepositV07(f.ctx, f.chain, f.wallet)
			require.NoError(t, err)
			t.Logf("native MAX: case=%s wallet=%s salt=%s tx=%v value=%s estimated=%s left=%s entryPointDeposit=%s metadata=%v",
				tc.name, f.wallet, f.salt, metadata["transactionHash"], value, estimated, left, deposit, metadata)
		})
	}
}

func fundNativeMaxToken(t *testing.T, f *budgetSpike, token common.Address) {
	t.Helper()
	contractABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"transfer","stateMutability":"nonpayable","inputs":[{"type":"address"},{"type":"uint256"}],"outputs":[{"type":"bool"}]}]`))
	require.NoError(t, err)
	auth, err := bind.NewKeyedTransactorWithChainID(f.ownerKey, big.NewInt(testPolicyChain))
	require.NoError(t, err)
	auth.Context = f.ctx
	tx, err := bind.NewBoundContract(token, contractABI, f.chain, f.chain, f.chain).Transact(auth, "transfer", f.wallet, big.NewInt(100))
	require.NoError(t, err)
	receipt, err := bind.WaitMined(f.ctx, f.chain, tx)
	require.NoError(t, err)
	require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status)
	f.readBlock = receipt.BlockNumber
}

func fundNativeMaxDeposit(t *testing.T, f *budgetSpike) {
	t.Helper()
	contractABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"depositTo","stateMutability":"payable","inputs":[{"type":"address"}],"outputs":[]}]`))
	require.NoError(t, err)
	auth, err := bind.NewKeyedTransactorWithChainID(f.ownerKey, big.NewInt(testPolicyChain))
	require.NoError(t, err)
	auth.Context, auth.Value = f.ctx, big.NewInt(1_000_000_000_000_000)
	tx, err := bind.NewBoundContract(preset.EntryPointV07(), contractABI, f.chain, f.chain, f.chain).Transact(auth, "depositTo", f.wallet)
	require.NoError(t, err)
	receipt, err := bind.WaitMined(f.ctx, f.chain, tx)
	require.NoError(t, err)
	require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status)
	f.readBlock = receipt.BlockNumber
}
