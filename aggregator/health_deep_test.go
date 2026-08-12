package aggregator

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// newDeepHealthAggregator builds an aggregator with a real operator pool and
// no chain registry, isolating the operator half of the verdict. Tests that
// need the worker half call serveWorker to attach a real one.
func newDeepHealthAggregator(t *testing.T) (*Aggregator, func()) {
	t.Helper()
	db := testutil.TestMustDB()
	agg := &Aggregator{
		logger:       testutil.GetLogger(),
		db:           db,
		operatorPool: &OperatorPool{db: db},
	}
	return agg, func() { db.Close() }
}

// stubWorker is a real in-process ChainWorker server.
//
// The worker fan-out is the only part of collection that touches the context
// or the network, so a nil chainRegistry makes any test of that behaviour
// vacuous — it would pass whether or not the code under test is correct.
// These tests therefore talk to an actual gRPC server over a loopback socket.
type stubWorker struct {
	avsproto.UnimplementedChainWorkerServer
	status string
	block  int64
}

func (s *stubWorker) WorkerHealthCheck(context.Context, *avsproto.WorkerHealthCheckReq) (*avsproto.WorkerHealthCheckResp, error) {
	return &avsproto.WorkerHealthCheckResp{Status: s.status, LatestBlock: s.block}, nil
}

// serveWorker starts a worker on a loopback port and points the aggregator's
// registry at it. Passing a nil worker registers an address that nothing is
// listening on, which is how an unreachable worker is simulated.
func serveWorker(t *testing.T, agg *Aggregator, chainID int64, name string, worker *stubWorker) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()

	if worker == nil {
		// Free the port again so dials are refused rather than accepted.
		if err := listener.Close(); err != nil {
			t.Fatalf("closing listener: %v", err)
		}
	} else {
		server := grpc.NewServer()
		avsproto.RegisterChainWorkerServer(server, worker)
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
	}

	agg.chainRegistry = NewChainRegistry([]*config.ChainConfig{{
		ChainID:    chainID,
		Name:       name,
		WorkerAddr: addr,
	}}, chainID, agg.logger)
	agg.chainRegistry.Connect(t.Context())
	t.Cleanup(agg.chainRegistry.Close)
}

// seedOperator persists an operator record with a chosen ping timestamp.
//
// OperatorPool.Checkin always stamps time.Now(), so it cannot express a stale
// operator or a millisecond-epoch record. This writes the same key and payload
// Checkin does, differing only in the epoch under test.
func seedOperator(t *testing.T, agg *Aggregator, address string, lastPing int64) {
	t.Helper()

	data, err := json.Marshal(&OperatorNode{Address: address, LastPingEpoch: lastPing})
	if err != nil {
		t.Fatalf("encoding operator: %v", err)
	}
	if err := agg.db.Set(append(operatorPrefix, []byte(address)...), data); err != nil {
		t.Fatalf("persisting operator: %v", err)
	}
}

// get drives the route through Echo so the status-code mapping is covered,
// not just the collector.
func get(t *testing.T, agg *Aggregator) (int, deepHealthResp) {
	t.Helper()

	e := echo.New()
	agg.registerDeepHealth(e)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/deep", nil))

	var body deepHealthResp
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v (raw: %s)", err, rec.Body.String())
	}
	return rec.Code, body
}

// The failure this endpoint exists to catch: the process is up and serving,
// but nothing is watching the chains, so no trigger can ever fire. The
// shallow probes return 200 here.
func TestDeepHealthDegradedWhenNoOperatorConnected(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	code, body := get(t, agg)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d — an uptime monitor must go red with no chain watcher", code, http.StatusServiceUnavailable)
	}
	if body.Status != deepHealthDegraded {
		t.Errorf("status field = %q, want %q", body.Status, deepHealthDegraded)
	}
	if body.Operators.Connected != 0 {
		t.Errorf("connected = %d, want 0", body.Operators.Connected)
	}
}

func TestDeepHealthOKWithFreshOperator(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	seedOperator(t, agg, "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D", time.Now().Unix())

	code, body := get(t, agg)

	if code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %+v)", code, http.StatusOK, body)
	}
	if body.Status != deepHealthOK {
		t.Errorf("status field = %q, want %q", body.Status, deepHealthOK)
	}
	if body.Operators.Connected != 1 {
		t.Errorf("connected = %d, want 1", body.Operators.Connected)
	}
}

