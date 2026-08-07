package taskengine

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

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
func seedUsablePolicy(t *testing.T, db storage.Storage, owner, wallet common.Address, id string, entity uint32) {
	t.Helper()
	ownerAddr, walletAddr, signer := owner, wallet, common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	require.NoError(t, StoreSessionPolicy(db, &model.SessionPolicy{
		ID: id, Owner: &ownerAddr, Runner: &walletAddr, ChainID: testPolicyChain,
		EntityID: entity, SessionSigner: &signer, Status: model.SessionPolicyActive,
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
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
