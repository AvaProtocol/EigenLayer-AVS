package aggregator

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/version"
)

// GET /health/deep — component-level health, for uptime monitoring.
//
// This exists because the two shallower probes cannot fail in the ways that
// actually matter:
//
//   - GET /health and /up return "up" once agg.status == runningStatus.
//   - GET /api/v1/health always returns 200 with {status, chainId, version};
//     its contract is consumed by the SDK's HealthResource, so it cannot start
//     returning 503 without breaking clients.
//
// Neither touches a worker or an operator, so every chain worker can be dead
// and both stay green. This endpoint asks each worker directly and checks that
// at least one operator is still pinging, then returns 503 when something is
// wrong so an external monitor goes red.
const (
	// deepHealthTTL bounds how often the fan-out actually runs. The endpoint
	// is unauthenticated, so without a cache a caller could amplify one cheap
	// HTTP request into a gRPC call per chain worker. With the cache the worst
	// case is one round of calls per TTL regardless of request rate.
	deepHealthTTL = 15 * time.Second

	// deepHealthWorkerTimeout caps a single WorkerHealthCheck. The worker asks
	// its chain RPC for the latest block, so this has to tolerate a slow
	// upstream without stalling the whole response.
	deepHealthWorkerTimeout = 3 * time.Second

	// operatorStaleAfter is how long without a ping before an operator stops
	// counting as connected. Operators ping every 5s, so this tolerates 12
	// consecutive misses before we call one gone.
	operatorStaleAfter = 60 * time.Second

	deepHealthOK       = "ok"
	deepHealthDegraded = "degraded"
)

type workerHealth struct {
	ChainID     int64  `json:"chain_id"`
	ChainName   string `json:"chain_name,omitempty"`
	Status      string `json:"status"`
	LatestBlock int64  `json:"latest_block,omitempty"`
	Error       string `json:"error,omitempty"`
}

type operatorHealth struct {
	Connected int      `json:"connected"`
	Addresses []string `json:"addresses"`
}

type deepHealthResp struct {
	Status    string         `json:"status"`
	Version   string         `json:"version"`
	CheckedAt time.Time      `json:"checked_at"`
	Workers   []workerHealth `json:"workers"`
	Operators operatorHealth `json:"operators"`
}

type deepHealthCache struct {
	mu     sync.Mutex
	at     time.Time
	result *deepHealthResp
}

// get returns a cached snapshot, recomputing it once the TTL has passed. The
// lock is held across collection on purpose: concurrent callers that arrive
// during a refresh wait for that one refresh rather than each starting their
// own fan-out.
func (c *deepHealthCache) get(ctx context.Context, agg *Aggregator) *deepHealthResp {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.result != nil && time.Since(c.at) < deepHealthTTL {
		return c.result
	}

	c.result = agg.collectDeepHealth(ctx)
	c.at = time.Now()
	return c.result
}

// registerDeepHealth mounts the endpoint on the aggregator's HTTP router. It
// lives outside the /api/v1 group (like /telemetry and the gas-manager
// webhook) so it needs no OpenAPI regeneration and no JWT.
func (agg *Aggregator) registerDeepHealth(e *echo.Echo) {
	cache := &deepHealthCache{}

	e.GET("/health/deep", func(c echo.Context) error {
		snapshot := cache.get(c.Request().Context(), agg)

		code := http.StatusOK
		if snapshot.Status != deepHealthOK {
			code = http.StatusServiceUnavailable
		}
		return c.JSON(code, snapshot)
	})
}

func (agg *Aggregator) collectDeepHealth(ctx context.Context) *deepHealthResp {
	resp := &deepHealthResp{
		Status:    deepHealthOK,
		Version:   version.Get(),
		CheckedAt: time.Now().UTC(),
		Workers:   []workerHealth{},
	}

	if agg.chainRegistry != nil {
		resp.Workers = agg.checkWorkers(ctx)
	}
	for _, w := range resp.Workers {
		// The worker reports DEGRADED when it cannot reach its chain RPC,
		// which is as execution-fatal for that chain as being unreachable.
		if w.Status != "OK" {
			resp.Status = deepHealthDegraded
		}
	}

	resp.Operators = agg.checkOperators()
	// Operators are the only chain watchers. With none of them attached no
	// trigger will ever fire, no matter how healthy the rest looks.
	if resp.Operators.Connected == 0 {
		resp.Status = deepHealthDegraded
	}

	return resp
}

func (agg *Aggregator) checkWorkers(ctx context.Context) []workerHealth {
	ids := agg.chainRegistry.SupportedChainIDs()
	slices.Sort(ids)

	results := make([]workerHealth, len(ids))
	var wg sync.WaitGroup
	for i, chainID := range ids {
		wg.Add(1)
		go func(slot int, id int64) {
			defer wg.Done()
			results[slot] = agg.checkWorker(ctx, id)
		}(i, chainID)
	}
	wg.Wait()

	return results
}

func (agg *Aggregator) checkWorker(ctx context.Context, chainID int64) workerHealth {
	out := workerHealth{ChainID: chainID, Status: "down"}

	if chainCfg, err := agg.chainRegistry.GetChainConfig(chainID); err == nil && chainCfg != nil {
		out.ChainName = chainCfg.Name
	}

	entry, err := agg.chainRegistry.GetWorker(chainID)
	if err != nil {
		out.Error = err.Error()
		return out
	}

	// GetWorker succeeding only proves a client object exists — grpc.NewClient
	// is non-blocking, so nothing has touched the network yet. This RPC is what
	// separates "configured" from "actually alive".
	callCtx, cancel := context.WithTimeout(ctx, deepHealthWorkerTimeout)
	defer cancel()

	health, err := entry.Client.WorkerHealthCheck(callCtx, &avsproto.WorkerHealthCheckReq{})
	if err != nil {
		out.Error = err.Error()
		return out
	}

	out.Status = health.GetStatus()
	out.LatestBlock = health.GetLatestBlock()
	return out
}

func (agg *Aggregator) checkOperators() operatorHealth {
	out := operatorHealth{Addresses: []string{}}
	if agg.operatorPool == nil {
		return out
	}

	cutoff := time.Now().Add(-operatorStaleAfter)
	for _, node := range agg.operatorPool.GetAll() {
		// GetAll reads persisted records, so it keeps returning operators that
		// disconnected long ago. Presence in the list means nothing on its own;
		// ping freshness is the only evidence one is currently attached.
		if node.LastSeenAt().After(cutoff) {
			out.Connected++
			out.Addresses = append(out.Addresses, node.Address)
		}
	}
	sort.Strings(out.Addresses)

	return out
}
