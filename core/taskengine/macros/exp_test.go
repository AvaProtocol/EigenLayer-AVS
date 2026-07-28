package macros

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

// testNoStaleBound is a max-age override (seconds) large enough that a live
// testnet feed never reads as stale, so RPC-gated smoke tests don't flake on
// Sepolia freshness. ~50 years, and kept under math.MaxInt32 so the untyped
// constant is safe on 32-bit builds. Staleness logic itself is unit-tested in
// chainlink_test.go.
const testNoStaleBound = 50 * 365 * 24 * 3600

func TestChainlinkLatestRoundData(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())

	// https://sepolia.etherscan.io/address/0x694AA1769357215DE4FAC081bf1f309aDC325306
	value := chainlinkLatestRoundData("0x694AA1769357215DE4FAC081bf1f309aDC325306", testNoStaleBound)
	if value == nil {
		t.Errorf("fail to query chainlink answer. expect a value, got nil")
	}
}
