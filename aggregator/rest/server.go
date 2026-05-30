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
	"context"
	"strings"

	"github.com/ethereum/go-ethereum/ethclient"
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
	engine         *taskengine.Engine
	logger         sdklogging.Logger
	config         *config.Config
	operators      OperatorLister
	smartWalletRpc *ethclient.Client
	// smartWalletRpcByChain holds per-chain RPC clients in gateway mode,
	// keyed by chain ID. Populated from the aggregator's
	// agg.smartWalletRpcByChain map. nil in single-chain mode — callers
	// fall back to smartWalletRpc. See resolveSmartWalletForChain.
	smartWalletRpcByChain map[int64]*ethclient.Client
	priceService          taskengine.PriceService
	withdraws             WithdrawService
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

// WithdrawService abstracts the bundler-driven smart-wallet withdrawal
// path the gRPC layer historically owned. The REST WithdrawWallet
// handler depends on it; the aggregator package supplies the
// implementation, which lets the REST package stay free of bundler /
// paymaster / WebSocket plumbing.
type WithdrawService interface {
	Withdraw(ctx context.Context, req WithdrawRequest) (WithdrawResult, error)
}

// WithdrawRequest is the chain-agnostic shape the REST handler hands
// to WithdrawService. Mirrors the OpenAPI WithdrawRequest, plus the
// resolved owner address (from the JWT) and the smart wallet address
// (path parameter on the REST route).
type WithdrawRequest struct {
	Owner              string
	SmartWalletAddress string
	RecipientAddress   string
	Amount             string
	Token              string
	ChainID            int64
}

// WithdrawResult is what the WithdrawService returns once the UserOp
// has been submitted (and optionally awaited). REST renders it into
// the OpenAPI WithdrawResponse shape.
type WithdrawResult struct {
	UserOpHash      string
	TransactionHash string
	Status          string
	Message         string
	SubmittedAt     int64
}

// ServerDeps bundles the wiring dependencies so NewServer's signature
// stays manageable as the REST surface grows. Each field is optional —
// handlers that need a missing dependency return a structured 501.
type ServerDeps struct {
	Operators      OperatorLister
	SmartWalletRpc *ethclient.Client
	// SmartWalletRpcByChain holds per-chain RPC clients in gateway mode.
	// In single-chain mode, leave nil — REST handlers fall back to
	// SmartWalletRpc.
	SmartWalletRpcByChain map[int64]*ethclient.Client
	PriceService          taskengine.PriceService
	WithdrawSvc           WithdrawService
}

// NewServer wires the REST handler with its dependencies. Constructed once
// at aggregator startup and shared across all in-flight requests; the
// Echo router handles request-level concurrency.
func NewServer(engine *taskengine.Engine, logger sdklogging.Logger, cfg *config.Config, deps ServerDeps) *Server {
	return &Server{
		engine:                engine,
		logger:                logger,
		config:                cfg,
		operators:             deps.Operators,
		smartWalletRpc:        deps.SmartWalletRpc,
		smartWalletRpcByChain: deps.SmartWalletRpcByChain,
		priceService:          deps.PriceService,
		withdraws:             deps.WithdrawSvc,
	}
}

// resolveSmartWalletForChain picks the (rpc, smart-wallet config) pair the
// REST handlers should use for a given request chain ID. In gateway mode
// each chain has its own ethclient (dialed at startup against
// chain.SmartWallet.EthRpcUrl) and its own SmartWalletConfig (with the
// chain-specific factory/paymaster); resolving both together keeps gas
// estimates and wallet operations on the chain the caller actually
// requested. Falls back to the engine's default pair when chainID is 0
// or unknown — that covers single-chain mode and legacy callers.
func (s *Server) resolveSmartWalletForChain(chainID int64) (*ethclient.Client, *config.SmartWalletConfig) {
	rpc := s.smartWalletRpc
	if chainID > 0 && s.smartWalletRpcByChain != nil {
		if chainRpc, ok := s.smartWalletRpcByChain[chainID]; ok {
			rpc = chainRpc
		}
	}
	sw := s.engine.ResolveSmartWalletConfig(chainID)
	if sw == nil {
		sw = s.config.SmartWallet
	}
	return rpc, sw
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
	api.Use(restmw.RateLimit(s.rateLimitConfig(), restmw.NewInMemoryBackend()))

	// Wrap the api group with a filter that drops route registrations
	// whose path contains `/:<param>:<verb>` — Echo's router collapses
	// `/x/:id`, `/x/:id:foo`, and `/x/:id:bar` into the same tree
	// position and the last-registered route wins, hijacking the
	// parameter binding. The shadow routes registered below handle
	// those URLs via the path rewriter.
	generated.RegisterHandlersWithBaseURL(filteringRouter{Group: api}, s, "")
	registerColonActionShimRoutes(api, s)
}

