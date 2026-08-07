package taskengine

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Granting REPLACES. A runner carries at most one usable grant, and submit is
// what keeps it that way — the send path's refusal to pick between two is the
// backstop, not the mechanism.

// grantOn runs a full prepare → sign → submit against one wallet.
func grantOn(
	t *testing.T,
	engine *Engine,
	ownerKey *ecdsa.PrivateKey,
	owner, wallet common.Address,
) (*model.SessionPolicy, []string) {
	t.Helper()
	user := &model.User{Address: owner}
	in := SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Justification: "replace the last one",
		Permissions: testPermissions(),
	}
	prepared, err := engine.PrepareSessionPolicy(user, in)
	require.NoError(t, err)
	stored, superseded, err := engine.SubmitSessionPolicy(user, in,
		prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, ownerKey, prepared.Digest))
	require.NoError(t, err)
	return stored, superseded
}

// usableOn returns the wallet's usable policies straight from storage.
func usableOn(t *testing.T, db storage.Storage, owner, wallet common.Address) []*model.SessionPolicy {
	t.Helper()
	all, err := ListSessionPolicies(db, testPolicyChain, owner)
	require.NoError(t, err)
	usable := make([]*model.SessionPolicy, 0, len(all))
	for _, p := range all {
		if p.Runner != nil && *p.Runner == wallet && p.Usable() {
			usable = append(usable, p)
		}
	}
	return usable
}

// seedUsablePolicy writes a usable grant directly, bypassing submit — the only
// way to reproduce a runner that stacked grants before replacement existed.
// InstallCall is a real installValidation so N-way teardown can derive uninstall.
func seedUsablePolicy(t *testing.T, db storage.Storage, owner, wallet common.Address, id string, entity uint32) {
	t.Helper()
	ownerAddr, walletAddr, signer := owner, wallet, common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity, Signer: signer, Global: true,
		Hooks: [][]byte{aa.AllowlistExecHook(entity)},
	})
	require.NoError(t, err)
	require.NoError(t, StoreSessionPolicy(db, &model.SessionPolicy{
		ID: id, Owner: &ownerAddr, Runner: &walletAddr, ChainID: testPolicyChain,
		EntityID: entity, SessionSigner: &signer, Status: model.SessionPolicyActive,
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    install,
			CarrierNonce:   big.NewInt(1),
			Deadline:       1785541743,
			OwnerSignature: make([]byte, 65),
			AppliedAt:      1,
		},
	}))
}

// The regression this whole change exists for: two grants in sequence used to
// leave two usable policies, and the wallet then refused to execute at all.
// No concurrency involved — an ordinary re-grant was enough.
func TestSecondGrantReplacesTheFirst(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	first, superseded := grantOn(t, engine, ownerKey, owner, wallet)
	require.Empty(t, superseded, "a first grant replaces nothing")

	second, superseded := grantOn(t, engine, ownerKey, owner, wallet)
	require.Equal(t, []string{first.ID}, superseded, "the second grant reports what it replaced")

	usable := usableOn(t, db, owner, wallet)
	require.Len(t, usable, 1, "a runner carries exactly one usable grant")
	require.Equal(t, second.ID, usable[0].ID, "the newest grant is the one that survives")

	// And the send path resolves it rather than refusing the wallet.
	resolved, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, second.ID, resolved.ID)
}

// A superseded grant is revoked, never deleted. NextSessionEntityID counts
// stored records to find the next free entity, so deleting one would hand its
// entity to the next grant — and if the deleted grant's install had already
// reached the chain, that install would land on an entity the account already
// has.
func TestSupersededGrantIsRetainedAndKeepsItsEntity(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.EqualValues(t, 1, first.EntityID)
	second, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.EqualValues(t, 2, second.EntityID)

	// The superseded record still exists, marked revoked.
	stored, err := engine.GetSessionPolicyByID(&model.User{Address: owner}, testPolicyChain, wallet, first.ID)
	require.NoError(t, err)
	require.Equal(t, model.SessionPolicyRevoked, stored.Status)
	require.False(t, stored.Usable())

	// So entity 1 is never handed out again.
	next, err := NextSessionEntityID(db, testPolicyChain, owner, wallet)
	require.NoError(t, err)
	require.EqualValues(t, 3, next, "a superseded entity is spent, not recycled")
}

