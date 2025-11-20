package aa

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSmartWalletAddress_CREATE2Formula(t *testing.T) {
	// Test that computeSmartWalletAddress uses the correct CREATE2 formula:
	// keccak256(0xff || factoryAddr || salt || keccak256(initCode))[12:]

	// Set up test factory address (using a known test address)
	factoryAddr := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	ownerAddr := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	salt := big.NewInt(0)

	// Compute address using our function
	computedAddr, err := computeSmartWalletAddress(factoryAddr, ownerAddr, salt)
	require.NoError(t, err, "computeSmartWalletAddress should not error")
	require.NotEqual(t, common.Address{}, computedAddr, "computed address should not be zero")

	// Verify the address is deterministic (same inputs = same output)
	computedAddr2, err := computeSmartWalletAddress(factoryAddr, ownerAddr, salt)
	require.NoError(t, err)
	assert.Equal(t, computedAddr, computedAddr2, "address computation should be deterministic")

	// Verify CREATE2 formula manually
	// Get init code
	initCodeHex, err := GetInitCodeForFactory(ownerAddr.Hex(), factoryAddr, salt)
	require.NoError(t, err, "GetInitCodeForFactory should not error")

	// Decode and hash init code
	initCodeBytes, err := hexutil.Decode(initCodeHex)
	require.NoError(t, err)
	initCodeHash := crypto.Keccak256(initCodeBytes)

	// Build CREATE2 hash: keccak256(0xff || factoryAddr || salt || initCodeHash)
	saltBytes := make([]byte, 32)
	salt.FillBytes(saltBytes)

	var b []byte
	b = append(b, 0xff)
	b = append(b, factoryAddr.Bytes()...)
	b = append(b, saltBytes...)
	b = append(b, initCodeHash...)
	expectedHash := crypto.Keccak256(b)
	expectedAddr := common.BytesToAddress(expectedHash[12:])

	assert.Equal(t, expectedAddr, computedAddr, "computed address should match manual CREATE2 calculation")
}

func TestComputeSmartWalletAddress_DifferentSalts(t *testing.T) {
	factoryAddr := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	ownerAddr := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")

	// Test with different salts - should produce different addresses
	addr0, err := computeSmartWalletAddress(factoryAddr, ownerAddr, big.NewInt(0))
	require.NoError(t, err)

	addr1, err := computeSmartWalletAddress(factoryAddr, ownerAddr, big.NewInt(1))
	require.NoError(t, err)

	addr2, err := computeSmartWalletAddress(factoryAddr, ownerAddr, big.NewInt(2))
	require.NoError(t, err)

	// All addresses should be different
	assert.NotEqual(t, addr0, addr1, "different salts should produce different addresses")
	assert.NotEqual(t, addr1, addr2, "different salts should produce different addresses")
	assert.NotEqual(t, addr0, addr2, "different salts should produce different addresses")
}

func TestComputeSmartWalletAddress_DifferentOwners(t *testing.T) {
	factoryAddr := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	salt := big.NewInt(0)

	owner1 := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	owner2 := common.HexToAddress("0x578B110b0a7c06e66b7B1a33C39635304aaF733c")

	// Test with different owners - should produce different addresses
	addr1, err := computeSmartWalletAddress(factoryAddr, owner1, salt)
	require.NoError(t, err)

	addr2, err := computeSmartWalletAddress(factoryAddr, owner2, salt)
	require.NoError(t, err)

	assert.NotEqual(t, addr1, addr2, "different owners should produce different addresses")
}

func TestComputeSmartWalletAddress_DifferentFactories(t *testing.T) {
	ownerAddr := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	salt := big.NewInt(0)

	factory1 := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	factory2 := common.HexToAddress("0x0000000000000000000000000000000000000001")

	// Test with different factories - should produce different addresses
	addr1, err := computeSmartWalletAddress(factory1, ownerAddr, salt)
	require.NoError(t, err)

	addr2, err := computeSmartWalletAddress(factory2, ownerAddr, salt)
	require.NoError(t, err)

	assert.NotEqual(t, addr1, addr2, "different factories should produce different addresses")
}

func TestComputeSmartWalletAddress_InvalidInputs(t *testing.T) {
	factoryAddr := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	ownerAddr := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")

	// Test with nil salt - should panic (current behavior)
	// This documents the current behavior - if we want to handle nil salt gracefully in the future,
	// we should update the function to check for nil and return an error
	assert.Panics(t, func() {
		_, _ = computeSmartWalletAddress(factoryAddr, ownerAddr, nil)
	}, "nil salt should cause a panic")
}

func TestComputeSmartWalletAddress_MatchesGetInitCodeForFactory(t *testing.T) {
	// Test that the init code used in computeSmartWalletAddress matches what GetInitCodeForFactory returns
	factoryAddr := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	ownerAddr := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	salt := big.NewInt(0)

	// Get init code directly
	initCodeHex, err := GetInitCodeForFactory(ownerAddr.Hex(), factoryAddr, salt)
	require.NoError(t, err)
	require.NotEmpty(t, initCodeHex, "init code should not be empty")

	// Compute address - this internally calls GetInitCodeForFactory
	computedAddr, err := computeSmartWalletAddress(factoryAddr, ownerAddr, salt)
	require.NoError(t, err)

	// Verify the address is valid (not zero)
	assert.NotEqual(t, common.Address{}, computedAddr, "computed address should not be zero")

	// The fact that computeSmartWalletAddress succeeds means GetInitCodeForFactory worked correctly
	// We've already verified the CREATE2 formula in the first test
}
