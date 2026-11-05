package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

type budgetChangeFake struct {
	sent            atomic.Int64
	observation     *budgetObservation
	sendError       error
	reset           int64
	readError       bool
	validationError error
	viewError       error
	canonicalError  error
	changed         bool
	authorityError  error
	authorityCheck  func(context.Context, common.Address, common.Address) error
	caller          func(ethereum.CallMsg) ([]byte, error)
	sending         chan struct{}
	releaseSend     chan struct{}
	latestBlock     uint64
	blockHashes     map[uint64]common.Hash
	recipientCode   map[common.Address][]byte
	codeReadError   error
}
type budgetFakeView struct {
	f     *budgetChangeFake
	block BudgetBlock
}

func (f *budgetChangeFake) View(_ context.Context, _ int64, number *big.Int) (budgetChainView, error) {
	if f.viewError != nil {
		return nil, f.viewError
	}
	b := uint64(10)
	if f.observation != nil {
		b = f.observation.Block.Number + 1
	}
	if f.latestBlock != 0 {
		b = f.latestBlock
	}
	if number != nil {
		b = number.Uint64()
	}
	hash := common.BigToHash(new(big.Int).SetUint64(b))
	if override, ok := f.blockHashes[b]; ok {
		hash = override
	}
	return &budgetFakeView{f: f, block: BudgetBlock{Number: b, Hash: hash}}, nil
}
func (v *budgetFakeView) CodeAt(_ context.Context, address common.Address) ([]byte, error) {
	return v.f.recipientCode[address], v.f.codeReadError
}
func (v *budgetFakeView) Block() BudgetBlock                    { return v.block }
func (v *budgetFakeView) Close()                                {}
func (v *budgetFakeView) VerifyCanonical(context.Context) error { return v.f.canonicalError }
func (v *budgetFakeView) ValidateOwner(context.Context, *BudgetPolicyPreparation) error {
	return v.f.validationError
}
func (v *budgetFakeView) CheckAuthorities(ctx context.Context, owner, wallet common.Address) error {
	if v.f.authorityCheck != nil {
		return v.f.authorityCheck(ctx, owner, wallet)
	}
	return v.f.authorityError
}
func (v *budgetFakeView) Nonce(context.Context, common.Address) (*big.Int, error) {
	return userop.EncodeNonceMAv2(0, userop.ValidationOptionGlobal, 0)
}
func (v *budgetFakeView) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if v.f.readError {
		return nil, errors.New("RPC unavailable")
	}
	if v.f.caller != nil {
		return v.f.caller(call)
	}
	selector := common.Bytes2Hex(call.Data[:4])
	if selector == common.Bytes2Hex(crypto.Keccak256([]byte("allowance(address,address)"))[:4]) {
		amount := int64(0)
		if common.BytesToAddress(call.Data[36:68]) == testRouterAddr {
			amount = 123
		}
		if v.block.Number == 11 || v.f.observation != nil {
			amount = v.f.reset
		}
		if v.f.changed {
			amount++
		}
		return budgetWord(amount), nil
	}
	// ABI: (uint8,bytes25[],bytes25[],bytes4[]) with all fields empty.
	out := []byte{}
	for _, word := range []int64{32, 0, 128, 160, 192, 0, 0, 0} {
		out = append(out, budgetWord(word)...)
	}
	return out, nil
}
func (f *budgetChangeFake) Observe(context.Context, *BudgetHold) (*budgetObservation, error) {
	if f.observation != nil {
		return f.observation, nil
	}
	nonce, _ := userop.EncodeNonceMAv2(0, userop.ValidationOptionGlobal, 0)
	return &budgetObservation{Nonce: nonce}, nil
}
func (f *budgetChangeFake) Send(_ context.Context, p *BudgetPolicyPreparation, op *userop.UserOperationV07) (common.Hash, error) {
	f.sent.Add(1)
	if f.sending != nil {
		close(f.sending)
	}
	if f.releaseSend != nil {
		<-f.releaseSend
	}
	if len(op.Signature) != 67 {
		return common.Hash{}, errors.New("signature was not framed")
	}
	return p.UserOpHash, f.sendError
}
func newBudgetChangeTest(t *testing.T) (*budgetPrepareFixture, *BudgetPolicyPreparation, *budgetChangeFake, []byte) {
	t.Helper()
	f := newBudgetPrepareFixture(t)
	p := f.prepare(t)
	fake := &budgetChangeFake{}
	f.n.config.WalletBudgetSigningEnabled = true
	f.n.budgetChangeBackend = fake
	signature, e := crypto.Sign(accounts.TextHash(p.UserOpHash.Bytes()), f.ownerKey)
	require.NoError(t, e)
	return f, p, fake, signature
}
func budgetMined(p *BudgetPolicyPreparation, success bool) *budgetObservation {
	nonce := new(big.Int).Add((*userop.UserOperationV07)(p.Operation).Nonce, big.NewInt(1))
	return &budgetObservation{Nonce: nonce, Hash: p.UserOpHash, Success: success, Block: BudgetBlock{Number: 11, Hash: common.BigToHash(big.NewInt(11))}, TransactionHash: common.HexToHash("0xabc")}
}

