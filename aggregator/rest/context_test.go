package rest

import (
	"net/http"
	"testing"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
)

// The zero address is hex-shaped, so IsHexAddress alone let a token bound
// to it through. Such a token derived wallets for owner 0x0 and then failed
// deep in the policy engine as a 503 (EIGENLAYER-AVS-3C). It is a 401 now,
// like any other subject that cannot be a real owner.
func TestRequireUserRejectsSubjectsThatCannotBeOwners(t *testing.T) {
	s := &Server{}
	cases := []struct {
		subject string
		code    string
	}{
		{"", "AUTH_REQUIRED"},
		{"0x0000000000000000000000000000000000000000", "AUTH_INVALID_SUBJECT"},
		{"0X0000000000000000000000000000000000000000", "AUTH_INVALID_SUBJECT"},
		{"studio-user-42", "AUTH_INVALID_SUBJECT"},
		{"0x1234", "AUTH_INVALID_SUBJECT"},
	}
	for _, tc := range cases {
		_, err := s.requireUser(ctxWithUser(tc.subject))
		assertHTTPStatus(t, err, http.StatusUnauthorized)
		if code := err.(*restmw.HTTPError).Code; code != tc.code {
			t.Fatalf("subject %q: expected %s, got %s", tc.subject, tc.code, code)
		}
	}

	user, err := s.requireUser(ctxWithUser(testPartnerSubject))
	if err != nil {
		t.Fatalf("expected a real EOA subject to authenticate, got: %v", err)
	}
	if user.Address.Hex() != testPartnerSubject {
		t.Fatalf("expected owner %s, got %s", testPartnerSubject, user.Address.Hex())
	}
}
