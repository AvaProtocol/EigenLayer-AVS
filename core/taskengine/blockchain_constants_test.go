package taskengine

import (
	"testing"
)

func TestGetDefaultGasPrice(t *testing.T) {
	cases := []struct {
		name    string
		chainID uint64
		want    uint64
	}{
		{"Ethereum Mainnet", 1, 5_000_000_000},
		{"Ethereum Sepolia", 11155111, 500_000_000},
		{"Base", 8453, 50_000_000},
		{"Base Sepolia", 84532, 10_000_000},
		{"Unknown chain falls back to DefaultGasPrice", 99999, DefaultGasPrice},
		{"Zero chain falls back to DefaultGasPrice", 0, DefaultGasPrice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetDefaultGasPrice(tc.chainID); got != tc.want {
				t.Errorf("GetDefaultGasPrice(%d) = %d, want %d", tc.chainID, got, tc.want)
			}
		})
	}
}

func TestLookupStablecoin(t *testing.T) {
	cases := []struct {
		name    string
		chainID uint64
		address string
		wantOk  bool
		wantSym string
		wantDec uint32
	}{
		{"USDC mainnet", 1, "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", true, "USDC", 6},
		{"USDC mainnet uppercase", 1, "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", true, "USDC", 6},
		{"DAI mainnet", 1, "0x6b175474e89094c44da98b954eedeac495271d0f", true, "DAI", 18},
		{"PYUSD mainnet", 1, "0x6c3ea9036406852006290770bedfcaba0e23a0e8", true, "PYUSD", 6},
		{"GUSD has 2 decimals", 1, "0x056fd409e1d7a124bd7017459dfea2f387b6d5cd", true, "GUSD", 2},
		{"USDC base", 8453, "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", true, "USDC", 6},
		{"USDC sepolia (Circle test)", 11155111, "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238", true, "USDC", 6},
		{"unknown ERC20 mainnet", 1, "0x0000000000000000000000000000000000000001", false, "", 0},
		{"unknown chain", 99999, "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", false, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := LookupStablecoin(tc.chainID, tc.address)
			if ok != tc.wantOk {
				t.Errorf("LookupStablecoin(%d, %s) ok = %v, want %v", tc.chainID, tc.address, ok, tc.wantOk)
			}
			if info.Symbol != tc.wantSym {
				t.Errorf("symbol = %q, want %q", info.Symbol, tc.wantSym)
			}
			if info.Decimals != tc.wantDec {
				t.Errorf("decimals = %d, want %d", info.Decimals, tc.wantDec)
			}
		})
	}
}
