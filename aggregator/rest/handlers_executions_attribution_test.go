package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestListExecutionsKeepsWorkflowAttributionAcrossPages(t *testing.T) {
	for _, staleField := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical rows", true: "stale embedded workflow"}[staleField], func(t *testing.T) {
			db := testutil.TestMustDB()
			t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
			cfg := testutil.GetAggregatorConfig()
			server := &Server{engine: taskengine.New(db, cfg, nil, testutil.GetLogger()), logger: testutil.GetLogger(), config: cfg}
			owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
			workflows := []generated.Ulid{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
			executionIDs := []string{"01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY"}
			for i, id := range workflows {
				workflow := &model.Workflow{Task: &avsproto.Task{Id: string(id), Owner: owner.Hex(), Status: avsproto.TaskStatus_Completed}}
				taskBytes, err := protojson.Marshal(workflow.Task)
				require.NoError(t, err)
				require.NoError(t, db.Set(taskengine.WorkflowStorageKey(workflow.Id, workflow.Status), taskBytes))
				execution := &avsproto.Execution{Id: executionIDs[i], StartAt: int64(i + 1), Status: avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS}
				if i == 1 && staleField {
					execution.WorkflowId = string(workflows[0])
				} // Storage key, not stale embedded data, owns attribution.
				data, err := protojson.Marshal(execution)
				require.NoError(t, err)
				key := taskengine.TaskExecutionKey(workflow, execution.Id)
				require.NoError(t, db.Set(key, data))
			}
			list := func(params generated.ListExecutionsParams) generated.ExecutionList {
				ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/executions", nil), httptest.NewRecorder())
				ctx.Set("auth.user", &restmw.AuthenticatedUser{Subject: owner.Hex(), ChainID: 1})
				require.NoError(t, server.ListExecutions(ctx, params))
				var out generated.ExecutionList
				require.NoError(t, json.Unmarshal(ctx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &out))
				return out
			}
			limit := generated.PageLimit(1)
			first := list(generated.ListExecutionsParams{WorkflowId: &workflows, Limit: &limit})
			require.Len(t, first.Data, 1)
			require.Equal(t, workflows[1], first.Data[0].WorkflowId)
			require.True(t, first.PageInfo.HasNextPage)
			after := generated.PageAfter(*first.PageInfo.EndCursor)
			second := list(generated.ListExecutionsParams{WorkflowId: &workflows, Limit: &limit, After: &after})
			require.Len(t, second.Data, 1)
			require.Equal(t, workflows[0], second.Data[0].WorkflowId)
			before := generated.PageBefore(*second.PageInfo.StartCursor)
			back := list(generated.ListExecutionsParams{WorkflowId: &workflows, Limit: &limit, Before: &before})
			require.Len(t, back.Data, 1)
			require.Equal(t, workflows[1], back.Data[0].WorkflowId)
			all := list(generated.ListExecutionsParams{WorkflowId: &workflows})
			require.Len(t, all.Data, 2)
			require.Equal(t, workflows[1], all.Data[0].WorkflowId)
			require.Equal(t, workflows[0], all.Data[1].WorkflowId)
		})
	}
}

func TestExecutionListFallbackPreservesOwnIdentity(t *testing.T) {
	malformedSummary, err := structpb.NewValue(map[string]any{"subject": 123, "body": map[string]any{}})
	require.NoError(t, err)
	own := &avsproto.Execution{Id: "execution", WorkflowId: "own", Summary: malformedSummary}
	out, err := executionsRespToOpenAPI(&avsproto.ListExecutionsResp{Items: []*avsproto.Execution{own}}, "other")
	require.NoError(t, err)
	require.Len(t, out.Data, 1)
	require.Equal(t, generated.Ulid("own"), out.Data[0].WorkflowId)
	require.Equal(t, generated.ExecutionStatus("error"), out.Data[0].Status)
	old := &avsproto.Execution{Id: "old"}
	out, err = executionsRespToOpenAPI(&avsproto.ListExecutionsResp{Items: []*avsproto.Execution{old}}, "path-workflow")
	require.NoError(t, err)
	require.Equal(t, generated.Ulid("path-workflow"), out.Data[0].WorkflowId)
	_, err = executionsRespToOpenAPI(&avsproto.ListExecutionsResp{Items: []*avsproto.Execution{old}}, "")
	require.ErrorContains(t, err, "no owning workflow")
}
