package aa

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestPackValidationConfigLayout(t *testing.T) {
	module := common.HexToAddress(SingleSignerValidationModuleAddressHex)
	got := PackValidationConfig(module, 1, ValidationFlagGlobal|ValidationFlagUserOp)

	// module (20) ++ entityId (4, BE) ++ flags (1) — the §3.1 layout that was
	// verified against deployed bytecode.
	want := "00000000000099de0bf6fa90deb851e2a2df7d83" + "00000001" + "05"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("ValidationConfig = %x, want %s", got, want)
	}
}

func TestPackSingleSignerInstallData(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	got, err := PackSingleSignerInstallData(1, signer)
	if err != nil {
		t.Fatal(err)
	}
	// abi.encode(uint32(1), address) = two 32-byte words.
	if len(got) != 64 {
		t.Fatalf("install data is %d bytes, want 64", len(got))
	}
	if got[31] != 1 {
		t.Fatalf("entity id word = %x, want …01", got[:32])
	}
	if !bytes.Equal(got[44:64], signer.Bytes()) {
		t.Fatalf("signer word = %x, want …%x", got[32:64], signer.Bytes())
	}
}

func TestPackSessionSignerInstallSelectorAndShape(t *testing.T) {
	controller := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	call, err := PackSessionSignerInstall(SessionGrant{EntityID: MinSessionEntityID, Signer: controller})
	if err != nil {
		t.Fatal(err)
	}

	// installValidation(bytes25,bytes4[],bytes,bytes[]) — the selector §0.1 of
	// the master doc verified against deployed bytecode.
	if sel := hex.EncodeToString(call[:4]); sel != "1bbf564c" {
		t.Fatalf("selector = %s, want 1bbf564c", sel)
	}

	// The config word leads the arguments: bytes25 is right-padded to 32.
	config := PackValidationConfig(SingleSignerValidationModuleAddress(), MinSessionEntityID,
		ValidationFlagGlobal|ValidationFlagUserOp)
	if !bytes.Equal(call[4:4+25], config[:]) {
		t.Fatalf("leading config = %x, want %x", call[4:4+25], config)
	}

	// The controller must be granted userOp + global but NEVER signature
	// validation — the scoping §2.4 of the findings doc presents as the
	// security improvement.
	if config[24]&ValidationFlagSignature != 0 {
		t.Fatal("controller install must not carry isSignatureValidation")
	}
}

func TestPackSessionSignerInstallRejectsZeroAddress(t *testing.T) {
	if _, err := PackSessionSignerInstall(SessionGrant{EntityID: MinSessionEntityID}); err == nil {
		t.Fatal("expected an error for the zero controller address")
	}
}

// Entities are allocated per grant, so the entity id has to reach both the
// ValidationConfig and the module's install data. If they ever disagree the
// account installs one entity and the module binds the signer to another —
// the grant looks applied and every operation under it fails validation.
func TestSessionGrantEntityReachesBothEncodings(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	const entity = uint32(7)

	call, err := PackSessionSignerInstall(SessionGrant{EntityID: entity, Signer: signer})
	if err != nil {
		t.Fatalf("PackSessionSignerInstall: %v", err)
	}
	// ValidationConfig sits in the first argument word, right-padded: module
	// (20) ++ entityId (4) ++ flags (1).
	cfg := call[4:29]
	got := uint32(cfg[20])<<24 | uint32(cfg[21])<<16 | uint32(cfg[22])<<8 | uint32(cfg[23])
	if got != entity {
		t.Errorf("ValidationConfig entity = %d, want %d", got, entity)
	}
	installData, err := PackSingleSignerInstallData(entity, signer)
	if err != nil {
		t.Fatalf("PackSingleSignerInstallData: %v", err)
	}
	if !bytes.Contains(call, installData) {
		t.Error("the install call does not carry install data for the same entity")
	}
}

func TestSessionGrantFlags(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

	t.Run("no selectors means global", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer}
		if g.Flags()&ValidationFlagGlobal == 0 {
			t.Error("expected the global flag")
		}
		if g.Flags()&ValidationFlagUserOp == 0 {
			t.Error("expected the userOp flag")
		}
	})

	// A global validation never consults the selector list, so setting both
	// would silently widen a grant the owner believed was scoped.
	t.Run("selectors clear global", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer, Selectors: [][4]byte{{0x09, 0x5e, 0xa7, 0xb3}}}
		if g.Flags()&ValidationFlagGlobal != 0 {
			t.Error("an enumerated selector set must not also be global")
		}
	})

	// The gateway holds this key. Letting it answer isValidSignature would let
	// it sign arbitrary messages AS the user, which is not what execution
	// authority means.
	t.Run("signature validation is off unless asked for", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer}
		if g.Flags()&ValidationFlagSignature != 0 {
			t.Error("signature validation must default off")
		}
		g.AllowSignatureValidation = true
		if g.Flags()&ValidationFlagSignature == 0 {
			t.Error("signature validation must be settable")
		}
	})
}

func TestSessionGrantRejectsTheOwnerEntity(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	// Entity 0 is the fallback signer. Installing over it would replace the
	// owner's own validation with a gateway-held key.
	if _, err := PackSessionSignerInstall(SessionGrant{EntityID: 0, Signer: signer}); err == nil {
		t.Error("expected entity 0 to be rejected")
	}
}

// Hooks ride the same call, which is what puts them under the owner's single
// signature instead of needing one of their own.
func TestSessionGrantCarriesHooks(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	hook := append(bytes.Repeat([]byte{0xab}, 26), 0x01, 0x02)

	withHook, err := PackSessionSignerInstall(SessionGrant{EntityID: 1, Signer: signer, Hooks: [][]byte{hook}})
	if err != nil {
		t.Fatalf("PackSessionSignerInstall: %v", err)
	}
	if !bytes.Contains(withHook, hook) {
		t.Error("hook payload is not present in the install call")
	}
	without, err := PackSessionSignerInstall(SessionGrant{EntityID: 1, Signer: signer})
	if err != nil {
		t.Fatalf("PackSessionSignerInstall: %v", err)
	}
	if len(withHook) <= len(without) {
		t.Error("hooks did not lengthen the encoded call")
	}
}
