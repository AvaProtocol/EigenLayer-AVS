package taskengine

import (
	"crypto/ecdsa"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// The applied-marking loop: the resolver hands the send path a callback, the
// callback transitions the stored record, and the NEXT resolution stops
// attaching the deferred action. This is what keeps a grant's install from
// being replayed onto an entity that already exists.

func storedPendingGrant(t *testing.T) (*Engine, *model.User, common.Address, *model.SessionPolicy) {
	t.Helper()
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot", Permissions: testPermissions(),
	})
	require.NoError(t, err)
	stored, _, err := engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot", Permissions: testPermissions(),
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, ownerKey, prepared.Digest))
	require.NoError(t, err)
	return engine, user, wallet, stored
}

func testResolverKey(controller *ecdsa.PrivateKey) func(common.Address) (*ecdsa.PrivateKey, error) {
	return func(common.Address) (*ecdsa.PrivateKey, error) { return controller, nil }
}

func TestResolverMarksAppliedAndStopsAttachingTheInstall(t *testing.T) {
	engine, user, wallet, stored := storedPendingGrant(t)
	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	resolver := NewSessionResolver(engine.db, testResolverKey(controllerKey), nil)

	// First resolution: pending grant → deferred install attached, with the
	// callback that will record its landing.
	auth, err := resolver(testPolicyChain, user.Address, wallet)
	require.NoError(t, err)
	require.NotNil(t, auth)
	require.True(t, auth.Deferred(), "a pending grant's first operation carries the install")
	require.NotNil(t, auth.OnApplied, "the send path needs a way to record the landing")

	// The send path saw the receipt.
	require.NoError(t, auth.OnApplied("0xabc123"))

	updated, err := engine.GetSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.Equal(t, model.SessionPolicyActive, updated.Status)
	require.Equal(t, "0xabc123", updated.Grant.AppliedUserOpHash)
	require.True(t, updated.Grant.Applied())

	// Second resolution: the install is history; the operation runs plain.
	auth, err = resolver(testPolicyChain, user.Address, wallet)
	require.NoError(t, err)
	require.NotNil(t, auth)
	require.False(t, auth.Deferred(), "an applied install must never be attached again")
	require.Nil(t, auth.OnApplied)
	require.EqualValues(t, stored.EntityID, auth.EntityID)
}

func TestMarkAppliedByIDIsIdempotentAndRaceSafe(t *testing.T) {
	engine, user, wallet, stored := storedPendingGrant(t)
	owner := user.Address

	// Idempotent: marking twice keeps the first record.
	require.NoError(t, MarkSessionGrantAppliedByID(engine.db, testPolicyChain, owner, wallet, stored.ID, "0xfirst"))
	require.NoError(t, MarkSessionGrantAppliedByID(engine.db, testPolicyChain, owner, wallet, stored.ID, "0xsecond"))
	updated, err := engine.GetSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.Equal(t, "0xfirst", updated.Grant.AppliedUserOpHash)

	// Revoked-in-flight: the record moved on; a late mark must not resurrect
	// it as active.
	_, cleanupRequired, cleanup, err := engine.RevokeSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.True(t, cleanupRequired, "an applied grant's revoke leaves on-chain state")
	require.NotNil(t, cleanup, "applied revoke must hand back the uninstall call")
	require.Equal(t, stored.EntityID, cleanup.EntityID)
	require.Equal(t, wallet, cleanup.Target)
	require.NotEmpty(t, cleanup.CallData)
	require.NoError(t, MarkSessionGrantAppliedByID(engine.db, testPolicyChain, owner, wallet, stored.ID, "0xlate"))
	after, err := ListSessionPolicies(engine.db, testPolicyChain, owner)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, model.SessionPolicyRevoked, after[0].Status, "a late mark must never resurrect a revoked grant")

	// Deleted-in-flight (a PENDING grant revoked while its op was in the
	// mempool): no record left; the mark is a no-op, not an error.
	require.NoError(t, MarkSessionGrantAppliedByID(engine.db, testPolicyChain, owner, wallet, "01jarbitrarymissingpolicyid", ""))
}