// rateLimitConfig returns the rate-limit settings — config overrides
// when both knobs are positive, the middleware default otherwise.
func (s *Server) rateLimitConfig() restmw.RateLimitConfig {
	if s.config != nil && s.config.RestRateLimitPerSecond > 0 && s.config.RestRateLimitBurst > 0 {
		return restmw.RateLimitConfig{
			RatePerSecond: s.config.RestRateLimitPerSecond,
			Burst:         s.config.RestRateLimitBurst,
		}
	}
	return restmw.DefaultRateLimit
}

// filteringRouter wraps an *echo.Group and skips route registrations
// whose path contains `<param-segment>:<verb>` — those would shadow
// the simpler `<param-segment>` route in Echo's radix tree. The
// suppressed routes are re-registered by registerColonActionShimRoutes
// using a path the router can disambiguate.
type filteringRouter struct {
	*echo.Group
}

// shouldDropRoute returns true for any path whose final segment contains a
// `:`-suffixed action verb. Two collision classes both fall under this:
//
//   - Item-level: `/workflows/:id:pause` shadows the simpler `/workflows/:id`
//     because Echo treats `id:pause` as a single parameter name.
//   - Collection-level: `/workflows:simulate` and `/workflows:estimateFees`
//     collapse into the same radix-tree node (same method + same `/workflows:`
//     prefix), so the last-registered route silently wins for both URIs.
//
// Every dropped route gets re-registered by registerColonActionShimRoutes
// under a disambiguated `/.../<__action__>/<verb>` path. The Pre middleware
// rewriteColonActions rewrites the original URI at request time so SDK
// clients keep using the spec-compliant `:verb` form on the wire.
func shouldDropRoute(path string) bool {
	segments := strings.Split(path, "/")
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		colon := strings.Index(seg, ":")
		if colon < 0 {
			continue
		}
		// A bare parameter segment like `:id` is fine (single leading colon).
		// Anything else — `:id:pause` or `workflows:simulate` — collides.
		if colon == 0 && strings.Count(seg, ":") == 1 {
			continue
		}
		return true
	}
	return false
}

func (f filteringRouter) GET(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.GET(path, h, m...)
}

func (f filteringRouter) POST(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.POST(path, h, m...)
}

func (f filteringRouter) PUT(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.PUT(path, h, m...)
}

func (f filteringRouter) PATCH(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.PATCH(path, h, m...)
}

func (f filteringRouter) DELETE(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.DELETE(path, h, m...)
}

func (f filteringRouter) HEAD(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.HEAD(path, h, m...)
}

func (f filteringRouter) OPTIONS(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.OPTIONS(path, h, m...)
}

func (f filteringRouter) CONNECT(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.CONNECT(path, h, m...)
}

func (f filteringRouter) TRACE(path string, h echo.HandlerFunc, m ...echo.MiddlewareFunc) *echo.Route {
	if shouldDropRoute(path) {
		return nil
	}
	return f.Group.TRACE(path, h, m...)
}

