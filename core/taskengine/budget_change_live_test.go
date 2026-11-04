//go:build integration

package taskengine

import (
	"context"
	"fmt"
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
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// Real pricing, owner signature, bundler, chain reconciliation and storage.
// Discovery uses a throwaway service key over the fixture's known pairs. This
// does NOT qualify the separate Moralis completeness/lag release canary.
func TestBudgetOwnerHandoff_Sepolia(t *testing.T) {
	if testing.Short() || os.Getenv("WALLET_BUDGET_SIGNING") != "1" {
		t.Skip("set WALLET_BUDGET_SIGNING=1 for isolated Sepolia budget handoff tests")
	}
	t.Setenv("WALLET_BUDGET_FEASIBILITY", "1")
	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "counterfactual_install", true: "legacy_migration_two_resets"}[migration], func(t *testing.T) {
			f := newBudgetSpike(t, migration)
			t.Cleanup(func() { f.readBlock = nil })
			t.Cleanup(func() { resetLiveBudgetPoolApproval(t, f) })
			sessionKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			f.sessionKey = sessionKey
			serviceKey, err := crypto.GenerateKey()
			require.NoError(t, err)
			cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
			require.NoError(t, err)
			cfg.SmartWallet = f.sw
			cfg.Chains = nil
			cfg.SessionSignerPrivateKey = sessionKey
			cfg.WalletBudgetPreparationEnabled = true
			cfg.WalletBudgetSigningEnabled = true
			cfg.WalletBudgetDiscoverySigner = crypto.PubkeyToAddress(serviceKey.PublicKey).Hex()
			cfg.WalletBudgetMaxFeeWei = budgetDefaultMaxFeeWei
			db, err := storage.NewWithPath(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			n := New(db, cfg, nil, testutil.GetLogger())
			n.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) { return f.chain, nil }
			factory := aa.MAv2FactoryAddress()
			require.NoError(t, StoreWallet(db, testPolicyChain, f.owner, &model.SmartWallet{Owner: &f.owner, Address: &f.wallet, Factory: &factory, Salt: f.salt}))
			user := &model.User{Address: f.owner}
			until := time.Now().Add(time.Hour).UnixMilli()
			pool := budgetContracts["aavePool"][testPolicyChain]
			actions := append([]model.AllowedAction{}, testPermissions().AllowedActions...)
			actions = append(actions, model.AllowedAction{Target: &pool, Selectors: []string{functionSelector("repay(address,uint256,uint256,address)")}})
			in := SessionPolicyInput{Wallet: f.wallet, ChainID: testPolicyChain, AgentLabel: "Isolated live budget", Budget: &WalletBudgetInput{ValidUntil: until, ERC20: []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "1000"}}, Native: &model.NativeSpendCap{Amount: "6000000000000000"}}, Addition: &PolicyAddition{AllowedActions: actions}}
			refreshDiscovery := func() {
				in.ApprovalDiscovery = &BudgetApprovalDiscovery{Owner: f.owner, Wallet: f.wallet, ChainID: testPolicyChain, Source: "moralis", FetchedAt: time.Now().UnixMilli(), Complete: true,
					AllowedContracts: []common.Address{testTokenAddr, testRouterAddr, pool}, Candidates: []BudgetApprovalPair{{Token: testTokenAddr, Spender: testRouterAddr}, {Token: testTokenAddr, Spender: pool}}}
				signBudgetDiscovery(t, serviceKey, in.ApprovalDiscovery)
			}
			if migration {
				perms := testPermissions()
				perms.SpendCap.Amount = "1000"
				perms.ValidUntilMs = until
				perms.NativeSpendCap = &model.NativeSpendCap{Amount: "6000000000000000"}
				hooks, hookErr := perms.HooksFor(1)
				require.NoError(t, hookErr)
				signer := crypto.PubkeyToAddress(sessionKey.PublicKey)
				install, packErr := aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: 1, Signer: signer, Hooks: hooks, Global: true})
				require.NoError(t, packErr)
				unlimited := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
				hash, sendErr := f.ownerBatch(t, []aa.Call{f.call(f.wallet, install), f.call(testTokenAddr, budgetTokenCall("approve(address,uint256)", testRouterAddr, unlimited)), f.call(testTokenAddr, budgetTokenCall("approve(address,uint256)", pool, big.NewInt(123)))})
				require.NoError(t, sendErr)
				legacy := &model.SessionPolicy{ID: "live-legacy-budget-migration", Owner: &f.owner, Runner: &f.wallet, ChainID: testPolicyChain, EntityID: 1, SessionSigner: &signer, Status: model.SessionPolicyActive, ValidUntil: until,
					Grant: &model.SessionGrantAuthorization{AppliedAt: time.Now().UnixMilli(), AppliedUserOpHash: hash.Hex(), CarrierNonce: big.NewInt(0), OwnerSignature: make([]byte, 65), InstallCall: install, RequiresExecuteUserOp: true}}
				attachDeclaredPermissions(legacy, perms)
				require.NoError(t, StoreSessionPolicy(db, legacy))
				t.Logf("legacy install and two approvals: wallet=%s tx=%s userOp=%s", f.wallet, f.lastReceipt.TxHash, hash)
			} else {
				refreshDiscovery()
				_, prepareErr := n.PrepareSessionPolicyWithContext(f.ctx, user, in)
				var refusal *BudgetPolicyError
				require.ErrorAs(t, prepareErr, &refusal)
				require.Equal(t, BudgetFeeInsufficientCode, refusal.Code)
				// Fund without deploying: the first owner operation must exercise
				// the counterfactual factory path, not a setup transaction.
				fundBudgetLiveWallet(t, f, big.NewInt(20_000_000_000_000_000))
				t.Cleanup(func() { f.readBlock = nil; f.recoverFunds(t) })
				code, codeErr := f.chain.CodeAt(f.ctx, f.wallet, nil)
				require.NoError(t, codeErr)
				require.Empty(t, code)
			}
			refreshDiscovery()
			prepared, err := n.PrepareSessionPolicyWithContext(f.ctx, user, in)
			require.NoError(t, err)
			p := prepared.BudgetPreparation
			if migration {
				require.Len(t, p.ApprovalResets, 2)
				require.Len(t, p.Retirements, 1)
			}
			sig, err := crypto.Sign(accounts.TextHash(p.UserOpHash.Bytes()), f.ownerKey)
			require.NoError(t, err)
			result, err := n.SubmitBudgetPolicyPreparation(f.ctx, user, f.wallet, testPolicyChain, p.ID, sig)
			if err != nil && p.Base != nil {
				view, viewErr := n.budgetBackend().View(f.ctx, testPolicyChain, nil)
				if viewErr == nil {
					_, detail := aa.ReadERC20SpendLimit(f.ctx, view, p.Base.EntityID, testTokenAddr, f.wallet)
					t.Logf("final read diagnostic block=%d: %s", view.Block().Number, strings.ReplaceAll(fmt.Sprint(detail), f.sw.EthRpcUrl, "[RPC]"))
					view.Close()
				}
			}
			require.NoError(t, err)
			require.Equal(t, ownerSubmissionSubmitted, result.State, result.Reason)
			waitCtx, cancel := context.WithTimeout(f.ctx, 3*time.Minute)
			defer cancel()
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for result.State != budgetChangeActive {
				select {
				case <-waitCtx.Done():
					t.Fatalf("budget not active: state=%s reason=%s hash=%s", result.State, result.Reason, p.UserOpHash)
				case <-ticker.C:
					result, err = n.GetBudgetPolicyPreparationWithContext(waitCtx, user, f.wallet, testPolicyChain, p.ID)
					require.NoError(t, err)
				}
			}
			require.NoError(t, budgetHoldPresent(db, testPolicyChain, f.owner, f.wallet))
			active, err := ActiveSessionPolicyForWallet(db, testPolicyChain, f.owner, f.wallet)
			require.NoError(t, err)
			require.NoError(t, active.Validate())
			for _, reset := range p.ApprovalResets {
				data := budgetTokenCall("allowance(address,address)", f.wallet, new(big.Int).SetBytes(reset.Spender.Bytes()))
				value, readErr := f.chain.CallContract(f.ctx, ethereum.CallMsg{To: &reset.Token, Data: data}, new(big.Int).SetUint64(result.InclusionBlock.Number))
				require.NoError(t, readErr)
				require.Zero(t, new(big.Int).SetBytes(value).Sign())
			}
			t.Logf("budget active: wallet=%s salt=%s preparation=%s tx=%s userOp=%s finalReadBlock=%d inclusionBlock=%d maxFeeWei=%s", f.wallet, f.salt, p.ID, result.TransactionHash, p.UserOpHash, result.FinalBlock.Number, result.InclusionBlock.Number, p.MaxFeeWei)
			// Exercise the ordinary session send after activation. A zero approve
			// moves no token, but the native budget must pay for its network fee.
			before, readErr := aa.ReadNativeSpendLimit(f.ctx, f.chain, active.EntityID, f.wallet)
			require.NoError(t, readErr)
			resolver := newSessionResolver(db, configuredSessionSigner(cfg), nil, nil)
			session, resolveErr := resolver(testPolicyChain, f.owner, f.wallet)
			require.NoError(t, resolveErr)
			require.True(t, session.WrapExecuteUserOp)
			require.False(t, session.Deferred())
			preset.SetSessionSendGuard(func(ctx context.Context, chain int64, owner, wallet common.Address) (*preset.SessionSendLease, error) {
				lease, guardErr := n.guardBudgetSessionSend(ctx, chain, owner, wallet)
				if guardErr != nil {
					return nil, guardErr
				}
				record := lease.BeforeBroadcast
				lease.BeforeBroadcast = func(ctx context.Context, op *userop.UserOperationV07) error {
					require.LessOrEqual(t, preset.MaxOwnerGasCost(op).Cmp(big.NewInt(5_000_000_000_000_000)), 0)
					return record(ctx, op)
				}
				return lease, nil
			})
			t.Cleanup(func() { preset.SetSessionSendGuard(nil) })
			data, packErr := aa.PackExecuteBatchMAv2([]aa.Call{f.call(testTokenAddr, budgetTokenCall("approve(address,uint256)", testRouterAddr, big.NewInt(0)))})
			require.NoError(t, packErr)
			_, sessionReceipt, sendErr := preset.SendUserOpMAv2(f.sw, f.owner, data, &f.wallet, f.salt, session, testutil.GetLogger())
			require.NoError(t, sendErr)
			require.NotNil(t, sessionReceipt)
			require.EqualValues(t, types.ReceiptStatusSuccessful, sessionReceipt.Status)
			after, readErr := aa.ReadNativeSpendLimit(f.ctx, budgetBlockReader{f.chain, sessionReceipt.BlockNumber}, active.EntityID, f.wallet)
			require.NoError(t, readErr)
			require.Negative(t, after.Cmp(before), "one ordinary session operation must consume native budget for gas")
			t.Logf("post-activation session: tx=%s nativeBefore=%s nativeAfter=%s", sessionReceipt.TxHash, before, after)
		})
	}
}

