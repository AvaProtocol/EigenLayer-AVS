package ethsighash

import (
	"encoding/hex"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"strings"
	"testing"
)

func TestGetMethodFromSelector(t *testing.T) {
	// ERC20 ABI with transfer and balanceOf methods
	const abiJSON = `[
		{
			"constant": false,
			"inputs": [
				{
					"name": "_to",
					"type": "address"
				},
				{
					"name": "_value",
					"type": "uint256"
				}
			],
			"name": "transfer",
			"outputs": [],
			"payable": false,
			"stateMutability": "nonpayable",
			"type": "function"
		},
		{
			"constant": true,
			"inputs": [
				{
					"name": "who",
					"type": "address"
				}
			],
			"name": "balanceOf",
			"outputs": [
				{
					"name": "",
					"type": "uint256"
				}
			],
			"payable": false,
			"stateMutability": "view",
			"type": "function"
		}
	]`

	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		t.Fatalf("failed to parse ABI: %v", err)
	}

	// Helper function to decode hex string to bytes
	decodeHex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatalf("failed to decode hex: %v", err)
		}
		return b
	}

	tests := []struct {
		name        string
		selector    []byte
		wantMethod  string
		wantErr     bool
		errContains string
	}{
		{
			name:       "valid balanceOf selector",
			selector:   decodeHex("70a08231000000000000000000000000ce289bb9fb0a9591317981223cbe33d5dc42268d"),
			wantMethod: "balanceOf",
			wantErr:    false,
		},
		{
			name:       "valid transfer selector",
			selector:   decodeHex("a9059cbb000000000000000000000000ce289bb9fb0a9591317981223cbe33d5dc42268d0000000000000000000000000000000000000000000000000de0b6b3a7640000"),
			wantMethod: "transfer",
			wantErr:    false,
		},
		{
			name:        "invalid selector length",
			selector:    []byte{0x70, 0xa0},
			wantErr:     true,
			errContains: "invalid selector length",
		},
		{
			name:        "unknown selector",
			selector:    decodeHex("12345678"),
			wantErr:     true,
			errContains: "no matching method found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, err := GetMethodFromSelector(parsedABI, tt.selector)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
					return
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				if method != nil {
					t.Error("expected nil method but got non-nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if method == nil {
				t.Error("expected non-nil method but got nil")
				return
			}
			if method.Name != tt.wantMethod {
				t.Errorf("got method %q, want %q", method.Name, tt.wantMethod)
			}
		})
	}
}