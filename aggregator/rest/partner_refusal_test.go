package rest

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"testing"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
)

// The policy surface never honors partner delegation, and the refusal must be
// explicit — a reasoned 403, not a handler that quietly ignores the header.

func TestRefusePartnerAssertion_NoHeaderPassesThrough(t *testing.T) {
	if err := refusePartnerAssertion(ctxWithAssertion(""), "Session-policy management"); err != nil {
		t.Fatalf("a request without an assertion must pass: %v", err)
	}
}

func TestRefusePartnerAssertion_RefusesWithoutVerifying(t *testing.T) {
	// Garbage that could never verify — refused for WHERE it was sent, not
	// for what it is.
	err := refusePartnerAssertion(ctxWithAssertion("not-a-jwt"), "Session-policy management")
	assertDelegationRefusal(t, err)
}

func TestRefusePartnerAssertion_RefusesEvenAValidAssertion(t *testing.T) {
	// A fully valid assertion from an active, correctly-scoped partner is
	// refused identically: the boundary is categorical, and the response must
	// not become an oracle for assertion validity.
	_, priv, _ := ed25519.GenerateKey(nil)
	err := refusePartnerAssertion(
		ctxWithAssertion(signAssertion(t, priv, validClaims())), "Session-policy management")
	assertDelegationRefusal(t, err)
}

func assertDelegationRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var httpErr *restmw.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected an HTTPError, got %T: %v", err, err)
	}
	if httpErr.Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", httpErr.Status)
	}
	if httpErr.Code != "PARTNER_DELEGATION_UNSUPPORTED" {
		t.Fatalf("code = %s, want PARTNER_DELEGATION_UNSUPPORTED", httpErr.Code)
	}
	// The reason must name the boundary and the way forward.
	if !strings.Contains(httpErr.Detail, "fund authority") ||
		!strings.Contains(httpErr.Detail, "Bearer JWT") {
		t.Fatalf("detail should explain the fund-authority boundary and point at the JWT path, got: %s", httpErr.Detail)
	}
}
