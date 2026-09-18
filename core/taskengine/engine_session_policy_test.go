package taskengine

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
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

// A grant naming a chain this gateway does not serve is refused at prepare —
// before any owner signature is collected — and again at submit, which
// re-derives the grant from the client's echo and so cannot inherit prepare's
// verdict on a body that names a different chain. Letting one through would
// store sp:<chain>:* records nothing here ever reads back, for authority no
// configured bundler could ever send under.
func TestSessionPolicyRefusesUnservedChain(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	// Base mainnet: a real chain, deliberately not one config/test.yaml serves.
	const unservedChain = int64(8453)
	require.False(t, engine.isChainConfigured(unservedChain), "fixture must not serve the chain under test")

	_, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: unservedChain,
		AgentLabel: "TradingBot", Permissions: testPermissions(),
	})
	require.ErrorIs(t, err, ErrSessionChainNotServed)

	// Submit is gated on its own: prepare against a chain we serve, then echo
	// a body naming one we do not.
	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "TradingBot", Permissions: testPermissions(),
	})
	require.NoError(t, err)
	_, _, err = engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: unservedChain,
		AgentLabel: "TradingBot", Permissions: testPermissions(),
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, ownerKey, prepared.Digest))
	require.ErrorIs(t, err, ErrSessionChainNotServed)

	stranded, err := ListSessionPolicies(db, unservedChain, owner)
	require.NoError(t, err)
	require.Empty(t, stranded, "a refused grant must leave nothing under sp:%d:*", unservedChain)
}

func TestPrepareSessionPolicyLooksUpEachRecipientOnce(t *testing.T) {
	engine, _, _, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	var lookups int
	perms := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
		CodeAt: func(common.Address) ([]byte, error) {
			lookups++
			return nil, nil
		},
	}
	_, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "SendETH", Permissions: perms,
	})
	require.NoError(t, err)
	require.Equal(t, 1, lookups, "Validate+HooksFor must share one CodeAt per recipient")
}

func TestPrepareSessionPolicyRefusesControllerAsNativeRecipient(t *testing.T) {
	engine, _, _, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	signer, err := engine.sessionSignerAddress()
	require.NoError(t, err)
	perms := SessionPermissions{
		NativeRecipients: []*common.Address{&signer},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
		CodeAt:           func(common.Address) ([]byte, error) { return nil, nil },
	}
	_, err = engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "SendETH", Permissions: perms,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "session signer")
}

func TestSessionPolicyPrepareSubmitNativeRoundTrip(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	perms := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
		CodeAt:           func(common.Address) ([]byte, error) { return nil, nil },
	}

	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "SendETH", Permissions: perms,
	})
	require.NoError(t, err)
	require.True(t, prepared.Policy.Grant.RequiresExecuteUserOp)
	require.Equal(t, alice, *prepared.Policy.NativeRecipients[0])

	stored, _, err := engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "SendETH", Permissions: perms,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, ownerKey, prepared.Digest))
	require.NoError(t, err)
	require.Equal(t, "10000000000000000", stored.NativeSpendCap.GrantedCap)
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

