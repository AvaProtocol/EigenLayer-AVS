package signer

import (
	"github.com/Layr-Labs/eigensdk-go/crypto/bls"
	"golang.org/x/crypto/sha3"
)

// Generate a byte32 fixed hash for a message to be use in various place for
// signature. Many algorithm require a fixed 32 byte input
func Byte32Digest(data []byte) ([32]byte, error) {
	var digest [32]byte
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(data)
	copy(data[:], hasher.Sum(nil)[:32])

	return digest, nil
}

func SignBlsMessage(blsKeypair *bls.KeyPair, msg []byte) *bls.Signature {
	data, e := Byte32Digest(msg)
	if e != nil {
		return nil
	}

	return blsKeypair.SignMessage(data)
}
