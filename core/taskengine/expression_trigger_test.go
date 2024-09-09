package taskengine

import (
	"testing"
)

func TestChainlinkLatestAnswer(t *testing.T) {
	SetRpc("https://sepolia.drpc.org")

	// https://sepolia.etherscan.io/address/0x9aCb42Ac07C72cFc29Cd95d9DEaC807E93ada1F6#code
	value := chainlinkLatestAnswer("0x694AA1769357215DE4FAC081bf1f309aDC325306")
	if value == nil {
		t.Errorf("fail to query chainlink answer. expect a value, got nil")
	}
}