// Replacement is scoped to the runner: granting on one wallet must not disturb
// the owner's other wallets.
func TestSupersedeIsScopedToTheRunner(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	otherWallet := common.HexToAddress("0x00000000000000000000000000000000000000a2")
	factory := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	require.NoError(t, StoreWallet(db, testPolicyChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &otherWallet, Factory: &factory, Salt: big.NewInt(1),
	}))

	onOther, _ := grantOn(t, engine, ownerKey, owner, otherWallet)
	grantOn(t, engine, ownerKey, owner, wallet)
	replacement, superseded := grantOn(t, engine, ownerKey, owner, wallet)
	require.NotContains(t, superseded, onOther.ID, "another wallet's grant is not this wallet's business")

	require.Len(t, usableOn(t, db, owner, otherWallet), 1)
	require.Equal(t, onOther.ID, usableOn(t, db, owner, otherWallet)[0].ID)
	require.Equal(t, replacement.ID, usableOn(t, db, owner, wallet)[0].ID)
}

// Grants that stacked before replacement existed — the wallets already stuck
// in the wild — are repaired by granting again. No migration, no cleanup call.
func TestGrantingAgainRepairsAWalletThatAlreadyStacked(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	seedUsablePolicy(t, db, owner, wallet, "01legacygrantaaaaaaaaaaaaa", 1)
	seedUsablePolicy(t, db, owner, wallet, "01legacygrantbbbbbbbbbbbbb", 2)
	require.Len(t, usableOn(t, db, owner, wallet), 2, "precondition: the broken state")

	_, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.Error(t, err, "precondition: the send path refuses this wallet")

	fresh, superseded := grantOn(t, engine, ownerKey, owner, wallet)
	require.Len(t, superseded, 2, "both leftovers are replaced")

	usable := usableOn(t, db, owner, wallet)
	require.Len(t, usable, 1)
	require.Equal(t, fresh.ID, usable[0].ID)
	resolved, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.NoError(t, err, "the wallet executes again")
	require.Equal(t, fresh.ID, resolved.ID)
}

// The backstop keeps a stable machine code. Studio branches on the code; the
// prose around it is free to change.
func TestAmbiguousWalletRefusalCarriesItsCode(t *testing.T) {
	_, db, _, owner, wallet := newPolicyTestEngine(t)
	seedUsablePolicy(t, db, owner, wallet, "01legacygrantaaaaaaaaaaaaa", 1)
	seedUsablePolicy(t, db, owner, wallet, "01legacygrantbbbbbbbbbbbbb", 2)

	_, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.Error(t, err)
	require.Contains(t, err.Error(), SessionPolicyAmbiguousCode)
}

// Concurrent submits cannot both land. They race for the same validation
// entity, and the loser is told to prepare again rather than being stored
// alongside the winner.
func TestConcurrentSubmitsLeaveExactlyOneUsableGrant(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	in := SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Justification: "two tabs, one wallet",
		Permissions: testPermissions(),
	}

	// Every writer prepares before any of them submits — the two-tabs case.
	const writers = 6
	prepared := make([]*PreparedSessionGrant, writers)
	signatures := make([][]byte, writers)
	for i := range prepared {
		p, err := engine.PrepareSessionPolicy(user, in)
		require.NoError(t, err)
		prepared[i], signatures[i] = p, signDigest(t, ownerKey, p.Digest)
		require.EqualValues(t, 1, p.Policy.EntityID, "all of them allocated the same free entity")
	}

	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range prepared {
		wg.Add(1)
		go func(p *PreparedSessionGrant, sig []byte) {
			defer wg.Done()
			if _, _, err := engine.SubmitSessionPolicy(user, in,
				p.Policy.ID, p.Policy.EntityID, p.Policy.Grant.Deadline, sig); err == nil {
				accepted.Add(1)
			}
		}(prepared[i], signatures[i])
	}
	wg.Wait()

	require.EqualValues(t, 1, accepted.Load(), "exactly one concurrent submit is accepted")
	require.Len(t, usableOn(t, db, owner, wallet), 1)
	_, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.NoError(t, err)
}

