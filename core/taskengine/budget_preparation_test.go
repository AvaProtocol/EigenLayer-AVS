package taskengine

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

type budgetPrepareFixture struct {
	n       *Engine
	user    *model.User
	in      SessionPolicyInput
	key     *ecdsa.PrivateKey
	backend *ownerBackendFixture
	calls   []aa.Call
}

func newBudgetPrepareFixture(t *testing.T) *budgetPrepareFixture {
	t.Helper()
	n, _, _, owner, wallet := newPolicyTestEngine(t)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	n.config.WalletBudgetPreparationEnabled = true
	n.config.WalletBudgetDiscoverySigner = crypto.PubkeyToAddress(key.PublicKey).Hex()
	f := &budgetPrepareFixture{n: n, user: &model.User{Address: owner}, key: key, backend: &ownerBackendFixture{}}
	n.budgetOwnerPrepareOverride = func(ctx context.Context, sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int, batch preset.OwnerBatch, l logger.Logger) (*preset.PreparedOwnerUserOp, error) {
		require.True(t, sw.DisableGasSponsorship)
		require.Empty(t, sw.AlchemyPaymasterPolicyID)
		f.calls = append([]aa.Call(nil), batch.Calls...)
		return f.backend.Prepare(ctx, sw, owner, sender, salt, batch, l)
	}
	n.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) {
		return budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
			if *call.To == testTokenAddr && common.BytesToAddress(call.Data[36:68]) == testRouterAddr {
				return budgetWord(123), nil
			}
			return budgetWord(0), nil
		}), nil
	}
	f.in = SessionPolicyInput{Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Budget test", Budget: &WalletBudgetInput{ERC20: []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "1000"}}, Native: &model.NativeSpendCap{Amount: "1000"}, ValidUntil: time.Now().Add(time.Hour).UnixMilli()}, Addition: &PolicyAddition{AllowedActions: testPermissions().AllowedActions}}
	f.in.ApprovalDiscovery = &BudgetApprovalDiscovery{Owner: owner, Wallet: wallet, ChainID: testPolicyChain, Source: "moralis", FetchedAt: time.Now().UnixMilli(), Complete: true, AllowedContracts: []common.Address{testTokenAddr, testRouterAddr}, Candidates: []BudgetApprovalPair{{Token: testTokenAddr, Spender: testRouterAddr}}}
	signBudgetDiscovery(t, key, f.in.ApprovalDiscovery)
	return f
}
func (f *budgetPrepareFixture) prepare(t *testing.T) *BudgetPolicyPreparation {
	t.Helper()
	p, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
	require.NoError(t, err)
	require.NotNil(t, p.BudgetPreparation)
	return p.BudgetPreparation
}

func TestBudgetPrepareStoresExactReviewWithoutInstallingAuthority(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	f.n.config.SmartWallet.DisableGasSponsorship = false
	f.n.config.SmartWallet.AlchemyPaymasterPolicyID = "must-not-use"
	p := f.prepare(t)
	require.Len(t, f.calls, 2)
	require.Equal(t, testTokenAddr, f.calls[0].Target)
	require.Equal(t, selectorApprove, "0x"+common.Bytes2Hex(f.calls[0].Data[:4]))
	require.Zero(t, new(big.Int).SetBytes(f.calls[0].Data[36:]).Sign())
	require.Equal(t, f.in.Wallet, f.calls[1].Target)
	require.Equal(t, []BudgetApprovalReset{{Token: testTokenAddr, Spender: testRouterAddr, Amount: "123"}}, p.ApprovalResets)
	require.Equal(t, "1000", p.Budget.ERC20[0].Remaining)
	require.Empty(t, p.Policy.Grant.OwnerSignature)
	require.False(t, f.n.config.SmartWallet.DisableGasSponsorship)
	require.Equal(t, "must-not-use", f.n.config.SmartWallet.AlchemyPaymasterPolicyID)
	policies, err := ListSessionPolicies(f.n.db, testPolicyChain, f.user.Address)
	require.NoError(t, err)
	require.Empty(t, policies)
	require.Zero(t, f.backend.sent.Load())
	loaded, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, p.UserOpHash, loaded.UserOpHash)
	require.Equal(t, p.MaxFeeWei, loaded.MaxFeeWei)
	require.Equal(t, p.UserOpHash.Hex(), func() string {
		h, e := (*userop.UserOperationV07)(loaded.Operation).GetUserOpHash(preset.EntryPointV07(), big.NewInt(testPolicyChain))
		require.NoError(t, e)
		return h.Hex()
	}())
	f.n.config.OwnerExecutionEnabled = true
	_, err = f.n.SubmitOwnerExecution(context.Background(), f.user, p.ID, make([]byte, 65))
	require.ErrorContains(t, err, "prepared owner execution not found")
	require.Zero(t, f.backend.sent.Load())
	_, _, err = f.n.SubmitSessionPolicy(f.user, f.in, p.Policy.ID, p.Policy.EntityID, p.Policy.Grant.Deadline, make([]byte, 65))
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetHandoffRequiredCode, refusal.Code)
	require.Zero(t, f.backend.sent.Load())
}

