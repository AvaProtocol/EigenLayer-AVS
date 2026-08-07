package preset

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
)

// checkUserOpExecutionSuccess survived the removal of the v0.6 send path, so
// its coverage has to survive with it. It reads the `success` bool out of a
// UserOperationEvent's data — nonce (32) ++ success (32) ++ actualGasCost (32)
// ++ actualGasUsed (32) — and the interesting cases are the malformed ones: a
// short or empty payload must read as failure rather than panicking or
// reporting a success that never happened.
func TestCheckUserOpExecutionSuccess(t *testing.T) {
	tests := []struct {
		name     string
		log      types.Log
		expected bool
	}{
		{
			name: "successful execution",
			log: types.Log{
				Data: func() []byte {
					data := make([]byte, 128)
					data[63] = 1
					return data
				}(),
			},
			expected: true,
		},
		{
			name: "failed execution",
			log: types.Log{
				Data: make([]byte, 128),
			},
			expected: false,
		},
		{
			name: "exactly 64 bytes",
			log: types.Log{
				Data: make([]byte, 64),
			},
			expected: false,
		},
		{
			name: "data too short",
			log: types.Log{
				Data: make([]byte, 127),
			},
			expected: false,
		},
		{
			name: "empty data",
			log: types.Log{
				Data: []byte{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkUserOpExecutionSuccess(tt.log)
			assert.Equal(t, tt.expected, result)
		})
	}
}