// Submit stores the new grant and then revokes the old one — two writes, and
// between them the runner momentarily has two usable grants. The resolver
// takes the same lock in shared mode, so an execution running alongside a
// re-grant never observes that window. Worth running under -race.
func TestReGrantingIsNeverVisibleAsAnAmbiguousWallet(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	const grants = 25
	done := make(chan struct{})
	var readerErr atomic.Value
	var reads atomic.Int64

	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet); err != nil {
				readerErr.Store(err)
				return
			}
			reads.Add(1)
		}
	}()

	for i := 0; i < grants; i++ {
		grantOn(t, engine, ownerKey, owner, wallet)
	}
	close(done)

	if err, ok := readerErr.Load().(error); ok && err != nil {
		t.Fatalf("the send path saw a wallet mid-replacement: %v", err)
	}
	require.Positive(t, reads.Load(), "the reader has to actually have run for this to mean anything")
	require.Len(t, usableOn(t, db, owner, wallet), 1)
}

// failSetOnPolicy fails the write of one policy id and passes everything else
// through, so a supersede can be made to fail after the new grant has landed.
type failSetOnPolicy struct {
	storage.Storage
	policyID string
}

func (f failSetOnPolicy) Set(key, value []byte) error {
	if strings.Contains(string(key), f.policyID) {
		return fmt.Errorf("storage is down")
	}
	return f.Storage.Set(key, value)
}

// A supersede that fails partway must not answer 201. The grant did land, so
// the client must not retry the submit — the entity is spent — but the wallet
// is ambiguous until the leftovers are cleared.
func TestFailedSupersedeIsReportedRatherThanSwallowed(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	engine.db = failSetOnPolicy{Storage: db, policyID: first.ID}

	in := SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Justification: "supersede will fail",
		Permissions: testPermissions(),
	}
	prepared, err := engine.PrepareSessionPolicy(user, in)
	require.NoError(t, err)
	stored, superseded, err := engine.SubmitSessionPolicy(user, in,
		prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, ownerKey, prepared.Digest))

	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionPolicySupersedeFailed)
	require.Nil(t, stored, "no policy is returned on a failed replace")
	require.Empty(t, superseded, "nothing was successfully superseded")

	// The new grant IS stored — which is exactly why this cannot report success.
	require.Len(t, usableOn(t, db, owner, wallet), 2)
}

// The same runner must always reach the same mutex. Sharding is only safe
// because of this: two writers that hashed differently would each hold a lock
// and neither would exclude the other.
func TestRunnerAuthorityLockIsStable(t *testing.T) {
	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")

	first := sessionAuthorityLock(testPolicyChain, owner, wallet)
	for i := 0; i < 32; i++ {
		require.Same(t, first, sessionAuthorityLock(testPolicyChain, owner, wallet),
			"the same runner must always resolve to the same lock")
	}

	// All three components participate, so unrelated runners are not forced to
	// serialize on one shard. (A collision is possible and harmless — this
	// asserts the inputs are used at all, not that they never collide.)
	spread := map[*sync.RWMutex]struct{}{}
	for i := 0; i < 64; i++ {
		var other common.Address
		other[19] = byte(i)
		spread[sessionAuthorityLock(testPolicyChain, owner, other)] = struct{}{}
		spread[sessionAuthorityLock(int64(i+1), owner, wallet)] = struct{}{}
		spread[sessionAuthorityLock(testPolicyChain, other, wallet)] = struct{}{}
	}
	require.Greater(t, len(spread), 1, "chain, owner and runner must all feed the shard")
}

// A grant's install can confirm after a later grant has already replaced it.
// Ordered that way, the re-read alone is enough: the record already reads
// revoked, so the mark is a no-op. See the concurrent case below for the half
// the lock is actually there for.
func TestLateInstallConfirmationCannotResurrectASupersededGrant(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.Equal(t, model.SessionPolicyPending, first.Status)

	// The replacement lands while the first grant's install is still in flight.
	second, superseded := grantOn(t, engine, ownerKey, owner, wallet)
	require.Equal(t, []string{first.ID}, superseded)

	// Now the in-flight operation confirms and reports the install applied.
	require.NoError(t, MarkSessionGrantAppliedByID(db, testPolicyChain, owner, wallet, first.ID, "0xlate"))

	usable := usableOn(t, db, owner, wallet)
	require.Len(t, usable, 1, "the late confirmation must not add a second usable grant")
	require.Equal(t, second.ID, usable[0].ID)

	stale, err := engine.GetSessionPolicyByID(&model.User{Address: owner}, testPolicyChain, wallet, first.ID)
	require.NoError(t, err)
	require.Equal(t, model.SessionPolicyRevoked, stale.Status, "the superseded grant stays revoked")
}

