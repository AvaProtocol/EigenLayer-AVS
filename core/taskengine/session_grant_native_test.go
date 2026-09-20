package taskengine

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

type stubCodeAndFee struct {
	code    []byte
	codeErr error
	fee     *big.Int
	feeErr  error
}

func (s stubCodeAndFee) CodeAt(context.Context, common.Address) ([]byte, error) {
	return s.code, s.codeErr
}
func (s stubCodeAndFee) MaxFeePerGas(context.Context) (*big.Int, error) {
	return s.fee, s.feeErr
}

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
	coverageCode := strings.SplitN(FormatSessionPolicyTargetNotAllowed(nil, ""), ":", 2)[0]
	if strings.HasPrefix(msg, coverageCode+":") {
		t.Fatalf("native refusal must not reuse the coverage code %q, got %q", coverageCode, msg)
	}
	if !strings.Contains(strings.ToLower(msg), "re-grant") {
		t.Fatalf("native refusal must advise re-granting with nativeRecipients, got %q", msg)
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
	amount := big.NewInt(1)

	t.Run("skips when there is no usable policy", func(t *testing.T) {
		p := &ETHTransferProcessor{
			CommonProcessor:   &CommonProcessor{},
			smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111},
		}
		if msg := p.preflightSessionGrant(recipient, amount); msg != "" {
			t.Fatalf("no policy: send path fails no session authorization, got %q", msg)
		}
	})

	t.Run("skips when the chain is not modular account v2", func(t *testing.T) {
		p := &ETHTransferProcessor{
			CommonProcessor:   &CommonProcessor{},
			smartWalletConfig: &config.SmartWalletConfig{ChainID: 11155111, AccountProvider: "something_else"},
		}
		if msg := p.preflightSessionGrant(recipient, amount); msg != "" {
			t.Fatalf("non-MA-v2 chain has no session hooks to trip, got %q", msg)
		}
	})

	t.Run("skips when there is no smart wallet config", func(t *testing.T) {
		p := &ETHTransferProcessor{CommonProcessor: &CommonProcessor{}}
		if msg := p.preflightSessionGrant(recipient, amount); msg != "" {
			t.Fatalf("expected skip with no config, got %q", msg)
		}
	})
}

func TestPreflightNativePermission(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	bob := common.HexToAddress("0x000000000000000000000000000000000000b0b0")
	twoGwei := big.NewInt(2_000_000_000)
	reader := stubCodeAndFee{fee: twoGwei}
	policy := &model.SessionPolicy{
		ID:               "01native",
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "100000000000000000", GrantedCap: "100000000000000000"}, // 0.1 ETH
	}

	t.Run("no policy native send", func(t *testing.T) {
		msg := PreflightNativePermission(nil, NativeIntent{Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend}, nil)
		if !strings.HasPrefix(msg, SessionPolicyNativeNotAllowedCode+":") {
			t.Fatalf("got %q", msg)
		}
		if !strings.Contains(msg, "re-grant") {
			t.Fatalf("must advise re-grant, got %q", msg)
		}
	})
	t.Run("no nativeRecipients", func(t *testing.T) {
		p := &model.SessionPolicy{ID: "01caponly", NativeSpendCap: &model.NativeSpendCap{Amount: "1"}}
		msg := PreflightNativePermission(p, NativeIntent{Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend}, nil)
		if !strings.HasPrefix(msg, SessionPolicyNativeNotAllowedCode+":") {
			t.Fatalf("nativeSpendCap alone is not an ETH send, got %q", msg)
		}
	})
	t.Run("recipient not in list", func(t *testing.T) {
		msg := PreflightNativePermission(policy, NativeIntent{Recipient: bob, Amount: big.NewInt(1), Kind: NativeSend}, nil)
		if !strings.HasPrefix(msg, SessionPolicyRecipientNotAllowedCode+":") {
			t.Fatalf("got %q", msg)
		}
	})
	t.Run("covering send sponsored", func(t *testing.T) {
		msg := PreflightNativePermission(policy, NativeIntent{
			Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend, Sponsored: true,
		}, reader)
		if msg != "" {
			t.Fatalf("covering sponsored send: %q", msg)
		}
	})
	t.Run("contract recipient", func(t *testing.T) {
		msg := PreflightNativePermission(policy, NativeIntent{
			Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend, Sponsored: true,
		}, stubCodeAndFee{code: []byte{0x60}, fee: twoGwei})
		if !strings.HasPrefix(msg, SessionPolicyRecipientNotEOACode+":") {
			t.Fatalf("got %q", msg)
		}
	})
	t.Run("payable write without native cap passes", func(t *testing.T) {
		uniswap := &model.SessionPolicy{ID: "01uni"}
		msg := PreflightNativePermission(uniswap, NativeIntent{
			Amount: big.NewInt(1e18), Kind: NativeValue,
		}, nil)
		if msg != "" {
			t.Fatalf("Uniswap ETH-in without NT: %q", msg)
		}
	})
	t.Run("self-funded cap exceeded", func(t *testing.T) {
		tiny := &model.SessionPolicy{
			ID:               "01tiny",
			NativeRecipients: []*common.Address{&alice},
			NativeSpendCap:   &model.NativeSpendCap{Amount: "1", GrantedCap: "1"},
		}
		msg := PreflightNativePermission(tiny, NativeIntent{
			Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend,
		}, reader)
		if !strings.HasPrefix(msg, SessionPolicyNativeCapExceededCode+":") {
			t.Fatalf("got %q", msg)
		}
	})
}

