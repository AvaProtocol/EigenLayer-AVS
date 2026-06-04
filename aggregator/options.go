package aggregator

import "github.com/labstack/echo/v4"

// HTTPMounter registers additional routes on the echo router that the
// aggregator's HTTP server creates. Used to keep the aggregator
// package from importing optional consumer packages directly — for
// example, the REST API surface (aggregator/rest) mounts itself via
// this hook from cmd/runAggregator.go instead of being wired into
// startHttpServer().
//
// The *Aggregator handle is passed in so mounters can pull
// dependencies (engine, logger, config, AvsClientSurface) without
// rebuilding them. Mounters run AFTER the aggregator's built-in
// legacy routes are registered, so they can rely on the engine,
// rpcServer, smartWalletRpc, and priceService being populated.
type HTTPMounter func(agg *Aggregator, e *echo.Echo)

// StartOption tunes the behavior of Aggregator.Start.
type StartOption func(*startOptions)

type startOptions struct {
	httpMounts []HTTPMounter
}

// WithHTTPMount registers an extra route mounter that runs after the
// aggregator's built-in legacy routes (/health, /operator, /telemetry,
// /_debug/*) are registered. Multiple options are applied in
// registration order, so the last registered mounter sees a router
// that already has every prior mounter's routes.
func WithHTTPMount(m HTTPMounter) StartOption {
	return func(o *startOptions) { o.httpMounts = append(o.httpMounts, m) }
}
