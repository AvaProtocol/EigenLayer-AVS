package aggregator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

// newDeepHealthAggregator builds an aggregator with a real operator pool.
//
// chainRegistry is deliberately left nil so these tests isolate the operator
// half of the verdict; the worker fan-out needs live gRPC targets and is
// covered by the registry's own tests.
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
