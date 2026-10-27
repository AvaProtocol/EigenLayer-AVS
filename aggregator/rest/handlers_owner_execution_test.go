package rest

import (
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
		httpStatus int
	}{
		{codes.InvalidArgument, http.StatusBadRequest},
		{codes.PermissionDenied, http.StatusForbidden},
		{codes.NotFound, http.StatusNotFound},
		{codes.FailedPrecondition, http.StatusPreconditionFailed},
		{codes.ResourceExhausted, http.StatusTooManyRequests},
	} {
		var httpErr *restmw.HTTPError
		require.ErrorAs(t, mapOwnerExecutionError(status.Error(tc.code, "failure")), &httpErr)
		require.Equal(t, tc.httpStatus, httpErr.Status)
	}
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
	require.Equal(t, http.StatusPreconditionFailed, httpErr.Status)
	require.Contains(t, httpErr.Detail, "disabled")
}