func TestBudgetSubmitReportsLatestLegacyGrantDeadlineAfterPrepare(t *testing.T) {
	f, p, backend, signature := newBudgetChangeTest(t)
	earlier := uint64(time.Now().Add(time.Hour).Unix())
	later := earlier + 3600
	// These signatures did not exist when the budget operation was prepared.
	storeOutstandingLegacyBudgetGrant(t, f, earlier)
	storeOutstandingLegacyBudgetGrant(t, f, later)

	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, signature)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetLegacyGrantPendingCode, refusal.Code)
	require.Equal(t, int64(later)*1000, refusal.LegacyGrantDeadline)
	require.Equal(t, "An outstanding legacy signature can still install.", refusal.Detail)
	require.Zero(t, backend.sent.Load())
}

func TestBudgetSubmitActivatesOnlyAfterReceiptAndResetProof(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	result, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionSubmitted, result.State)
	require.EqualValues(t, 1, b.sent.Load())
	require.ErrorContains(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet), BudgetChangingCode)
	usable, e := ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, e)
	require.Nil(t, usable)
	b.observation = budgetMined(p, true)
	b.reset = 5
	result, e = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionSubmitted, result.State)
	require.Contains(t, result.Reason, testTokenAddr.Hex())
	require.True(t, result.ResetRepairRequired)
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	b.reset = 0
	result, e = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, budgetChangeActive, result.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	usable, e = ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, e)
	require.Equal(t, p.Policy.ID, usable.ID)
	require.True(t, usable.Grant.Applied())
	require.NoError(t, usable.Validate(), "an owner-installed budget has no deferred bearer signature")
	resolver := newSessionResolver(f.n.db, configuredSessionSigner(f.n.config), nil, nil)
	auth, e := resolver(testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, e)
	require.NotNil(t, auth)
	require.NoError(t, auth.Validate())
	require.False(t, auth.Deferred(), "the budget is already installed")
	require.True(t, auth.WrapExecuteUserOp, "budget execution hooks require the wrapper on later runs")
	require.EqualValues(t, 1, b.sent.Load())
}
func TestBudgetSubmitRejectsWrongSignatureWithoutSending(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	sig[0] ^= 1
	_, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.Error(t, e)
	require.Zero(t, b.sent.Load())
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}
func TestBudgetChangedBalanceRefusesBeforeRelay(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	b.changed = true
	_, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.ErrorContains(t, e, "SESSION_POLICY_BASE_CHANGED")
	require.Zero(t, b.sent.Load())
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	refused, e := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionUnsent, refused.State)
	b.changed = false
	refused, e = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionUnsent, refused.State, "a stale review cannot be retried as a fresh submit")
	require.Zero(t, b.sent.Load())
}
func TestBudgetRejectedSendStaysDurablePastWindow(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	b.sendError = budgetTestRPCRefusal{}
	result, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionRejected, result.State)
	result.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	require.NoError(t, f.n.saveBudgetTransition(result, nil))
	result, e = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionRejected, result.State)
	require.EqualValues(t, 1, b.sent.Load())
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	b.observation = budgetMined(p, false)
	result, e = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, budgetChangeReverted, result.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}
func TestBudgetFeeAndChainRefusals(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	f.n.config.WalletBudgetMaxFeeWei = "1"
	_, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.ErrorContains(t, e, BudgetFeeCeilingCode)
	require.Zero(t, b.sent.Load())
	require.Error(t, f.n.requireBudgetSigning(1))
	f.n.config.WalletBudgetSigningEnabled = false
	require.Error(t, f.n.requireBudgetSigning(testPolicyChain))
}
func TestBudgetUnsupportedRoutesFailClosed(t *testing.T) {
	for _, target := range []common.Address{common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3"), common.HexToAddress("0xbad")} {
		p := SessionPermissions{AllowedActions: []model.AllowedAction{{Target: &target, Selectors: []string{selectorApprove}}}}
		require.ErrorContains(t, refuseUnsupportedBudgetRoutes(testPolicyChain, p), BudgetRouteUnsupportedCode)
	}
}

