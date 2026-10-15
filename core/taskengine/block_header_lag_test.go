package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
)

// fakeHeaderReader answers per block: a header, or an error.
type fakeHeaderReader struct {
	errs  map[uint64]error
	reads []uint64
}

func (f *fakeHeaderReader) HeaderByNumber(_ context.Context, number *big.Int) (*BlockHeader, error) {
	n := number.Uint64()
	f.reads = append(f.reads, n)
	if err, ok := f.errs[n]; ok {
		return nil, err
	}
	return &BlockHeader{Number: n}, nil
}

func TestIsBlockNotFound(t *testing.T) {
	if !isBlockNotFound(ethereum.NotFound) {
		t.Error("go-ethereum's sentinel must count")
	}
	if !isBlockNotFound(fmt.Errorf("failed to get block header for block 11917193 on chain 11155111: %w", ethereum.NotFound)) {
		t.Error("a wrapped sentinel must count")
	}
	if !isBlockNotFound(errors.New("worker GetBlockHeader (chain 11155111): rpc error: not found")) {
		t.Error("the worker's plain-text form must count")
	}
	if isBlockNotFound(errors.New("429 rate limit")) {
		t.Error("a rate limit is not a missing block")
	}
	if isBlockNotFound(nil) {
		t.Error("nil is not an error")
	}
}

// The SDK E2E shape: eth_blockNumber says N, the header of N is not found on
// the node that answers next. N-1 is there.
func TestLatestHeaderBehindLagSettlesOneBack(t *testing.T) {
	reader := &fakeHeaderReader{errs: map[uint64]error{100: ethereum.NotFound}}
	header, settled, err := latestHeaderBehindLag(context.Background(), reader, 100)
	if err != nil {
		t.Fatal(err)
	}
	if settled != 99 || header.Number != 99 {
		t.Errorf("settled on %d (header %d), want 99", settled, header.Number)
	}
	if len(reader.reads) != 1 || reader.reads[0] != 99 {
		t.Errorf("reads = %v, want [99]", reader.reads)
	}
}

func TestLatestHeaderBehindLagSettlesTwoBack(t *testing.T) {
	reader := &fakeHeaderReader{errs: map[uint64]error{99: ethereum.NotFound}}
	_, settled, err := latestHeaderBehindLag(context.Background(), reader, 100)
	if err != nil {
		t.Fatal(err)
	}
	if settled != 98 {
		t.Errorf("settled on %d, want 98", settled)
	}
}

// Three missing blocks is not lag. The last not-found error comes back and the
// walk stops at the retry cap.
func TestLatestHeaderBehindLagGivesUpAfterTheCap(t *testing.T) {
	reader := &fakeHeaderReader{errs: map[uint64]error{99: ethereum.NotFound, 98: ethereum.NotFound, 97: ethereum.NotFound}}
	_, settled, err := latestHeaderBehindLag(context.Background(), reader, 100)
	if !isBlockNotFound(err) {
		t.Fatalf("want a not-found error, got %v", err)
	}
	if settled != 100 {
		t.Errorf("settled = %d, want the head back when nothing was found", settled)
	}
	if len(reader.reads) != latestHeaderLagRetries {
		t.Errorf("reads = %v, want %d", reader.reads, latestHeaderLagRetries)
	}
}

// Any other failure is not papered over by stepping back further.
func TestLatestHeaderBehindLagStopsOnAnotherError(t *testing.T) {
	boom := errors.New("connection reset")
	reader := &fakeHeaderReader{errs: map[uint64]error{99: boom, 98: nil}}
	_, _, err := latestHeaderBehindLag(context.Background(), reader, 100)
	if !errors.Is(err, boom) {
		t.Fatalf("want the read error, got %v", err)
	}
	if len(reader.reads) != 1 {
		t.Errorf("reads = %v, want to stop after the first non-lag error", reader.reads)
	}
}

// Genesis has nothing behind it.
func TestLatestHeaderBehindLagAtGenesis(t *testing.T) {
	reader := &fakeHeaderReader{}
	_, settled, err := latestHeaderBehindLag(context.Background(), reader, 0)
	if !isBlockNotFound(err) || settled != 0 || len(reader.reads) != 0 {
		t.Errorf("genesis: err=%v settled=%d reads=%v", err, settled, reader.reads)
	}
}
