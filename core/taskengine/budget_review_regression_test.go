package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

type budgetRecipientReader struct{ ChainStateReader }

func (budgetRecipientReader) CodeAt(context.Context, common.Address) ([]byte, error) {
	return nil, nil // The recipient is an ordinary EOA at preparation time.
}

func TestBudgetSubmitRechecksNativeRecipientCode(t *testing.T) {
	for _, name := range []string{"EOA", "delegated", "contract", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			recipient := common.HexToAddress("0x1234")
			f.in.Addition.NativeRecipients = []*common.Address{&recipient}
			f.in.ApprovalDiscovery.AllowedContracts = append(f.in.ApprovalDiscovery.AllowedContracts, recipient)
			signBudgetDiscovery(t, f.key, f.in.ApprovalDiscovery)
			// The entity-occupancy RPC and recipient reader represent the
			// preparation snapshot. Submit must use its own pinned view.
			rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID json.RawMessage `json:"id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x%064x"}`, request.ID, 0)
			}))
			t.Cleanup(rpc.Close)
			f.n.config.SmartWallet.EthRpcUrl = rpc.URL
			previous := GetChainStateReaderForChain(uint64(testPolicyChain))
			RegisterChainStateReader(uint64(testPolicyChain), budgetRecipientReader{})
			t.Cleanup(func() {
				unregisterChainStateReader(uint64(testPolicyChain))
				RegisterChainStateReader(uint64(testPolicyChain), previous)
			})
			f.n.sessionChainReads = true
			p := f.prepare(t)
			backend := &budgetChangeFake{recipientCode: map[common.Address][]byte{}}
			switch name {
			case "delegated":
				backend.recipientCode[recipient] = append([]byte{0xef, 0x01, 0x00}, common.HexToAddress("0x5678").Bytes()...)
			case "contract":
				backend.recipientCode[recipient] = []byte{0x60, 0x00}
			case "unavailable":
				backend.codeReadError = errors.New("code read failed")
			}
			f.n.config.WalletBudgetSigningEnabled = true
			f.n.budgetChangeBackend = backend
			sig, err := crypto.Sign(accounts.TextHash(p.UserOpHash.Bytes()), f.ownerKey)
			require.NoError(t, err)
			_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
			if name == "EOA" {
				require.NoError(t, err)
				require.EqualValues(t, 1, backend.sent.Load())
				return
			}
			require.Error(t, err)
			require.Zero(t, backend.sent.Load(), "changed recipient code must be refused before relay")
			require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
		})
	}
}

func TestBudgetExternalNonceRefusesStaleOrOrphanedSnapshot(t *testing.T) {
	f, p, backend, sig := newBudgetChangeTest(t)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	backend.observation = budgetMined(p, true)
	backend.observation.Hash = common.HexToHash("0x999")
	backend.latestBlock = 10 // Canonical but older than the nonce-consuming inclusion.
	require.ErrorContains(t, f.n.ReconcileBudgetChanges(context.Background(), testPolicyChain, f.user.Address, f.in.Wallet), "predates inclusion")
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	backend.latestBlock = 12
	backend.blockHashes = map[uint64]common.Hash{11: common.HexToHash("0xdead")}
	require.ErrorContains(t, f.n.ReconcileBudgetChanges(context.Background(), testPolicyChain, f.user.Address, f.in.Wallet), "inclusion evidence changed")
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	delete(backend.blockHashes, 11)
	require.NoError(t, f.n.ReconcileBudgetChanges(context.Background(), testPolicyChain, f.user.Address, f.in.Wallet))
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	result, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, budgetChangeSuperseded, result.State)
}

func TestBudgetResetRepairRefusesStaleOrOrphanedSnapshot(t *testing.T) {
	f, p, backend, sig := newBudgetChangeTest(t)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	backend.observation = budgetMined(p, true)
	backend.reset = 5
	result, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.True(t, result.ResetRepairRequired)
	backend.reset = 0
	backend.latestBlock = 10
	result, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionSubmitted, result.State)
	require.Contains(t, result.Reason, "predates inclusion")
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	backend.latestBlock = 12
	backend.blockHashes = map[uint64]common.Hash{11: common.HexToHash("0xdead")}
	result, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionSubmitted, result.State)
	require.Contains(t, result.Reason, "inclusion evidence changed")
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	delete(backend.blockHashes, 11)
	result, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, budgetChangeActive, result.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}

func TestBudgetUnsentReviewNeverJoinsDurableHold(t *testing.T) {
	f, p, backend, sig := newBudgetChangeTest(t)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	replacement := f.prepare(t)
	otherSig, err := crypto.Sign(accounts.TextHash(replacement.UserOpHash.Bytes()), f.ownerKey)
	require.NoError(t, err)
	backend.changed = true
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, replacement.ID, otherSig)
	require.ErrorContains(t, err, "SESSION_POLICY_BASE_CHANGED")
	holds, err := budgetHoldRows(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, err)
	require.Len(t, holds, 1)
	require.Equal(t, []string{p.ID}, holds[0].Attempts)
	// Expiring an unsent review must not make the held attempts unreadable.
	require.NoError(t, f.n.db.Delete(budgetPreparationKey(f.user.Address, replacement.ID)))
	backend.changed = false
	backend.observation = budgetMined(p, true)
	result, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, budgetChangeActive, result.State)
}

func TestBudgetCancelSharesPreparationCapWithNormalPrepare(t *testing.T) {
	f, p, _, sig := newBudgetChangeTest(t)
	f.n.ownerExecutionBackend = f.backend
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	for i := 0; i < ownerPreparationMaxPerOwner-1; i++ {
		f.prepare(t)
	}
	// Both requests compete for the last unsigned slot, using the same
	// per-owner lock even though cancellation has a different pricing path.
	results := make(chan error, 2)
	go func() {
		_, prepareErr := f.n.PrepareSessionPolicyWithContext(context.Background(), f.user, f.in)
		results <- prepareErr
	}()
	go func() {
		_, cancelErr := f.n.PrepareBudgetCancel(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID)
		results <- cancelErr
	}()
	successes, refusals := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case prepareErr := <-results:
			if prepareErr == nil {
				successes++
			} else {
				require.ErrorContains(t, prepareErr, BudgetPreparationLimitCode)
				refusals++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("preparation lock did not complete")
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, refusals)
	before := f.backend.prepared.Load()
	_, err = f.n.PrepareBudgetCancel(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.ErrorContains(t, err, BudgetPreparationLimitCode)
	require.Equal(t, before, f.backend.prepared.Load(), "quota refusal must happen before pricing")
	require.ErrorContains(t, f.n.checkBudgetPreparationCap(f.user.Address, time.Now()), BudgetPreparationLimitCode)
}

// Compile-time assurance that the fake supports the pinned code-read boundary.
var _ budgetChainView = (*budgetFakeView)(nil)