type budgetTestRPCRefusal struct{}

func (budgetTestRPCRefusal) Error() string  { return "bundler fee refusal" }
func (budgetTestRPCRefusal) ErrorCode() int { return -32500 }
func TestBudgetUncertainSendNeverBecomesUnsent(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	b.sendError = errors.New("network disconnected")
	result, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionSubmitting, result.State)
	result.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	require.NoError(t, f.n.saveBudgetTransition(result, nil))
	f.n.config.WalletBudgetSigningEnabled = false
	result, e = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionSubmitting, result.State)
	require.EqualValues(t, 1, b.sent.Load())
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}
func TestBudgetRecoveryDoesNotResumeWhenResetReadFails(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	_, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	b.observation = budgetMined(p, true)
	b.readError = true
	result, e := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, ownerSubmissionSubmitted, result.State)
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	b.readError = false
	result, e = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, budgetChangeActive, result.State)
}
func TestBudgetCancelUsesSameNonceAndDoesNotInstallPermission(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	f.n.ownerExecutionBackend = f.backend
	_, e := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, e)
	cancel, e := f.n.PrepareBudgetCancel(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.True(t, cancel.IsCancel)
	signature, e := crypto.Sign(accounts.TextHash(cancel.UserOpHash.Bytes()), f.ownerKey)
	require.NoError(t, e)
	_, e = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, cancel.ID, signature)
	require.NoError(t, e)
	b.observation = budgetMined(cancel, true)
	_, e = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, cancel.ID)
	require.NoError(t, e)
	old, e := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, e)
	require.Equal(t, budgetChangeSuperseded, old.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	policy, e := ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, e)
	require.Nil(t, policy)
	require.EqualValues(t, 2, b.sent.Load())
}

func TestBudgetDefaultFeeCeilingIsFiveMilliETH(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	p := &BudgetPolicyPreparation{MaxFeeWei: "5000000000000000"}
	require.NoError(t, f.n.checkBudgetFee(p))
	p.MaxFeeWei = "5000000000000001"
	require.ErrorContains(t, f.n.checkBudgetFee(p), BudgetFeeCeilingCode)
	f.n.config.WalletBudgetMaxFeeWei = "6000000000000000"
	require.NoError(t, f.n.checkBudgetFee(p))
}

func TestBudgetRelayDoesNotBlockUnrelatedAuthorityReads(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	b.sending, b.releaseSend = make(chan struct{}), make(chan struct{})
	submitted := make(chan error, 1)
	go func() {
		_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
		submitted <- err
	}()
	<-b.sending
	other := common.Address{}
	for i := int64(1000); ; i++ {
		candidate := common.BigToAddress(big.NewInt(i))
		if candidate != f.in.Wallet && sessionAuthorityShard(testPolicyChain, f.user.Address, candidate) == sessionAuthorityShard(testPolicyChain, f.user.Address, f.in.Wallet) {
			other = candidate
			break
		}
	}
	read := make(chan error, 1)
	go func() {
		_, err := ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, other)
		read <- err
	}()
	readDuringRelay := false
	select {
	case err := <-read:
		require.NoError(t, err)
		readDuringRelay = true
	case <-time.After(time.Second):
	}
	close(b.releaseSend)
	require.NoError(t, <-submitted)
	if !readDuringRelay {
		require.NoError(t, <-read)
	}
	require.True(t, readDuringRelay, "an unrelated wallet must not wait on the bundler")
}

type budgetWriteFailure struct {
	storage.Storage
	writes int
	failAt int
}

func (db *budgetWriteFailure) AtomicWrite(updates map[string][]byte, deletes [][]byte) error {
	db.writes++
	if db.writes == db.failAt {
		return errors.New("injected durable commit failure")
	}
	return db.Storage.(budgetAtomicWriter).AtomicWrite(updates, deletes)
}
func TestBudgetMarkerAndPostSendStorageFailures(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(map[int]string{1: "before_rpc", 2: "after_rpc"}[failAt], func(t *testing.T) {
			f, p, b, sig := newBudgetChangeTest(t)
			f.n.db = &budgetWriteFailure{Storage: f.n.db, failAt: failAt}
			_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
			require.ErrorContains(t, err, "commit failure")
			stored, err := f.n.loadBudgetChange(f.user, f.in.Wallet, testPolicyChain, p.ID)
			require.NoError(t, err)
			if failAt == 1 {
				require.Zero(t, b.sent.Load())
				require.Equal(t, ownerSubmissionPrepared, stored.State)
				require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
				return
			}
			require.Equal(t, ownerSubmissionSubmitting, stored.State)
			require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
			_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
			require.NoError(t, err)
			require.EqualValues(t, 1, b.sent.Load(), "recovery must never rebroadcast")
		})
	}
}

