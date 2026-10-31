package rest

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func TestBudgetPolicyRoutesRefuseStatelessSigning(t *testing.T) {
	r := newPolicyRig(t)
	budget := map[string]interface{}{"validUntil": time.Now().Add(time.Hour).UnixMilli(), "erc20": []interface{}{map[string]interface{}{"token": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", "amount": "1000"}}}
	body := map[string]interface{}{"chainId": policyTestChain, "agentLabel": "Budget", "budget": budget}
	rec := r.call(t, body, func(c echo.Context) error {
		return r.server.PrepareWalletPolicy(c, generated.EthereumAddress(r.wallet.Hex()))
	}, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), taskengine.BudgetHandoffRequiredCode)
	require.NotContains(t, rec.Body.String(), "typedData")
	require.NotContains(t, rec.Body.String(), "digest")
	rec = r.call(t, body, func(c echo.Context) error {
		return r.server.SubmitWalletPolicy(c, generated.EthereumAddress(r.wallet.Hex()))
	}, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), taskengine.BudgetHandoffRequiredCode)
	budget["erc20"] = []interface{}{map[string]interface{}{"token": "garbage-address-that-must-not-be-coerced", "amount": "1000"}}
	rec = r.call(t, body, func(c echo.Context) error {
		return r.server.PrepareWalletPolicy(c, generated.EthereumAddress(r.wallet.Hex()))
	}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), taskengine.BudgetInvalidInputCode)
}

func TestBudgetPolicyHTTPGuardsFullAdditiveAndOldSignedRequests(t *testing.T) {
	for _, expired := range []bool{false, true} {
		r := newPolicyRig(t)
		address := generated.EthereumAddress(r.wallet.Hex())
		body := prepareBody(policyTestChain)
		rec := r.call(t, body, func(c echo.Context) error { return r.server.PrepareWalletPolicy(c, address) }, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var prepared generated.PreparedPolicy
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prepared))
		sig, err := crypto.Sign(common.FromHex(prepared.Digest), r.ownerKey)
		require.NoError(t, err)
		sig[64] += 27
		oldSubmit := generated.SubmitPolicyRequest{ChainId: body.ChainId, PolicyId: prepared.PolicyId, EntityId: prepared.EntityId, Deadline: prepared.Deadline,
			ValidUntil: prepared.ValidUntil, AgentLabel: body.AgentLabel, AllowedActions: body.AllowedActions, Erc20SpendCap: body.Erc20SpendCap, Signature: fmt.Sprintf("0x%x", sig)}
		until := time.Now().Add(time.Hour).UnixMilli()
		if expired {
			until = time.Now().Add(-time.Minute).UnixMilli()
		}
		p := &model.SessionPolicy{ID: "01budgethttp", Owner: &r.owner, Runner: &r.wallet, ChainID: policyTestChain, EntityID: 9,
			Status: model.SessionPolicyActive, Sizing: model.BudgetSizingBudget, ValidUntil: until, SessionSigner: &r.owner,
			Grant: &model.SessionGrantAuthorization{InstallCall: []byte{1}, OwnerSignature: []byte{1}, CarrierNonce: big.NewInt(1), AppliedAt: 1}}
		require.NoError(t, taskengine.StoreSessionPolicy(r.db, p))
		// No basePolicyId is sent: each entry point must discover the budget.
		rec = r.call(t, body, func(c echo.Context) error { return r.server.PrepareWalletPolicy(c, address) }, nil)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), taskengine.BudgetReplaceRefusedCode)
		body.Add = &generated.SessionPolicyAddition{}
		rec = r.call(t, body, func(c echo.Context) error { return r.server.PrepareWalletPolicy(c, address) }, nil)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), taskengine.BudgetHandoffRequiredCode)
		rec = r.call(t, oldSubmit, func(c echo.Context) error { return r.server.SubmitWalletPolicy(c, address) }, nil)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), taskengine.BudgetReplaceRefusedCode)
		policies, err := taskengine.ListSessionPolicies(r.db, policyTestChain, r.owner)
		require.NoError(t, err)
		require.Len(t, policies, 1)
		require.Equal(t, p.ID, policies[0].ID)
	}
}

func TestBudgetAuthorizationMapsReviewFields(t *testing.T) {
	token := common.HexToAddress("0x1234")
	auth := sessionAuthorizationToAPI(&taskengine.SessionAuthorization{Status: taskengine.AuthNotCovered, MissingTokens: []common.Address{token}, NeedsUntil: 1794000000000})
	require.Equal(t, []generated.EthereumAddress{generated.EthereumAddress(token.Hex())}, *auth.MissingTokens)
	require.EqualValues(t, 1794000000000, *auth.NeedsUntil)
	legacy := sessionAuthorizationToAPI(&taskengine.SessionAuthorization{Status: taskengine.AuthCovered})
	require.Nil(t, legacy.MissingTokens)
	require.Nil(t, legacy.NeedsUntil)
}
