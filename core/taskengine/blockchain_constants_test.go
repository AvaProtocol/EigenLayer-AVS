package taskengine

import (
	"testing"
)

func TestBlockSearchRanges(t *testing.T) {
	testCases := []struct {
		name                string
		chainID             uint64
		expectedThreeMonths uint64
		expectedSixMonths   uint64
	}{
		{
			name:                "Ethereum Mainnet",
			chainID:             1,
			expectedThreeMonths: 648000,  // 90 days at 12s blocks
			expectedSixMonths:   1296000, // 180 days at 12s blocks
		},
		{
			name:                "Ethereum Sepolia",
			chainID:             11155111,
			expectedThreeMonths: 648000,  // 90 days at 12s blocks
			expectedSixMonths:   1296000, // 180 days at 12s blocks
		},
		{
			name:                "Base Mainnet",
			chainID:             8453,
			expectedThreeMonths: 3888000, // 90 days at 2s blocks
			expectedSixMonths:   7776000, // 180 days at 2s blocks
		},
		{
			name:                "Base Sepolia",
			chainID:             84532,
			expectedThreeMonths: 3888000, // 90 days at 2s blocks
			expectedSixMonths:   7776000, // 180 days at 2s blocks
		},
		{
			name:                "BNB Smart Chain Mainnet",
			chainID:             56,
			expectedThreeMonths: 10368000, // 90 days at 0.75s blocks
			expectedSixMonths:   20736000, // 180 days at 0.75s blocks
		},
		{
			name:                "BNB Smart Chain Testnet",
			chainID:             97,
			expectedThreeMonths: 10368000, // 90 days at 0.75s blocks
			expectedSixMonths:   20736000, // 180 days at 0.75s blocks
		},
		{
			name:                "Polygon Mainnet",
			chainID:             137,
			expectedThreeMonths: 3888000, // 90 days at 2s blocks
			expectedSixMonths:   7776000, // 180 days at 2s blocks
		},
		{
			name:                "Unknown Chain (defaults to Ethereum)",
			chainID:             999999,
			expectedThreeMonths: 648000,  // Default to Ethereum timing
			expectedSixMonths:   1296000, // Default to Ethereum timing
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ranges := GetBlockSearchRanges(tc.chainID)

			if ranges.ThreeMonths != tc.expectedThreeMonths {
				t.Errorf("Chain %d (%s): Expected ThreeMonths=%d, got %d",
					tc.chainID, tc.name, tc.expectedThreeMonths, ranges.ThreeMonths)
			}

			if ranges.SixMonths != tc.expectedSixMonths {
				t.Errorf("Chain %d (%s): Expected SixMonths=%d, got %d",
					tc.chainID, tc.name, tc.expectedSixMonths, ranges.SixMonths)
			}

			// Test the slice version
			searchRanges := GetChainSearchRanges(tc.chainID)
			expectedSlice := []uint64{tc.expectedThreeMonths, tc.expectedSixMonths}

			if len(searchRanges) != 2 {
				t.Errorf("Chain %d (%s): Expected 2 search ranges, got %d",
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
			chain:          "Base/Polygon",
			blockTimeFloat: 2.0,
		},
		{
			name:           "BNB Chain - 0.75 second blocks",
			blockTimeMs:    750,
			expectedDaily:  115200, // 86400 / 0.75 = 115200
			chain:          "BNB Chain",
			blockTimeFloat: 0.75,
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

			// Test 90-day calculation
			expectedThreeMonths := tc.expectedDaily * 90
			t.Logf("%s: %d blocks/day × 90 days = %d blocks (3 months)",
				tc.chain, tc.expectedDaily, expectedThreeMonths)

			// Test 180-day calculation
			expectedSixMonths := tc.expectedDaily * 180
			t.Logf("%s: %d blocks/day × 180 days = %d blocks (6 months)",
				tc.chain, tc.expectedDaily, expectedSixMonths)
		})
	}
}

func BenchmarkGetChainSearchRanges(b *testing.B) {
	chainIDs := []uint64{1, 56, 8453, 137, 11155111, 84532, 97}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chainID := chainIDs[i%len(chainIDs)]
		_ = GetChainSearchRanges(chainID)
	}
}
