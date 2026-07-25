package macros

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

func TestChainlinkLatestRoundData(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())

	// https://sepolia.etherscan.io/address/0x694AA1769357215DE4FAC081bf1f309aDC325306
	// Sepolia ETH/USD. Pass a very large max age so this live-feed smoke test does
	// not flake on a testnet feed that updates infrequently; staleness logic is
	// covered by the unit tests in chainlink_test.go.
	value := chainlinkLatestRoundData("0x694AA1769357215DE4FAC081bf1f309aDC325306", 100*365*24*3600)
	if value == nil {
		t.Errorf("fail to query chainlink answer. expect a value, got nil")
	}
}
