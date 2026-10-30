package rest

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// S4 exercises JSON binding, permission guard, real REST handlers, engine,
// persistence and blocking execution. The node is deliberately local: proving
// that no end date works must not depend on provider or testnet availability.
func TestWalletBudgetManualWorkflowWithoutEndDateOverHTTP(t *testing.T) {
	r := newPolicyRig(t)
	t.Cleanup(r.server.engine.Stop)
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("auth.user", &restmw.AuthenticatedUser{Subject: r.owner.Hex(), ChainID: policyTestChain})
			return next(c)
		}
	})
	e.POST("/api/v1/workflows", r.server.CreateWorkflow)
	e.POST("/api/v1/workflows/:id/trigger", func(c echo.Context) error { return r.server.TriggerWorkflow(c, generated.Ulid(c.Param("id"))) })
	body := map[string]interface{}{
		"name":           "budget spike, no end date",
		"trigger":        map[string]interface{}{"id": "start", "name": "start", "type": "manual", "config": map[string]interface{}{"lang": "json", "data": map[string]interface{}{}}},
		"nodes":          []interface{}{map[string]interface{}{"id": "check", "name": "check", "type": "customCode", "config": map[string]interface{}{"lang": "javascript", "source": "return {answer: 42};"}}},
		"edges":          []interface{}{map[string]interface{}{"id": "start-check", "source": "start", "target": "check"}},
		"inputVariables": map[string]interface{}{"settings": map[string]interface{}{"runner": r.wallet.Hex(), "chain_id": policyTestChain}},
	}
	call := func(path string, body interface{}) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	created := call("/api/v1/workflows", body)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var workflow generated.Workflow
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &workflow))
	stored, err := r.server.engine.GetWorkflow(&model.User{Address: r.owner}, string(workflow.Id))
	require.NoError(t, err)
	require.Zero(t, stored.ExpiredAt)
	require.True(t, stored.IsRunable())
	run := call("/api/v1/workflows/"+string(workflow.Id)+"/trigger", map[string]interface{}{"triggerType": "manual", "isBlocking": true})
	require.Equal(t, http.StatusOK, run.Code, run.Body.String())
	var execution map[string]interface{}
	require.NoError(t, json.Unmarshal(run.Body.Bytes(), &execution))
	require.Equal(t, "success", execution["status"], run.Body.String())
	require.NotEmpty(t, execution["steps"])
	stored, err = r.server.engine.GetWorkflow(&model.User{Address: r.owner}, string(workflow.Id))
	require.NoError(t, err)
	require.Zero(t, stored.ExpiredAt, "running must not invent an expiry")

	t.Run("expired wallet permission refuses a fund-moving run", func(t *testing.T) {
		// Keep deployment checking off to reach the RUN guard. An end date on
		// the workflow cannot be responsible for refusing this execution.
		oldDeployCheck := r.server.config.SessionPolicyDeployCheck
		t.Cleanup(func() { r.server.config.SessionPolicyDeployCheck = oldDeployCheck })
		r.server.config.SessionPolicyDeployCheck = false
		r.server.engine.InstallSessionResolver()
		t.Cleanup(func() { preset.SetSessionResolver(nil); preset.SetEOA7702AccountLookup(nil) })
		signer := crypto.PubkeyToAddress(r.server.config.SessionSignerPrivateKey.PublicKey)
		token := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
		policy := &model.SessionPolicy{ID: "01budgetexpiry000000000000", ChainID: policyTestChain, Owner: &r.owner, Runner: &r.wallet, SessionSigner: &signer,
			EntityID: 7, Status: model.SessionPolicyActive, ValidUntil: time.Now().Add(-time.Minute).UnixMilli(),
			AllowedActions: []model.AllowedAction{{Target: &token, Selectors: []string{"0x095ea7b3"}}},
			Grant:          &model.SessionGrantAuthorization{InstallCall: []byte{1}, AppliedAt: time.Now().Add(-time.Hour).UnixMilli(), Deadline: uint64(time.Now().Add(time.Hour).Unix()), OwnerSignature: make([]byte, 65), CarrierNonce: big.NewInt(1)}}
		require.NoError(t, taskengine.StoreSessionPolicy(r.db, policy))
		body["nodes"] = []interface{}{map[string]interface{}{"id": "check", "name": "check", "type": "contractWrite", "config": map[string]interface{}{
			"chainId": policyTestChain, "contractAddress": token.Hex(),
			"contractAbi": []interface{}{map[string]interface{}{"type": "function", "name": "approve", "stateMutability": "nonpayable", "inputs": []interface{}{map[string]interface{}{"name": "spender", "type": "address"}, map[string]interface{}{"name": "amount", "type": "uint256"}}, "outputs": []interface{}{map[string]interface{}{"type": "bool"}}}},
			"methodCalls": []interface{}{map[string]interface{}{"methodName": "approve", "methodParams": []string{"0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", "0"}}},
		}}}
		created := call("/api/v1/workflows", body)
		require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
		require.NoError(t, json.Unmarshal(created.Body.Bytes(), &workflow))
		stored, err := r.server.engine.GetWorkflow(&model.User{Address: r.owner}, string(workflow.Id))
		require.NoError(t, err)
		require.Zero(t, stored.ExpiredAt)
		refused := call("/api/v1/workflows/"+string(workflow.Id)+"/trigger", map[string]interface{}{"triggerType": "manual", "isBlocking": true})
		require.Contains(t, refused.Body.String(), taskengine.SessionPolicyExpiredCode)
		var failed map[string]interface{}
		require.NoError(t, json.Unmarshal(refused.Body.Bytes(), &failed))
		require.Equal(t, "failed", failed["status"], refused.Body.String())
		// Characterize the known phase 1 gap explicitly. When budget verdicts
		// fix it, update these assertions to require the actionable message.
		steps, ok := failed["steps"].([]interface{})
		require.True(t, ok)
		require.NotEmpty(t, steps)
		last, ok := steps[len(steps)-1].(map[string]interface{})
		require.True(t, ok)
		require.Equal(t, "INVALID_REQUEST", last["errorCode"])
		message, ok := last["error"].(string)
		require.True(t, ok)
		require.Contains(t, message, taskengine.SessionPolicyExpiredCode)
		require.True(t, len(message) >= 3 && message[len(message)-3:] == "...", message)
		require.NotContains(t, message, "grant again")
		t.Log("S4 GAP asserted: truncated expiry message omits the renewal instruction")
	})
}
