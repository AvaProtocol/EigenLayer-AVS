package taskengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestBudgetPrepareWarmsInventoryBeforeOwnerReviewAndSubmitRechecks(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	inventory, rpcFixture := newBudgetInventoryView(t, 20, 11, 10_000)
	inventory.n = f.n
	rpcFixture.wallet = f.in.Wallet
	rpcFixture.logs = []types.Log{budgetInstallLog(rpcFixture, 11)}
	backend := &budgetChangeFake{}
	f.n.budgetChangeBackend = &budgetInventoryBackend{budgetChangeBackend: backend, inventory: inventory}
	p := f.prepare(t)
	cached := readBudgetInventory(t, inventory, f.in.Wallet)
	require.EqualValues(t, 20, cached.Cursor.Number)
	history, queries, _ := budgetInventoryReadCounts(rpcFixture)
	require.Positive(t, history)
	require.Equal(t, 1, queries, "the cold inventory is built before returning the review")
	require.Zero(t, backend.sent.Load())
	require.NoError(t, budgetHoldPresent(f.n.db, p.ChainID, p.Owner, p.Wallet))
	// Preparation's preflight is not an authorization decision for submit.
	rpcFixture.registered = true
	f.n.config.WalletBudgetSigningEnabled = true
	signature, err := crypto.Sign(accounts.TextHash(p.UserOpHash.Bytes()), f.ownerKey)
	require.NoError(t, err)
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, p.Wallet, p.ChainID, p.ID, signature)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
	var submitError *BudgetSubmitError
	require.ErrorAs(t, err, &submitError)
	require.False(t, submitError.SubmissionStarted)
	require.Zero(t, backend.sent.Load())
	historyAfter, queriesAfter, _ := budgetInventoryReadCounts(rpcFixture)
	require.Equal(t, history, historyAfter)
	require.Equal(t, queries, queriesAfter, "submit uses the warmed history but reads registration live")
}

func TestBudgetPrepareAuthorityFailureReturnsNoSignableReview(t *testing.T) {
	for _, name := range []string{"snapshot", "inventory", "unknown authority"} {
		t.Run(name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			backend := &budgetChangeFake{}
			switch name {
			case "snapshot":
				backend.viewError = errors.New("snapshot unavailable")
			case "inventory":
				backend.authorityError = ErrBudgetReadUnavailable
			case "unknown authority":
				backend.authorityError = &BudgetPolicyError{Code: BudgetAuthorityUnknownCode, Detail: "Remove the unrecognized authority."}
			}
			f.n.budgetChangeBackend = backend
			review, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
			require.Nil(t, review)
			if name == "unknown authority" {
				var refusal *BudgetPolicyError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
			} else {
				require.ErrorIs(t, err, ErrBudgetReadUnavailable)
			}
			rows, loadErr := f.n.db.GetByPrefix(budgetPreparationPrefix(f.user.Address))
			require.NoError(t, loadErr)
			require.Empty(t, rows)
			require.NoError(t, budgetHoldPresent(f.n.db, f.in.ChainID, f.user.Address, f.in.Wallet))
			require.Zero(t, backend.sent.Load())
		})
	}
}

func TestBudgetPrepareWarmupDoesNotHoldRunOrSendLock(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	entered, resume := make(chan struct{}), make(chan struct{})
	releaseWarmup := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(releaseWarmup)
	f.n.budgetChangeBackend = &budgetChangeFake{authorityCheck: func(ctx context.Context, _, _ common.Address) error {
		close(entered)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	done := make(chan error, 1)
	go func() {
		_, err := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not reach authority warmup")
	}
	gate := budgetGateKey(f.in.ChainID, f.user.Address, f.in.Wallet)
	for _, locks := range []*budgetPreparationLockSet{&f.n.budgetWorkflowLocks, &f.n.budgetSendLocks} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		release, err := locks.acquire(ctx, gate)
		cancel()
		if err != nil {
			releaseWarmup()
			<-done
			t.Fatal("unsigned warmup held a wallet run/send lock:", err)
		}
		release()
	}
	releaseWarmup()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not finish")
	}
}
