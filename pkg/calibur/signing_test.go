package calibur

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestBuildDomainSeparator(t *testing.T) {
	chainID := int64(1) // mainnet
	eoaAddress := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	caliburAddress := DefaultCaliburAddress

	domainSep := BuildDomainSeparator(chainID, eoaAddress, caliburAddress)

	// Domain separator should be deterministic
	domainSep2 := BuildDomainSeparator(chainID, eoaAddress, caliburAddress)
	if domainSep != domainSep2 {
		t.Fatal("domain separator is not deterministic")
	}

	// Different chain ID should produce different separator
	domainSepDiffChain := BuildDomainSeparator(8453, eoaAddress, caliburAddress)
	if domainSep == domainSepDiffChain {
		t.Fatal("different chain IDs should produce different domain separators")
	}

	// Different EOA should produce different separator
	differentEOA := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	domainSepDiffEOA := BuildDomainSeparator(chainID, differentEOA, caliburAddress)
	if domainSep == domainSepDiffEOA {
		t.Fatal("different EOA addresses should produce different domain separators")
	}
}

func TestHashCall(t *testing.T) {
	call := &Call{
		To:    common.HexToAddress("0xdead"),
		Value: big.NewInt(1000),
		Data:  []byte{0x01, 0x02, 0x03},
	}

	hash1 := HashCall(call)
	hash2 := HashCall(call)

	if hash1 != hash2 {
		t.Fatal("HashCall is not deterministic")
	}

	// Different data should produce different hash
	call2 := &Call{
		To:    common.HexToAddress("0xdead"),
		Value: big.NewInt(1000),
		Data:  []byte{0x01, 0x02, 0x04}, // different last byte
	}
	hash3 := HashCall(call2)
	if hash1 == hash3 {
		t.Fatal("different call data should produce different hashes")
	}
}

func TestHashBatchedCall(t *testing.T) {
	batch := &BatchedCall{
		Calls: []Call{
			{
				To:    common.HexToAddress("0xdead"),
				Value: big.NewInt(1000),
				Data:  []byte{0x01},
			},
			{
				To:    common.HexToAddress("0xbeef"),
				Value: big.NewInt(0),
				Data:  []byte{0x02, 0x03},
			},
		},
		RevertOnFailure: true,
	}

	hash1 := HashBatchedCall(batch)

	// Toggle revertOnFailure should change hash
	batch.RevertOnFailure = false
	hash2 := HashBatchedCall(batch)
	if hash1 == hash2 {
		t.Fatal("different revertOnFailure should produce different hashes")
	}
}

func TestSignBatchedCall(t *testing.T) {
	// Generate a test key
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	eoaAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
	domainSep := BuildDomainSeparator(1, eoaAddress, DefaultCaliburAddress)

	call := &SignedBatchedCall{
		BatchedCall: BatchedCall{
			Calls: []Call{
				{
					To:    common.HexToAddress("0xdead"),
					Value: big.NewInt(1000),
					Data:  []byte{},
				},
			},
			RevertOnFailure: true,
		},
		Nonce:    big.NewInt(0),
		KeyHash:  ComputeKeyHash(&privateKey.PublicKey),
		Executor: common.Address{},
		Deadline: big.NewInt(0),
	}

	sig, err := SignBatchedCall(domainSep, call, privateKey)
	if err != nil {
		t.Fatal(err)
	}

	if len(sig) != 65 {
		t.Fatalf("expected 65-byte signature, got %d", len(sig))
	}

	// Verify the signature recovers to the correct address
	structHash := HashSignedBatchedCall(call)
	digest := crypto.Keccak256Hash(
		[]byte{0x19, 0x01},
		domainSep[:],
		structHash[:],
	)

	// Normalize v back for recovery
	sigForRecover := make([]byte, 65)
	copy(sigForRecover, sig)
	sigForRecover[64] -= 27

	recoveredPub, err := crypto.Ecrecover(digest.Bytes(), sigForRecover)
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := crypto.UnmarshalPubkey(recoveredPub)
	if err != nil {
		t.Fatal(err)
	}

	recoveredAddress := crypto.PubkeyToAddress(*pubKey)
	signerAddress := crypto.PubkeyToAddress(privateKey.PublicKey)

	if recoveredAddress != signerAddress {
		t.Fatalf("signature recovered to %s, expected %s", recoveredAddress.Hex(), signerAddress.Hex())
	}
}

func TestComputeKeyHash(t *testing.T) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	hash1 := ComputeKeyHash(&privateKey.PublicKey)
	hash2 := ComputeKeyHash(&privateKey.PublicKey)

	if hash1 != hash2 {
		t.Fatal("ComputeKeyHash is not deterministic")
	}

	// Different key should produce different hash
	privateKey2, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	hash3 := ComputeKeyHash(&privateKey2.PublicKey)
	if hash1 == hash3 {
		t.Fatal("different public keys should produce different keyHashes")
	}
}
