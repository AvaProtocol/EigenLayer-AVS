package taskengine

import (
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
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

func TestPreflightSessionGrantCoverageNativeOnlyRefusesContractWrite(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	policy := &model.SessionPolicy{
		ID: "01nativeonlyaaaaaaaaaaaaaa", Owner: &owner, Runner: &wallet,
		ChainID: 11155111, EntityID: 1, SessionSigner: &signer,
		Status:           model.SessionPolicyPending,
		NativeRecipients: []*common.Address{&owner},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "1", GrantedCap: "1"},
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
			CarrierNonce:   big.NewInt(1),
			Deadline:       1785541743,
			OwnerSignature: make([]byte, 65),
		},
	}
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
	}

	vm := &VM{
		db: db, TaskOwner: owner, mu: new(sync.Mutex),
		vars: map[string]any{"aa_sender": wallet.Hex()},
	}
	r := &ContractWriteProcessor{
		CommonProcessor:   &CommonProcessor{vm: vm},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111},
		owner:             owner,
	}
	msg := r.preflightSessionGrantCoverage([]PlannedCall{{
		Target:   common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"),
		Selector: "0x04e45aaf",
		Label:    "exactInputSingle",
	}})
	if !strings.HasPrefix(msg, "SESSION_POLICY_TARGET_NOT_ALLOWED:") {
		t.Fatalf("native-only grant must refuse contractWrite, got %q", msg)
	}
}

func TestPreflightSessionGrantCoverageSelfFundedNativeCapNeedsFee(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	router := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")
	policy := &model.SessionPolicy{
		ID: "01mixedcapaaaaaaaaaaaaaaaa", Owner: &owner, Runner: &wallet,
		ChainID: 11155111, EntityID: 1, SessionSigner: &signer,
		Status: model.SessionPolicyPending,
		AllowedActions: []model.AllowedAction{
			{Target: &router, Selectors: []string{"0x04e45aaf"}},
		},
		NativeSpendCap: &model.NativeSpendCap{Amount: "100000000000000000", GrantedCap: "100000000000000000"},
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
			CarrierNonce:   big.NewInt(1),
			Deadline:       1785541743,
			OwnerSignature: make([]byte, 65),
		},
	}
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
	}

	vm := &VM{
		db: db, TaskOwner: owner, mu: new(sync.Mutex),
		vars: map[string]any{"aa_sender": wallet.Hex()},
	}
	planned := []PlannedCall{{
		Target: router, Selector: "0x04e45aaf", Label: "exactInputSingle",
		Value: big.NewInt(1),
	}}

	bare := &ContractWriteProcessor{
		CommonProcessor:   &CommonProcessor{vm: vm},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111},
		owner:             owner,
	}
	msg := bare.preflightSessionGrantCoverage(planned)
	if !strings.Contains(msg, SessionPolicyNativeCapExceededCode) {
		t.Fatalf("self-funded payable under NT without ethclient must fail closed, got %q", msg)
	}

	priced := &ContractWriteProcessor{
		CommonProcessor:   &CommonProcessor{vm: vm},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111},
		owner:             owner,
		nativeReader: stubCodeAndFee{
			fee: big.NewInt(2_000_000_000),
		},
	}
	if got := priced.preflightSessionGrantCoverage(planned); got != "" {
		t.Fatalf("injected fee reader must price the cap, got %q", got)
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
	if strings.Contains(msg, "Uniswap") || strings.Contains(msg, "uniswap") {
		t.Fatalf("remediation must not be Uniswap-specific: %s", msg)
	}
	// Selector formatting is normalized even if input lacked 0x / mixed case.
	msg2 := FormatSessionPolicyTargetNotAllowed([]PlannedCall{
		{Target: weth, Selector: "095EA7B3", Label: "approve"},
	}, "")
	if !strings.Contains(msg2, "selector=0x095ea7b3") {
		t.Fatalf("expected normalized selector in %s", msg2)
	}
}
