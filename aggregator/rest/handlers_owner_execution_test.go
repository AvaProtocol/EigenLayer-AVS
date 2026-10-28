package rest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOwnerExecutionMountedRoutesRequireUserJWT(t *testing.T) {
	e := echo.New()
	s := &Server{config: &config.Config{}, logger: testutil.GetLogger()}
	s.Mount(e)
	for _, path := range []string{"/api/v1/workflows:prepare", "/api/v1/executions/" + model.GenerateID() + ":submit"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Partner-Assertion", "partner-alone-is-not-user-transaction-authority")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
}

func TestOwnerExecutionSignatureOnlyBodyIsStrict(t *testing.T) {
	s := &Server{}
	validSignature := "0x" + strings.Repeat("11", 65)
	for _, body := range []string{
		`{"signature":"` + validSignature + `","userOperation":{}}`,
		`{"signature":"` + validSignature + `","chainId":8453}`,
		`{"signature":"` + validSignature + `","nonce":"0x0"}`,
		`{"signature":"` + validSignature + `"} {}`,
		`{"signature":"0x12"}`,
		`null`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/executions/id:submit", strings.NewReader(body))
		ctx := echo.New().NewContext(req, httptest.NewRecorder())
		ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: "0x1111111111111111111111111111111111111111", ChainID: 11155111})
		err := s.SubmitExecution(ctx, generated.Ulid(model.GenerateID()))
		var httpErr *restmw.HTTPError
		require.ErrorAs(t, err, &httpErr)
		require.Equal(t, http.StatusBadRequest, httpErr.Status)
	}
}

func TestOwnerExecutionProblemMapping(t *testing.T) {
	for _, tc := range []struct {
		code       codes.Code
		message    string
		httpStatus int
		problem    string
	}{
		{codes.InvalidArgument, "failure", http.StatusBadRequest, "OWNER_EXECUTION_INVALID"},
		{codes.PermissionDenied, "failure", http.StatusForbidden, "OWNER_EXECUTION_SOURCE_FORBIDDEN"},
		{codes.NotFound, "failure", http.StatusNotFound, "OWNER_EXECUTION_NOT_FOUND"},
		{codes.FailedPrecondition, "failure", http.StatusPreconditionFailed, "OWNER_EXECUTION_PREPARE_REQUIRED"},
		{codes.FailedPrecondition, "owner operation was not sent; prepare and sign again", http.StatusPreconditionFailed, "OWNER_EXECUTION_PREPARE_REQUIRED"},
		{codes.FailedPrecondition, taskengine.OwnerExecutionDisabledDetail, http.StatusForbidden, "OWNER_EXECUTION_DISABLED"},
		{codes.FailedPrecondition, config.EOA7702ExecuteDisabledDetail, http.StatusForbidden, "OWNER_EXECUTION_DISABLED"},
		{codes.FailedPrecondition, "owner operation could not be prepared: " + config.EOA7702ExecuteDisabledDetail, http.StatusPreconditionFailed, "OWNER_EXECUTION_PREPARE_REQUIRED"},
		{codes.FailedPrecondition, "the feature is disabled for this wallet", http.StatusPreconditionFailed, "OWNER_EXECUTION_PREPARE_REQUIRED"},
		{codes.ResourceExhausted, "failure", http.StatusTooManyRequests, "OWNER_EXECUTION_LIMIT"},
		{codes.Unauthenticated, "owner JWT required", http.StatusUnauthorized, "OWNER_EXECUTION_UNAUTHENTICATED"},
		{codes.Unavailable, "bundler unavailable", http.StatusServiceUnavailable, "OWNER_EXECUTION_UNAVAILABLE"},
	} {
		var httpErr *restmw.HTTPError
		require.ErrorAs(t, mapOwnerExecutionError(status.Error(tc.code, tc.message)), &httpErr)
		require.Equal(t, tc.httpStatus, httpErr.Status, tc.message)
		require.Equal(t, tc.problem, httpErr.Code, tc.message)
	}
	err := (&Server{}).reportOwnerExecutionError(errors.New("database unavailable"))
	var httpErr *restmw.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadGateway, httpErr.Status)
	require.Equal(t, "OWNER_EXECUTION_FAILED", httpErr.Code)
	require.Empty(t, httpErr.Detail)
}

func TestOwnerPrepareDisabledIsActionable(t *testing.T) {
	// A concrete JWT is present; the new path stays disabled by default and
	// reaches neither a chain RPC nor the session signer.
	n := taskengine.New(nil, &config.Config{SmartWallet: &config.SmartWalletConfig{EthRpcUrl: "http://127.0.0.1:1", ChainID: 11155111}}, nil, testutil.GetLogger())
	s := &Server{engine: n}
	body := `{"chainId":11155111,"trigger":{"id":"start","name":"start","type":"manual","config":{"lang":"json","data":{}}},"nodes":[],"inputVariables":{"settings":{"runner":"0x2222222222222222222222222222222222222222"}}}`
	ctx := ctxWithUser("0x1111111111111111111111111111111111111111")
	ctx.SetRequest(httptest.NewRequest(http.MethodPost, "/api/v1/workflows:prepare", strings.NewReader(body)))
	err := s.PrepareWorkflow(ctx)
	var httpErr *restmw.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusForbidden, httpErr.Status)
	require.Equal(t, "OWNER_EXECUTION_DISABLED", httpErr.Code)
	require.Equal(t, taskengine.OwnerExecutionDisabledDetail, httpErr.Detail)
}