func resetLiveBudgetPoolApproval(t *testing.T, f *budgetSpike) {
	t.Helper()
	pool := budgetContracts["aavePool"][testPolicyChain]
	data := budgetTokenCall("allowance(address,address)", f.wallet, new(big.Int).SetBytes(pool.Bytes()))
	out, err := f.chain.CallContract(f.ctx, ethereum.CallMsg{To: &testTokenAddr, Data: data}, nil)
	require.NoError(t, err)
	require.Len(t, out, 32)
	if new(big.Int).SetBytes(out).Sign() > 0 {
		_, err = f.ownerBatch(t, []aa.Call{f.call(testTokenAddr, budgetTokenCall("approve(address,uint256)", pool, big.NewInt(0)))})
		require.NoError(t, err, "pool approval remains on fixture %s, salt %s", f.wallet, f.salt)
	}
}

// Recovery is limited to an explicitly named fixture derived from the existing
// test owner's key. An explicit, capped top-up may pay for interrupted cleanup.
func TestBudgetHandoffRecoverFixture_Sepolia(t *testing.T) {
	if testing.Short() || os.Getenv("WALLET_BUDGET_SIGNING") != "1" || os.Getenv("WALLET_BUDGET_SALT") == "" {
		t.Skip("set WALLET_BUDGET_SIGNING=1 and the interrupted fixture's WALLET_BUDGET_SALT")
	}
	t.Setenv("WALLET_BUDGET_FEASIBILITY", "1")
	f := newBudgetSpike(t, false)
	code, err := f.chain.CodeAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code)
	if raw := os.Getenv("WALLET_BUDGET_RECOVERY_FUND_WEI"); raw != "" {
		amount, ok := new(big.Int).SetString(raw, 10)
		require.True(t, ok)
		require.Positive(t, amount.Sign())
		require.LessOrEqual(t, amount.Cmp(big.NewInt(20_000_000_000_000_000)), 0)
		fundBudgetLiveWallet(t, f, amount)
	}
	resetLiveBudgetPoolApproval(t, f)
	f.readBlock = nil
	f.recoverFunds(t)
}

