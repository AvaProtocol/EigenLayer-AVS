package rest

import (
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestMapSimulateError(t *testing.T) {
	manualData := taskengine.NewStructuredError(
		avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
		"ManualTrigger data is required and cannot be null",
	)

	cases := []struct {
		name   string
		err    error
		status int
		code   string
		detail string
	}{
		{
			name:   "grpc invalid argument",
			err:    status.Error(codes.InvalidArgument, "inputVariables.settings.runner is required"),
			status: http.StatusBadRequest,
			code:   "WORKFLOWS_SIMULATE_INVALID",
			detail: "inputVariables.settings.runner is required",
		},
		{
			name:   "wrapped manual trigger validation",
			err:    fmt.Errorf("failed to simulate trigger: %w", manualData),
			status: http.StatusBadRequest,
			code:   "WORKFLOWS_SIMULATE_INVALID",
			detail: "failed to simulate trigger: ManualTrigger data is required and cannot be null",
		},
		{
			name: "node config validation",
			err: fmt.Errorf("failed to compile workflow for simulation: %w", taskengine.NewStructuredError(
				avsproto.ErrorCode_INVALID_NODE_CONFIG,
				"source is required",
			)),
			status: http.StatusBadRequest,
			code:   "WORKFLOWS_SIMULATE_INVALID",
			detail: "failed to compile workflow for simulation: source is required",
		},
		{
			name:   "validation band starts at invalid request",
			err:    taskengine.NewStructuredError(avsproto.ErrorCode_INVALID_REQUEST, "invalid request: no"),
			status: http.StatusBadRequest,
			code:   "WORKFLOWS_SIMULATE_INVALID",
			detail: "invalid request: no",
		},
		{
			name:   "resource state stays a gateway failure",
			err:    taskengine.NewStructuredError(avsproto.ErrorCode_TASK_ALREADY_EXISTS, "task exists"),
			status: http.StatusBadGateway,
			code:   "WORKFLOWS_SIMULATE_FAILED",
			detail: "task exists",
		},
		{
			name:   "rpc failure stays a gateway failure",
			err:    taskengine.NewStructuredError(avsproto.ErrorCode_RPC_NODE_ERROR, "header lookup failed"),
			status: http.StatusBadGateway,
			code:   "WORKFLOWS_SIMULATE_FAILED",
			detail: "header lookup failed",
		},
		{
			name:   "tenderly failure stays a gateway failure",
			err:    fmt.Errorf("failed to simulate trigger: %w", taskengine.NewStructuredError(avsproto.ErrorCode_TENDERLY_API_ERROR, "tenderly 500")),
			status: http.StatusBadGateway,
			code:   "WORKFLOWS_SIMULATE_FAILED",
			detail: "failed to simulate trigger: tenderly 500",
		},
		{
			name:   "plain error stays a gateway failure",
			err:    fmt.Errorf("failed to create VM for simulation: %w", fmt.Errorf("nil wallet config")),
			status: http.StatusBadGateway,
			code:   "WORKFLOWS_SIMULATE_FAILED",
			detail: "failed to create VM for simulation: nil wallet config",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapSimulateError(tc.err)
			got, ok := mapped.(*restmw.HTTPError)
			if !ok {
				t.Fatalf("mapSimulateError returned %T, want *middleware.HTTPError", mapped)
			}
			if got.Status != tc.status || got.Code != tc.code || got.Detail != tc.detail {
				t.Fatalf("status=%d code=%q detail=%q, want status=%d code=%q detail=%q",
					got.Status, got.Code, got.Detail, tc.status, tc.code, tc.detail)
			}
			if tc.status == http.StatusBadRequest && got.Title != "Invalid simulation payload" {
				t.Fatalf("title=%q, want Invalid simulation payload", got.Title)
			}
			if tc.status == http.StatusBadGateway && got.Title != "Simulation failed" {
				t.Fatalf("title=%q, want Simulation failed", got.Title)
			}
		})
	}
}
