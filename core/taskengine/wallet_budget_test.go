package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type budgetCaller func(ethereum.CallMsg) ([]byte, error)

func (f budgetCaller) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return f(call)
}

type budgetContextCaller func(context.Context, ethereum.CallMsg) ([]byte, error)

func (f budgetContextCaller) CallContract(ctx context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return f(ctx, call)
}
func budgetWord(n int64) []byte { return common.LeftPadBytes(big.NewInt(n).Bytes(), 32) }

func budgetPolicy() *model.SessionPolicy {
	owner, runner, token := common.HexToAddress("0x1234"), common.HexToAddress("0x5678"), testTokenAddr
	return &model.SessionPolicy{ID: "01budgettest", ChainID: testPolicyChain, Owner: &owner, Runner: &runner, EntityID: 1,
		Status: model.SessionPolicyActive, ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
		ERC20SpendCap: &model.ERC20SpendCap{Token: &token, Amount: "100", GrantedCap: "500"}, NativeSpendCap: &model.NativeSpendCap{Amount: "200", GrantedCap: "600"},
		Grant: &model.SessionGrantAuthorization{AppliedAt: 1}}
}

func TestWalletBudgetFieldsAndFailures(t *testing.T) {
	p := budgetPolicy()
	caller := budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
		if *call.To == aa.NativeTokenLimitModuleAddress() {
			return budgetWord(150), nil
		}
		return budgetWord(380), nil
	})
	now := time.Unix(1000, 0)
	b, err := ReadWalletBudget(context.Background(), p, nil, caller, now)
	require.NoError(t, err)
	require.Equal(t, model.BudgetSizingWorkflow, b.Sizing)
	require.Equal(t, p.ID, b.PolicyID)
	require.Equal(t, p.ChainID, b.ChainID)
	require.Equal(t, p.ValidUntil, b.ValidUntil)
	require.Equal(t, now.UnixMilli(), b.ReadAt)
	require.Equal(t, BudgetTokenRow{Token: testTokenAddr, Amount: "500", Remaining: "380", Used: "120"}, b.ERC20[0])
	require.Equal(t, &BudgetNativeRow{Amount: "600", Remaining: "150", Used: "450"}, b.Native)
	require.Empty(t, b.Native.GasWei)
	for _, failNative := range []bool{false, true} {
		_, err = ReadWalletBudget(context.Background(), p, nil, budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
			if (*call.To == aa.NativeTokenLimitModuleAddress()) == failNative {
				return nil, errors.New("RPC down")
			}
			return budgetWord(100), nil
		}), now)
		require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	}
	p.Status = model.SessionPolicyPending
	b, err = ReadWalletBudget(context.Background(), p, nil, nil, now)
	require.NoError(t, err)
	require.Equal(t, "500", b.ERC20[0].Remaining)
	require.Equal(t, "600", b.Native.Remaining)
	require.Equal(t, model.SessionPolicyPending, b.Status)
	p.Status = model.SessionPolicyActive
	p.ERC20SpendCaps = []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "0", GrantedCap: "500"}}
	b, err = ReadWalletBudget(context.Background(), p, nil, budgetCaller(func(ethereum.CallMsg) ([]byte, error) { return budgetWord(0), nil }), now)
	require.NoError(t, err)
	require.Equal(t, "0", b.ERC20[0].Remaining)
	require.Equal(t, "500", b.ERC20[0].Used)
}

func TestBudgetAmountsBoundaryAndCarriedTotals(t *testing.T) {
	threshold := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	for _, delta := range []int64{-1, 0, 1} {
		amount := new(big.Int).Add(threshold, big.NewInt(delta))
		_, _, unlimited, err := budgetAmounts(amount.String(), big.NewInt(0))
		require.NoError(t, err)
		require.Equal(t, delta >= 0, unlimited)
	}
	max256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	_, _, unlimited, err := budgetAmounts(max256.String(), max256)
	require.NoError(t, err)
	require.True(t, unlimited, "the contract threshold also includes uint256 max")
	_, used, _, err := budgetAmounts("10", big.NewInt(20))
	require.NoError(t, err)
	require.Equal(t, "0", used)
	p := budgetPolicy()
	p.Sizing = model.BudgetSizingBudget
	p.BudgetStartedAt = 123
	p.BudgetStartAmounts = []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "1000"}}
	p.BudgetStartNative = &model.NativeSpendCap{Amount: "2000"}
	b, err := ReadWalletBudget(context.Background(), p, nil, budgetCaller(func(ethereum.CallMsg) ([]byte, error) { return budgetWord(400), nil }), time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(123), b.StartedAt)
	require.Equal(t, "1000", b.ERC20[0].Amount)
	require.Equal(t, "600", b.ERC20[0].Used)
	p.BudgetStartAmounts = nil
	_, err = ReadWalletBudget(context.Background(), p, nil, nil, time.Now())
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
}

