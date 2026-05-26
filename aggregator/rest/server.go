// Package rest is the public REST API surface for the AVS aggregator.
//
// Every route is generated from api/openapi.yaml via `make rest-gen` — the
// ServerInterface lives in the generated subpackage and the Server struct
// below implements it. Each resource family lives in its own handlers_*
// file; all of them are methods on the same Server type so they share a
// single engine reference and configuration.
//
// During the REST cutover, handlers initially return 501 Not Implemented;
// they get filled in alongside the engine rename (Task -> Workflow) and
// the public gRPC removal so the engine layer is consistent.
package rest

import (
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// Server implements the generated ServerInterface. Every handler family
// (workflows, executions, wallets, secrets, tokens, nodes, triggers,
// operators, auth, health) is a method on this struct, kept in separate
// handlers_*.go files for readability.
//
// The engine is the single business-logic layer; REST handlers are thin
// wrappers that parse the request, call the engine, and serialize the
// result. Validation lives in a shared validators package (added alongside
// handler bodies in a follow-up commit).
type Server struct {
	engine *taskengine.Engine
	logger sdklogging.Logger
	config *config.Config
}

// NewServer wires the REST handler with its dependencies. Constructed once
// at aggregator startup and shared across all in-flight requests; the
// Echo router handles request-level concurrency.
func NewServer(engine *taskengine.Engine, logger sdklogging.Logger, cfg *config.Config) *Server {
	return &Server{
		engine: engine,
		logger: logger,
		config: cfg,
	}
}

// Mount registers every REST route on the supplied Echo instance under
// the /api/v1 prefix and installs the REST middleware stack. The
// supplied router is typically the aggregator's existing HTTP server;
// the REST surface runs alongside the legacy /up and /operator routes
// during the cutover window.
//
// Middleware ordering matters — see aggregator/rest/middleware/requestid.go
// for the full rationale. Briefly: requestid first so everything else can
// reference it; jwt before ratelimit so buckets key on a verified subject;
// problem error handler is installed at the Echo level (one for the whole
// process) rather than per-route.
//
// The generated RegisterHandlersWithBaseURL ensures the spec's
// operationId-to-handler mapping is enforced at compile time — adding a
// new path or operation in api/openapi.yaml triggers a build error here
// until the corresponding Server method exists.
func (s *Server) Mount(e *echo.Echo) {
	// Process-level error handler: every error from any REST route gets
	// turned into application/problem+json with a stable shape.
	e.HTTPErrorHandler = restmw.ProblemErrorHandler(s.logger)

	// Mount under a /api/v1 group so middleware applies only to the REST
	// surface — the legacy /up + /operator + /telemetry routes keep their
	// own (empty) middleware stack.
	api := e.Group("/api/v1")
	api.Use(restmw.RequestID())
	api.Use(restmw.Sentry())
	api.Use(restmw.CORS())
	if s.config != nil && len(s.config.JwtSecret) > 0 {
		api.Use(restmw.JWT(restmw.JWTConfig{SigningKey: s.config.JwtSecret}))
	}
	api.Use(restmw.RateLimit(restmw.DefaultRateLimit, restmw.NewInMemoryBackend()))

	generated.RegisterHandlersWithBaseURL(api, s, "")
}

// notImplemented is the common stub used by handlers that haven't been
// wired up yet. Returns 501 with a structured problem+json body so SDK
// integration tests get an actionable response while the rest of the
// scaffolding lands.
func (s *Server) notImplemented(ctx echo.Context, name string) error {
	return ctx.JSON(501, map[string]any{
		"type":   "https://docs.avaprotocol.org/errors/not-implemented",
		"title":  "Not implemented",
		"status": 501,
		"detail": name + " is not yet wired to the engine. Stub will be filled in a follow-up commit.",
		"code":   "NOT_IMPLEMENTED",
	})
}
