package taskengine

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
)

// latestHeaderLagRetries is how many blocks behind the reported head a
// "latest" block-trigger run may settle when the head's own header is not
// found yet. Two covers a node that is a block or two behind its neighbour;
// anything further is not lag.
const latestHeaderLagRetries = 2

// blockHeaderReader is the one ChainStateReader method the lag fallback needs.
type blockHeaderReader interface {
	HeaderByNumber(ctx context.Context, number *big.Int) (*BlockHeader, error)
}

// isBlockNotFound is true for go-ethereum's NotFound sentinel and for the
// same text after a worker round-trip has wrapped it into a plain message.
// The text match is anchored to the end of the message, which is where the
// serialized sentinel lands, and a JSON-RPC "method not found" (-32601) is
// excluded by name: that is a node that cannot answer, not a block that is not
// there yet, and stepping back a block would hide it.
func isBlockNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ethereum.NotFound) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.HasSuffix(msg, "method not found") {
		return false
	}
	return strings.HasSuffix(msg, "not found")
}

// latestHeaderBehindLag reads the header of latest-1, then latest-2, after the
// head's own header was not found. It returns the header and the block it
// settled on. A read that fails for any other reason stops the walk and is
// returned as-is; a head still unknown after the retries returns the last
// not-found error. latest is the block eth_blockNumber reported.
func latestHeaderBehindLag(ctx context.Context, reader blockHeaderReader, latest uint64) (*BlockHeader, uint64, error) {
	var lastErr error = ethereum.NotFound
	for back := uint64(1); back <= latestHeaderLagRetries && back <= latest; back++ {
		candidate := latest - back
		header, err := reader.HeaderByNumber(ctx, new(big.Int).SetUint64(candidate))
		if err == nil {
			return header, candidate, nil
		}
		lastErr = err
		if !isBlockNotFound(err) {
			return nil, candidate, err
		}
	}
	return nil, latest, lastErr
}