func TestBudgetPreparationDiscoveryGateBeforeReadsAndPricing(t *testing.T) {
	for _, name := range []string{"disabled", "missing", "stale", "partial", "forged"} {
		t.Run(name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			switch name {
			case "disabled":
				f.n.config.WalletBudgetPreparationEnabled = false
			case "missing":
				f.in.ApprovalDiscovery = nil
			case "stale":
				f.in.ApprovalDiscovery.FetchedAt = time.Now().Add(-3 * time.Minute).UnixMilli()
				signBudgetDiscovery(t, f.key, f.in.ApprovalDiscovery)
			case "partial":
				f.in.ApprovalDiscovery.Complete = false
				signBudgetDiscovery(t, f.key, f.in.ApprovalDiscovery)
			case "forged":
				f.in.ApprovalDiscovery.Signature[0] ^= 1
			}
			f.n.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) { t.Fatal("must refuse before chain reads"); return nil, nil }
			_, err := f.n.PrepareSessionPolicy(f.user, f.in)
			require.Error(t, err)
			require.Zero(t, f.backend.prepared.Load())
		})
	}
}

func TestBudgetPreparationCannotReadAnotherOwnerWalletOrChain(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	p := f.prepare(t)
	_, err := f.n.GetBudgetPolicyPreparation(&model.User{Address: common.HexToAddress("0xbad")}, f.in.Wallet, testPolicyChain, p.ID)
	require.ErrorIs(t, err, ErrWalletNotOwned)
	_, err = f.n.GetBudgetPolicyPreparation(f.user, common.HexToAddress("0xbad"), testPolicyChain, p.ID)
	require.Error(t, err)
	_, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, 1, p.ID)
	require.Error(t, err)
	p.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.NoError(t, f.n.db.Set(budgetPreparationKey(f.user.Address, p.ID), data))
	_, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.ErrorIs(t, err, ErrSessionPolicyNotFound)
}

