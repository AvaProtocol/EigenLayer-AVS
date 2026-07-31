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

func TestPackControllerInstallSelectorAndShape(t *testing.T) {
	controller := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	call, err := PackControllerInstall(controller)
	if err != nil {
		t.Fatal(err)
	}

	// installValidation(bytes25,bytes4[],bytes,bytes[]) — the selector §0.1 of
	// the master doc verified against deployed bytecode.
	if sel := hex.EncodeToString(call[:4]); sel != "1bbf564c" {
		t.Fatalf("selector = %s, want 1bbf564c", sel)
	}

	// The config word leads the arguments: bytes25 is right-padded to 32.
	config := PackValidationConfig(SingleSignerValidationModuleAddress(), ControllerEntityID,
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

func TestPackControllerInstallRejectsZeroAddress(t *testing.T) {
	if _, err := PackControllerInstall(common.Address{}); err == nil {
		t.Fatal("expected an error for the zero controller address")
	}
}
