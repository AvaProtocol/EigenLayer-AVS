package aggregator

import (
	"bufio"
	"fmt"
	"net"
	"strings"
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

func newFlattenTestAggregator(t *testing.T) (*Aggregator, *blockingFlattenStorage) {
	t.Helper()
	inner, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	db := &blockingFlattenStorage{Storage: inner, started: make(chan struct{}), release: make(chan struct{})}
	return &Aggregator{logger: logger.NewNoOpLogger(), db: db}, db
}

// Shutdown closes the DB right after waitForFlatten, so it must not return
// while a REPL flatten is still compacting.
func TestWaitForFlattenWaitsForRunningFlatten(t *testing.T) {
	agg, db := newFlattenTestAggregator(t)

	require.NoError(t, agg.startFlatten())
	<-db.started
	require.ErrorIs(t, agg.startFlatten(), errFlattenRunning)

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
	require.ErrorIs(t, agg.startFlatten(), errFlattenShuttingDown)
	require.EqualValues(t, 1, db.calls.Load())
}

// A REPL connection accepted before shutdown outlives the listener. A flatten
// sent on it after stopRepl must be refused, or it could start compacting
// after shutdown's wait and run into db.Close.
func TestReplFlattenRefusedOnOpenConnectionAfterShutdownBegins(t *testing.T) {
	agg, db := newFlattenTestAggregator(t)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go handleConnection(agg, server)
	out := bufio.NewReader(client)
	readUntil(t, client, out, "> ")

	agg.stopRepl()
	_, err := fmt.Fprintln(client, "flatten")
	require.NoError(t, err)
	require.Contains(t, readUntil(t, client, out, "> "), errFlattenShuttingDown.Error())
	require.Zero(t, db.calls.Load(), "storage Flatten must not run")
}

// readUntil reads from the REPL until its output contains marker.
func readUntil(t *testing.T, conn net.Conn, r *bufio.Reader, marker string) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var b strings.Builder
	for !strings.Contains(b.String(), marker) {
		c, err := r.ReadByte()
		require.NoError(t, err, "REPL output so far: %q", b.String())
		b.WriteByte(c)
	}
	return b.String()
}