// rewriteColonActions is the Pre middleware that turns any `<prefix>:<verb>`
// URI into `<prefix>/__action__/<verb>` so Echo's router can disambiguate
// sibling actions. Covers both:
//
//   - Item-level: `/workflows/<id>:pause` → `/workflows/<id>/__action__/pause`
//   - Collection-level: `/workflows:simulate` → `/workflows/__action__/simulate`
//
// SDK clients keep using the OpenAPI-compliant `:verb` form on the wire; the
// rewrite is invisible to them. registerColonActionShimRoutes re-registers
// every dropped route under the shim path.
func rewriteColonActions(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()
		path := req.URL.Path
		idx := strings.LastIndex(path, "/")
		if idx < 0 {
			return next(c)
		}
		lastSegment := path[idx+1:]
		// A leading colon means a parametric segment like `:id`, not an
		// action verb — leave it alone.
		colon := strings.Index(lastSegment, ":")
		if colon <= 0 {
			return next(c)
		}
		parent := path[:idx]
		prefix := lastSegment[:colon]
		action := lastSegment[colon+1:]
		req.URL.Path = parent + "/" + prefix + colonActionShim + action
		c.SetRequest(req)
		return next(c)
	}
}

// registerColonActionShimRoutes re-registers every `<prefix>:<verb>` route
// under the disambiguated `<prefix>/__action__/<verb>` form that Echo's
// radix tree can route. The Pre middleware rewriteColonActions rewrites
// inbound URIs to match. The list mirrors every colon-suffix path in
// api/openapi.yaml — extend both sides together when adding a new action.
//
// Collection-level routes (no path parameter) use the generated wrapper
// so query-parameter binding stays consistent with the rest of the API.
// Item-level routes (`:id`, `:address`) wrap the typed Server methods
// directly because those Server methods take the ULID/address as a
// separate argument and the shim has to pluck it out of the URL.
func registerColonActionShimRoutes(api *echo.Group, s *Server) {
	wrapper := generated.ServerInterfaceWrapper{Handler: s}

	// Item-level actions
	api.POST("/workflows/:id"+colonActionShim+"pause", func(c echo.Context) error {
		return s.PauseWorkflow(c, generated.Ulid(c.Param("id")))
	})
	api.POST("/workflows/:id"+colonActionShim+"resume", func(c echo.Context) error {
		return s.ResumeWorkflow(c, generated.Ulid(c.Param("id")))
	})
	api.POST("/workflows/:id"+colonActionShim+"trigger", func(c echo.Context) error {
		return s.TriggerWorkflow(c, generated.Ulid(c.Param("id")))
	})
	api.POST("/wallets/:address"+colonActionShim+"withdraw", func(c echo.Context) error {
		return s.WithdrawWallet(c, generated.EthereumAddress(c.Param("address")))
	})
	api.GET("/wallets/:address"+colonActionShim+"getNonce", func(c echo.Context) error {
		return s.GetWalletNonce(c, generated.EthereumAddress(c.Param("address")))
	})
	// Executions item-level: query params are read manually because Ulid
	// is a string alias the default Echo binder silently skips.
	api.GET("/executions/:id"+colonActionShim+"getStatus", func(c echo.Context) error {
		params := generated.GetExecutionStatusParams{
			WorkflowId: generated.Ulid(c.QueryParam("workflowId")),
		}
		return s.GetExecutionStatus(c, generated.Ulid(c.Param("id")), params)
	})
	api.GET("/executions/:id"+colonActionShim+"stream", func(c echo.Context) error {
		params := generated.StreamExecutionParams{
			WorkflowId: generated.Ulid(c.QueryParam("workflowId")),
		}
		if interval := c.QueryParam("interval"); interval != "" {
			params.Interval = &interval
		}
		return s.StreamExecution(c, generated.Ulid(c.Param("id")), params)
	})

	// Collection-level actions. Routed via the generated wrapper so the
	// oapi-codegen runtime handles query-param binding the same way the
	// non-shim routes would.
	api.POST("/auth"+colonActionShim+"exchange", wrapper.AuthExchange)
	api.POST("/workflows"+colonActionShim+"simulate", wrapper.SimulateWorkflow)
	api.POST("/workflows"+colonActionShim+"estimateFees", wrapper.EstimateWorkflowFees)
	api.GET("/workflows"+colonActionShim+"count", wrapper.CountWorkflows)
	api.GET("/executions"+colonActionShim+"count", wrapper.CountExecutions)
	api.GET("/executions"+colonActionShim+"stats", wrapper.ExecutionStats)
	api.POST("/nodes"+colonActionShim+"run", wrapper.RunNode)
	api.POST("/triggers"+colonActionShim+"run", wrapper.RunTrigger)
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
