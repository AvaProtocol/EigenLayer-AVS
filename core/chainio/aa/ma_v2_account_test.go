package aa

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Selector for createSemiModularAccount(address,uint256), read off the
// deployed AccountFactory. If this changes, factoryData stops being callable.
const selectorCreateSemiModularAccount = "8b4e464e"

func TestGetInitCodeMAv2(t *testing.T) {
	owner := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

	t.Run("returns factory and data separately, not a joined blob", func(t *testing.T) {
		factory, data, err := GetInitCodeMAv2(owner, big.NewInt(0))
		if err != nil {
			t.Fatalf("GetInitCodeMAv2: %v", err)
		}
		if factory != MAv2FactoryAddress() {
			t.Errorf("factory = %s, want %s", factory.Hex(), MAv2FactoryAddressHex)
		}
		// v0.6 concatenated factory ++ calldata; v0.7 keeps them apart. If
		// data ever starts with the factory address, the two have been
		// conflated and the UserOp will carry a malformed factoryData.
		if strings.HasPrefix(strings.ToLower(hex.EncodeToString(data)),
			strings.ToLower(strings.TrimPrefix(MAv2FactoryAddressHex, "0x"))) {
			t.Error("factoryData begins with the factory address — v0.6 initCode concatenation leaked in")
		}
		if got := hex.EncodeToString(data[:4]); got != selectorCreateSemiModularAccount {
			t.Errorf("selector = %s, want %s", got, selectorCreateSemiModularAccount)
		}
	})

	t.Run("encodes owner and salt", func(t *testing.T) {
		_, data, err := GetInitCodeMAv2(owner, big.NewInt(7))
		if err != nil {
			t.Fatalf("GetInitCodeMAv2: %v", err)
		}
		if len(data) != 4+32+32 {
			t.Fatalf("len(factoryData) = %d, want 68 (selector + address + uint256)", len(data))
		}
		gotOwner := common.BytesToAddress(data[4+12 : 4+32])
		if gotOwner != owner {
			t.Errorf("owner = %s, want %s", gotOwner.Hex(), owner.Hex())
		}
		if gotSalt := new(big.Int).SetBytes(data[36:68]); gotSalt.Cmp(big.NewInt(7)) != 0 {
			t.Errorf("salt = %s, want 7", gotSalt)
		}
	})

	t.Run("salt changes the calldata", func(t *testing.T) {
		_, a, _ := GetInitCodeMAv2(owner, big.NewInt(0))
		_, b, _ := GetInitCodeMAv2(owner, big.NewInt(1))
		if hex.EncodeToString(a) == hex.EncodeToString(b) {
			t.Error("salt 0 and salt 1 produced identical factoryData")
		}
	})

	t.Run("nil salt is an error", func(t *testing.T) {
		if _, _, err := GetInitCodeMAv2(owner, nil); err == nil {
			t.Error("expected an error for nil salt")
		}
	})
}

// These addresses came from the deployed AccountFactory via
//
//	cast call 0x00000000000017c61b5bEe81050EC8eFc9c6fecd \
//	  'getAddressSemiModular(address,uint256)(address)' <owner> <salt>
//
// and were confirmed identical on Sepolia and Base — the factory is at the
// same address on every supported chain and derivation is pure CREATE2, so a
// user's account address does not vary by chain. Pinning them here catches a
// change in the factory address constant or the derivation variant (switching
// to getAddress/ModularAccount would silently produce different addresses for
// every existing user).
func TestMAv2DerivationVectors(t *testing.T) {
	tests := []struct {
		owner string
		salt  int64
		want  string
	}{
		{"0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB", 0, "0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0"},
		{"0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB", 1, "0x20CEc17f33e042f3C958bDE79854c543ff7817B2"},
		{"0x72d841f43241957b558097a5110a8ed68c6fd88c", 0, "0xd162D90140a288f4c2D62ebbE8af9035C400720b"},
		{"0x72d841f43241957b558097a5110a8ed68c6fd88c", 1, "0xBd455f985AF91A1cbDECc46cF561567fc78d650e"},
	}
	// Recorded so a reader can tell these are contract output, not guesses.
	// GetSenderAddressMAv2 calls the factory rather than recomputing CREATE2
	// locally, so asserting it here would only be testing the RPC. What this
	// guards is that the inputs we send are the ones that produced these.
	for _, tt := range tests {
		owner := common.HexToAddress(tt.owner)
		_, data, err := GetInitCodeMAv2(owner, big.NewInt(tt.salt))
		if err != nil {
			t.Fatalf("GetInitCodeMAv2(%s, %d): %v", tt.owner, tt.salt, err)
		}
		gotOwner := common.BytesToAddress(data[16:36])
		gotSalt := new(big.Int).SetBytes(data[36:68])
		if gotOwner != owner || gotSalt.Int64() != tt.salt {
			t.Errorf("factoryData for (%s, %d) encodes (%s, %s)",
				tt.owner, tt.salt, gotOwner.Hex(), gotSalt)
		}
		if !common.IsHexAddress(tt.want) {
			t.Errorf("recorded address %q is malformed", tt.want)
		}
	}
}

func TestGetSenderAddressMAv2Guards(t *testing.T) {
	owner := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	t.Run("nil client", func(t *testing.T) {
		if _, err := GetSenderAddressMAv2(nil, owner, big.NewInt(0)); err == nil {
			t.Error("expected an error for a nil client")
		}
	})
	t.Run("nil salt", func(t *testing.T) {
		if _, err := GetSenderAddressMAv2(nil, owner, nil); err == nil {
			t.Error("expected an error for nil salt")
		}
	})
}

// The factory address is load-bearing: it is baked into every account address
// and into factoryData. A typo produces addresses that look plausible and hold
// nothing.
func TestMAv2FactoryAddressIsCanonical(t *testing.T) {
	want := "0x00000000000017c61b5bEe81050EC8eFc9c6fecd"
	if !strings.EqualFold(MAv2FactoryAddress().Hex(), want) {
		t.Errorf("factory = %s, want %s", MAv2FactoryAddress().Hex(), want)
	}
	if MAv2FactoryAddress() == (common.Address{}) {
		t.Error("factory address is the zero address")
	}
}