func TestBudgetRestartReconcilesBeforeSessionSend(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	restarted := New(f.n.db, f.n.config, nil, f.n.logger)
	restarted.budgetChangeBackend = b
	lease, err := restarted.guardBudgetSessionSend(context.Background(), testPolicyChain, f.user.Address, f.in.Wallet)
	require.Nil(t, lease)
	require.ErrorContains(t, err, BudgetChangingCode)
	b.observation = budgetMined(p, true)
	lease, err = restarted.guardBudgetSessionSend(context.Background(), testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.Release()
	require.EqualValues(t, 1, b.sent.Load())
	current, err := ActiveSessionPolicyForWallet(restarted.db, testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, err)
	require.Equal(t, p.Policy.ID, current.ID)
}

func TestBudgetReplacementRefusalCannotReleaseOriginalHold(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	replacement := f.prepare(t)
	require.Greater(t, replacement.Policy.EntityID, p.Policy.EntityID)
	sig2, err := crypto.Sign(accounts.TextHash(replacement.UserOpHash.Bytes()), f.ownerKey)
	require.NoError(t, err)
	b.sendError = budgetTestRPCRefusal{}
	refused, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, replacement.ID, sig2)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionRejected, refused.State)
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	refused.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	require.NoError(t, f.n.saveBudgetTransition(refused, nil))
	b.observation = budgetMined(p, true)
	old, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, budgetChangeActive, old.State)
	other, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, budgetChangeSuperseded, other.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}

func TestBudgetEntityReservationRefusesEarlierUnsignedReview(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	other := f.prepare(t)
	require.Equal(t, p.Policy.EntityID, other.Policy.EntityID)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	otherSig, err := crypto.Sign(accounts.TextHash(other.UserOpHash.Bytes()), f.ownerKey)
	require.NoError(t, err)
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, other.ID, otherSig)
	require.ErrorIs(t, err, ErrSessionEntityTaken)
	require.EqualValues(t, 1, b.sent.Load())
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}

func TestBudgetExternalNonceRecoveryChecksUnknownAuthority(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.NoError(t, err)
	b.observation = budgetMined(p, true)
	b.observation.Hash = common.HexToHash("0x123")
	b.authorityError = &BudgetPolicyError{Code: BudgetAuthorityUnknownCode, Detail: "Remove unrecognized entity 99, then check again."}
	result, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, ownerSubmissionSubmitted, result.State)
	require.Contains(t, result.Reason, "99")
	require.Error(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	b.authorityError = nil // owner removed the unexpected registration
	result, err = f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
	require.NoError(t, err)
	require.Equal(t, budgetChangeSuperseded, result.State)
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	active, err := ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, err)
	require.Nil(t, active, "an unknown operation never activates a stored review")
}

func TestBudgetSessionFlightMustSettleBeforeFinalRead(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	nonce, err := userop.EncodeNonceMAv2(7, userop.ValidationOptionGlobal, 0)
	require.NoError(t, err)
	flight := budgetSessionFlight{Hash: common.HexToHash("0x456"), Nonce: nonce.String()}
	raw, err := json.Marshal(flight)
	require.NoError(t, err)
	require.NoError(t, f.n.db.Set(append(budgetFlightPrefix(testPolicyChain, f.user.Address, f.in.Wallet), "0x456"...), raw))
	b.caller = func(ethereum.CallMsg) ([]byte, error) { return common.LeftPadBytes(nonce.Bytes(), 32), nil }
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.ErrorContains(t, err, "still in flight")
	require.Zero(t, b.sent.Load())
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
	// The final snapshot must be at least as new as a confirmed session receipt.
	watermark, err := json.Marshal(budgetSessionFlight{Block: 12})
	require.NoError(t, err)
	require.NoError(t, f.n.db.Delete(append(budgetFlightPrefix(testPolicyChain, f.user.Address, f.in.Wallet), "0x456"...)))
	require.NoError(t, f.n.db.Set(budgetWatermarkKey(testPolicyChain, f.user.Address, f.in.Wallet), watermark))
	_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.ErrorContains(t, err, "newer than the read snapshot")
	require.Zero(t, b.sent.Load())
}

