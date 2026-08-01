package aa

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestPackModuleEntity(t *testing.T) {
	got := PackModuleEntity(SingleSignerValidationModuleAddress(), 1)
	want := "00000000000099de0bf6fa90deb851e2a2df7d83" + "00000001"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("ModuleEntity = %x, want %s", got, want)
	}
}

func TestPackSessionSignerUninstallShape(t *testing.T) {
	timeRangeData, err := PackTimeRangeUninstallData(1)
	if err != nil {
		t.Fatal(err)
	}
	call, err := PackSessionSignerUninstall(1, [][]byte{timeRangeData, nil, {}})
	if err != nil {
		t.Fatal(err)
	}

	// uninstallValidation(bytes24,bytes,bytes[]) — cast sig 0xb6b1ccfe.
	if sel := hex.EncodeToString(call[:4]); sel != "b6b1ccfe" {
		t.Fatalf("selector = %s, want b6b1ccfe", sel)
	}

	// The ModuleEntity leads the arguments, right-padded to a word.
	entity := PackModuleEntity(SingleSignerValidationModuleAddress(), 1)
	if !bytes.Equal(call[4:4+24], entity[:]) {
		t.Fatalf("leading module entity = %x, want %x", call[4:4+24], entity)
	}

	// The uninstall data is abi.encode(uint32 1) — a single word ending in 01.
	uninstallData, err := PackSingleSignerUninstallData(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(uninstallData) != 32 || uninstallData[31] != 1 {
		t.Fatalf("uninstall data = %x, want a single word of 1", uninstallData)
	}
	if !bytes.Contains(call, uninstallData) {
		t.Fatal("the uninstall call does not carry the module's entity payload")
	}
}

func TestPackSessionSignerUninstallRejectsOwnerEntity(t *testing.T) {
	// Uninstalling entity 0 is not revocation, it is disabling the owner.
	if _, err := PackSessionSignerUninstall(0, nil); err == nil {
		t.Fatal("expected entity 0 to be rejected")
	}
}

func TestUninstallAndTimeRangePayloadsMatch(t *testing.T) {
	// Both modules take a bare abi.encode(uint32) on uninstall. If these ever
	// diverge the shared implementation must split.
	a, err := PackSingleSignerUninstallData(7)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PackTimeRangeUninstallData(7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("SSVM and TimeRange uninstall payloads diverged")
	}
}

func TestPackUninstallValidationEmptyHookEntries(t *testing.T) {
	// Empty entries (skip that hook's onUninstall) must encode as zero-length
	// bytes, not nil-panic or drop from the array — the account requires one
	// entry per installed hook.
	data, err := PackSingleSignerUninstallData(2)
	if err != nil {
		t.Fatal(err)
	}
	call, err := PackUninstallValidation(common.HexToAddress("0x1111111111111111111111111111111111111111"), 2, data, [][]byte{nil, nil, nil})
	if err != nil {
		t.Fatal(err)
	}
	if len(call) == 0 {
		t.Fatal("empty call")
	}
}