func TestBudgetPreparationRefusesFeeShortfallAndChangedQuote(t *testing.T) {
	for _, name := range []string{"fee", "calldata", "nonce", "paymaster", "signature", "gas", "base changed", "read failure"} {
		t.Run(name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			f.n.budgetOwnerPrepareOverride = func(ctx context.Context, sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int, b preset.OwnerBatch, l logger.Logger) (*preset.PreparedOwnerUserOp, error) {
				if name == "fee" {
					return nil, &preset.OwnerFundingError{Required: big.NewInt(1234), Available: big.NewInt(100)}
				}
				p, err := f.backend.Prepare(ctx, sw, owner, sender, salt, b, l)
				require.NoError(t, err)
				switch name {
				case "calldata":
					p.Operation.CallData = []byte{1}
				case "nonce":
					p.Operation.Nonce = big.NewInt(0)
				case "paymaster":
					p.Operation.Paymaster = &owner
				case "signature":
					p.Operation.Signature = []byte{1}
				case "gas":
					p.Operation.CallGasLimit = nil
				case "base changed":
					old, _ := semanticsBudget()
					old.Owner = &owner
					old.Runner = &sender
					old.SessionSigner = &owner
					old.Grant.OwnerSignature = make([]byte, 65)
					old.Grant.InstallCall = []byte{1}
					old.Grant.CarrierNonce = big.NewInt(0)
					require.NoError(t, StoreSessionPolicy(f.n.db, old))
				}
				return p, nil
			}
			if name == "read failure" {
				f.n.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) { return nil, errors.New("down") }
			}
			_, err := f.n.PrepareSessionPolicy(f.user, f.in)
			require.Error(t, err)
			if name == "fee" {
				var e *BudgetPolicyError
				require.ErrorAs(t, err, &e)
				require.Equal(t, BudgetFeeInsufficientCode, e.Code)
				require.Equal(t, "1234", e.Details["neededWei"])
			}
			rows, err := f.n.db.GetByPrefix(budgetPreparationPrefix(f.user.Address))
			require.NoError(t, err)
			require.Empty(t, rows)
			require.Zero(t, f.backend.sent.Load())
		})
	}
}

func TestBudgetPreparationLegacyOutstandingSignatureAndMigration(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		applied, revoked, expired bool
		want                      string
	}{
		{name: "unsigned legacy pending", want: BudgetLegacyGrantPendingCode},
		{name: "revoked but still signable", revoked: true, want: BudgetLegacyGrantPendingCode},
		{name: "expired signature", expired: true},
		{name: "installed migration", applied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			p, _ := semanticsBudget()
			p.Owner = &f.user.Address
			p.Runner = &f.in.Wallet
			p.SessionSigner = &f.user.Address
			p.Sizing = model.BudgetSizingWorkflow
			p.BudgetStartAmounts = nil
			p.BudgetStartNative = nil
			p.BudgetStartedAt = 0
			p.AllowedActions = testPermissions().AllowedActions
			f.in.Budget.ValidUntil = p.ValidUntil
			p.Grant = &model.SessionGrantAuthorization{InstallCall: []byte{1}, CarrierNonce: big.NewInt(0), OwnerSignature: make([]byte, 65), Deadline: uint64(time.Now().Add(time.Minute).Unix())}
			if tc.applied {
				p.Grant.AppliedAt = 1
				signer := crypto.PubkeyToAddress(f.key.PublicKey)
				hooks, err := testPermissions().HooksFor(p.EntityID)
				require.NoError(t, err)
				p.Grant.InstallCall, err = aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: p.EntityID, Signer: signer, Hooks: hooks, Global: true})
				require.NoError(t, err)
				f.n.config.SmartWallet.EthRpcUrl = budgetRegistrationRPC(t).URL
			}
			if tc.revoked {
				p.Status = model.SessionPolicyRevoked
			}
			if tc.expired {
				p.Grant.Deadline = uint64(time.Now().Add(-time.Minute).Unix())
			}
			require.NoError(t, StoreSessionPolicy(f.n.db, p))
			before, err := json.Marshal(p)
			require.NoError(t, err)
			prepared, err := f.n.PrepareSessionPolicy(f.user, f.in)
			if tc.want != "" {
				var e *BudgetPolicyError
				require.ErrorAs(t, err, &e)
				require.Equal(t, tc.want, e.Code)
				require.Zero(t, f.backend.prepared.Load())
			} else {
				require.NoError(t, err)
				require.NotNil(t, prepared.BudgetPreparation)
				if tc.applied {
					require.Len(t, prepared.BudgetPreparation.Retirements, 1)
					require.Len(t, f.calls, 3)
				}
			}
			after, err := f.n.GetSessionPolicyByID(f.user, testPolicyChain, f.in.Wallet, p.ID)
			require.NoError(t, err)
			data, err := json.Marshal(after)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(data))
		})
	}
}

