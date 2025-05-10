package aggregator

import (
	"testing"
	"time"
)

func TestLastSeen(t *testing.T) {
	now := time.Now()

	testCases := []struct {
		name           string
		lastPingEpoch  int64
		expectedFormat string
	}{
		{
			name:           "seconds ago (milliseconds)",
			lastPingEpoch:  now.Add(-10 * time.Second).UnixMilli(),
			expectedFormat: "10s ago",
		},
		{
			name:           "minutes ago (milliseconds)",
			lastPingEpoch:  now.Add(-5 * time.Minute).UnixMilli(),
			expectedFormat: "5m0s ago",
		},
		{
			name:           "hours ago (milliseconds)",
			lastPingEpoch:  now.Add(-2 * time.Hour).UnixMilli(),
			expectedFormat: "2h0m ago",
		},
		{
			name:           "days ago (milliseconds)",
			lastPingEpoch:  now.Add(-48 * time.Hour).UnixMilli(),
			expectedFormat: "2d0h ago",
		},
		{
			name:           "seconds ago (seconds)",
			lastPingEpoch:  now.Add(-10 * time.Second).Unix(),
			expectedFormat: "10s ago",
		},
		{
			name:           "minutes ago (seconds)",
			lastPingEpoch:  now.Add(-5 * time.Minute).Unix(),
			expectedFormat: "5m0s ago",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := &OperatorNode{
				Address:       "0x123",
				RemoteIP:      "127.0.0.1",
				LastPingEpoch: tc.lastPingEpoch,
				Version:       "1.0.0",
			}

			result := node.LastSeen()
			if result != tc.expectedFormat {
				t.Errorf("Expected format %s, got %s", tc.expectedFormat, result)
			}
		})
	}
}
