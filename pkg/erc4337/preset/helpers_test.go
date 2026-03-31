package preset

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
)

func TestGetSenderLock(t *testing.T) {
	tests := []struct {
		name     string
		addr1    common.Address
		addr2    common.Address
		sameLock bool
	}{
		{
			name:     "same address returns the same lock",
			addr1:    common.HexToAddress("0x0000000000000000000000000000000000000001"),
			addr2:    common.HexToAddress("0x0000000000000000000000000000000000000001"),
			sameLock: true,
		},
		{
			name:     "different addresses same last byte should return the same lock",
			addr1:    common.HexToAddress("0x1111111111111111111111111111111111111101"),
			addr2:    common.HexToAddress("0x9999999999999999999999999999999999999901"),
			sameLock: true,
		},
		{
			name:     "different last bytes should return different locks",
			addr1:    common.HexToAddress("0x0000000000000000000000000000000000000001"),
			addr2:    common.HexToAddress("0x0000000000000000000000000000000000000002"),
			sameLock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lock1 := getSenderLock(tt.addr1)
			lock2 := getSenderLock(tt.addr2)
			if tt.sameLock {
				assert.Same(t, lock1, lock2, "expected same lock for %s and %s", tt.addr1.Hex(), tt.addr2.Hex())
			} else {
				assert.NotSame(t, lock1, lock2, "expected different locks for %s and %s", tt.addr1.Hex(), tt.addr2.Hex())
			}
		})
	}
}

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

func TestGetVerifyingPaymasterRequestForDuration(t *testing.T) {
	addr := common.HexToAddress("0x0000000000000000000000000000000000000001")

	tests := []struct {
		name     string
		duration time.Duration
	}{
		{name: "15 minutes", duration: 15 * time.Minute},
		{name: "1 hour", duration: time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().Unix()
			result := GetVerifyingPaymasterRequestForDuration(addr, tt.duration)
			after := time.Now().Unix()

			assert.Equal(t, addr, result.PaymasterAddress)
			assert.Equal(t, int64(0), result.ValidAfter.Int64())

			expectedMin := before + int64(tt.duration.Seconds())
			expectedMax := after + int64(tt.duration.Seconds())
			validUntil := result.ValidUntil.Int64()
			assert.GreaterOrEqual(t, validUntil, expectedMin, "ValidUntil too small")
			assert.LessOrEqual(t, validUntil, expectedMax, "ValidUntil too large")
		})
	}
}

func TestErrPaymasterNonceConflict(t *testing.T) {
	err := &ErrPaymasterNonceConflict{Nonce: big.NewInt(42)}
	assert.EqualError(t, err, "paymaster nonce conflict: rebuild required with nonce 42")
}
