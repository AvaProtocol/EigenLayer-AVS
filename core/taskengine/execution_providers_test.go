package taskengine

import "testing"

func TestGetNetworkName(t *testing.T) {
	tests := []struct {
		chainID int64
		want    string
	}{
		{1, "ethereum"},
		{11155111, "sepolia"},
		{8453, "base"},
		{84532, "base-sepolia"},
		{56, "bnb-mainnet"},
		{42161, "arbitrum"},
		{10, "optimism"},
		{130, "unichain"},
		{4663, "robinhood"},
		{424242, "unknown"},
	}
	for _, tt := range tests {
		if got := GetNetworkName(tt.chainID); got != tt.want {
			t.Errorf("GetNetworkName(%d) = %q, want %q", tt.chainID, got, tt.want)
		}
	}
}