// The lock is asserted directly, because the race it closes cannot be won
// reliably from a test: the confirmation's read-then-write is a few
// microseconds, while the submit racing it spends far longer in signature
// recovery before it supersedes anything, so the interleaving effectively
// never occurs on demand.
//
// What is checkable is the property that was actually changed — that marking a
// grant applied acquires the runner's write lock, and therefore cannot run
// between a submit's store and its supersede. Hold the lock, and the
// confirmation must wait for it.
func TestApplyingAGrantWaitsForTheRunnersWriteLock(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	pending, _ := grantOn(t, engine, ownerKey, owner, wallet)

	lock := sessionAuthorityLock(testPolicyChain, owner, wallet)
	lock.Lock()

	done := make(chan error, 1)
	go func() {
		done <- MarkSessionGrantAppliedByID(db, testPolicyChain, owner, wallet, pending.ID, "0xinflight")
	}()

	select {
	case <-done:
		lock.Unlock()
		t.Fatal("marking a grant applied did not wait for the runner's write lock: a replacement " +
			"landing between its read and its write would be overwritten, resurrecting a superseded grant")
	case <-time.After(150 * time.Millisecond):
		// Still blocked, which is the point. Without the lock this completes
		// in microseconds.
	}

	lock.Unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("still blocked after the runner's lock was released")
	}

	require.Len(t, usableOn(t, db, owner, wallet), 1)
}

// A replacement carries the prior grant's teardown, so one owner signature
// makes the replacement true on chain as well as in storage (#717). Proven on
// Sepolia; these tests pin the payload the gateway builds.

// installValidationCount reports how many installValidation calls a deferred
// payload carries, and whether it is a batch.
func deferredShape(t *testing.T, deferred []byte) (batch bool, installs, uninstalls int) {
	t.Helper()
	installSel := crypto.Keccak256([]byte("installValidation(bytes25,bytes4[],bytes,bytes[])"))[:4]
	uninstallSel := crypto.Keccak256([]byte("uninstallValidation(bytes24,bytes,bytes[])"))[:4]

	if len(deferred) >= 4 && string(deferred[:4]) == string(installSel) {
		return false, 1, 0
	}
	_, _, datas, err := aa.UnpackExecuteCalldata(deferred)
	require.NoError(t, err, "a non-install deferred payload must be an unpackable batch")
	for _, d := range datas {
		if len(d) < 4 {
			continue
		}
		switch string(d[:4]) {
		case string(installSel):
			installs++
		case string(uninstallSel):
			uninstalls++
		}
	}
	return true, installs, uninstalls
}

// A first grant has nothing installed to remove, so it stays a bare install —
// the payload shape, and therefore the digest the owner signs, is unchanged
// from before this feature.
func TestFirstGrantCarriesNoTeardown(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot",
		Permissions: testPermissions(),
	})
	require.NoError(t, err)
	require.Empty(t, prepared.Supersedes, "nothing installed, nothing to supersede")

	batch, _, _ := deferredShape(t, prepared.Policy.Grant.InstallCall)
	require.False(t, batch, "a first grant is a plain installValidation")
	_ = ownerKey
}

// A grant that is only PENDING has no on-chain entity, so replacing it must
// not batch a teardown for something that was never installed.
func TestReplacingAPendingGrantCarriesNoTeardown(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.Equal(t, model.SessionPolicyPending, first.Status, "precondition: not applied")

	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot",
		Permissions: testPermissions(),
	})
	require.NoError(t, err)
	require.Empty(t, prepared.Supersedes,
		"a pending grant's install never reached the chain — there is nothing to uninstall")
	batch, _, _ := deferredShape(t, prepared.Policy.Grant.InstallCall)
	require.False(t, batch)
}

