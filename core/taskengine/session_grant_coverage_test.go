package taskengine

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func addr(s string) *common.Address {
	a := common.HexToAddress(s)
	return &a
}

func TestSelectorFromCalldata(t *testing.T) {
	if got := SelectorFromCalldata(nil); got != "0x00000000" {
		t.Fatalf("empty: got %s", got)
	}
	// approve(address,uint256) = 0x095ea7b3
	data := common.FromHex("0x095ea7b3000000000000000000000000dead")
	if got := SelectorFromCalldata(data); got != "0x095ea7b3" {
		t.Fatalf("approve selector: got %s", got)
	}
}

func TestMissingGrantCalls_USDCCovered_WETHMissing(t *testing.T) {
	usdc := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")
	router := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")

	allowed := []model.AllowedAction{
		{Target: addr(router.Hex()), Selectors: []string{"0x04e45aaf"}},
		{Target: addr(usdc.Hex()), Selectors: []string{"0x095ea7b3"}},
	}

	// Demoted sell: WETH approve + router swap
	planned := []PlannedCall{
		{Target: weth, Selector: "0x095ea7b3", Label: "approve"},
		{Target: router, Selector: "0x04e45aaf", Label: "exactInputSingle"},
	}
	missing := MissingGrantCalls(allowed, planned)
	if len(missing) != 1 {
		t.Fatalf("want 1 missing (WETH approve), got %d: %+v", len(missing), missing)
	}
	if missing[0].Target != weth {
		t.Fatalf("missing target = %s, want WETH", missing[0].Target.Hex())
	}

	// USDC buy batch fully covered
	buy := []PlannedCall{
		{Target: usdc, Selector: "0x095ea7b3", Label: "approve"},
		{Target: router, Selector: "0x04e45aaf", Label: "exactInputSingle"},
	}
	if m := MissingGrantCalls(allowed, buy); len(m) != 0 {
		t.Fatalf("USDC buy should be covered, missing %+v", m)
	}
}

func TestMissingGrantCalls_EmptyAllowlistSkips(t *testing.T) {
	planned := []PlannedCall{{Target: common.HexToAddress("0x1"), Selector: "0x095ea7b3"}}
	if m := MissingGrantCalls(nil, planned); m != nil {
		t.Fatalf("empty allowlist should skip preflight, got %+v", m)
	}
}

func TestMissingGrantCalls_CaseInsensitive(t *testing.T) {
	token := common.HexToAddress("0xAbcDef0123456789AbcDef0123456789AbcDef01")
	allowed := []model.AllowedAction{
		{Target: addr(token.Hex()), Selectors: []string{"0x095EA7B3"}},
	}
	planned := []PlannedCall{
		{Target: token, Selector: "0x095ea7b3"},
	}
	if m := MissingGrantCalls(allowed, planned); len(m) != 0 {
		t.Fatalf("case-insensitive match failed: %+v", m)
	}
}

func TestFormatSessionPolicyTargetNotAllowed(t *testing.T) {
	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")
	msg := FormatSessionPolicyTargetNotAllowed([]PlannedCall{
		{Target: weth, Selector: "0x095ea7b3", Label: "approve"},
	}, "01abc")
	if !strings.HasPrefix(msg, "SESSION_POLICY_TARGET_NOT_ALLOWED:") {
		t.Fatalf("prefix: %s", msg)
	}
	if !strings.Contains(msg, "01abc") {
		t.Fatalf("policy id: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "095ea7b3") {
		t.Fatalf("selector: %s", msg)
	}
}
