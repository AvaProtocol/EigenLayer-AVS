package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestBudgetRetiredGrantProblemCarriesOwnerCleanupAtBothRefusalSites(t *testing.T) {
	wallet := common.HexToAddress("0x1111")
	data, err := aa.PackSessionSignerUninstall(99, nil)
	require.NoError(t, err)
	for _, stage := range []string{"prepare", "submit"} {
		t.Run(stage, func(t *testing.T) {
			refusal := &taskengine.BudgetPolicyError{Code: taskengine.BudgetRetiredGrantInstalledCode, Detail: "Remove the old permission with the owner wallet, then check again.", OnChainCleanup: &taskengine.OnChainRevokeCleanup{EntityID: 99, Target: wallet, CallData: data, ChainID: 11155111}}
			input := fmt.Errorf("private inventory context: %w", refusal)
			mapper := mapPolicyError
			if stage == "submit" {
				mapper = mapBudgetSubmitError
				input = &taskengine.BudgetSubmitError{SubmissionStarted: false, Cause: input}
			}
			e := echo.New()
			e.HTTPErrorHandler = restmw.ProblemErrorHandler(nil)
			e.POST("/", func(echo.Context) error { return mapper(input) })
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
			require.Equal(t, http.StatusConflict, rec.Code)
			var problem generated.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
			require.Equal(t, taskengine.BudgetRetiredGrantInstalledCode, *problem.Code)
			require.NotContains(t, *problem.Detail, "private inventory")
			require.NotContains(t, *problem.Detail, "paused")
			require.NotNil(t, problem.OnChainCleanup)
			require.EqualValues(t, 99, problem.OnChainCleanup.EntityId)
			require.Equal(t, wallet.Hex(), string(problem.OnChainCleanup.Target))
			require.Equal(t, hexutil.Encode(data), problem.OnChainCleanup.CallData)
			require.EqualValues(t, 11155111, problem.OnChainCleanup.ChainId)
			if stage == "submit" {
				require.NotNil(t, problem.SubmissionStarted)
				require.False(t, *problem.SubmissionStarted)
			}
		})
	}
}

func TestOtherBudgetRefusalsCannotCarryRetiredGrantCleanup(t *testing.T) {
	for _, code := range []string{taskengine.BudgetAuthorityUnknownCode, taskengine.BudgetChangingCode, taskengine.BudgetFeeInsufficientCode} {
		refusal := &taskengine.BudgetPolicyError{Code: code, Detail: "Refused.", OnChainCleanup: &taskengine.OnChainRevokeCleanup{EntityID: 99}}
		var problem *restmw.HTTPError
		require.ErrorAs(t, mapPolicyError(refusal), &problem)
		require.Nil(t, problem.OnChainCleanup)
	}
}
