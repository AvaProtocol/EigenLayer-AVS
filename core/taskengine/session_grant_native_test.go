package taskengine

import (
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

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
			t.Fatalf("target %s is not selector-scoped; the blanket native-ETH refusal "+
				"in preflightSessionGrant/ExecuteWithdraw is no longer sound", input.Target.Hex())
		}
		if len(input.Selectors) == 0 {
			t.Fatalf("target %s is selector-scoped with an empty selector set", input.Target.Hex())
		}
	}
}
