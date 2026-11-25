package aggregator

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

// blockingFlattenStorage holds its first Flatten until release is closed.
type blockingFlattenStorage struct {
	storage.Storage
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockingFlattenStorage) Flatten(int) error {
	if s.calls.Add(1) == 1 {
		close(s.started)
		<-s.release
	}
	return nil
}

// Shutdown closes the DB right after waitForFlatten, so it must not return
// while a REPL flatten is still compacting.
func TestWaitForFlattenWaitsForRunningFlatten(t *testing.T) {
	inner, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	db := &blockingFlattenStorage{Storage: inner, started: make(chan struct{}), release: make(chan struct{})}
	agg := &Aggregator{logger: logger.NewNoOpLogger(), db: db}

	require.True(t, agg.startFlatten())
	<-db.started
	require.False(t, agg.startFlatten(), "a second flatten must not start while one runs")

	waited := make(chan struct{})
	go func() {
		agg.waitForFlatten()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("waitForFlatten returned while the flatten was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(db.release)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForFlatten did not return after the flatten finished")
	}

	require.True(t, agg.startFlatten(), "a new flatten can start once the last one finished")
	agg.waitForFlatten()
}
