package calibur

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// EIP-712 type hashes — precomputed from Calibur's Solidity source.
var (
	// keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract,bytes32 salt)")
	domainTypeHash = crypto.Keccak256Hash([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract,bytes32 salt)",
	))

	// keccak256("SignedBatchedCall(BatchedCall batchedCall,uint256 nonce,bytes32 keyHash,address executor,uint256 deadline)BatchedCall(Call[] calls,bool revertOnFailure)Call(address to,uint256 value,bytes data)")
	signedBatchedCallTypeHash = crypto.Keccak256Hash([]byte(
		"SignedBatchedCall(BatchedCall batchedCall,uint256 nonce,bytes32 keyHash,address executor,uint256 deadline)" +
			"BatchedCall(Call[] calls,bool revertOnFailure)" +
			"Call(address to,uint256 value,bytes data)",
	))

	// keccak256("BatchedCall(Call[] calls,bool revertOnFailure)Call(address to,uint256 value,bytes data)")
	batchedCallTypeHash = crypto.Keccak256Hash([]byte(
		"BatchedCall(Call[] calls,bool revertOnFailure)" +
			"Call(address to,uint256 value,bytes data)",
	))

	// keccak256("Call(address to,uint256 value,bytes data)")
	callTypeHash = crypto.Keccak256Hash([]byte(
		"Call(address to,uint256 value,bytes data)",
	))

	nameHash    = crypto.Keccak256Hash([]byte(EIP712Name))
	versionHash = crypto.Keccak256Hash([]byte(EIP712Version))
)

// BuildDomainSeparator computes the EIP-712 domain separator for a Calibur wallet.
// The verifyingContract is the user's EOA (which delegates to Calibur via EIP-7702).
// The salt encodes the Calibur implementation address.
func BuildDomainSeparator(chainID int64, eoaAddress common.Address, caliburImplAddress common.Address) [32]byte {
	// Salt is packed as: saltPrefix || implementation address
	// In Calibur, salt = saltPrefix.pack(implementation) where saltPrefix is bytes12(0)
	// Result: bytes12(0) || bytes20(implementationAddress)
	var salt [32]byte
	copy(salt[12:], caliburImplAddress.Bytes())

	chainIDBig := new(big.Int).SetInt64(chainID)

	// EIP-712 domain separator encoding
	encoded := packHash(
		domainTypeHash.Bytes(),
		nameHash.Bytes(),
		versionHash.Bytes(),
		common.LeftPadBytes(chainIDBig.Bytes(), 32),
		common.LeftPadBytes(eoaAddress.Bytes(), 32),
		salt[:],
	)

	return crypto.Keccak256Hash(encoded)
}

// HashCall computes the EIP-712 struct hash for a single Call.
func HashCall(call *Call) [32]byte {
	valueBytes := common.LeftPadBytes(call.Value.Bytes(), 32)
	dataHash := crypto.Keccak256Hash(call.Data)

	encoded := packHash(
		callTypeHash.Bytes(),
		common.LeftPadBytes(call.To.Bytes(), 32),
		valueBytes,
		dataHash.Bytes(),
	)

	return crypto.Keccak256Hash(encoded)
}

// HashBatchedCall computes the EIP-712 struct hash for a BatchedCall.
func HashBatchedCall(batch *BatchedCall) [32]byte {
	// Hash the calls array: keccak256(abi.encodePacked(hash(call1), hash(call2), ...))
	var callHashes []byte
	for i := range batch.Calls {
		h := HashCall(&batch.Calls[i])
		callHashes = append(callHashes, h[:]...)
	}
	callsArrayHash := crypto.Keccak256Hash(callHashes)

	revertFlag := make([]byte, 32)
	if batch.RevertOnFailure {
		revertFlag[31] = 1
	}

	encoded := packHash(
		batchedCallTypeHash.Bytes(),
		callsArrayHash.Bytes(),
		revertFlag,
	)

	return crypto.Keccak256Hash(encoded)
}

// HashSignedBatchedCall computes the EIP-712 struct hash for a SignedBatchedCall.
func HashSignedBatchedCall(call *SignedBatchedCall) [32]byte {
	batchedCallHash := HashBatchedCall(&call.BatchedCall)
	nonceBytes := common.LeftPadBytes(call.Nonce.Bytes(), 32)
	deadlineBytes := common.LeftPadBytes(call.Deadline.Bytes(), 32)

	encoded := packHash(
		signedBatchedCallTypeHash.Bytes(),
		batchedCallHash[:],
		nonceBytes,
		call.KeyHash[:],
		common.LeftPadBytes(call.Executor.Bytes(), 32),
		deadlineBytes,
	)

	return crypto.Keccak256Hash(encoded)
}

// SignBatchedCall signs a SignedBatchedCall using EIP-712 typed data signing.
// Returns the 65-byte signature (r || s || v).
func SignBatchedCall(domainSeparator [32]byte, call *SignedBatchedCall, privateKey *ecdsa.PrivateKey) ([]byte, error) {
	structHash := HashSignedBatchedCall(call)

	// EIP-712 digest: keccak256("\x19\x01" || domainSeparator || structHash)
	digest := crypto.Keccak256Hash(
		[]byte{0x19, 0x01},
		domainSeparator[:],
		structHash[:],
	)

	sig, err := crypto.Sign(digest.Bytes(), privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign batched call: %w", err)
	}

	// go-ethereum returns v as 0 or 1; EIP-712 expects 27 or 28
	if sig[64] < 27 {
		sig[64] += 27
	}

	return sig, nil
}

// packHash concatenates multiple 32-byte chunks for ABI encoding.
func packHash(parts ...[]byte) []byte {
	var result []byte
	for _, p := range parts {
		result = append(result, p...)
	}
	return result
}
