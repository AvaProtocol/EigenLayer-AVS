package taskengine

import (
	"testing"
)

func TestBlockSearchRanges(t *testing.T) {
	testCases := []struct {
		name               string
		chainID            uint64
		expectedOneMonth   uint64
		expectedTwoMonths  uint64
		expectedFourMonths uint64
	}{
		{
			name:               "Ethereum Mainnet",
			chainID:            1,
			expectedOneMonth:   21600, // ~3 days at 12s blocks (1/10 of original 216000)
			expectedTwoMonths:  43200, // ~6 days at 12s blocks (1/10 of original 432000)
			expectedFourMonths: 86400, // ~12 days at 12s blocks (1/10 of original 864000)
		},
		{
			name:               "Ethereum Sepolia",
			chainID:            11155111,
			expectedOneMonth:   21600, // ~3 days at 12s blocks (1/10 of original 216000)
			expectedTwoMonths:  43200, // ~6 days at 12s blocks (1/10 of original 432000)
			expectedFourMonths: 86400, // ~12 days at 12s blocks (1/10 of original 864000)
		},
		{
			name:               "Base Mainnet",
			chainID:            8453,
			expectedOneMonth:   129600, // ~3 days at 2s blocks (1/10 of original 1296000)
			expectedTwoMonths:  259200, // ~6 days at 2s blocks (1/10 of original 2592000)
			expectedFourMonths: 518400, // ~12 days at 2s blocks (1/10 of original 5184000)
		},
		{
			name:               "Base Sepolia",
			chainID:            84532,
			expectedOneMonth:   129600, // ~3 days at 2s blocks (1/10 of original 1296000)
			expectedTwoMonths:  259200, // ~6 days at 2s blocks (1/10 of original 2592000)
			expectedFourMonths: 518400, // ~12 days at 2s blocks (1/10 of original 5184000)
		},
		{
			name:               "Unknown Chain (defaults to Ethereum)",
			chainID:            999999,
			expectedOneMonth:   21600, // Default to reduced Ethereum timing (1/10 of original 216000)
			expectedTwoMonths:  43200, // Default to reduced Ethereum timing (1/10 of original 432000)
			expectedFourMonths: 86400, // Default to reduced Ethereum timing (1/10 of original 864000)
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ranges := GetBlockSearchRanges(tc.chainID)

			if ranges.OneMonth != tc.expectedOneMonth {
				t.Errorf("Chain %d (%s): Expected OneMonth=%d, got %d",
					tc.chainID, tc.name, tc.expectedOneMonth, ranges.OneMonth)
			}

			if ranges.TwoMonths != tc.expectedTwoMonths {
				t.Errorf("Chain %d (%s): Expected TwoMonths=%d, got %d",
					tc.chainID, tc.name, tc.expectedTwoMonths, ranges.TwoMonths)
			}

			if ranges.FourMonths != tc.expectedFourMonths {
				t.Errorf("Chain %d (%s): Expected FourMonths=%d, got %d",
					tc.chainID, tc.name, tc.expectedFourMonths, ranges.FourMonths)
			}

			// Test the slice version
			searchRanges := GetChainSearchRanges(tc.chainID)
			expectedSlice := []uint64{tc.expectedOneMonth, tc.expectedTwoMonths, tc.expectedFourMonths}

			if len(searchRanges) != 3 {
				t.Errorf("Chain %d (%s): Expected 3 search ranges, got %d",
					tc.chainID, tc.name, len(searchRanges))
			}

			for i, expected := range expectedSlice {
				if i < len(searchRanges) && searchRanges[i] != expected {
					t.Errorf("Chain %d (%s): Expected searchRanges[%d]=%d, got %d",
						tc.chainID, tc.name, i, expected, searchRanges[i])
				}
			}
		})
	}
}

func TestBlockCalculations(t *testing.T) {
	// Verify the mathematics behind our block calculations
	testCases := []struct {
		name           string
		blockTimeMs    uint64 // milliseconds
		expectedDaily  uint64
		chain          string
		blockTimeFloat float64 // seconds as float for accurate calculation
	}{
		{
			name:           "Ethereum - 12 second blocks",
			blockTimeMs:    12000,
			expectedDaily:  7200, // 86400 / 12 = 7200
			chain:          "Ethereum/Sepolia",
			blockTimeFloat: 12.0,
		},
		{
			name:           "Base - 2 second blocks",
			blockTimeMs:    2000,
			expectedDaily:  43200, // 86400 / 2 = 43200
			chain:          "Base",
			blockTimeFloat: 2.0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			secondsPerDay := float64(86400)
			actualDaily := uint64(secondsPerDay / tc.blockTimeFloat)

			if actualDaily != tc.expectedDaily {
				t.Errorf("%s: Expected %d blocks/day, calculated %d",
					tc.chain, tc.expectedDaily, actualDaily)
			}

			// Test 30-day calculation
			expectedOneMonth := tc.expectedDaily * 30
			t.Logf("%s: %d blocks/day × 30 days = %d blocks (1 month)",
				tc.chain, tc.expectedDaily, expectedOneMonth)

			// Test 60-day calculation
			expectedTwoMonths := tc.expectedDaily * 60
			t.Logf("%s: %d blocks/day × 60 days = %d blocks (2 months)",
				tc.chain, tc.expectedDaily, expectedTwoMonths)

			// Test 120-day calculation
			expectedFourMonths := tc.expectedDaily * 120
			t.Logf("%s: %d blocks/day × 120 days = %d blocks (4 months)",
				tc.chain, tc.expectedDaily, expectedFourMonths)
		})
	}
}

func BenchmarkGetChainSearchRanges(b *testing.B) {
	// Only include supported chains: Ethereum and Base
	chainIDs := []uint64{1, 11155111, 8453, 84532}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chainID := chainIDs[i%len(chainIDs)]
		_ = GetChainSearchRanges(chainID)
	}
}

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
