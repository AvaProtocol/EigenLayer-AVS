package taskengine

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// The /policies engine surface, exercised fully offline: prepare, sign,
// submit, list, get, revoke. Nothing here touches a chain — the grant flow's
// whole point is that only the FIRST WORKFLOW OPERATION does.

const testPolicyChain = int64(11155111)

var (
	testTokenAddr  = common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	testRouterAddr = common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")
)

func newPolicyTestEngine(t *testing.T) (*Engine, storage.Storage, *ecdsa.PrivateKey, common.Address, common.Address) {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	// The full test config: Engine's constructor dials the configured RPC, so
	// a hand-built config with no URL panics. Nothing in the policy flow
	// itself touches the chain. The fixture config carries no controller key
	// and pins chain 1; both are overridable per its own comments — the key
	// is only ever used here as an ADDRESS (the assigned session signer).
	cfg := testutil.GetAggregatorConfig()
	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	cfg.SmartWallet.ControllerPrivateKey = controllerKey
	cfg.SmartWallet.ChainID = testPolicyChain
	engine := New(db, cfg, nil, testutil.GetLogger())

	ownerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	wallet := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	factory := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	require.NoError(t, StoreWallet(db, testPolicyChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &wallet, Factory: &factory, Salt: big.NewInt(0),
	}))
	return engine, db, ownerKey, owner, wallet
}

func testPermissions() SessionPermissions {
	token, router := testTokenAddr, testRouterAddr
	return SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &router, Selectors: []string{"0x04e45aaf"}}, // exactInputSingle
			{Target: &token, Selectors: []string{"0x095ea7b3"}},  // approve
		},
		SpendCap:     &model.ERC20SpendCap{Token: &token, Amount: "500000000"},
		ValidUntilMs: time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
	}
}

func signDigest(t *testing.T, key *ecdsa.PrivateKey, digest common.Hash) []byte {
	t.Helper()
	sig, err := crypto.Sign(digest.Bytes(), key)
	require.NoError(t, err)
	sig[64] += 27
	return sig
}

func TestSessionPolicyPrepareRejectsSimpleAccountRunner(t *testing.T) {
	engine, db, _, owner, _ := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	// Legacy v0.6 SimpleAccount factory (not MA v2).
	v06Factory := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	v06Wallet := common.HexToAddress("0x00000000000000000000000000000000000000b6")
	require.NoError(t, StoreWallet(db, testPolicyChain, owner, &model.SmartWallet{
		Owner: &owner, Address: &v06Wallet, Factory: &v06Factory, Salt: big.NewInt(0),
	}))

	_, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: v06Wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Justification: "should refuse",
		Permissions: testPermissions(),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionWalletNotMAv2)
}

func TestSessionPolicyPrepareSubmitRoundTrip(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	perms := testPermissions()

	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Justification: "swaps you approve",
		Permissions: perms,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, prepared.Policy.EntityID, "first grant takes entity 1")
	require.True(t, prepared.Policy.Grant.RequiresExecuteUserOp, "a cap installs an execution hook")

	// Nothing stored by prepare: an abandoned screen leaves no state.
	policies, err := engine.ListSessionPoliciesForWallet(user, testPolicyChain, wallet)
	require.NoError(t, err)
	require.Empty(t, policies)

	stored, _, err := engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Justification: "swaps you approve",
		Permissions: perms,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, ownerKey, prepared.Digest))
	require.NoError(t, err)
	require.Equal(t, model.SessionPolicyPending, stored.Status)
	require.Len(t, stored.Grant.OwnerSignature, 65)
	require.Equal(t, "500000000", stored.ERC20SpendCap.GrantedCap)
	require.Len(t, stored.AllowedActions, 2)

	got, err := engine.GetSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.Equal(t, stored.ID, got.ID)
}

