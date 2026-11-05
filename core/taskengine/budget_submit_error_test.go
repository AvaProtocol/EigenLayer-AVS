package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

func TestBudgetPreparationPreservesOwnerDelegateRefusal(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	delegate := common.HexToAddress("0x63c0c19a282a1B52b07dD5a65b58948A07DAE32B")
	f.n.budgetOwnerPrepareOverride = func(context.Context, *config.SmartWalletConfig, common.Address, common.Address, *big.Int, preset.OwnerBatch, logger.Logger) (*preset.PreparedOwnerUserOp, error) {
		return nil, &preset.OwnerDelegationError{Delegate: delegate, Cause: config.ErrEOADelegationMissing}
	}
	_, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
	require.ErrorIs(t, err, ErrEOADelegationMissing)
	var refusal *preset.OwnerDelegationError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, delegate, refusal.Delegate)
	require.ErrorAs(t, ownerPreconditionError(err, "owner preparation: %v"), &refusal)
}

func TestBudgetPreparationOwnerRPCFailureIsReadUnavailable(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	var sawChainID atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method == "eth_chainId" {
			sawChainID.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"error": map[string]any{"code": -32000, "message": "private RPC endpoint is behind block 123"},
		}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	// Run the actual preset/account inspector, not the fixture's priced op.
	f.n.budgetOwnerPrepareOverride = func(ctx context.Context, sw *config.SmartWalletConfig, owner, wallet common.Address, salt *big.Int, batch preset.OwnerBatch, l logger.Logger) (*preset.PreparedOwnerUserOp, error) {
		local := *sw
		local.EthRpcUrl = server.URL
		return preset.PrepareSelfFundedOwnerBatch(ctx, &local, owner, wallet, salt, batch, l)
	}
	_, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.ErrorContains(t, err, "reading owner execution chain ID")
	require.ErrorContains(t, err, "private RPC endpoint is behind block 123")
	require.True(t, sawChainID.Load(), "owner inspector never read the RPC")
}

func TestBudgetPreparationOwnerConfigurationFailureKeepsItsClassification(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	cause := fmt.Errorf("%w: SMA-7702 runtime pin is not configured", config.ErrSMA7702Unavailable)
	f.n.budgetOwnerPrepareOverride = func(context.Context, *config.SmartWalletConfig, common.Address, common.Address, *big.Int, preset.OwnerBatch, logger.Logger) (*preset.PreparedOwnerUserOp, error) {
		return nil, cause
	}
	_, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
	require.ErrorIs(t, err, config.ErrSMA7702Unavailable)
	require.NotErrorIs(t, err, ErrBudgetReadUnavailable)
	require.ErrorIs(t, err, cause)
}

func TestBudgetSubmitCheckFailureProvesUnsentAndKeepsCause(t *testing.T) {
	f, p, backend, signature := newBudgetChangeTest(t)
	rpcFailure := errors.New("RPC node has not reached pinned block")
	backend.validationError = rpcFailure
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.ErrorIs(t, err, rpcFailure)
	var refusal *BudgetSubmitError
	require.ErrorAs(t, err, &refusal)
	require.False(t, refusal.SubmissionStarted)
	require.Zero(t, backend.sent.Load())
	stored, err := f.n.loadBudgetChange(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionPrepared, stored.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	backend.validationError = nil
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
	require.NoError(t, err)
	require.EqualValues(t, 1, backend.sent.Load())
}

func TestBudgetSubmitFinalChecksKeepCauseAndProveUnsent(t *testing.T) {
	cause := errors.New("injected RPC failure at final snapshot")
	for _, stage := range []string{"snapshot", "authority", "canonical"} {
		t.Run(stage, func(t *testing.T) {
			f, p, backend, signature := newBudgetChangeTest(t)
			switch stage {
			case "snapshot":
				backend.viewError = cause
			case "authority":
				backend.authorityError = fmt.Errorf("%w: authority inventory: %w", ErrBudgetReadUnavailable, cause)
			case "canonical":
				backend.canonicalError = fmt.Errorf("%w: canonical snapshot: %w", ErrBudgetReadUnavailable, cause)
			}
			_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
			require.ErrorIs(t, err, ErrBudgetReadUnavailable)
			require.ErrorIs(t, err, cause)
			var refusal *BudgetSubmitError
			require.ErrorAs(t, err, &refusal)
			require.False(t, refusal.SubmissionStarted)
			require.Zero(t, backend.sent.Load())
		})
	}
}

func TestBudgetSubmitMarkerErrorsNeverProveUnsent(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			f, p, backend, signature := newBudgetChangeTest(t)
			f.n.db = &budgetWriteFailure{Storage: f.n.db, failAt: failAt}
			_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
			var failure *BudgetSubmitError
			require.ErrorAs(t, err, &failure)
			require.True(t, failure.SubmissionStarted)
			require.EqualValues(t, failAt-1, backend.sent.Load())
		})
	}
}

