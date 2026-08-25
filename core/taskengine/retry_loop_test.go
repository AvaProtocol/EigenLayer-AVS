package taskengine

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestLoopRetry_DoesNotCascadeTimeoutToLaterIterations: a sequential loop
// iteration that spends its retry backoff past iterationTimeout used to leave
// the single worker sleeping against the VM-wide context. The collector then
// submitted the next item and immediately started *its* timeout clock, so
// later iterations were marked timed out without ever running.
func TestLoopRetry_DoesNotCascadeTimeoutToLaterIterations(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	node, err := CreateNodeFromType(NodeTypeLoop, map[string]interface{}{
		"inputVariable":    "{{items}}",
		"iterVal":          "value",
		"iterKey":          "index",
		"iterationTimeout": float64(1),
		"executionMode":    "sequential",
		"runner": map[string]interface{}{
			"type": "restApi",
			"config": map[string]interface{}{
				"url":    srv.URL,
				"method": "GET",
			},
		},
	}, "loop_retry_timeout")
	if err != nil {
		t.Fatalf("CreateNodeFromType: %v", err)
	}
	node.RetryPolicy = &avsproto.RetryPolicy{
		MaxAttempts: 5,
		BackoffMs:   5000, // 5s > 1s iteration timeout
		RetryOn:     []string{retryClassHTTP5xx},
	}

	vm, err := NewVMWithData(nil, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("NewVMWithData: %v", err)
	}
	vm.WithLogger(testutil.GetLogger())

	start := time.Now()
	_, _ = vm.RunNodeWithInputs(node, map[string]interface{}{
		"items": []interface{}{"a", "b"},
	})

	got := hits.Load()
	if got < 2 {
		t.Fatalf("expected each iteration to hit the server at least once, got %d hits (later iterations were likely skipped while the worker stayed in retry backoff)", got)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("loop took %s; cancelled retry sleep should let the second iteration start well before the 5s backoff finishes", elapsed)
	}
}
