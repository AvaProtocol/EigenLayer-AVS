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
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// colonActionShim is the path prefix the rewriter swaps `:` for. Echo's
// router treats `:id:pause` as a single parameter (named `id:pause`),
// so a Google AIP-136 / Stripe-style action URL like
// `/workflows/<id>:pause` can't bind `id` correctly when registered
// directly. The middleware below rewrites these URLs to
// `/workflows/<id>/__action__/pause` before routing, and shadow routes
// matching that pattern delegate to the same handler the generated
// router exposed. SDKs continue to send the colon form on the wire.
const colonActionShim = "/__action__/"

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
	engine    *taskengine.Engine
	logger    sdklogging.Logger
	config    *config.Config
	operators OperatorLister
}

// OperatorLister is the minimal surface the REST package needs from the
// aggregator's operator pool — kept as an interface so we don't import
// the aggregator package (would cycle) and so tests can inject a fake.
type OperatorLister interface {
	List() []OperatorView
}

// OperatorView is the chain-agnostic shape the REST layer renders. The
// aggregator package adapts its internal *OperatorNode into this shape
// when constructing the Server so the proto/internal type stays
// internal.
type OperatorView struct {
	Address           string
	Name              string
	Version           string
	BlockNumber       int64
	EventCount        int64
	LastPingEpochMs   int64
	SupportedChainIDs []int64
}

// NewServer wires the REST handler with its dependencies. Constructed once
// at aggregator startup and shared across all in-flight requests; the
// Echo router handles request-level concurrency.
func NewServer(engine *taskengine.Engine, logger sdklogging.Logger, cfg *config.Config, operators OperatorLister) *Server {
	return &Server{
		engine:    engine,
		logger:    logger,
		config:    cfg,
		operators: operators,
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

	// Path rewriter for Google AIP-136 colon-suffix actions. Runs
	// before routing so the actual matcher sees a path Echo can route.
	// See colonActionShim above.
	e.Pre(rewriteColonActions)

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
	registerColonActionShimRoutes(api, s)
}

// rewriteColonActions is the Pre middleware that turns
// `/workflows/<id>:pause` into `/workflows/<id>/__action__/pause` so
// Echo's router can bind `:id` cleanly. Only rewrites when the colon
// follows a non-empty segment and is preceded by another segment that
// looks like a parameter (no colon at all means the route doesn't
// need rewriting; e.g. `/workflows:count` is already fine because the
// colon segment is the last and the wrapper has no path param to bind).
func rewriteColonActions(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()
		path := req.URL.Path
		// Walk path segments from right to left; rewrite the first
		// segment that has a colon AND is preceded by another segment.
		// (`/x:y` is fine — `/x/y:z` is the problematic case.)
		idx := strings.LastIndex(path, "/")
		if idx <= 0 {
			return next(c)
		}
		lastSegment := path[idx+1:]
		// Detect colon inside the segment but not at position 0
		// (a leading colon would mean an empty id, which is malformed).
		colon := strings.Index(lastSegment, ":")
		if colon <= 0 {
			return next(c)
		}
		// Bail if the parent path is the api root — `/auth:exchange`
		// has no parameter before the colon and Echo handles it fine.
		parent := path[:idx]
		if parent == "" || parent == "/api/v1" {
			return next(c)
		}
		id := lastSegment[:colon]
		action := lastSegment[colon+1:]
		newPath := parent + "/" + id + colonActionShim + action
		req.URL.Path = newPath
		c.SetRequest(req)
		return next(c)
	}
}

// registerColonActionShimRoutes wires the rewritten paths to the same
// handler methods the generated wrapper would have called for the
// original colon-suffix routes. The set is closed (one entry per
// `/{id}:<verb>` or `/{address}:<verb>` route in api/openapi.yaml);
// add to it when new actions land in the spec.
func registerColonActionShimRoutes(api *echo.Group, s *Server) {
	// Workflows
	api.POST("/workflows/:id"+colonActionShim+"pause", func(c echo.Context) error {
		return s.PauseWorkflow(c, generated.Ulid(c.Param("id")))
	})
	api.POST("/workflows/:id"+colonActionShim+"resume", func(c echo.Context) error {
		return s.ResumeWorkflow(c, generated.Ulid(c.Param("id")))
	})
	api.POST("/workflows/:id"+colonActionShim+"trigger", func(c echo.Context) error {
		return s.TriggerWorkflow(c, generated.Ulid(c.Param("id")))
	})

	// Wallets
	api.POST("/wallets/:address"+colonActionShim+"withdraw", func(c echo.Context) error {
		return s.WithdrawWallet(c, generated.EthereumAddress(c.Param("address")))
	})
	api.GET("/wallets/:address"+colonActionShim+"getNonce", func(c echo.Context) error {
		return s.GetWalletNonce(c, generated.EthereumAddress(c.Param("address")))
	})

	// Executions
	api.GET("/executions/:id"+colonActionShim+"getStatus", func(c echo.Context) error {
		return s.GetExecutionStatus(c, generated.Ulid(c.Param("id")))
	})
	api.GET("/executions/:id"+colonActionShim+"stream", func(c echo.Context) error {
		// StreamExecution takes a params struct (for the interval
		// query). Build it from the request the same way the generated
		// wrapper would.
		var params generated.StreamExecutionParams
		_ = (&echo.DefaultBinder{}).BindQueryParams(c, &params)
		return s.StreamExecution(c, generated.Ulid(c.Param("id")), params)
	})
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
