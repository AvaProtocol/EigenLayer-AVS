package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestBudgetSubmitProblemKeepsServerCauseAndSendBoundary(t *testing.T) {
	cause := errors.New("private RPC endpoint is behind block 123")
	for _, started := range []bool{false, true} {
		err := &taskengine.BudgetSubmitError{SubmissionStarted: started, Cause: fmt.Errorf("%w: owner identity: %w", taskengine.ErrBudgetReadUnavailable, cause)}
		var problem *restmw.HTTPError
		require.ErrorAs(t, mapBudgetSubmitError(err), &problem)
		require.Equal(t, http.StatusServiceUnavailable, problem.Status)
		require.Equal(t, "Budget couldn't be submitted", problem.Title)
		require.ErrorIs(t, problem, cause)
		require.Contains(t, problem.Error(), cause.Error(), "server log retains diagnostic cause")
		e := echo.New()
		e.HTTPErrorHandler = restmw.ProblemErrorHandler(nil)
		e.POST("/submit", func(echo.Context) error { return problem })
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/submit", nil))
		require.NotContains(t, rec.Body.String(), cause.Error())
		var body restmw.Problem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.NotNil(t, body.SubmissionStarted)
		require.Equal(t, started, *body.SubmissionStarted)
		var declared generated.Problem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &declared))
		require.NotNil(t, declared.SubmissionStarted)
		require.Equal(t, started, *declared.SubmissionStarted)
	}
	var preparation *restmw.HTTPError
	require.ErrorAs(t, mapPolicyError(taskengine.ErrBudgetReadUnavailable), &preparation)
	require.Nil(t, preparation.SubmissionStarted)
	require.Equal(t, "Budget couldn't be prepared", preparation.Title)
	require.Empty(t, preparation.OwnerDelegate)
}

func TestBudgetPrepareOwnerReadFailureKeepsCausePrivate(t *testing.T) {
	cause := errors.New("reading owner execution chain ID: private RPC endpoint is behind block 123")
	err := fmt.Errorf("%w: budget owner operation could not be prepared: %w", taskengine.ErrBudgetReadUnavailable, cause)
	var problem *restmw.HTTPError
	require.ErrorAs(t, mapPolicyError(err), &problem)
	require.Equal(t, http.StatusServiceUnavailable, problem.Status)
	require.Equal(t, taskengine.BudgetReadUnavailableCode, problem.Code)
	require.ErrorIs(t, problem, cause)
	e := echo.New()
	e.HTTPErrorHandler = restmw.ProblemErrorHandler(nil)
	e.POST("/prepare", func(echo.Context) error { return problem })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/prepare", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "private RPC")
	var body generated.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "Budget couldn't be prepared", body.Title)
	require.Nil(t, body.SubmissionStarted)
}

func TestOwnerDelegationProblemAcrossOwnerAndBudgetRoutes(t *testing.T) {
	delegate := common.HexToAddress("0x63c0c19a282a1B52b07dD5a65b58948A07DAE32B")
	refusal := &preset.OwnerDelegationError{Delegate: delegate, Cause: config.ErrEOADelegationMissing}
	for _, mapper := range []func(error) error{mapPolicyError, mapBudgetSubmitError, mapOwnerExecutionError} {
		var problem *restmw.HTTPError
		require.ErrorAs(t, mapper(fmt.Errorf("owner preparation: %w", refusal)), &problem)
		require.Equal(t, http.StatusConflict, problem.Status)
		require.Equal(t, "EOA_DELEGATION_MISSING", problem.Code)
		require.Equal(t, delegate.Hex(), problem.OwnerDelegate)
		require.NotContains(t, problem.Detail, "ef0100")
		require.NotContains(t, problem.Detail, "back to a regular account")
	}
}

func TestOwnerAccountProblemsKeepConfigurationAndPermanentRefusalsDistinct(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		status int
		code   string
	}{
		{"execute disabled", config.ErrEOA7702ExecuteDisabled, http.StatusForbidden, "OWNER_EXECUTION_DISABLED"},
		{"missing pin", config.ErrSMA7702Unavailable, http.StatusServiceUnavailable, "OWNER_ACCOUNT_UNAVAILABLE"},
		{"wrong RPC chain", preset.ErrOwnerConfiguration, http.StatusServiceUnavailable, "OWNER_ACCOUNT_UNAVAILABLE"},
		{"unsupported implementation or EntryPoint", preset.ErrOwnerAccountUnsupported, http.StatusConflict, "OWNER_ACCOUNT_UNSUPPORTED"},
		{"undelegated EOA", config.ErrEOADelegationMissing, http.StatusConflict, "EOA_DELEGATION_MISSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%w: private account/RPC detail", tc.cause)
			for _, mapper := range []func(error) error{mapPolicyError, mapBudgetSubmitError, mapOwnerExecutionError, mapDelegationError} {
				var p *restmw.HTTPError
				require.ErrorAs(t, mapper(cause), &p)
				require.Equal(t, tc.status, p.Status)
				require.Equal(t, tc.code, p.Code)
				require.ErrorIs(t, p, cause)
				require.NotContains(t, p.Detail, "Try again")
				require.NotContains(t, p.Detail, "back to a regular account")
				require.Empty(t, p.OwnerDelegate)
				e := echo.New()
				e.HTTPErrorHandler = restmw.ProblemErrorHandler(nil)
				e.POST("/prepare", func(echo.Context) error { return p })
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/prepare", nil))
				require.Equal(t, tc.status, rec.Code)
				require.NotContains(t, rec.Body.String(), "private account/RPC detail")
			}
		})
	}
}

func TestUnknownPolicyFailureKeepsCausePrivate(t *testing.T) {
	cause := errors.New("database/rpc at private service failed")
	for _, mapper := range []func(error) error{mapPolicyError, mapBudgetSubmitError} {
		var p *restmw.HTTPError
		require.ErrorAs(t, mapper(cause), &p)
		require.Equal(t, http.StatusServiceUnavailable, p.Status)
		require.Equal(t, "POLICIES_UNAVAILABLE", p.Code)
		require.ErrorIs(t, p, cause)
		require.NotContains(t, p.Detail, cause.Error())
	}
}

func TestPolicyValidationKeepsAuthoredReasonWithoutOuterDiagnostics(t *testing.T) {
	for _, text := range []string{"the signing window (deadline 123) has expired; prepare the grant again", "expiresInSeconds must be between 0 and 9223372036", "agent label is required"} {
		validation := &taskengine.PolicyValidationError{Cause: errors.New(text)}
		// Only the marked validation is public, not an outer private wrapper.
		for _, mapper := range []func(error) error{mapPolicyError, mapBudgetSubmitError} {
			var problem *restmw.HTTPError
			require.ErrorAs(t, mapper(fmt.Errorf("private diagnostic: %w", validation)), &problem)
			require.Equal(t, http.StatusBadRequest, problem.Status)
			require.Equal(t, "POLICIES_REJECTED", problem.Code)
			require.Equal(t, text, problem.Detail)
		}
	}
}