func TestSessionPolicyRevokePendingRetainsForPossibleLateInstall(t *testing.T) {
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

	// Pending with InstallCall is retained as revoked so a late-landing install
	// can mark AppliedAt without resurrecting usable status (#717 orphan).
	deleted, cleanupRequired, cleanup, err := engine.RevokeSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.False(t, deleted, "InstallCall must survive for possible late apply / cleanup")
	require.False(t, cleanupRequired, "entity is not known on chain until AppliedAt")
	require.Nil(t, cleanup)

	got, err := engine.GetSessionPolicyByID(user, testPolicyChain, wallet, stored.ID)
	require.NoError(t, err)
	require.Equal(t, model.SessionPolicyRevoked, got.Status)
	require.False(t, got.Usable())
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

	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")
	two := base
	two.AllowedActions = append(two.AllowedActions, model.AllowedAction{Target: &weth, Selectors: []string{"0x095ea7b3"}})
	two.SpendCaps = []model.ERC20SpendCap{
		{Token: &token, Amount: "500000000"},
		{Token: &weth, Amount: "1"},
	}
	two.SpendCap = &two.SpendCaps[0]
	require.NoError(t, two.Validate())

	mismatch := two
	mismatch.SpendCap = &model.ERC20SpendCap{Token: &token, Amount: "1"}
	require.Error(t, mismatch.Validate(), "singular cap must match an erc20SpendCaps entry")

	dup := two
	dup.SpendCaps = []model.ERC20SpendCap{
		{Token: &token, Amount: "1"},
		{Token: &token, Amount: "2"},
	}
	dup.SpendCap = nil
	require.Error(t, dup.Validate(), "duplicate cap tokens")

	padded := two
	padded.SpendCap = &model.ERC20SpendCap{Token: &token, Amount: "0500000000"}
	require.NoError(t, padded.Validate(), "alias amount must compare as integer, not string")

	spaced := two
	spaced.SpendCap = &model.ERC20SpendCap{Token: &token, Amount: " 500000000 "}
	require.Error(t, spaced.Validate(), "whitespace in amount must not match OpenAPI ^[0-9]+$")

	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	native := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(time.Hour).UnixMilli(),
		CodeAt:           func(common.Address) ([]byte, error) { return nil, nil },
	}
	require.NoError(t, native.Validate())

	noCap := native
	noCap.NativeSpendCap = nil
	require.Error(t, noCap.Validate(), "nativeRecipients require a nativeSpendCap")

	tooMany := native
	recs := make([]*common.Address, MaxNativeRecipients+1)
	for i := range recs {
		a := common.BigToAddress(big.NewInt(int64(i + 1)))
		recs[i] = &a
	}
	tooMany.NativeRecipients = recs
	require.Error(t, tooMany.Validate(), "max 5 native recipients")

	module := native
	mod := aa.AllowlistModuleAddress()
	module.NativeRecipients = []*common.Address{&mod}
	require.Error(t, module.Validate(), "known module")

	overlap := base
	overlap.NativeRecipients = []*common.Address{&token}
	overlap.NativeSpendCap = &model.NativeSpendCap{Amount: "1"}
	overlap.CodeAt = native.CodeAt
	require.Error(t, overlap.Validate(), "native recipient must not be an allowed-action target")

	noReader := native
	noReader.CodeAt = nil
	err := noReader.Validate()
	require.Error(t, err, "fail closed without CodeAt")
	require.Contains(t, err.Error(), "InstallSessionResolver")

	controller := native
	controller.SessionSigner = &alice
	require.Error(t, controller.Validate(), "session signer is not a native recipient")

	contract := native
	contract.CodeAt = func(common.Address) ([]byte, error) { return []byte{0x60, 0x00}, nil }
	require.Error(t, contract.Validate(), "contract recipient")

	flagged := contract
	flagged.AllowContractRecipient = true
	require.NoError(t, flagged.Validate(), "allowContractRecipient skips code check")

	neither := SessionPermissions{ValidUntilMs: time.Now().Add(time.Hour).UnixMilli()}
	require.Error(t, neither.Validate(), "needs allowedActions and/or nativeRecipients")
}

func TestMaxNativeRecipientsFollowsReplaceNotInstall(t *testing.T) {
	if MaxNativeRecipients != 5 {
		t.Fatalf("MaxNativeRecipients = %d, want 5 (A0: 20-row deferred replace AA23s)", MaxNativeRecipients)
	}
}

func TestAttachDeclaredPermissionsCapAlias(t *testing.T) {
	token := testTokenAddr
	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")
	until := time.Now().Add(time.Hour).UnixMilli()

	singular := testPermissions()
	policy := &model.SessionPolicy{}
	attachDeclaredPermissions(policy, singular)
	require.NotNil(t, policy.ERC20SpendCap)
	require.Empty(t, policy.ERC20SpendCaps, "singular-only grant must not invent erc20SpendCaps")

	listed := SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &token, Selectors: []string{"0xa9059cbb"}},
			{Target: &weth, Selectors: []string{"0xa9059cbb"}},
		},
		SpendCap: &model.ERC20SpendCap{Token: &weth, Amount: "1"},
		SpendCaps: []model.ERC20SpendCap{
			{Token: &token, Amount: "500000000"},
			{Token: &weth, Amount: "1"},
		},
		ValidUntilMs: until,
	}
	require.NoError(t, listed.Validate())
	listedPolicy := &model.SessionPolicy{}
	attachDeclaredPermissions(listedPolicy, listed)
	require.Len(t, listedPolicy.ERC20SpendCaps, 2)
	require.NotNil(t, listedPolicy.ERC20SpendCap.Token)
	require.Equal(t, weth, *listedPolicy.ERC20SpendCap.Token, "alias must keep the submitted singular, not list[0]")

	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	native := SessionPermissions{
		NativeRecipients:       []*common.Address{&alice},
		NativeSpendCap:         &model.NativeSpendCap{Amount: "10000000000000000"},
		AllowContractRecipient: true,
		ValidUntilMs:           until,
	}
	nativePolicy := &model.SessionPolicy{}
	attachDeclaredPermissions(nativePolicy, native)
	require.Len(t, nativePolicy.NativeRecipients, 1)
	require.Equal(t, "10000000000000000", nativePolicy.NativeSpendCap.GrantedCap)
	require.True(t, nativePolicy.AllowContractRecipient)
}
