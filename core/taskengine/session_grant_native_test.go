package taskengine

import (
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// A native transfer under a session grant cannot be made to work by editing
// the grant, so the refusal must be a distinct code from the coverage miss and
// must not send the caller off to re-grant. These assertions are the contract
// Studio maps against; loosening them silently would put users back in the
// re-grant loop this replaced.
func TestFormatSessionPolicyNativeNotAllowed(t *testing.T) {
	recipient := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")

	msg := FormatSessionPolicyNativeNotAllowed(recipient, "")
	if !strings.HasPrefix(msg, SessionPolicyNativeNotAllowedCode+":") {
		t.Fatalf("message must lead with the machine code, got %q", msg)
	}
	if !strings.Contains(msg, recipient.Hex()) {
		t.Fatalf("message should name the recipient, got %q", msg)
	}
	// Derive the coverage code from its own formatter rather than restating
	// the literal, so this keeps testing "the two codes differ" even if
	// either string is renamed.
	coverageCode := strings.SplitN(FormatSessionPolicyTargetNotAllowed(nil, ""), ":", 2)[0]
	if strings.HasPrefix(msg, coverageCode+":") {
		t.Fatalf("native refusal must not reuse the coverage code %q, got %q", coverageCode, msg)
	}
	// The coverage error tells callers to "re-grant the session policy".
	// Repeating that here would be actively wrong: no REST grant shape
	// authorizes empty inner calldata.
	if strings.Contains(strings.ToLower(msg), "re-grant") {
		t.Fatalf("native refusal must not advise re-granting, got %q", msg)
	}

	withPolicy := FormatSessionPolicyNativeNotAllowed(recipient, "01m0hf01w")
	if !strings.Contains(withPolicy, "01m0hf01w") {
		t.Fatalf("policy id should be echoed when known, got %q", withPolicy)
	}
}

// The preflight is what keeps a native transfer from reaching the bundler and
// coming back as opaque AA23. It must fire on an MA v2 chain and stay out of
// the way anywhere else.
func TestETHTransferPreflightSessionGrant(t *testing.T) {
	recipient := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")

	t.Run("refuses on modular account v2", func(t *testing.T) {
		p := &ETHTransferProcessor{
			CommonProcessor: &CommonProcessor{},
			// Empty AccountProvider defaults to modular_account_v2.
			smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111},
		}
		msg := p.preflightSessionGrant(recipient)
		if msg == "" {
			t.Fatal("expected a refusal on an MA v2 chain")
		}
		if !strings.HasPrefix(msg, SessionPolicyNativeNotAllowedCode+":") {
			t.Fatalf("expected the native code, got %q", msg)
		}
	})

	t.Run("skips when the chain is not modular account v2", func(t *testing.T) {
		p := &ETHTransferProcessor{
			CommonProcessor:   &CommonProcessor{},
			smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111, AccountProvider: "something_else"},
		}
		if msg := p.preflightSessionGrant(recipient); msg != "" {
			t.Fatalf("non-MA-v2 chain has no session hooks to trip, got %q", msg)
		}
	})

	t.Run("skips when there is no smart wallet config", func(t *testing.T) {
		p := &ETHTransferProcessor{CommonProcessor: &CommonProcessor{}}
		if msg := p.preflightSessionGrant(recipient); msg != "" {
			t.Fatalf("expected skip with no config, got %q", msg)
		}
	})
}

// The native-ETH refusals refuse on MA v2 unconditionally, which is only
// correct while every grant this package builds is selector-scoped: the
// AllowlistModule skips its `data.length < 4` check only when
// hasSelectorAllowlist is false, so a false entry WOULD authorize an empty
// calldata inner call and make the blanket refusal wrong.
//
// That coupling used to live only in comments across two files. If this test
// fails because a new grant shape sets HasSelectorAllowlist=false, the fix is
// not to loosen the assertion — it is to narrow preflightSessionGrant and the
// ExecuteWithdraw check to consider the actual grant instead of the chain.
func TestHooksForNativeRecipientRowsAreUnscoped(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	permissions := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(time.Hour).UnixMilli(),
		CodeAt:           func(common.Address) ([]byte, error) { return nil, nil },
	}
	inputs, err := permissions.allowlistInputs()
	if err != nil {
		t.Fatalf("allowlistInputs: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("native-only grant: got %d inputs, want 1", len(inputs))
	}
	if inputs[0].HasSelectorAllowlist {
		t.Fatal("native recipient rows must set HasSelectorAllowlist=false")
	}
	if inputs[0].HasERC20SpendLimit {
		t.Fatal("native recipient rows must not set an ERC-20 spend limit")
	}

	hooks, err := permissions.HooksFor(1)
	if err != nil {
		t.Fatalf("HooksFor: %v", err)
	}
	if len(hooks) != 5 {
		t.Fatalf("native-only grant: got %d hooks, want 5 (AL-val, AL-exec, NT-val, NT-exec, TR)", len(hooks))
	}
}

