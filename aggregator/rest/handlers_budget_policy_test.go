package rest

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
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

func TestBudgetAuthorizationMapsReviewFields(t *testing.T) {
	token := common.HexToAddress("0x1234")
	auth := sessionAuthorizationToAPI(&taskengine.SessionAuthorization{Status: taskengine.AuthNotCovered, MissingTokens: []common.Address{token}, NeedsUntil: 1794000000000})
	require.Equal(t, []generated.EthereumAddress{generated.EthereumAddress(token.Hex())}, *auth.MissingTokens)
	require.EqualValues(t, 1794000000000, *auth.NeedsUntil)
	legacy := sessionAuthorizationToAPI(&taskengine.SessionAuthorization{Status: taskengine.AuthCovered})
	require.Nil(t, legacy.MissingTokens)
	require.Nil(t, legacy.NeedsUntil)
}
