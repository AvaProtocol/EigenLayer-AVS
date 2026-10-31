package taskengine

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func TestBudgetInputNeverBuildsOrSubmitsADeferredGrant(t *testing.T) {
	engine, db, _, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	in := SessionPolicyInput{Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Budget", Budget: &WalletBudgetInput{ValidUntil: time.Now().Add(time.Hour).UnixMilli()}}
	_, err := engine.PrepareSessionPolicy(user, in)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetHandoffRequiredCode, refusal.Code)
	_, _, err = engine.SubmitSessionPolicy(user, in, "", 0, 0, nil)
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetHandoffRequiredCode, refusal.Code)
	policies, err := ListSessionPolicies(db, testPolicyChain, owner)
	require.NoError(t, err)
	require.Empty(t, policies)
}

func TestBudgetWalletRejectsLegacyPrepareAndSignedReplacement(t *testing.T) {
	engine, db, key, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	current, _ := grantOn(t, engine, key, owner, wallet)
	in := SessionPolicyInput{Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Legacy replacement", Permissions: testPermissions(), ExpiresInSeconds: 3600}
	prepared, err := engine.PrepareSessionPolicy(user, in)
	require.NoError(t, err)
	sig := signDigest(t, key, prepared.Digest)
	current.Sizing = model.BudgetSizingBudget
	current.BudgetStartedAt = time.Now().Add(-time.Hour).UnixMilli()
	current.BudgetStartAmounts = []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "500000000"}}
	current.Status = model.SessionPolicyActive
	current.Grant.AppliedAt = 1
	require.NoError(t, StoreSessionPolicy(db, current))
	_, err = engine.PrepareSessionPolicy(user, in)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetReplaceRefusedCode, refusal.Code)
	_, _, err = engine.SubmitSessionPolicy(user, in, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, sig)
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetReplaceRefusedCode, refusal.Code)
	in.Addition = &PolicyAddition{}
	_, err = engine.PrepareSessionPolicy(user, in)
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetHandoffRequiredCode, refusal.Code)
	policies, err := ListSessionPolicies(db, testPolicyChain, owner)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, current.ID, policies[0].ID)
	require.Equal(t, model.BudgetSizingBudget, policies[0].Sizing)
	// A valid old signature cannot race a budget switch and change its rows.
	require.Equal(t, big.NewInt(500000000).String(), policies[0].BudgetStartAmounts[0].Amount)
}