// A tampered echo (here: a raised cap) produces different calldata, hence a
// different digest, hence a signature that recovers to a stranger. The exact
// property the stateless submit exists for.
func TestSessionPolicySubmitRejectsTamperedEcho(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	perms := testPermissions()

	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot", Permissions: perms,
	})
	require.NoError(t, err)
	sig := signDigest(t, ownerKey, prepared.Digest)

	tampered := testPermissions()
	tampered.SpendCap.Amount = "500000000000" // 1000x the signed cap

	_, _, err = engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot", Permissions: tampered,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, sig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "signed by", "the tamper must surface as a signature mismatch")
}

func TestSessionPolicySubmitRejectsWrongSigner(t *testing.T) {
	engine, _, _, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot", Permissions: testPermissions(),
	})
	require.NoError(t, err)

	strangerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	_, _, err = engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot", Permissions: testPermissions(),
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, strangerKey, prepared.Digest))
	require.Error(t, err)
	require.Contains(t, err.Error(), "signed by")
}

// Two prepares race the same entity; the slower submit must 409, not
// silently overwrite the faster grant's signer on chain.
func TestSessionPolicySubmitDetectsEntityRace(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	preparedA, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "A", Permissions: testPermissions(),
	})
	require.NoError(t, err)
	preparedB, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "B", Permissions: testPermissions(),
	})
	require.NoError(t, err)
	require.Equal(t, preparedA.Policy.EntityID, preparedB.Policy.EntityID, "both prepares saw entity 1")

	_, _, err = engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "A", Permissions: testPermissions(),
	}, preparedA.Policy.ID, preparedA.Policy.EntityID, preparedA.Policy.Grant.Deadline,
		signDigest(t, ownerKey, preparedA.Digest))
	require.NoError(t, err)

	_, _, err = engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "B", Permissions: testPermissions(),
	}, preparedB.Policy.ID, preparedB.Policy.EntityID, preparedB.Policy.Grant.Deadline,
		signDigest(t, ownerKey, preparedB.Digest))
	require.ErrorIs(t, err, ErrSessionEntityTaken)
}

func TestSessionPolicyOwnershipGate(t *testing.T) {
	engine, _, _, _, wallet := newPolicyTestEngine(t)
	strangerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	stranger := &model.User{Address: crypto.PubkeyToAddress(strangerKey.PublicKey)}

	_, err = engine.PrepareSessionPolicy(stranger, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "X", Permissions: testPermissions(),
	})
	require.ErrorIs(t, err, ErrWalletNotOwned)

	_, err = engine.ListSessionPoliciesForWallet(stranger, testPolicyChain, wallet)
	require.ErrorIs(t, err, ErrWalletNotOwned)
}

func TestSessionPolicyRevokePendingDeletesOutright(t *testing.T) {
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

	deleted, cleanupRequired, cleanup, err := engine.RevokeSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.True(t, deleted, "a pending grant was never on-chain; revoke deletes it")
	require.False(t, cleanupRequired)
	require.Nil(t, cleanup)

	_, err = engine.GetSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.True(t, errors.Is(err, ErrSessionPolicyNotFound))
}

func TestSessionPermissionsValidation(t *testing.T) {
	token := testTokenAddr
	base := testPermissions()

	capless := base
	capless.SpendCap = nil
	require.Error(t, capless.Validate(), "v1 requires a cap — it installs the exec hook that stops self-administration")

	uncovered := base
	other := common.HexToAddress("0x00000000000000000000000000000000000000f9")
	uncovered.SpendCap = &model.ERC20SpendCap{Token: &other, Amount: "1"}
	require.Error(t, uncovered.Validate(), "the cap token must be an allowed target")

	anyFunction := base
	anyFunction.AllowedActions = []model.AllowedAction{{Target: &token, Selectors: nil}}
	require.Error(t, anyFunction.Validate(), "any-function grants are not offered")

	expired := base
	expired.ValidUntilMs = time.Now().Add(-time.Hour).UnixMilli()
	require.Error(t, expired.Validate())
}
