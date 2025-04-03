package trigger

import (
	"testing"
	"time"
)

func TestEpochToCronMilliseconds(t *testing.T) {
	testCases := []struct {
		name           string
		epochMillis    int64
		expectedTime   time.Time
	}{
		{
			name:           "midnight UTC in milliseconds",
			epochMillis:    1743724800000, // 2025-04-04 00:00:00 UTC in milliseconds
			expectedTime:   time.Date(2025, 4, 4, 0, 0, 0, 0, time.UTC),
		},
		{
			name:           "noon UTC in milliseconds",
			epochMillis:    1743768000000, // 2025-04-04 12:00:00 UTC in milliseconds
			expectedTime:   time.Date(2025, 4, 4, 12, 0, 0, 0, time.UTC),
		},
		{
			name:           "specific time in milliseconds",
			epochMillis:    1743771845000, // 2025-04-04 13:04:05 UTC in milliseconds
			expectedTime:   time.Date(2025, 4, 4, 13, 4, 5, 0, time.UTC),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			convertedTime := time.Unix(tc.epochMillis/1000, 0)
			
			if !convertedTime.Equal(tc.expectedTime) {
				t.Errorf("Expected time %v, got %v", tc.expectedTime, convertedTime)
			}
		})
	}
}
