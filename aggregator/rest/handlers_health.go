package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/version"
)

// GetHealth — GET /api/v1/health
//
// Returns the aggregator's liveness/readiness state. Used by load
// balancers, k8s probes, and external uptime monitoring. Always honors
// authentication absence (security: []) so monitoring tools can probe
// without credentials.
func (s *Server) GetHealth(ctx echo.Context) error {
	status := generated.Ok
	httpStatus := http.StatusOK

	// HealthStatus is a minimal response; the existing aggregator status
	// machinery (initStatus / runningStatus / shutdownStatus) is opaque
	// to this package — we conservatively report `ok` whenever the
	// process has reached the point of serving requests.
	resp := generated.HealthStatus{
		Status:  status,
		Version: ptr(version.Get()),
	}
	if s.config != nil && s.config.SmartWallet != nil && s.config.SmartWallet.ChainID > 0 {
		chainID := s.config.SmartWallet.ChainID
		resp.ChainId = &chainID
	}
	return ctx.JSON(httpStatus, resp)
}

// ptr returns a pointer to v — used in OpenAPI-generated structs where
// every optional field is a pointer.
func ptr[T any](v T) *T { return &v }
