package preset

import (
	"testing"
	"time"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestGetSenderLock(t *testing.T) {
	tests := []struct {
		name			string
		addr1			common.Address
		addr2 		common.Address
		sameLock	bool
	}{
		{
			name:			"same address returns the same lock",
			addr1:		common.HexToAddress("0x0000000000000000000000000000000000000001"),
			addr2: 		common.HexToAddress("0x0000000000000000000000000000000000000001"),
			sameLock: true,
		},
		{
			name: 			"different addresses same last byte should return the same lock",
			addr1:		common.HexToAddress("0x1111111111111111111111111111111111111101"),
			addr2: 		common.HexToAddress("0x9999999999999999999999999999999999999901"),
			sameLock:	true,
		},
		{
			name:			"different last bytes should return different locks",
			addr1:		common.HexToAddress("0x0000000000000000000000000000000000000001"),
			addr2:		common.HexToAddress("0x0000000000000000000000000000000000000002"),
			sameLock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lock1 := getSenderLock(tt.addr1)
			lock2 := getSenderLock(tt.addr2)
			if tt.sameLock && lock1 != lock2 {
				t.Errorf("Expected same lock for %s as well as %s, but returned different instead", tt.addr1.Hex(), tt.addr2.Hex())
			}
			if !tt.sameLock && lock1 == lock2 {
				t.Errorf("Expected different locks for %s and %s, but returned same instead", tt.addr1.Hex(), tt.addr2.Hex())
			}
		})
	}
}

func TestCheckUserOpExecutionSuccess(t *testing.T) {
	tests := []struct {
		name			string
		log				types.Log
		expected 	bool
	}{
		{
			name:		"successful execution",
			log: types.Log {
				Data: func() []byte {
					data := make([]byte, 128)
					data[63] = 1
					return data
				}(),
			},
			expected: true,
		},
		{
			name:		"failed execution",
			log: types.Log {
				Data: make([]byte, 128),
			},
			expected: false,
		},
		{
			name:		"data too short",
			log: types.Log {
				Data: make([]byte, 127),
			},
			expected: false,
		},
		{
			name:		"empty data",
			log: types.Log{
				Data: []byte{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkUserOpExecutionSuccess(tt.log)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v instead", tt.expected, result)
			}
		})
	}
}

func TestGetVerifyingPaymasterRequestForDuration(t *testing.T) {
	addr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	duration := 15 * time.Minute
	
	before := time.Now().Unix()
	result := GetVerifyingPaymasterRequestForDuration(addr, duration)
	after := time.Now().Unix()

	if result.PaymasterAddress != addr {
		t.Errorf("Expected address %s, got %s instead", addr.Hex(), result.PaymasterAddress.Hex())
	}
	
	if result.ValidAfter.Int64() != 0 {
		t.Errorf("Expected ValidAfter to be 0, got %d instead", result.ValidAfter.Int64())
	}

	expectedMin := before + int64(duration.Seconds())
	expectedMax := after  + int64(duration.Seconds())
	validUntil 	:= result.ValidUntil.Int64()

	if validUntil < expectedMin || validUntil > expectedMax {
		t.Errorf("ValidUntil %d not in the valid range [%d, %d]", validUntil, expectedMin, expectedMax)
	}
}

func TestErrPaymasterNonceConflict(t *testing.T) {
	err 		 := &ErrPaymasterNonceConflict{Nonce: big.NewInt(42)}
	got 		 := err.Error()
	expected := "paymaster nonce conflict: rebuild required with nonce 42"
	if got != expected {
		t.Errorf("Expected %s, got %s instead", expected, got)
	}
}