func TestBudgetUnmeteredClassification(t *testing.T) {
	p := budgetPolicy()
	pool, wrapped, router, unknown := budgetContracts["aavePool"][p.ChainID], budgetContracts["wrappedNative"][p.ChainID], budgetContracts["swapRouter"][p.ChainID], common.HexToAddress("0x9999")
	p.AllowedActions = []model.AllowedAction{
		{Target: &testTokenAddr, Selectors: []string{"0xa9059cbb", "0x095ea7b3"}},
		{Target: &pool, Selectors: []string{functionSelector("supply(address,uint256,address,uint16)"), functionSelector("withdraw(address,uint256,address)"), functionSelector("borrow(address,uint256,uint256,uint16,address)")}},
		{Target: &wrapped, Selectors: []string{functionSelector("deposit()"), functionSelector("withdraw(uint256)")}},
		{Target: &router, Selectors: []string{"0x04e45aaf"}},
		{Target: &unknown, Selectors: []string{functionSelector("withdraw(address,uint256,address)")}},
	}
	rows := classifyUnmeteredPermissions(p)
	require.Len(t, rows, 4)
	require.Equal(t, UnmeteredAaveWithdraw, rows[0].Kind)
	require.Equal(t, UnmeteredAaveBorrow, rows[1].Kind)
	require.Equal(t, UnmeteredWrappedNative, rows[2].Kind)
	require.Equal(t, &wrapped, rows[2].Token)
	require.Len(t, rows[2].Selectors, 2)
	require.Equal(t, UnmeteredOther, rows[3].Kind)
	require.False(t, rows[3].Added)
}

func registrationReply(registered bool) []byte {
	flag := int64(0)
	if registered {
		flag = 5
	}
	var out []byte
	for _, n := range []int64{32, flag, 128, 160, 192, 0, 0, 0} {
		out = append(out, budgetWord(n)...)
	}
	return out
}

func TestBudgetRetirementStatesNeverBlockAmounts(t *testing.T) {
	current := budgetPolicy()
	current.Status = model.SessionPolicyPending
	retired := budgetPolicy()
	retired.EntityID = 7
	retired.Status = model.SessionPolicyRevoked
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: 7, Signer: common.HexToAddress("0xabcd"), Selectors: [][4]byte{{1}}})
	require.NoError(t, err)
	retired.Grant.InstallCall = install
	for _, tc := range []struct {
		name                            string
		registered, residual, readError bool
		want                            RetirementState
	}{
		{"registered", true, false, false, RetirementRegistered},
		{"residual", false, true, false, RetirementResidual},
		{"read failed", false, false, true, RetirementUnverified},
		{"clean", false, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
				if *call.To == *retired.Runner {
					if tc.readError {
						return nil, errors.New("RPC down")
					}
					return registrationReply(tc.registered), nil
				}
				if tc.residual {
					return budgetWord(1), nil
				}
				return budgetWord(0), nil
			})
			b, err := ReadWalletBudget(context.Background(), current, []*model.SessionPolicy{retired}, caller, time.Now())
			require.NoError(t, err)
			if tc.want == "" {
				require.Empty(t, b.Retirements)
				return
			}
			require.Len(t, b.Retirements, 1)
			require.Equal(t, tc.want, b.Retirements[0].State)
			if tc.want == RetirementRegistered {
				require.NotEmpty(t, b.Retirements[0].ERC20)
			} else {
				require.Empty(t, b.Retirements[0].ERC20)
			}
		})
	}
}

func TestBudgetEntityAllocationNeverReusesRetiredOrWraps(t *testing.T) {
	_, db, _, owner, wallet := newPolicyTestEngine(t)
	p := budgetPolicy()
	p.Owner = &owner
	p.Runner = &wallet
	p.Status = model.SessionPolicyRevoked
	p.EntityID = 71
	require.NoError(t, StoreSessionPolicy(db, p))
	next, err := NextSessionEntityID(db, testPolicyChain, owner, wallet)
	require.NoError(t, err)
	require.EqualValues(t, 72, next)
	p.EntityID = math.MaxUint32
	require.NoError(t, StoreSessionPolicy(db, p))
	_, err = NextSessionEntityID(db, testPolicyChain, owner, wallet)
	require.Error(t, err)
	p.EntityID = math.MaxUint32 - 1
	require.NoError(t, StoreSessionPolicy(db, p))
	probes := 0
	_, err = NextFreeSessionEntityID(context.Background(), db, testPolicyChain, owner, wallet,
		func(context.Context, common.Address, uint32) (bool, error) { probes++; return true, nil })
	require.Error(t, err)
	require.Equal(t, 1, probes, "the walk must never overflow into entity zero")
}

