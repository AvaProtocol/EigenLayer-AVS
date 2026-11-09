package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Exercise the Sentry 3F payload through JSON binding, both real handlers, and
// the engine. An invalid graph must neither persist nor reach trigger execution.
func TestWorkflowGraphValidationOverHTTP(t *testing.T) {
	r := newPolicyRig(t)
	t.Cleanup(r.server.engine.Stop)
	e := echo.New()
	e.HTTPErrorHandler = restmw.ProblemErrorHandler(nil)
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("auth.user", &restmw.AuthenticatedUser{Subject: r.owner.Hex(), ChainID: policyTestChain})
			return next(c)
		}
	})
	e.POST("/api/v1/workflows", r.server.CreateWorkflow)
	e.POST("/api/v1/workflows:simulate", r.server.SimulateWorkflow)

	payload := func(triggerID, nodeID, source, target string) map[string]interface{} {
		trigger := map[string]interface{}{"name": "manualTrigger", "type": "manual", "config": map[string]interface{}{"lang": "json", "data": map[string]interface{}{}}}
		if triggerID != "" {
			trigger["id"] = triggerID
		}
		return map[string]interface{}{
			"name":           "Sentry 3F regression",
			"trigger":        trigger,
			"nodes":          []interface{}{map[string]interface{}{"id": nodeID, "name": "c1", "type": "customCode", "config": map[string]interface{}{"lang": "javascript", "source": "return 42;"}}},
			"edges":          []interface{}{map[string]interface{}{"id": "e1", "source": source, "target": target}},
			"inputVariables": map[string]interface{}{"settings": map[string]interface{}{"runner": r.wallet.Hex()}},
		}
	}

	branchPayload := func(lastConditionID string) map[string]interface{} {
		body := payload("__trigger", "c1", "__trigger", "branch")
		body["nodes"] = append(body["nodes"].([]interface{}), map[string]interface{}{
			"id": "branch", "name": "branch", "type": "branch",
			"config": map[string]interface{}{"conditions": []interface{}{
				map[string]interface{}{"id": "0", "type": "if", "expression": "false"},
				map[string]interface{}{"id": lastConditionID, "type": "else"},
			}},
		})
		body["edges"] = append(body["edges"].([]interface{}), map[string]interface{}{"id": "e2", "source": "branch.2", "target": "c1"})
		return body
	}

	call := func(path string, body map[string]interface{}) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/api/v1/workflows", "/api/v1/workflows:simulate"} {
		for _, graph := range []struct {
			name   string
			body   map[string]interface{}
			detail string
		}{
			{"missing trigger ID", payload("", "c1", "__trigger", "c1"), "set trigger.id"},
			{"mismatched trigger ID", payload("start", "c1", "__trigger", "c1"), `unknown source "__trigger"`},
			{"dangling target", payload("start", "c1", "start", "missing"), `unknown target node "missing"`},
			{"dotted trigger ID", payload("start.trigger", "c1", "start.trigger", "c1"), `trigger ID "start.trigger" must not contain '.'`},
			{"dotted node ID", payload("start", "code.1", "start", "code.1"), `node ID "code.1" must not contain '.'`},
			{"stale branch condition", branchPayload("1"), `branch "branch" has no condition "2"; remove the stale edge or reconnect it to an existing condition`},
		} {
			t.Run(path+"/"+graph.name, func(t *testing.T) {
				rec := call(path, graph.body)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				var problem restmw.Problem
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
				require.Contains(t, problem.Detail, "invalid workflow graph")
				require.Contains(t, problem.Detail, graph.detail)
				if path == "/api/v1/workflows:simulate" {
					require.Equal(t, "WORKFLOWS_SIMULATE_INVALID", problem.Code)
				}
				stored, err := r.db.GetByPrefix(taskengine.ChainWorkflowByStatusStoragePrefix(policyTestChain, avsproto.TaskStatus_Enabled))
				require.NoError(t, err)
				require.Empty(t, stored, "invalid workflows must not be persisted")
			})
		}
	}

	for _, graph := range []struct {
		name string
		body map[string]interface{}
	}{
		// Supplying the same ID as the trigger edge fixes the Sentry payload.
		{"corrected trigger ID", payload("__trigger", "c1", "__trigger", "c1")},
		// Deleting a condition need not renumber surviving condition IDs.
		{"branch with nonconsecutive condition IDs", branchPayload("2")},
	} {
		t.Run(graph.name, func(t *testing.T) {
			created := call("/api/v1/workflows", graph.body)
			require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
			simulated := call("/api/v1/workflows:simulate", graph.body)
			require.Equal(t, http.StatusOK, simulated.Code, simulated.Body.String())
			var execution generated.Execution
			require.NoError(t, json.Unmarshal(simulated.Body.Bytes(), &execution))
			require.Equal(t, generated.ExecutionStatusSuccess, execution.Status)
			require.NotNil(t, execution.Steps)
			for _, step := range *execution.Steps {
				if step.Id == "c1" {
					require.True(t, step.Success)
					require.NotNil(t, step.Output)
					require.Equal(t, float64(42), (*step.Output)["data"])
					return
				}
			}
			t.Fatal("corrected workflow did not execute c1")
		})
	}
}