func TestBudgetFirstSubmitCannotUseExpiredReview(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	p.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	require.NoError(t, f.n.saveBudgetTransition(p, nil))
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.ErrorIs(t, err, ErrSessionPolicyNotFound)
	require.Zero(t, b.sent.Load())
	require.NoError(t, budgetHoldPresent(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet))
}

func TestBudgetUnknownStoredStateFailsClosed(t *testing.T) {
	f, p, b, sig := newBudgetChangeTest(t)
	p.State = "unknown"
	require.NoError(t, f.n.saveBudgetTransition(p, nil))
	_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.Zero(t, b.sent.Load())
}

func TestBudgetMigrationRechecksSpendAndSupersedesOnlyAtActivation(t *testing.T) {
	for _, spend := range []bool{true, false} {
		t.Run(map[bool]string{true: "spent_after_review", false: "unchanged"}[spend], func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			old, _ := semanticsBudget()
			old.Owner, old.Runner = &f.user.Address, &f.in.Wallet
			old.SessionSigner = &f.user.Address
			old.Sizing = model.BudgetSizingWorkflow
			old.BudgetStartAmounts, old.BudgetStartNative, old.BudgetStartedAt = nil, nil, 0
			old.AllowedActions = testPermissions().AllowedActions
			old.Grant.OwnerSignature = make([]byte, 65)
			old.Grant.CarrierNonce = big.NewInt(0)
			hooks, err := testPermissions().HooksFor(old.EntityID)
			require.NoError(t, err)
			old.Grant.InstallCall, err = aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: old.EntityID, Signer: *old.SessionSigner, Hooks: hooks, Global: true})
			require.NoError(t, err)
			require.NoError(t, StoreSessionPolicy(f.n.db, old))
			f.in.Budget.ValidUntil = old.ValidUntil
			f.n.config.SmartWallet.EthRpcUrl = budgetRegistrationRPC(t).URL
			b := &budgetChangeFake{}
			read := func(call ethereum.CallMsg) ([]byte, error) {
				if *call.To == aa.AllowlistModuleAddress() {
					if spend {
						return budgetWord(399), nil
					}
					return budgetWord(400), nil
				}
				if *call.To == aa.NativeTokenLimitModuleAddress() {
					return budgetWord(400), nil
				}
				if *call.To == f.in.Wallet {
					return registrationReply(new(big.Int).SetBytes(call.Data[24:28]).Uint64() == uint64(old.EntityID)), nil
				}
				if *call.To == testTokenAddr && common.BytesToAddress(call.Data[36:68]) == testRouterAddr && b.observation == nil {
					return budgetWord(123), nil
				}
				return budgetWord(0), nil
			}
			// Preparation reads the old remainder before the simulated session spend.
			f.n.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) {
				return budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
					if *call.To == aa.AllowlistModuleAddress() || *call.To == aa.NativeTokenLimitModuleAddress() {
						return budgetWord(400), nil
					}
					if *call.To == testTokenAddr && common.BytesToAddress(call.Data[36:68]) == testRouterAddr {
						return budgetWord(123), nil
					}
					return budgetWord(0), nil
				}), nil
			}
			p := f.prepare(t)
			require.Len(t, p.Retirements, 1)
			b.caller = read
			f.n.budgetChangeBackend = b
			f.n.config.WalletBudgetSigningEnabled = true
			sig, err := crypto.Sign(accounts.TextHash(p.UserOpHash.Bytes()), f.ownerKey)
			require.NoError(t, err)
			_, err = f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, f.in.Wallet, testPolicyChain, p.ID, sig)
			if spend {
				require.ErrorContains(t, err, "SESSION_POLICY_BASE_CHANGED")
				require.Zero(t, b.sent.Load())
				return
			}
			require.NoError(t, err)
			usable, err := ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
			require.NoError(t, err)
			require.Equal(t, old.ID, usable.ID, "submit must not supersede the old authority")
			b.observation = budgetMined(p, true)
			result, err := f.n.GetBudgetPolicyPreparation(f.user, f.in.Wallet, testPolicyChain, p.ID)
			require.NoError(t, err)
			require.Equal(t, budgetChangeActive, result.State)
			usable, err = ActiveSessionPolicyForWallet(f.n.db, testPolicyChain, f.user.Address, f.in.Wallet)
			require.NoError(t, err)
			require.Equal(t, p.Policy.ID, usable.ID)
		})
	}
}
