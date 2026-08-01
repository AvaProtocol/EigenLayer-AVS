package aa

import (
	"bytes"
	"encoding/hex"
	"strings"
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
	call, err := PackSessionSignerInstall(SessionGrant{EntityID: MinSessionEntityID, Signer: controller, Global: true, AllowSelfAdministration: true})
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
	if _, err := PackSessionSignerInstall(SessionGrant{EntityID: MinSessionEntityID, Global: true, AllowSelfAdministration: true}); err == nil {
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

	call, err := PackSessionSignerInstall(SessionGrant{EntityID: entity, Signer: signer, Global: true, AllowSelfAdministration: true})
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

	t.Run("Global sets the global flag", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer, Global: true}
		if g.Flags()&ValidationFlagGlobal == 0 {
			t.Error("expected the global flag")
		}
		if g.Flags()&ValidationFlagUserOp == 0 {
			t.Error("expected the userOp flag")
		}
	})

	t.Run("selector-scoped grants are not global", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer, Selectors: [][4]byte{{0x09, 0x5e, 0xa7, 0xb3}}}
		if g.Flags()&ValidationFlagGlobal != 0 {
			t.Error("an enumerated selector set must not also be global")
		}
	})

	// The gateway holds this key. Letting it answer isValidSignature would let
	// it sign arbitrary messages AS the user, which is not what execution
	// authority means.
	t.Run("signature validation is off unless asked for", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer, Global: true}
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
	if _, err := PackSessionSignerInstall(SessionGrant{EntityID: 0, Signer: signer, Global: true, AllowSelfAdministration: true}); err == nil {
		t.Error("expected entity 0 to be rejected")
	}
}

// Hooks ride the same call, which is what puts them under the owner's single
// signature instead of needing one of their own.
func TestSessionGrantCarriesHooks(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	hook := append(bytes.Repeat([]byte{0xab}, 26), 0x01, 0x02)

	withHook, err := PackSessionSignerInstall(SessionGrant{EntityID: 1, Signer: signer, Global: true, Hooks: [][]byte{hook}, AllowSelfAdministration: true})
	if err != nil {
		t.Fatalf("PackSessionSignerInstall: %v", err)
	}
	if !bytes.Contains(withHook, hook) {
		t.Error("hook payload is not present in the install call")
	}
	without, err := PackSessionSignerInstall(SessionGrant{EntityID: 1, Signer: signer, Global: true, AllowSelfAdministration: true})
	if err != nil {
		t.Fatalf("PackSessionSignerInstall: %v", err)
	}
	if len(withHook) <= len(without) {
		t.Error("hooks did not lengthen the encoded call")
	}
}

// A global validation may call the account's own native functions. That is
// how one-click self-revoke works (uninstallValidation, proven on Sepolia),
// but the same authority reaches installValidation — so a bare global grant
// can escalate itself: add signature validation, widen its hooks, install a
// second signer.
//
// This previously WAS the default. Flags() inferred global from an empty
// Selectors, so the plainest possible grant — entity and signer, nothing else
// — produced the one shape that must never reach production. The rule lived in
// a doc sentence while the code made violating it the path of least
// resistance. It is now a build-time refusal.
func TestBareGlobalGrantIsRefused(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

	// The exact literal that used to be the easy, and wrong, thing to write.
	_, err := PackSessionSignerInstall(SessionGrant{EntityID: 1, Signer: signer, Global: true})
	if err == nil {
		t.Fatal("a global grant with no execution hook must be refused")
	}
	if !strings.Contains(err.Error(), "escalate itself") {
		t.Errorf("the error should say why, got: %v", err)
	}

	t.Run("an execution hook makes it safe", func(t *testing.T) {
		// The allowlist exec hook rejects non-execute selectors, which is what
		// stopped the policied grant from revoking itself in the spike.
		g := SessionGrant{EntityID: 1, Signer: signer, Global: true,
			Hooks: [][]byte{AllowlistExecHook(1)}}
		if !g.HasExecutionHook() {
			t.Fatal("AllowlistExecHook was not recognised as an execution hook")
		}
		if _, err := PackSessionSignerInstall(g); err != nil {
			t.Errorf("a hook-carrying global grant must be allowed: %v", err)
		}
	})

	t.Run("a validation-only hook does not make it safe", func(t *testing.T) {
		// TimeRange runs at validation, so it never sees the selector being
		// executed and cannot block a self-administering call.
		timeHook, err := TimeRangeValidationHook(1, 1785541743, 0)
		if err != nil {
			t.Fatalf("TimeRangeValidationHook: %v", err)
		}
		g := SessionGrant{EntityID: 1, Signer: signer, Global: true, Hooks: [][]byte{timeHook}}
		if g.HasExecutionHook() {
			t.Fatal("a validation hook must not count as an execution hook")
		}
		if _, err := PackSessionSignerInstall(g); err == nil {
			t.Error("validation-only hooks must not satisfy the guard")
		}
	})

	t.Run("selector scoping makes it safe", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer,
			Selectors: [][4]byte{{0xb6, 0x1d, 0x27, 0xf6}}} // execute
		if _, err := PackSessionSignerInstall(g); err != nil {
			t.Errorf("a selector-scoped grant must be allowed: %v", err)
		}
	})

	t.Run("the escape hatch is explicit", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer, Global: true, AllowSelfAdministration: true}
		if _, err := PackSessionSignerInstall(g); err != nil {
			t.Errorf("an acknowledged self-administering grant must build: %v", err)
		}
	})
}

// Scope must be stated rather than inferred, in both directions.
func TestGrantScopeMustBeUnambiguous(t *testing.T) {
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

	t.Run("no scope at all", func(t *testing.T) {
		if _, err := PackSessionSignerInstall(SessionGrant{EntityID: 1, Signer: signer}); err == nil {
			t.Error("a grant with neither Global nor Selectors must be refused")
		}
	})

	t.Run("both is wider than it reads", func(t *testing.T) {
		g := SessionGrant{EntityID: 1, Signer: signer, Global: true,
			Selectors:               [][4]byte{{0xb6, 0x1d, 0x27, 0xf6}},
			AllowSelfAdministration: true}
		if _, err := PackSessionSignerInstall(g); err == nil {
			t.Error("Global plus Selectors must be refused; the list is silently ignored on chain")
		}
	})
}