func fundBudgetLiveWallet(t *testing.T, f *budgetSpike, amount *big.Int) {
	t.Helper()
	auth, err := bind.NewKeyedTransactorWithChainID(f.ownerKey, big.NewInt(testPolicyChain))
	require.NoError(t, err)
	auth.Context, auth.Value = f.ctx, amount
	code, err := f.chain.CodeAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	if len(code) == 0 {
		// Native transfer to a counterfactual address; bind's contract-call
		// estimation otherwise rejects the known-empty destination.
		estimate, estimateErr := f.chain.EstimateGas(f.ctx, ethereum.CallMsg{From: f.owner, To: &f.wallet, Value: amount})
		require.NoError(t, estimateErr)
		auth.GasLimit = estimate + estimate/5
	}
	tx, err := bind.NewBoundContract(f.wallet, abi.ABI{}, f.chain, f.chain, f.chain).RawTransact(auth, nil)
	require.NoError(t, err)
	receipt, err := bind.WaitMined(f.ctx, f.chain, tx)
	require.NoError(t, err)
	t.Logf("fixture funding: wallet=%s salt=%s tx=%s codeBytes=%d gasLimit=%d gasUsed=%d", f.wallet, f.salt, tx.Hash(), len(code), tx.Gas(), receipt.GasUsed)
	require.EqualValues(t, types.ReceiptStatusSuccessful, receipt.Status)
}