func TestBudgetRefusalDetailsAndWrappedExpiry(t *testing.T) {
	for _, err := range []*StructuredError{NewBudgetUsedUpError(testTokenAddr, big.NewInt(100), big.NewInt(30)), NewBudgetEndedError(123456)} {
		step := &avsproto.Execution_Step{}
		finalizeStep(step, false, fmt.Errorf("send: %w", err), "", "")
		require.Equal(t, err.Code, step.ErrorCode)
		require.NotNil(t, step.ErrorDetails)
		if err.Code == avsproto.ErrorCode_BUDGET_ENDED {
			require.EqualValues(t, 123456, step.ErrorDetails.AsMap()["validUntil"])
		} else {
			require.Equal(t, err.Details, step.ErrorDetails.AsMap())
		}
	}
	err := fmt.Errorf("send: %s: expired; grant again to keep executing", SessionPolicyExpiredCode)
	require.Equal(t, avsproto.ErrorCode_SESSION_POLICY_EXPIRED, GetErrorCode(err))
	step := &avsproto.Execution_Step{}
	finalizeStep(step, false, nil, err.Error(), "")
	require.Equal(t, avsproto.ErrorCode_SESSION_POLICY_EXPIRED, step.ErrorCode)
	require.Contains(t, step.Error, "grant again")
	explicit := NewStructuredError(avsproto.ErrorCode_INVALID_ADDRESS, err.Error())
	require.Equal(t, avsproto.ErrorCode_INVALID_ADDRESS, GetErrorCode(fmt.Errorf("wrapped: %w", explicit)), "text cannot override an explicit code")
	large := new(big.Int).Lsh(big.NewInt(1), 200)
	finalizeStep(step, false, NewStructuredError(avsproto.ErrorCode_BUDGET_USED_UP, "refused", map[string]interface{}{
		"needed": large, "targets": []common.Address{testTokenAddr}, "remaining": "10",
	}), "", "")
	require.Equal(t, large.String(), step.ErrorDetails.AsMap()["needed"])
	require.Contains(t, step.ErrorDetails.AsMap()["targets"], testTokenAddr.Hex())
	require.Equal(t, "10", step.ErrorDetails.AsMap()["remaining"])
	finalizeStep(step, true, nil, "", "")
	require.Nil(t, step.ErrorDetails, "a reused step cannot retain stale refusal details")
}

func TestWalletBudgetReleasesAuthorityLockBeforeRPC(t *testing.T) {
	engine, db, _, owner, wallet := newPolicyTestEngine(t)
	p := budgetPolicy()
	p.Owner, p.Runner, p.NativeSpendCap = &owner, &wallet, nil
	p.SessionSigner = &owner
	p.Grant = &model.SessionGrantAuthorization{InstallCall: []byte{1}, OwnerSignature: []byte{1}, CarrierNonce: big.NewInt(1), AppliedAt: 1}
	require.NoError(t, StoreSessionPolicy(db, p))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID interface{} `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x" + common.Bytes2Hex(budgetWord(380))})
	}))
	defer rpc.Close()
	defer unblock()
	engine.config.SmartWallet.EthRpcUrl = rpc.URL
	type result struct {
		budget *WalletBudget
		err    error
	}
	done := make(chan result, 1)
	go func() {
		b, err := engine.GetWalletBudget(context.Background(), &model.User{Address: owner}, testPolicyChain, wallet)
		done <- result{b, err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("budget RPC did not start")
	}
	changed := make(chan error, 1)
	go func() {
		lock := sessionAuthorityLock(testPolicyChain, owner, wallet)
		lock.Lock()
		defer lock.Unlock()
		p.ERC20SpendCap.GrantedCap = "900"
		changed <- StoreSessionPolicy(db, p)
	}()
	select {
	case err := <-changed:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("authority writer blocked on the budget RPC")
	}
	unblock()
	got := <-done
	require.NoError(t, got.err)
	require.Equal(t, "500", got.budget.ERC20[0].Amount, "the request keeps its detached metadata snapshot")
}

func TestWalletBudgetRetirementDeadlineDoesNotBlockAmounts(t *testing.T) {
	current, retired := budgetPolicy(), budgetPolicy()
	retired.EntityID, retired.Status = 7, model.SessionPolicyRevoked
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	amountCalls, retirementCalls := 0, 0
	caller := budgetContextCaller(func(callCtx context.Context, call ethereum.CallMsg) ([]byte, error) {
		deadline, ok := callCtx.Deadline()
		require.True(t, ok)
		if *call.To != *retired.Runner {
			amountCalls++
			require.Greater(t, time.Until(deadline), budgetRetirementReadTimeout)
			return budgetWord(100), nil
		}
		retirementCalls++
		require.Equal(t, 2, amountCalls, "required balances are read before optional retirement health")
		require.LessOrEqual(t, time.Until(deadline), budgetRetirementReadTimeout)
		cancel()
		return nil, context.DeadlineExceeded
	})
	budget, err := ReadWalletBudget(ctx, current, []*model.SessionPolicy{retired, retired}, caller, time.Now())
	require.NoError(t, err)
	require.Equal(t, "100", budget.ERC20[0].Remaining)
	require.Equal(t, "100", budget.Native.Remaining)
	require.Equal(t, 1, retirementCalls, "no more RPCs after the retirement context ends")
	require.Len(t, budget.Retirements, 2)
	for _, row := range budget.Retirements {
		require.Equal(t, RetirementUnverified, row.State)
	}
}

// Stored synthetic entities in the supersede unit test are registered by this
// RPC fixture; an empty eth_call reply is no longer accepted as a clean entity.
func budgetRegistrationRPC(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "eth_call", req.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x" + common.Bytes2Hex(registrationReply(true))})
	}))
	t.Cleanup(server.Close)
	return server
}