func TestHooksForAlwaysScopesSelectors(t *testing.T) {
	usdc := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")

	permissions := SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &usdc, Selectors: []string{"0x095ea7b3"}},
			{Target: &weth, Selectors: []string{"0xa9059cbb", "0x095ea7b3"}},
		},
		SpendCap:     &model.ERC20SpendCap{Token: &usdc, Amount: "1000000"},
		ValidUntilMs: time.Now().Add(time.Hour).UnixMilli(),
	}

	inputs, err := permissions.allowlistInputs()
	if err != nil {
		t.Fatalf("allowlistInputs: %v", err)
	}
	if len(inputs) != len(permissions.AllowedActions) {
		t.Fatalf("expected one input per allowed action, got %d", len(inputs))
	}
	for _, input := range inputs {
		if !input.HasSelectorAllowlist {
			t.Fatalf("ERC-20/router target %s is not selector-scoped; A3 must then "+
				"read the actual grant instead of a chain-level native refusal", input.Target.Hex())
		}
		if len(input.Selectors) == 0 {
			t.Fatalf("target %s is selector-scoped with an empty selector set", input.Target.Hex())
		}
	}
}

func TestAllowlistInputsCapsEachSpendToken(t *testing.T) {
	usdc := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")
	router := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")

	permissions := SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &router, Selectors: []string{"0x04e45aaf"}},
			{Target: &usdc, Selectors: []string{"0x095ea7b3"}},
			{Target: &weth, Selectors: []string{"0x095ea7b3", "0xa9059cbb"}},
		},
		SpendCap: &model.ERC20SpendCap{Token: &usdc, Amount: "500000000"},
		SpendCaps: []model.ERC20SpendCap{
			{Token: &usdc, Amount: "500000000"},
			{Token: &weth, Amount: "1000000000000000000"},
		},
		ValidUntilMs: time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := permissions.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	inputs, err := permissions.allowlistInputs()
	if err != nil {
		t.Fatalf("allowlistInputs: %v", err)
	}
	byTarget := map[common.Address]aa.AllowlistInput{}
	for _, input := range inputs {
		byTarget[input.Target] = input
	}
	if !byTarget[usdc].HasERC20SpendLimit || byTarget[usdc].ERC20SpendLimit.String() != "500000000" {
		t.Fatalf("USDC cap: %+v", byTarget[usdc])
	}
	if !byTarget[weth].HasERC20SpendLimit || byTarget[weth].ERC20SpendLimit.String() != "1000000000000000000" {
		t.Fatalf("WETH cap: %+v", byTarget[weth])
	}
	if byTarget[router].HasERC20SpendLimit {
		t.Fatal("router must not carry an ERC-20 spend limit")
	}
}

func TestValidateRejectsSpendCapOnWrapSelectors(t *testing.T) {
	weth := common.HexToAddress("0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")
	permissions := SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &weth, Selectors: []string{"0x095ea7b3", "0xd0e30db0"}}, // approve + deposit
		},
		SpendCap:     &model.ERC20SpendCap{Token: &weth, Amount: "1"},
		ValidUntilMs: time.Now().Add(time.Hour).UnixMilli(),
	}
	err := permissions.Validate()
	if err == nil {
		t.Fatal("capping WETH with deposit() must fail: AllowlistModule would revert InvalidCalldataLength")
	}
	if !strings.Contains(err.Error(), "cannot cap") {
		t.Fatalf("want cannot-cap copy, got %q", err.Error())
	}
}
