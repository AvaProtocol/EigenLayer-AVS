package preset

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsUserOpRevert(t *testing.T) {
	if IsUserOpRevert(nil) {
		t.Fatal("nil")
	}
	if !IsUserOpRevert(errors.New("UserOp mined with success=false in UserOperationEvent")) {
		t.Fatal("marker")
	}
	if IsUserOpRevert(errors.New("AA23 reverted")) {
		t.Fatal("AA23 is not a mined user-target revert marker")
	}
}

func TestIsClientUserOpFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"infra dial", errors.New("dialing bundler: connection refused"), false},
		{"no grant", errors.New("no session authorization for smart wallet 0xabc (owner 0xdef); MA v2 execution requires a session grant"), true},
		{"multi grant", errors.New("wallet 0x has more than one usable session policy (a, b); revoke one"), true},
		{"preflight", errors.New("SESSION_POLICY_TARGET_NOT_ALLOWED: session grant does not allow: approve target=0xWETH"), true},
		{"prefund", errors.New("smart wallet 0x cannot pay gas: native balance and EntryPoint deposit are both zero"), true},
		{"webhook deny", fmt.Errorf("gas manager declined to sponsor: alchemy_requestGasAndPaymasterAndData (policy x): Request was denied by webhook"), true},
		{"AA23 via gas manager", fmt.Errorf("gas manager declined to sponsor: alchemy_requestGasAndPaymasterAndData (policy bf905871): validation reverted: [reason]: AA23 reverted"), true},
		{"execution reverted via GM", fmt.Errorf("gas manager declined to sponsor: alchemy_requestGasAndPaymasterAndData (policy x): execution reverted"), true},
		{"grant install failed", errors.New("SESSION_GRANT_INSTALL_FAILED: deferred grant install/replace did not land: AA23"), true},
		// Must stay Error → Sentry (infra / ambiguous)
		{"bare AA23", errors.New("validation reverted: [reason]: AA23 reverted"), false},
		{"SESSION_POLICY_LOOKUP_FAILED storage", errors.New("SESSION_POLICY_LOOKUP_FAILED: listing session policies: connection refused"), false},
		{"gas manager transport", fmt.Errorf("gas manager declined to sponsor: alchemy_requestGasAndPaymasterAndData (policy x): %w", errors.New("connection refused")), false},
		{"gas manager timeout", fmt.Errorf("gas manager declined to sponsor: alchemy_requestGasAndPaymasterAndData (policy x): context deadline exceeded"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClientUserOpFailure(tc.err); got != tc.want {
				t.Fatalf("IsClientUserOpFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
