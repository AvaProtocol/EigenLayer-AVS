package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
// coming back as opaque AA23. It must stay out of the way when it has nothing
// to check against.
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
	t.Run("applied grant does not re-check code", func(t *testing.T) {
		applied := *policy
		applied.Grant = &model.SessionGrantAuthorization{AppliedAt: 1}
		msg := PreflightNativePermission(&applied, NativeIntent{
			Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend, Sponsored: true,
		}, stubCodeAndFee{code: []byte{0x60}, fee: twoGwei})
		if msg != "" {
			t.Fatalf("an applied grant must leave a later contract code alone, got %q", msg)
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
	if g, want := nativePreflightGasUnitsFor(NativeSend, pending), (grantGasShape{}).firstOpUnits(NativeSend); g != want {
		t.Fatalf("send first-op = %d, want %d", g, want)
	}
	if g, want := nativePreflightGasUnitsFor(NativeValue, pending), (grantGasShape{}).firstOpUnits(NativeValue); g != want {
		t.Fatalf("value first-op = %d, want %d", g, want)
	}
	// A pending grant's first op is sized from what it installs.
	alice, bob := common.HexToAddress("0xa1"), common.HexToAddress("0xb2")
	token := common.HexToAddress("0x7000")
	wide := &model.SessionPolicy{
		NativeRecipients: []*common.Address{&alice, &bob},
		AllowedActions:   []model.AllowedAction{{Target: &token, Selectors: []string{"0xa9059cbb"}}},
	}
	if g, want := nativePreflightGasUnitsFor(NativeSend, wide), (grantGasShape{nativeRows: 2, targets: 1}).firstOpUnits(NativeSend); g != want {
		t.Fatalf("2-row 1-target first-op = %d, want %d", g, want)
	}
	applied := &model.SessionPolicy{Grant: &model.SessionGrantAuthorization{AppliedAt: 1}}
	if g := nativePreflightGasUnitsFor(NativeSend, applied); g != nativePreflightGasUnitsSteady {
		t.Fatalf("send steady = %d, want %d", g, nativePreflightGasUnitsSteady)
	}
	if g := nativePreflightGasUnitsFor(NativeValue, applied); g != nativePreflightGasUnitsValueSteady {
		t.Fatalf("value steady = %d, want %d", g, nativePreflightGasUnitsValueSteady)
	}
}

// The budgets cover every operation measured on Sepolia with bundler-priced
// gas (scripts/spike/native_recipient_cap, signed units: what
// NativeTokenLimitModule charges a self-funded op). A first grant prices
// the deploy; a replace prices each grant it tears down.
func TestNativeGasBudgetsCoverSepoliaMeasurement(t *testing.T) {
	rows := func(n int) grantGasShape { return grantGasShape{nativeRows: n} }
	mixed := grantGasShape{nativeRows: 20, targets: 8}
	replacing := func(shape grantGasShape, priors ...grantGasShape) grantGasShape {
		shape.teardowns = priors
		return shape
	}
	for _, m := range []struct {
		name   string
		shape  grantGasShape
		signed int64
	}{
		{"5 rows, first install with deploy", rows(5), 3_364_553},
		{"5 rows, replace", replacing(rows(5), rows(5)), 3_333_765},
		{"10 rows, first install with deploy", rows(10), 4_045_508},
		{"10 rows, replace", replacing(rows(10), rows(10)), 4_157_472},
		{"20 rows, first install with deploy", rows(20), 5_407_610},
		{"20 rows, install on a deployed wallet", rows(20), 5_025_428},
		{"20 rows, replace", replacing(rows(20), rows(20)), 5_805_602},
		{"20 rows, replace tearing down four 20-row grants", replacing(rows(20), rows(20), rows(20), rows(20), rows(20)), 8_580_582},
		{"20 rows + 8 targets, first install with deploy", mixed, 10_426_614},
		{"20 rows + 8 targets, replace a 20-row grant", replacing(mixed, rows(20)), 10_418_622},
		{"20 rows + 8 targets, replace the same shape", replacing(mixed, mixed), 10_797_774},
		{"1 row, replace 20 rows + 8 targets", replacing(rows(1), mixed), 3_807_242},
		{"1 row, replace a 1-row grant", replacing(rows(1), rows(1)), 2_674_960},
	} {
		if got := m.shape.firstOpUnits(NativeSend); got < m.signed {
			t.Errorf("%s: budget %d < measured %d", m.name, got, m.signed)
		}
	}
	const steadySigned = 767_036 // a send on an installed grant, any size
	if nativePreflightGasUnitsSteady < steadySigned {
		t.Errorf("steady budget %d < measured %d", nativePreflightGasUnitsSteady, steadySigned)
	}
}

// teardownShapes prices the resolved set in batch order, capped like it.
func TestTeardownShapesFollowTheBatch(t *testing.T) {
	a, b := common.HexToAddress("0xa1"), common.HexToAddress("0xb2")
	token := common.HexToAddress("0x7000")
	small := &model.SessionPolicy{NativeRecipients: []*common.Address{&a}}
	wide := &model.SessionPolicy{NativeRecipients: []*common.Address{&a, &b}, AllowedActions: []model.AllowedAction{{Target: &token}}}
	got := teardownShapes([]*model.SessionPolicy{small, nil, wide, small, small, wide})
	if len(got) != maxOnChainTeardowns {
		t.Fatalf("teardowns = %d, want the cap %d", len(got), maxOnChainTeardowns)
	}
	if got[0].nativeRows != 1 || got[1].targets != 1 || got[2].targets != 0 {
		t.Fatalf("batch order = %+v", got)
	}
	if shapes := teardownShapes(nil); len(shapes) != 0 {
		t.Fatalf("a first grant tears nothing down: %+v", shapes)
	}
}

// Native recipient rows set HasSelectorAllowlist=false. Empty-calldata
// preflight must read nativeRecipients, not refuse every MA v2 chain.
func TestValidateNativeRecipient7702Designation(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	code := make([]byte, 23)
	code[0], code[1], code[2] = 0xef, 0x01, 0x00
	copy(code[3:], config.SMA7702Delegate().Bytes())
	permissions := SessionPermissions{
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10000000000000000"},
		ValidUntilMs:     time.Now().Add(time.Hour).UnixMilli(),
		CodeAt:           func(common.Address) ([]byte, error) { return code, nil },
	}
	err := permissions.Validate()
	if err == nil {
		t.Fatal("an unacknowledged 7702-designated native recipient must be refused")
	}
	if !strings.Contains(err.Error(), "7702-delegated") {
		t.Fatalf("got %q, want a 7702-delegated diagnosis", err)
	}
	if !strings.Contains(err.Error(), config.SMA7702Delegate().Hex()) {
		t.Fatalf("got %q, want the delegate address", err)
	}
	if strings.Contains(err.Error(), "is a contract") || strings.Contains(err.Error(), "send via contractWrite") {
		t.Fatalf("a designation must not be called a contract: %v", err)
	}
	permissions.DelegatedNativeRecipients = []model.DelegatedNativeRecipient{
		nativeAck(alice, config.SMA7702Delegate()),
	}
	if err := permissions.Validate(); err != nil {
		t.Fatalf("an acknowledgement of the current delegate must admit the recipient: %v", err)
	}
	permissions.DelegatedNativeRecipients = nil
	permissions.AllowContractRecipient = true
	if err := permissions.Validate(); err != nil {
		t.Fatalf("allowContractRecipient must admit a 7702-designated recipient: %v", err)
	}
}

func nativeDesignation(delegate common.Address) []byte {
	code := make([]byte, 23)
	code[0], code[1], code[2] = 0xef, 0x01, 0x00
	copy(code[3:], delegate.Bytes())
	return code
}

func nativeAck(recipient, delegate common.Address) model.DelegatedNativeRecipient {
	who, to := recipient, delegate
	return model.DelegatedNativeRecipient{Recipient: &who, Delegate: &to}
}

// The acceptance matrix for #872. A native recipient with code is allowed
// only when that code is an EIP-7702 designation and the owner acknowledged
// that exact delegate. Our own delegate is not special here.
func TestNativeRecipientDelegation(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	outsider := common.HexToAddress("0x0000000000000000000000000000000000000abc")
	ours := config.SMA7702Delegate()
	coinbase := common.HexToAddress("0x7702cb554e6bfb442cb743a7df23154544a7176c")
	other := common.HexToAddress("0x1111111111111111111111111111111111111111")
	contractCode := []byte{0x60, 0x80}
	lookalike := nativeDesignation(coinbase)
	lookalike[2] = 0x01

	type codeCase struct {
		name          string
		code          []byte
		acks          []model.DelegatedNativeRecipient
		allowContract bool
		wantOK        bool
		want          []string
		forbid        []string
	}
	cases := []codeCase{
		{name: "plain EOA", wantOK: true},
		{name: "plain EOA with an acknowledgement", acks: []model.DelegatedNativeRecipient{nativeAck(alice, coinbase)}, wantOK: true},
		{name: "plain contract", code: contractCode, want: []string{SessionPolicyRecipientNotEOACode, "is a contract"}, forbid: []string{SessionPolicyRecipientDelegateChangedCode, "7702-delegated"}},
		{name: "plain contract listed as delegated", code: contractCode, acks: []model.DelegatedNativeRecipient{nativeAck(alice, coinbase)}, want: []string{SessionPolicyRecipientNotEOACode, "is a contract"}, forbid: []string{SessionPolicyRecipientDelegateChangedCode}},
		{name: "plain contract with allowContractRecipient", code: contractCode, allowContract: true, wantOK: true},
		{name: "lookalike prefix is a contract", code: lookalike, want: []string{SessionPolicyRecipientNotEOACode, "is a contract"}, forbid: []string{"7702-delegated"}},
		{name: "our delegate unacknowledged", code: nativeDesignation(ours), want: []string{SessionPolicyRecipientNotAcknowledgedCode, "7702-delegated", ours.Hex()}, forbid: []string{SessionPolicyRecipientNotEOACode, "is a contract", "send via contractWrite"}},
		{name: "our delegate acknowledged", code: nativeDesignation(ours), acks: []model.DelegatedNativeRecipient{nativeAck(alice, ours)}, wantOK: true},
		{name: "our delegate with allowContractRecipient", code: nativeDesignation(ours), allowContract: true, wantOK: true},
		{name: "another delegate unacknowledged", code: nativeDesignation(coinbase), want: []string{SessionPolicyRecipientNotAcknowledgedCode, "7702-delegated", coinbase.Hex()}, forbid: []string{SessionPolicyRecipientNotEOACode, "is a contract", "send via contractWrite"}},
		{name: "another delegate acknowledged", code: nativeDesignation(coinbase), acks: []model.DelegatedNativeRecipient{nativeAck(alice, coinbase)}, wantOK: true},
		{name: "another delegate acknowledged as someone else", code: nativeDesignation(coinbase), acks: []model.DelegatedNativeRecipient{nativeAck(alice, other)}, want: []string{SessionPolicyRecipientDelegateChangedCode, other.Hex(), coinbase.Hex()}, forbid: []string{SessionPolicyRecipientNotEOACode, "is a contract"}},
	}

	for _, tc := range cases {
		t.Run("validate "+tc.name, func(t *testing.T) {
			permissions := SessionPermissions{
				NativeRecipients:          []*common.Address{&alice},
				DelegatedNativeRecipients: tc.acks,
				NativeSpendCap:            &model.NativeSpendCap{Amount: "10000000000000000"},
				AllowContractRecipient:    tc.allowContract,
				ValidUntilMs:              time.Now().Add(time.Hour).UnixMilli(),
				CodeAt: func(common.Address) ([]byte, error) {
					return tc.code, nil
				},
			}
			err := permissions.Validate()
			assertNativeCodeCase(t, errString(err), tc.wantOK, tc.want, tc.forbid)
		})
		t.Run("preflight "+tc.name, func(t *testing.T) {
			policy := &model.SessionPolicy{
				ID:                        "01native",
				NativeRecipients:          []*common.Address{&alice},
				DelegatedNativeRecipients: tc.acks,
				NativeSpendCap:            &model.NativeSpendCap{Amount: "100000000000000000"},
				AllowContractRecipient:    tc.allowContract,
			}
			msg := PreflightNativePermission(policy, NativeIntent{
				Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend, Sponsored: true,
			}, stubCodeAndFee{code: tc.code})
			assertNativeCodeCase(t, msg, tc.wantOK, tc.want, tc.forbid)
		})
	}

	t.Run("validate acknowledgement must name a native recipient", func(t *testing.T) {
		permissions := SessionPermissions{
			NativeRecipients:          []*common.Address{&alice},
			DelegatedNativeRecipients: []model.DelegatedNativeRecipient{nativeAck(outsider, coinbase)},
			NativeSpendCap:            &model.NativeSpendCap{Amount: "1"},
			ValidUntilMs:              time.Now().Add(time.Hour).UnixMilli(),
			CodeAt:                    func(common.Address) ([]byte, error) { return nil, nil },
		}
		err := permissions.Validate()
		if err == nil || !strings.Contains(err.Error(), "not in nativeRecipients") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("validate duplicate acknowledgement", func(t *testing.T) {
		permissions := SessionPermissions{
			NativeRecipients: []*common.Address{&alice},
			DelegatedNativeRecipients: []model.DelegatedNativeRecipient{
				nativeAck(alice, coinbase), nativeAck(alice, other),
			},
			NativeSpendCap: &model.NativeSpendCap{Amount: "1"},
			ValidUntilMs:   time.Now().Add(time.Hour).UnixMilli(),
			CodeAt:         func(common.Address) ([]byte, error) { return nil, nil },
		}
		err := permissions.Validate()
		if err == nil || !strings.Contains(err.Error(), "listed twice") {
			t.Fatalf("got %v", err)
		}
	})

	applied := &model.SessionPolicy{
		ID:                        "01applied",
		NativeRecipients:          []*common.Address{&alice},
		DelegatedNativeRecipients: []model.DelegatedNativeRecipient{nativeAck(alice, coinbase)},
		NativeSpendCap:            &model.NativeSpendCap{Amount: "100000000000000000"},
		Grant:                     &model.SessionGrantAuthorization{AppliedAt: 1},
	}
	send := NativeIntent{Recipient: alice, Amount: big.NewInt(1), Kind: NativeSend, Sponsored: true}

	t.Run("applied unacknowledged recipient is not re-read", func(t *testing.T) {
		policy := *applied
		policy.DelegatedNativeRecipients = nil
		msg := PreflightNativePermission(&policy, send, stubCodeAndFee{code: contractCode})
		if msg != "" {
			t.Fatalf("an applied grant must leave a later contract code alone, got %q", msg)
		}
	})
	t.Run("applied acknowledged recipient still designates the same delegate", func(t *testing.T) {
		msg := PreflightNativePermission(applied, send, stubCodeAndFee{code: nativeDesignation(coinbase)})
		if msg != "" {
			t.Fatalf("matching delegate: %q", msg)
		}
	})
	t.Run("applied acknowledged recipient is an EOA again", func(t *testing.T) {
		msg := PreflightNativePermission(applied, send, stubCodeAndFee{})
		if msg != "" {
			t.Fatalf("empty code: %q", msg)
		}
	})
	t.Run("applied acknowledged recipient changed delegate", func(t *testing.T) {
		msg := PreflightNativePermission(applied, send, stubCodeAndFee{code: nativeDesignation(other)})
		assertNativeCodeCase(t, msg, false, []string{SessionPolicyRecipientDelegateChangedCode, coinbase.Hex(), other.Hex(), "now designates"}, nil)
	})
	t.Run("applied acknowledged recipient is no longer a designation", func(t *testing.T) {
		msg := PreflightNativePermission(applied, send, stubCodeAndFee{code: contractCode})
		assertNativeCodeCase(t, msg, false, []string{SessionPolicyRecipientDelegateChangedCode, "no longer that designation"}, []string{SessionPolicyRecipientNotEOACode})
	})
	t.Run("applied acknowledged recipient fails closed without a reader", func(t *testing.T) {
		msg := PreflightNativePermission(applied, send, nil)
		assertNativeCodeCase(t, msg, false, []string{SessionPolicyRecipientCodeUnreadableCode, "no chain reader", applied.ID}, []string{SessionPolicyRecipientNotEOACode, SessionPolicyRecipientDelegateChangedCode})
	})
	t.Run("applied acknowledged recipient cannot be read", func(t *testing.T) {
		msg := PreflightNativePermission(applied, send, stubCodeAndFee{codeErr: fmt.Errorf("rpc timeout")})
		assertNativeCodeCase(t, msg, false, []string{SessionPolicyRecipientCodeUnreadableCode, "rpc timeout", applied.ID}, []string{SessionPolicyRecipientNotEOACode, SessionPolicyRecipientDelegateChangedCode})
	})
	t.Run("before apply a missing reader stays not an EOA", func(t *testing.T) {
		policy := *applied
		policy.Grant = nil
		msg := PreflightNativePermission(&policy, send, nil)
		assertNativeCodeCase(t, msg, false, []string{SessionPolicyRecipientNotEOACode, "fail closed"}, []string{SessionPolicyRecipientCodeUnreadableCode, SessionPolicyRecipientDelegateChangedCode})
	})
	t.Run("before apply a failed read stays not an EOA", func(t *testing.T) {
		policy := *applied
		policy.Grant = nil
		msg := PreflightNativePermission(&policy, send, stubCodeAndFee{codeErr: fmt.Errorf("rpc timeout")})
		assertNativeCodeCase(t, msg, false, []string{SessionPolicyRecipientNotEOACode, "looking up", "rpc timeout"}, []string{SessionPolicyRecipientCodeUnreadableCode, SessionPolicyRecipientDelegateChangedCode})
	})
	t.Run("allowContractRecipient skips the applied re-check", func(t *testing.T) {
		policy := *applied
		policy.AllowContractRecipient = true
		msg := PreflightNativePermission(&policy, send, stubCodeAndFee{code: nativeDesignation(other)})
		if msg != "" {
			t.Fatalf("allowContractRecipient: %q", msg)
		}
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func assertNativeCodeCase(t *testing.T, msg string, wantOK bool, want, forbid []string) {
	t.Helper()
	if wantOK {
		if msg != "" {
			t.Fatalf("got %q", msg)
		}
		return
	}
	if msg == "" {
		t.Fatal("expected a refusal")
	}
	for _, part := range want {
		if !strings.Contains(msg, part) {
			t.Fatalf("got %q, want %q", msg, part)
		}
	}
	for _, part := range forbid {
		if strings.Contains(msg, part) {
			t.Fatalf("got %q, must not contain %q", msg, part)
		}
	}
}

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

	// An acknowledgement does not add a selector allowlist. Empty calldata
	// has no selector the module can list.
	delegate := config.SMA7702Delegate()
	acknowledged := permissions
	acknowledged.DelegatedNativeRecipients = []model.DelegatedNativeRecipient{nativeAck(alice, delegate)}
	acknowledged.CodeAt = func(common.Address) ([]byte, error) { return nativeDesignation(delegate), nil }
	if err := acknowledged.Validate(); err != nil {
		t.Fatalf("acknowledged designation: %v", err)
	}
	ackedInputs, err := acknowledged.allowlistInputs()
	if err != nil {
		t.Fatalf("allowlistInputs: %v", err)
	}
	if len(ackedInputs) != 1 || ackedInputs[0].HasSelectorAllowlist || len(ackedInputs[0].Selectors) != 0 {
		t.Fatalf("acknowledged native row = %+v, want an unscoped row", ackedInputs)
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

// Prepare checks every recipient on the merged grant, so a refusal can be
// about another enabled task's recipient. The typed refusal names it, and
// REST copies the addresses onto the problem.
func TestNativeRecipientRefusalNamesTheRecipient(t *testing.T) {
	alice := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	coinbase := common.HexToAddress("0x7702cb554e6bfb442cb743a7df23154544a7176c")
	other := common.HexToAddress("0x1111111111111111111111111111111111111111")

	validate := func(code []byte, acks []model.DelegatedNativeRecipient) *NativeRecipientRefusal {
		t.Helper()
		err := SessionPermissions{
			NativeRecipients:          []*common.Address{&alice},
			DelegatedNativeRecipients: acks,
			NativeSpendCap:            &model.NativeSpendCap{Amount: "1"},
			ValidUntilMs:              time.Now().Add(time.Hour).UnixMilli(),
			CodeAt:                    func(common.Address) ([]byte, error) { return code, nil },
		}.Validate()
		var refusal *NativeRecipientRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("want a NativeRecipientRefusal, got %T %v", err, err)
		}
		if refusal.Recipient != alice || !strings.HasPrefix(refusal.Error(), refusal.Code+": ") {
			t.Fatalf("refusal = %+v (%q)", refusal, refusal.Error())
		}
		return refusal
	}

	contract := validate([]byte{0x60, 0x80}, nil)
	if contract.Code != SessionPolicyRecipientNotEOACode || contract.Delegate != nil || contract.AcknowledgedDelegate != nil {
		t.Fatalf("contract = %+v", contract)
	}
	if !strings.Contains(contract.Error(), alice.Hex()) {
		t.Fatalf("contract refusal must name the recipient: %q", contract.Error())
	}

	unacked := validate(nativeDesignation(coinbase), nil)
	if unacked.Code != SessionPolicyRecipientNotAcknowledgedCode || unacked.Delegate == nil || *unacked.Delegate != coinbase || unacked.AcknowledgedDelegate != nil {
		t.Fatalf("unacknowledged = %+v", unacked)
	}

	changed := validate(nativeDesignation(coinbase), []model.DelegatedNativeRecipient{nativeAck(alice, other)})
	if changed.Code != SessionPolicyRecipientDelegateChangedCode || changed.Delegate == nil || *changed.Delegate != coinbase ||
		changed.AcknowledgedDelegate == nil || *changed.AcknowledgedDelegate != other {
		t.Fatalf("changed = %+v", changed)
	}
}

// A pending replacement records the teardowns prepare priced, and the
// preflight rebuilds the same first op from them, so a cap prepare accepted
// can send. The reviewed case: one recipient, one 1-recipient teardown, one
// 1-wei send at 1 wei per gas.
func TestPendingReplacementPreflightUsesThePricedTeardowns(t *testing.T) {
	now := skillNow()
	a1 := "0x0000000000000000000000000000000000000001"
	task := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 1, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": "1"},
	})
	teardowns := []grantGasShape{{nativeRows: 1}}
	perms, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, big.NewInt(1)), WithTeardowns(teardowns))
	if err != nil {
		t.Fatal(err)
	}
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "3209281" {
		t.Fatalf("prepared cap = %+v", perms.NativeSpendCap)
	}
	pending := permissionsAsPolicy(SessionPolicyInput{ChainID: skillSepolia, Permissions: perms})
	pending.Grant.PricedTeardowns = recordedTeardowns(teardowns)
	if got := pendingInstallShape(pending); len(got.teardowns) != 1 || got.teardowns[0].nativeRows != 1 {
		t.Fatalf("pending install = %+v", got)
	}
	intent := NativeIntent{Recipient: common.HexToAddress(a1), Amount: big.NewInt(1), Kind: NativeSend}
	if msg := PreflightNativePermission(pending, intent, stubCodeAndFee{fee: big.NewInt(1)}); msg != "" {
		t.Fatalf("a cap prepare accepted must pass preflight, got %q", msg)
	}
}
