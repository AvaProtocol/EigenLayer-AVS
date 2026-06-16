package taskengine

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

// blockingChainStateReader embeds the ChainStateReader interface so it
// satisfies the full method set, but overrides only GetBlockNumber. The
// override signals when the worker call has started, then blocks until the
// caller's context is cancelled — mirroring an in-flight worker round-trip
// that a request cancellation should be able to interrupt.
type blockingChainStateReader struct {
	ChainStateReader
	started chan struct{}
}

func (b *blockingChainStateReader) GetBlockNumber(ctx context.Context) (uint64, error) {
	close(b.started)
	<-ctx.Done()
	return 0, ctx.Err()
}

func (b *blockingChainStateReader) HeaderByNumber(ctx context.Context, _ *big.Int) (*BlockHeader, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}


func TestRunBlockTriggerImmediately_ContextCancellationInterruptsWorkerCall(t *testing.T) {
	// runBlockTriggerImmediately only reads n.logger (nil-guarded) and the
	// global chain-state reader registry, so a zero-value Engine is enough —
	// this keeps the test independent of the gateway config fixture.
	engine := &Engine{}

	const testChainID int64 = 987654321
	reader := &blockingChainStateReader{started: make(chan struct{})}
	RegisterChainStateReader(uint64(testChainID), reader)
	defer RegisterChainStateReader(uint64(testChainID), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	triggerConfig := map[string]interface{}{
		"chain_id": testChainID,
	}

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, err := engine.runTriggerImmediately(ctx, NodeTypeBlockTrigger, triggerConfig, map[string]interface{}{})
		done <- result{err: err}
	}()

	// Wait until the worker call is actually in flight before cancelling, so
	// the test exercises interruption of an in-flight call rather than a
	// pre-cancelled context shortcut.
	select {
	case <-reader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker GetBlockNumber was never reached")
	}

	cancel()

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("expected an error after context cancellation, got nil")
		}
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("expected error to wrap context.Canceled, got: %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runBlockTriggerImmediately did not return after context cancellation; " +
			"cancellation was not propagated to the worker call")
	}
}