// The feature: replacing an APPLIED grant batches its teardown into the same
// owner signature. Install first — the operation carrying this is validated by
// the new entity, which must exist before the outer validation runs.
//
// Packing is asserted offline (no TeardownCheck): a live RPC that answers
// zero for every entity would drop all targets as "already clear".
func TestReplacingAnAppliedGrantBatchesItsTeardown(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.NoError(t, MarkSessionGrantApplied(db, first, "0xapplied"))

	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	if first.SessionSigner != nil {
		signer = *first.SessionSigner
	}
	prepared, err := PrepareSessionGrant(db, testPolicyChain, signer, "01replaceappliedaaaaaaaaaa", SessionGrantRequest{
		Owner: owner, Wallet: wallet, AgentLabel: "TradingBot",
		HooksFor: testPermissions().HooksFor,
	})
	require.NoError(t, err)

	require.Len(t, prepared.Supersedes, 1, "an installed grant must be superseded on chain too")
	require.Equal(t, first.ID, prepared.Supersedes[0].PolicyID)
	require.Equal(t, first.EntityID, prepared.Supersedes[0].EntityID)

	batch, installs, uninstalls := deferredShape(t, prepared.Policy.Grant.InstallCall)
	require.True(t, batch, "a replacement is a batch")
	require.Equal(t, 1, installs)
	require.Equal(t, 1, uninstalls)

	// The stored payload must remain replayable as-is: the resolver rebuilds
	// the signed data from it, so the batch — not the bare install — is what
	// the owner's signature covers.
	install, err := aa.InstallValidationWithin(prepared.Policy.Grant.InstallCall)
	require.NoError(t, err)
	require.NotEqual(t, prepared.Policy.Grant.InstallCall, install,
		"the stored call is the batch; the install is inside it")
}

// Stacked applied grants (pre-#729 residue) are cleaned N-way: one owner
// signature installs the new entity and uninstalls every known applied prior.
// Storage supersede still collapses usable to one; the batch clears the chain.
func TestStackedInstalledGrantsBatchAllTeardowns(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)

	seedUsablePolicy(t, db, owner, wallet, "01stackedaaaaaaaaaaaaaaaaa", 1)
	seedUsablePolicy(t, db, owner, wallet, "01stackedbbbbbbbbbbbbbbbbb", 2)
	require.Len(t, usableOn(t, db, owner, wallet), 2, "precondition: the broken state")

	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	prepared, err := PrepareSessionGrant(db, testPolicyChain, signer, "01stackedprepareaaaaaaaaaa", SessionGrantRequest{
		Owner: owner, Wallet: wallet, AgentLabel: "TradingBot",
		HooksFor: testPermissions().HooksFor,
	})
	require.NoError(t, err, "a stacked wallet must still be grantable")
	require.Len(t, prepared.Supersedes, 2, "both applied entities ride the replace batch")
	batch, installs, uninstalls := deferredShape(t, prepared.Policy.Grant.InstallCall)
	require.True(t, batch)
	require.Equal(t, 1, installs)
	require.Equal(t, 2, uninstalls)

	// And the storage-level repair from #716 still lands.
	fresh, superseded := grantOn(t, engine, ownerKey, owner, wallet)
	require.Len(t, superseded, 2)
	usable := usableOn(t, db, owner, wallet)
	require.Len(t, usable, 1)
	require.Equal(t, fresh.ID, usable[0].ID)
}

// Revoked-but-applied leftovers (orphans / pre-replace residue) are also
// uninstalled on the next grant — not only the current usable policy.
func TestRevokedAppliedLeftoversAreTornDownOnNextGrant(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.NoError(t, MarkSessionGrantApplied(db, first, "0xfirst"))
	// Soft-revoke without on-chain cleanup — the entity is still "installed"
	// as far as storage knows (AppliedAt set).
	_, cleanupRequired, _, err := engine.RevokeSessionPolicyByID(user, testPolicyChain, wallet, first.ID)
	require.NoError(t, err)
	require.True(t, cleanupRequired)

	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	if first.SessionSigner != nil {
		signer = *first.SessionSigner
	}
	prepared, err := PrepareSessionGrant(db, testPolicyChain, signer, "01revokedleftoveraaaaaaaaa", SessionGrantRequest{
		Owner: owner, Wallet: wallet, AgentLabel: "TradingBot",
		HooksFor: testPermissions().HooksFor,
	})
	require.NoError(t, err)
	require.Len(t, prepared.Supersedes, 1)
	require.Equal(t, first.EntityID, prepared.Supersedes[0].EntityID)
	batch, _, uninstalls := deferredShape(t, prepared.Policy.Grant.InstallCall)
	require.True(t, batch)
	require.Equal(t, 1, uninstalls)
	_ = ownerKey
}
