package aa

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Golden vector produced by
//
//	cast abi-encode "f(uint32,(address,bool,bool,uint256,bytes4[])[])" 1 \
//	  "[(0x1111…1111,true,true,100000000000000000000,[0xa9059cbb])]"
//
// so the Go tuple marshaling is checked against an independent encoder, not
// against itself.
const allowlistGolden = "0000000000000000000000000000000000000000000000000000000000000001" +
	"0000000000000000000000000000000000000000000000000000000000000040" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"0000000000000000000000000000000000000000000000000000000000000020" +
	"0000000000000000000000001111111111111111111111111111111111111111" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"0000000000000000000000000000000000000000000000056bc75e2d63100000" +
	"00000000000000000000000000000000000000000000000000000000000000a0" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"a9059cbb00000000000000000000000000000000000000000000000000000000"

func TestPackAllowlistInstallDataGolden(t *testing.T) {
	limit, _ := new(big.Int).SetString("100000000000000000000", 10) // 100e18
	got, err := PackAllowlistInstallData(1, []AllowlistInput{{
		Target:               common.HexToAddress("0x1111111111111111111111111111111111111111"),
		HasSelectorAllowlist: true,
		HasERC20SpendLimit:   true,
		ERC20SpendLimit:      limit,
		Selectors:            [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != allowlistGolden {
		t.Fatalf("allowlist install data does not match the cast golden vector:\ngot  %x", got)
	}
}

func TestPackAllowlistInstallDataRejectsFootguns(t *testing.T) {
	if _, err := PackAllowlistInstallData(1, nil); err == nil {
		t.Error("expected an empty allowlist to be rejected")
	}
	if _, err := PackAllowlistInstallData(1, []AllowlistInput{{
		Target:             common.HexToAddress("0x1111111111111111111111111111111111111111"),
		HasERC20SpendLimit: true, // flag set, limit zero
	}}); err == nil {
		t.Error("expected a zero limit with the flag set to be rejected")
	}
}

func TestPackTimeRangeInstallDataGolden(t *testing.T) {
	// cast abi-encode "f(uint32,uint48,uint48)" 7 1790000000 12345
	want := "0000000000000000000000000000000000000000000000000000000000000007" +
		"000000000000000000000000000000000000000000000000000000006ab13b80" +
		"0000000000000000000000000000000000000000000000000000000000003039"
	got, err := PackTimeRangeInstallData(7, 1790000000, 12345)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != want {
		t.Fatalf("time range install data = %x, want %s", got, want)
	}

	if _, err := PackTimeRangeInstallData(1, 0, 0); err == nil {
		t.Error("expected validUntil 0 (no expiry) to be rejected")
	}
	if _, err := PackTimeRangeInstallData(1, 100, 100); err == nil {
		t.Error("expected an empty window to be rejected")
	}
}

func TestHookEntryLayout(t *testing.T) {
	entry, err := TimeRangeValidationHook(3, 1790000000, 0)
	if err != nil {
		t.Fatal(err)
	}
	// [:25] HookConfig, [25:] initData — the offsets ModuleManagerInternals
	// slices at.
	if !bytes.Equal(entry[:20], TimeRangeModuleAddress().Bytes()) {
		t.Fatalf("module segment = %x", entry[:20])
	}
	if entity := uint32(entry[20])<<24 | uint32(entry[21])<<16 | uint32(entry[22])<<8 | uint32(entry[23]); entity != 3 {
		t.Fatalf("entity segment = %d, want 3", entity)
	}
	if entry[24] != HookFlagValidation {
		t.Fatalf("flags = %x, want validation (%x)", entry[24], HookFlagValidation)
	}
	initData, err := PackTimeRangeInstallData(3, 1790000000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(entry[25:], initData) {
		t.Fatal("initData segment does not round-trip")
	}
}

func TestAllowlistExecHookIsConfigOnly(t *testing.T) {
	entry := AllowlistExecHook(2)
	// The exec hook shares module state with the validation hook, which
	// installed it — a second onInstall would re-run updateAllowlist. Config
	// only, pre-hook only (the module's post-hook reverts NotImplemented).
	if len(entry) != 25 {
		t.Fatalf("exec hook entry is %d bytes, want 25 (config only)", len(entry))
	}
	if entry[24] != HookFlagExecHasPre {
		t.Fatalf("flags = %x, want exec-pre (%x)", entry[24], HookFlagExecHasPre)
	}
}

func TestWrapExecuteUserOp(t *testing.T) {
	inner, err := PackExecute(common.HexToAddress("0x2222222222222222222222222222222222222222"), big.NewInt(0), nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapExecuteUserOp(inner)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(wrapped[:4]) != "8dd7712f" {
		t.Fatalf("wrapper selector = %x, want 8dd7712f", wrapped[:4])
	}
	if !bytes.Equal(wrapped[4:], inner) {
		t.Fatal("inner calldata does not survive wrapping")
	}
	if _, err := WrapExecuteUserOp([]byte{0x01}); err == nil {
		t.Error("expected sub-selector calldata to be rejected")
	}

	unwrapped := UnwrapExecuteUserOp(wrapped)
	if !bytes.Equal(unwrapped, inner) {
		t.Fatal("UnwrapExecuteUserOp did not restore inner execute calldata")
	}
	if !bytes.Equal(UnwrapExecuteUserOp(inner), inner) {
		t.Fatal("UnwrapExecuteUserOp must leave unwrapped execute calldata unchanged")
	}
}