func TestNativePreflightGasUnitsFor(t *testing.T) {
	pending := &model.SessionPolicy{}
	if g := nativePreflightGasUnitsFor(NativeSend, pending); g != nativePreflightGasUnitsFirstOp {
		t.Fatalf("send first-op = %d, want %d", g, nativePreflightGasUnitsFirstOp)
	}
	if g := nativePreflightGasUnitsFor(NativeValue, pending); g != nativePreflightGasUnitsValueFirstOp {
		t.Fatalf("value first-op = %d, want %d", g, nativePreflightGasUnitsValueFirstOp)
	}
	applied := &model.SessionPolicy{Grant: &model.SessionGrantAuthorization{AppliedAt: 1}}
	if g := nativePreflightGasUnitsFor(NativeSend, applied); g != nativePreflightGasUnitsSteady {
		t.Fatalf("send steady = %d, want %d", g, nativePreflightGasUnitsSteady)
	}
	if g := nativePreflightGasUnitsFor(NativeValue, applied); g != nativePreflightGasUnitsValueSteady {
		t.Fatalf("value steady = %d, want %d", g, nativePreflightGasUnitsValueSteady)
	}
}

// Native recipient rows set HasSelectorAllowlist=false. Empty-calldata
// preflight must read nativeRecipients, not refuse every MA v2 chain.
func TestHooksForDoesNotRepeatCodeAt(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	var lookups int
	permissions := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(time.Hour).UnixMilli(),
		CodeAt: func(common.Address) ([]byte, error) {
			lookups++
			return nil, nil
		},
	}
	if err := permissions.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := permissions.HooksFor(1); err != nil {
		t.Fatalf("HooksFor: %v", err)
	}
	if lookups != 1 {
		t.Fatalf("CodeAt lookups = %d, want 1 (HooksFor must not re-run chain Validate)", lookups)
	}
}

func TestHooksForRefusesUnprovenNativeRecipients(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	permissions := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(time.Hour).UnixMilli(),
	}
	if _, err := permissions.HooksFor(1); err == nil {
		t.Fatal("expected packing without CodeAt to fail closed")
	} else if !strings.Contains(err.Error(), "CodeAt is unset") {
		t.Fatalf("got %q, want CodeAt is unset", err)
	}
}

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
