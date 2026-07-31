package userop

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// EntryPoint v0.7 splits the v0.6 UserOperation into two shapes that are easy
// to confuse:
//
//   - the *unpacked* form, which is what travels on the JSON-RPC wire
//     (eth_sendUserOperation, eth_estimateUserOperationGas). Factory and
//     paymaster each become their own fields.
//   - the *packed* form (PackedUserOperation), which is what the EntryPoint
//     contract hashes and executes. Gas limits and fees are squeezed into
//     bytes32 pairs, and factory/paymaster collapse back into initCode and
//     paymasterAndData.
//
// UserOperationV07 models the unpacked form because that is what callers build
// and what the bundler consumes; Pack* produce the packed encoding on demand.
//
// This is deliberately a separate type from the v0.6 UserOperation rather than
// a set of extra fields on it. The two hashing schemes are incompatible, and a
// single struct carrying both would make it possible to sign under the wrong
// one -- an error that only shows up as an on-chain AA24 revert.
type UserOperationV07 struct {
	Sender   common.Address `json:"sender"`
	Nonce    *big.Int       `json:"nonce"`
	CallData []byte         `json:"callData"`

	// Factory is nil once the account is deployed. When set, Factory and
	// FactoryData together form the v0.6-style initCode.
	Factory     *common.Address `json:"factory,omitempty"`
	FactoryData []byte          `json:"factoryData,omitempty"`

	CallGasLimit         *big.Int `json:"callGasLimit"`
	VerificationGasLimit *big.Int `json:"verificationGasLimit"`
	PreVerificationGas   *big.Int `json:"preVerificationGas"`
	MaxFeePerGas         *big.Int `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *big.Int `json:"maxPriorityFeePerGas"`

	// Paymaster is nil for an unsponsored operation. The three paymaster
	// fields are only meaningful when it is set.
	Paymaster                     *common.Address `json:"paymaster,omitempty"`
	PaymasterVerificationGasLimit *big.Int        `json:"paymasterVerificationGasLimit,omitempty"`
	PaymasterPostOpGasLimit       *big.Int        `json:"paymasterPostOpGasLimit,omitempty"`
	PaymasterData                 []byte          `json:"paymasterData,omitempty"`

	Signature []byte `json:"signature"`
}

// packUint128Pair encodes two values into a single bytes32, high first. Values
// wider than 128 bits are rejected rather than silently truncated: a truncated
// gas limit still produces a well-formed UserOp that fails much later, on
// chain, with no indication of where the number was lost.
func packUint128Pair(high, low *big.Int, highName, lowName string) ([32]byte, error) {
	var out [32]byte
	for _, f := range []struct {
		v    *big.Int
		name string
	}{{high, highName}, {low, lowName}} {
		if f.v == nil {
			return out, fmt.Errorf("%s is nil", f.name)
		}
		if f.v.Sign() < 0 {
			return out, fmt.Errorf("%s is negative", f.name)
		}
		if f.v.BitLen() > 128 {
			return out, fmt.Errorf("%s overflows uint128 (%d bits)", f.name, f.v.BitLen())
		}
	}
	high.FillBytes(out[0:16])
	low.FillBytes(out[16:32])
	return out, nil
}

// AccountGasLimits packs verificationGasLimit into the high 16 bytes and
// callGasLimit into the low 16, per ERC-4337 v0.7.
func (op *UserOperationV07) AccountGasLimits() ([32]byte, error) {
	return packUint128Pair(op.VerificationGasLimit, op.CallGasLimit,
		"verificationGasLimit", "callGasLimit")
}

// GasFees packs maxPriorityFeePerGas into the high 16 bytes and maxFeePerGas
// into the low 16. Note the ordering is priority-then-max, which is the
// reverse of how the fields are usually written out.
func (op *UserOperationV07) GasFees() ([32]byte, error) {
	return packUint128Pair(op.MaxPriorityFeePerGas, op.MaxFeePerGas,
		"maxPriorityFeePerGas", "maxFeePerGas")
}

// InitCode returns factory ++ factoryData, or empty when the account already
// exists.
func (op *UserOperationV07) InitCode() []byte {
	if op.Factory == nil {
		return []byte{}
	}
	return append(op.Factory.Bytes(), op.FactoryData...)
}

// PaymasterAndData returns paymaster ++ verificationGasLimit(16) ++
// postOpGasLimit(16) ++ data, or empty when the operation is unsponsored.
func (op *UserOperationV07) PaymasterAndData() ([]byte, error) {
	if op.Paymaster == nil {
		return []byte{}, nil
	}
	limits, err := packUint128Pair(
		op.PaymasterVerificationGasLimit, op.PaymasterPostOpGasLimit,
		"paymasterVerificationGasLimit", "paymasterPostOpGasLimit")
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, common.AddressLength+32+len(op.PaymasterData))
	out = append(out, op.Paymaster.Bytes()...)
	out = append(out, limits[:]...)
	out = append(out, op.PaymasterData...)
	return out, nil
}

var (
	bytes32Ty, _ = abi.NewType("bytes32", "", nil)
	addressTy, _ = abi.NewType("address", "", nil)
	uint256Ty, _ = abi.NewType("uint256", "", nil)
)

// PackForSignature returns the ABI encoding the EntryPoint hashes to produce
// the inner half of the userOpHash. Dynamic fields appear as their keccak
// digests, and signature is excluded -- it is what gets computed over this.
func (op *UserOperationV07) PackForSignature() ([]byte, error) {
	accountGasLimits, err := op.AccountGasLimits()
	if err != nil {
		return nil, err
	}
	gasFees, err := op.GasFees()
	if err != nil {
		return nil, err
	}
	paymasterAndData, err := op.PaymasterAndData()
	if err != nil {
		return nil, err
	}
	// Nonce and preVerificationGas are packed as raw uint256 rather than
	// through packUint128Pair, so they miss its guards. A negative value would
	// two's-complement into an enormous uint256 and produce a hash over
	// arguments nobody intended, rather than failing — the same class of
	// silently-wrong calldata every other numeric field here rejects.
	for _, f := range []struct {
		v    *big.Int
		name string
	}{{op.Nonce, "nonce"}, {op.PreVerificationGas, "preVerificationGas"}} {
		if f.v == nil {
			return nil, fmt.Errorf("%s is nil", f.name)
		}
		if f.v.Sign() < 0 {
			return nil, fmt.Errorf("%s is negative", f.name)
		}
	}

	args := abi.Arguments{
		{Type: addressTy}, {Type: uint256Ty}, {Type: bytes32Ty}, {Type: bytes32Ty},
		{Type: bytes32Ty}, {Type: uint256Ty}, {Type: bytes32Ty}, {Type: bytes32Ty},
	}
	return args.Pack(
		op.Sender,
		op.Nonce,
		toBytes32(crypto.Keccak256(op.InitCode())),
		toBytes32(crypto.Keccak256(op.CallData)),
		accountGasLimits,
		op.PreVerificationGas,
		gasFees,
		toBytes32(crypto.Keccak256(paymasterAndData)),
	)
}

// GetUserOpHash returns the hash the account signs, binding the operation to a
// specific EntryPoint and chain so a signature cannot be replayed across
// either.
func (op *UserOperationV07) GetUserOpHash(entryPoint common.Address, chainID *big.Int) (common.Hash, error) {
	if chainID == nil {
		return common.Hash{}, fmt.Errorf("chainID is nil")
	}
	inner, err := op.PackForSignature()
	if err != nil {
		return common.Hash{}, err
	}
	args := abi.Arguments{{Type: bytes32Ty}, {Type: addressTy}, {Type: uint256Ty}}
	outer, err := args.Pack(toBytes32(crypto.Keccak256(inner)), entryPoint, chainID)
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(crypto.Keccak256(outer)), nil
}

func toBytes32(b []byte) [32]byte {
	var out [32]byte
	copy(out[:], b)
	return out
}
