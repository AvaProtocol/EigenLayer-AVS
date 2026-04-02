package calibur

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// GenerateSubKey generates a fresh secp256k1 keypair for use as a Calibur sub-key.
func GenerateSubKey() (*ecdsa.PrivateKey, error) {
	return crypto.GenerateKey()
}

// ComputeKeyHash computes the Calibur keyHash for a secp256k1 public key.
// keyHash = keccak256(abi.encode(KeyType.Secp256k1, publicKeyBytes))
func ComputeKeyHash(publicKey *ecdsa.PublicKey) [32]byte {
	// Calibur expects the uncompressed public key bytes (65 bytes with 0x04 prefix,
	// or 64 bytes without — matching the EVM's ecrecover convention).
	// The encoding is: abi.encode(uint8(2), bytes(publicKeyBytes))
	// where 2 = KeyType.Secp256k1
	pubKeyBytes := crypto.FromECDSAPub(publicKey)

	// abi.encode(KeyType.Secp256k1, publicKeyBytes):
	// - uint8 keyType padded to 32 bytes
	// - bytes offset (32 bytes) = 64
	// - bytes length (32 bytes)
	// - bytes data (padded to 32-byte boundary)
	keyTypePadded := common.LeftPadBytes([]byte{KeyTypeSecp256k1}, 32)
	offset := common.LeftPadBytes(big.NewInt(64).Bytes(), 32)
	length := common.LeftPadBytes(big.NewInt(int64(len(pubKeyBytes))).Bytes(), 32)

	// Pad pubKeyBytes to 32-byte boundary
	paddedLen := ((len(pubKeyBytes) + 31) / 32) * 32
	paddedPubKey := make([]byte, paddedLen)
	copy(paddedPubKey, pubKeyBytes)

	var encoded []byte
	encoded = append(encoded, keyTypePadded...)
	encoded = append(encoded, offset...)
	encoded = append(encoded, length...)
	encoded = append(encoded, paddedPubKey...)

	return crypto.Keccak256Hash(encoded)
}

// getSeqSelector is the function selector for getSeq(uint256) on Calibur.
// keccak256("getSeq(uint256)")[:4]
var getSeqSelector = crypto.Keccak256([]byte("getSeq(uint256)"))[:4]

// GetNonce retrieves the next valid nonce for a given key from the Calibur contract.
// The nonce format is: upper 192 bits = key identifier, lower 64 bits = sequence number.
// The nonceKey parameter is the upper 192-bit key identifier (typically derived from keyHash).
func GetNonce(ctx context.Context, client *ethclient.Client, walletAddress common.Address, nonceKey *big.Int) (*big.Int, error) {
	// Build calldata: getSeq(uint256 key)
	calldata := make([]byte, 4+32)
	copy(calldata[:4], getSeqSelector)
	copy(calldata[4:], common.LeftPadBytes(nonceKey.Bytes(), 32))

	result, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &walletAddress,
		Data: calldata,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call getSeq on %s: %w", walletAddress.Hex(), err)
	}

	if len(result) < 32 {
		return nil, fmt.Errorf("unexpected getSeq result length: %d", len(result))
	}

	seq := new(big.Int).SetBytes(result[:32])

	// Build the full nonce: (nonceKey << 64) | seq
	fullNonce := new(big.Int).Lsh(nonceKey, 64)
	fullNonce.Or(fullNonce, seq)

	return fullNonce, nil
}