// An operator that stopped pinging stays in the pool's persisted records
// forever, so presence alone must not count as connected — this is exactly
// what makes a keyword match on /telemetry an inadequate substitute.
func TestDeepHealthIgnoresStaleOperator(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	seedOperator(t, agg, "0xdeadbeef00000000000000000000000000000000",
		time.Now().Add(-10*operatorStaleAfter).Unix())

	code, body := get(t, agg)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d — a stale operator must not count as connected", code, http.StatusServiceUnavailable)
	}
	if body.Operators.Connected != 0 {
		t.Errorf("connected = %d, want 0 (stale operator was counted)", body.Operators.Connected)
	}
}

// Millisecond-epoch records must normalize the same way as second-epoch ones;
// treating ms as s would date every modern operator to 1970 and read as stale.
func TestDeepHealthAcceptsMillisecondEpoch(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	seedOperator(t, agg, "0xc6B87cc9e85b07365b6aBEfff061F237F7cf7Dc3", time.Now().UnixMilli())

	_, body := get(t, agg)

	if body.Operators.Connected != 1 {
		t.Errorf("connected = %d, want 1 — millisecond epoch was misread as stale", body.Operators.Connected)
	}
}

// Whichever request triggers a refresh has its result cached and served to
// every other caller for the rest of the TTL. If that caller's cancellation
// propagated into collection, one client disconnecting mid-refresh would
// cache a false verdict for everyone — a spurious outage on an endpoint whose
// entire job is to be trusted by a monitor.
func TestDeepHealthIgnoresCallerCancellation(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	seedOperator(t, agg, "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D", time.Now().Unix())
	serveWorker(t, agg, testChainID, "sepolia", &stubWorker{status: "OK", block: 9_000_000})

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // caller is already gone before collection starts

	got := (&deepHealthCache{}).get(ctx, agg)

	if got.Status != deepHealthOK {
		t.Errorf("status = %q, want %q — a canceled caller poisoned the shared snapshot", got.Status, deepHealthOK)
	}
	if len(got.Workers) != 1 || got.Workers[0].Status != deepHealthOK {
		t.Errorf("workers = %+v, want one healthy worker", got.Workers)
	}
}

// The headline failure the endpoint exists for: the gateway is serving fine
// but a chain worker is unreachable, so nothing can execute on that chain.
func TestDeepHealthDegradedWhenWorkerUnreachable(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	seedOperator(t, agg, "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D", time.Now().Unix())
	serveWorker(t, agg, testChainID, "sepolia", nil) // nothing listening

	code, body := get(t, agg)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d with a dead worker", code, http.StatusServiceUnavailable)
	}
	if len(body.Workers) != 1 || body.Workers[0].Status != deepHealthDown {
		t.Fatalf("workers = %+v, want one down worker", body.Workers)
	}
	// The raw gRPC error embeds the dialed host:port; this route is
	// unauthenticated, so only the code may be surfaced.
	if strings.Contains(body.Workers[0].Error, "127.0.0.1") {
		t.Errorf("error %q leaks the worker address to anonymous callers", body.Workers[0].Error)
	}
}

// A worker that answers but cannot reach its chain RPC reports DEGRADED. That
// chain cannot execute, so it must not read as healthy — and the uppercase
// value from the RPC must be normalized to the payload's lowercase vocabulary.
func TestDeepHealthDegradedWhenWorkerRPCWedged(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	seedOperator(t, agg, "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D", time.Now().Unix())
	serveWorker(t, agg, testChainID, "sepolia", &stubWorker{status: "DEGRADED"})

	code, body := get(t, agg)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d for a degraded worker", code, http.StatusServiceUnavailable)
	}
	if len(body.Workers) != 1 || body.Workers[0].Status != deepHealthDegraded {
		t.Errorf("workers = %+v, want status %q (lowercased from the RPC)", body.Workers, deepHealthDegraded)
	}
}

// The endpoint is unauthenticated and fans out to every worker, so the cache
// is what stops it being an amplifier. Verify repeat calls reuse a snapshot.
func TestDeepHealthCachesSnapshot(t *testing.T) {
	agg, cleanup := newDeepHealthAggregator(t)
	defer cleanup()

	_, first := get(t, agg)

	cache := &deepHealthCache{}
	a := cache.get(t.Context(), agg)
	b := cache.get(t.Context(), agg)

	if a != b {
		t.Error("second call recomputed instead of reusing the cached snapshot")
	}
	if first.CheckedAt.IsZero() {
		t.Error("checked_at is zero — the response gives no way to spot a frozen snapshot")
	}
}