func TestBudgetPreparationCapIsAtomic(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	for i := 0; i < ownerPreparationMaxPerOwner-1; i++ {
		data, err := json.Marshal(BudgetPolicyPreparation{State: ownerSubmissionPrepared, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()})
		require.NoError(t, err)
		require.NoError(t, f.n.db.Set(budgetPreparationKey(f.user.Address, big.NewInt(int64(i)).String()), data))
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.n.PrepareSessionPolicy(f.user, f.in); results <- err }()
	}
	wg.Wait()
	close(results)
	ok, limited := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else {
			var e *BudgetPolicyError
			require.ErrorAs(t, err, &e)
			require.Equal(t, BudgetPreparationLimitCode, e.Code)
			limited++
		}
	}
	require.Equal(t, 1, ok)
	require.Equal(t, 1, limited)
}

func TestBudgetPreparationRejectsCorruptStoredReview(t *testing.T) {
	for _, name := range []string{"hash", "operation", "policy", "fee", "negative gas", "signature"} {
		t.Run(name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			p := f.prepare(t)
			switch name {
			case "hash":
				p.UserOpHash = common.Hash{}
			case "operation":
				p.Operation = nil
			case "policy":
				p.Policy.Grant = nil
			case "fee":
				p.MaxFeeWei = "1"
			case "negative gas":
				p.Operation.MaxFeePerGas = big.NewInt(-1)
			case "signature":
				p.Operation.Signature = []byte{1}
			}
			data, err := json.Marshal(p)
			require.NoError(t, err)
			require.NoError(t, f.n.db.Set(budgetPreparationKey(f.user.Address, p.ID), data))
			_, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
			require.ErrorIs(t, err, ErrBudgetReadUnavailable)
		})
	}
}

// Revoked in storage does not imply the corresponding account registration is gone.
func TestBudgetPreparationRetiresHistoricalAuthorityWithoutTruncating(t *testing.T) {
	for _, count := range []int{2, ownerBatchMaxCalls} {
		t.Run(big.NewInt(int64(count)).String(), func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			f.n.config.SmartWallet.EthRpcUrl = budgetRegistrationRPC(t).URL
			for i := 0; i < count; i++ {
				entity := uint32(i + 1)
				signer := f.user.Address
				hooks, err := testPermissions().HooksFor(entity)
				require.NoError(t, err)
				install, err := aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: entity, Signer: signer, Hooks: hooks, Global: true})
				require.NoError(t, err)
				p := &model.SessionPolicy{ID: "old-" + big.NewInt(int64(i)).String(), Owner: &f.user.Address, Runner: &f.in.Wallet, ChainID: testPolicyChain, EntityID: entity, Status: model.SessionPolicyRevoked, SessionSigner: &signer, Grant: &model.SessionGrantAuthorization{AppliedAt: 1, InstallCall: install}}
				require.NoError(t, StoreSessionPolicy(f.n.db, p))
			}
			p, err := f.n.PrepareSessionPolicy(f.user, f.in)
			if count == ownerBatchMaxCalls {
				var refusal *BudgetPolicyError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, BudgetInvalidInputCode, refusal.Code)
				require.Zero(t, f.backend.prepared.Load())
			} else {
				require.NoError(t, err)
				require.Len(t, p.BudgetPreparation.Retirements, count)
				require.Len(t, f.calls, count+2)
			}
			old, err := ListSessionPolicies(f.n.db, testPolicyChain, f.user.Address)
			require.NoError(t, err)
			require.Len(t, old, count)
			for _, row := range old {
				require.Equal(t, model.SessionPolicyRevoked, row.Status)
				require.Zero(t, row.Grant.TornDownAt)
			}
			require.Zero(t, f.backend.sent.Load())
		})
	}
}

func TestBudgetPreparationRefusesAttestationForSmallerTargetSet(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	f.in.ApprovalDiscovery.AllowedContracts = []common.Address{testTokenAddr}
	signBudgetDiscovery(t, f.key, f.in.ApprovalDiscovery)
	_, err := f.n.PrepareSessionPolicy(f.user, f.in)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetDiscoveryRequiredCode, refusal.Code)
	require.Zero(t, f.backend.prepared.Load())
}
