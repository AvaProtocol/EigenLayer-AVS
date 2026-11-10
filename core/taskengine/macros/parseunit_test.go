package macros

import (
	"math/big"
	"strings"
	"testing"
)

func TestParseUnit(t *testing.T) {
	e8 := new(big.Int).Exp(big.NewInt(10), big.NewInt(8), nil)

	// The regression: divide-by-decimal gave 327; ethers parseUnits gives 262199000000.
	got, err := ParseUnit("2621.99", 8)
	if err != nil {
		t.Fatalf("ParseUnit(2621.99, 8) unexpected error: %v", err)
	}
	if want := big.NewInt(262199000000); got.Cmp(want) != 0 {
		t.Fatalf("ParseUnit(2621.99, 8) = %s, want %s", got, want)
	}
	got, err = ParseUnit("1", 8) // integer scaling
	if err != nil {
		t.Fatalf("ParseUnit(1, 8) unexpected error: %v", err)
	}
	if got.Cmp(e8) != 0 {
		t.Fatalf("ParseUnit(1, 8) = %s, want %s", got, e8)
	}

	// Malformed or out-of-range input must be rejected with an error, not
	// silently coerced — and never with a panic, which goja cannot turn into a
	// JS exception (Sentry EIGENLAYER-AVS-3J).
	for _, bad := range []struct {
		val     string
		decimal uint
		wantErr string
	}{
		{"1.999", 2, "exceeds 2 decimals"},                   // over-precision
		{"-0.5", 8, "not a non-negative decimal"},            // negative with a "-0" whole part
		{"-1.5", 8, "not a non-negative decimal"},            // negative
		{"1.+5", 8, "not a non-negative decimal"},            // malformed fraction (leading +)
		{"1", 1000, "decimals 1000 out of range (max 78)"},   // decimals out of range
		{"1", 65535, "decimals 65535 out of range (max 78)"}, // the probe that produced EIGENLAYER-AVS-3J
	} {
		got, err := ParseUnit(bad.val, bad.decimal)
		if err == nil {
			t.Fatalf("ParseUnit(%q, %d) expected error, got %s", bad.val, bad.decimal, got)
		}
		if got != nil {
			t.Fatalf("ParseUnit(%q, %d) expected nil result on error, got %s", bad.val, bad.decimal, got)
		}
		if !strings.Contains(err.Error(), bad.wantErr) {
			t.Fatalf("ParseUnit(%q, %d) error %q does not mention %q", bad.val, bad.decimal, err, bad.wantErr)
		}
	}
}
