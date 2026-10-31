package rest

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func TestWalletBudgetPendingOverHTTP(t *testing.T) {
	r := newPolicyRig(t)
	signer := r.owner
	policy := &model.SessionPolicy{ID: "01budgetpending", Owner: &r.owner, Runner: &r.wallet, ChainID: policyTestChain, EntityID: 9,
		Status: model.SessionPolicyPending, ValidUntil: 123456, SessionSigner: &signer,
		Grant:          &model.SessionGrantAuthorization{InstallCall: []byte{1}, OwnerSignature: []byte{1}, CarrierNonce: big.NewInt(1)},
		NativeSpendCap: &model.NativeSpendCap{Amount: "500", GrantedCap: "500"}}
	require.NoError(t, taskengine.StoreSessionPolicy(r.db, policy))
	rec := r.call(t, nil, func(c echo.Context) error {
		return r.server.GetWalletBudget(c, generated.EthereumAddress(r.wallet.Hex()), generated.GetWalletBudgetParams{})
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "workflow", body["sizing"])
	require.Equal(t, "pending", body["status"])
	require.Equal(t, "500", body["native"].(map[string]interface{})["remaining"])
	require.NotContains(t, body["native"], "gasWei")
	require.NotContains(t, body, "liveApprovals")
	require.NotContains(t, body, "pendingChange")
	rec = r.call(t, nil, func(c echo.Context) error {
		return r.server.GetWalletBudget(c, generated.EthereumAddress(r.wallet.Hex()), generated.GetWalletBudgetParams{})
	}, map[string]string{"X-Partner-Assertion": "present"})
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	policy.Status = model.SessionPolicyActive
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	t.Cleanup(rpc.Close)
	r.server.config.SmartWallet.EthRpcUrl = rpc.URL
	require.NoError(t, taskengine.StoreSessionPolicy(r.db, policy))
	rec = r.call(t, nil, func(c echo.Context) error {
		return r.server.GetWalletBudget(c, generated.EthereumAddress(r.wallet.Hex()), generated.GetWalletBudgetParams{})
	}, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), taskengine.BudgetReadUnavailableCode)
}

func TestWalletBudgetMissingPermissionAndOwnership(t *testing.T) {
	r := newPolicyRig(t)
	rec := r.call(t, nil, func(c echo.Context) error {
		return r.server.GetWalletBudget(c, generated.EthereumAddress(r.wallet.Hex()), generated.GetWalletBudgetParams{})
	}, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	foreignOwner, foreignWallet := common.HexToAddress("0x1234"), common.HexToAddress("0x9999")
	policy := &model.SessionPolicy{ID: "01foreignbudget", Owner: &foreignOwner, Runner: &foreignWallet, ChainID: policyTestChain, EntityID: 7,
		Status: model.SessionPolicyPending, ValidUntil: 123456, NativeSpendCap: &model.NativeSpendCap{Amount: "987654321"}, SessionSigner: &foreignOwner,
		Grant: &model.SessionGrantAuthorization{InstallCall: []byte{1}, OwnerSignature: []byte{1}, CarrierNonce: big.NewInt(1)}}
	require.NoError(t, taskengine.StoreWallet(r.db, policyTestChain, foreignOwner, &model.SmartWallet{Owner: &foreignOwner, Address: &foreignWallet}))
	require.NoError(t, taskengine.StoreSessionPolicy(r.db, policy))
	rec = r.call(t, nil, func(c echo.Context) error {
		return r.server.GetWalletBudget(c, generated.EthereumAddress(foreignWallet.Hex()), generated.GetWalletBudgetParams{})
	}, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), policy.NativeSpendCap.Amount, "a stored foreign budget must never leak")
	rec = r.call(t, nil, func(c echo.Context) error {
		return r.server.GetWalletBudget(c, "0x0000000000000000000000000000000000000099", generated.GetWalletBudgetParams{})
	}, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
