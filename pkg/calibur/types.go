package calibur

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// DefaultCaliburAddress is the Calibur singleton deployed at the same address on all chains.
var DefaultCaliburAddress = common.HexToAddress("0x000000009B1D0aF20D8C6d0A44e162d11F9b8f00")

const (
	// EIP-712 domain parameters
	EIP712Name    = "Calibur"
	EIP712Version = "1.0.0"

	// KeyType values matching Calibur's KeyType enum
	KeyTypeP256         = 0
	KeyTypeWebAuthnP256 = 1
	KeyTypeSecp256k1    = 2
)

// Call represents a single operation in a Calibur batched call.
type Call struct {
	To    common.Address
	Value *big.Int
	Data  []byte
}

// BatchedCall represents a batch of calls to execute atomically.
type BatchedCall struct {
	Calls           []Call
	RevertOnFailure bool
}

// SignedBatchedCall is the full signed payload for Calibur's execute() function.
type SignedBatchedCall struct {
	BatchedCall BatchedCall
	Nonce       *big.Int
	KeyHash     [32]byte
	Executor    common.Address // address(0) = anyone can submit
	Deadline    *big.Int       // block timestamp expiry
}