func TestBudgetSubmitPreservesOwnerDelegateRefusal(t *testing.T) {
	f, p, backend, signature := newBudgetChangeTest(t)
	delegate := common.HexToAddress("0x63c0c19a282a1B52b07dD5a65b58948A07DAE32B")
	backend.validationError = &preset.OwnerDelegationError{Delegate: delegate, Cause: config.ErrEOADelegationMissing}
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
	require.ErrorIs(t, err, ErrEOADelegationMissing)
	var refusal *preset.OwnerDelegationError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, delegate, refusal.Delegate)
	require.Zero(t, backend.sent.Load())
}

func TestBudgetOwnerRefusalClassificationSurvivesPrepareAndSubmit(t *testing.T) {
	for _, cause := range []error{preset.ErrOwnerAccountUnsupported, preset.ErrOwnerConfiguration, config.ErrEOA7702ExecuteDisabled} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			f.n.budgetOwnerPrepareOverride = func(context.Context, *config.SmartWalletConfig, common.Address, common.Address, *big.Int, preset.OwnerBatch, logger.Logger) (*preset.PreparedOwnerUserOp, error) {
				return nil, cause
			}
			_, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
			require.ErrorIs(t, err, cause)
			require.NotErrorIs(t, err, ErrBudgetReadUnavailable)
			if cause != config.ErrEOA7702ExecuteDisabled {
				require.ErrorIs(t, ownerPreconditionError(cause, "owner preparation: %v"), cause)
			}
			g, p, backend, sig := newBudgetChangeTest(t)
			backend.validationError = cause
			_, err = g.n.SubmitBudgetPolicyPreparation(context.Background(), g.user, g.in.Wallet, testPolicyChain, p.ID, sig)
			require.ErrorIs(t, err, cause)
			require.NotErrorIs(t, err, ErrBudgetReadUnavailable)
			var failure *BudgetSubmitError
			require.ErrorAs(t, err, &failure)
			require.False(t, failure.SubmissionStarted)
			require.Zero(t, backend.sent.Load())
		})
	}
}

func TestBudgetRepeatUnsentPreparationDoesNotClaimSendingStarted(t *testing.T) {
	f, p, backend, signature := newBudgetChangeTest(t)
	backend.validationError = preset.ErrOwnerStateChanged
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
	require.Error(t, err)
	stored, err := f.n.loadBudgetChange(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionUnsent, stored.State)
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, make([]byte, 65))
	var failure *BudgetSubmitError
	require.ErrorAs(t, err, &failure)
	require.False(t, failure.SubmissionStarted)
	result, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionUnsent, result.State)
	require.Zero(t, backend.sent.Load())
}

func TestBudgetPreparationRetirementReadPreservesUnderlyingCause(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	policy := storeOutstandingLegacyBudgetGrant(t, f, uint64(time.Now().Add(-time.Minute).Unix()))
	policy.Grant.AppliedAt = 1
	require.NoError(t, StoreSessionPolicy(f.n.db, policy))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "private retirement RPC failure"}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	f.n.config.SmartWallet.EthRpcUrl = server.URL
	_, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.ErrorContains(t, err, "retirement registration is unavailable")
	require.ErrorContains(t, err, "private retirement RPC failure")
	var rpcErr rpc.Error
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, -32000, rpcErr.ErrorCode())
}
