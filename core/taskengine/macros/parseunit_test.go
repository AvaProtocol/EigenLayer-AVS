package macros

import (
	"math/big"
	"testing"
)

func TestParseUnit(t *testing.T) {
	e8 := new(big.Int).Exp(big.NewInt(10), big.NewInt(8), nil)

	// The regression: divide-by-decimal gave 327; ethers parseUnits gives 262199000000.
	if got, want := ParseUnit("2621.99", 8), big.NewInt(262199000000); got.Cmp(want) != 0 {
		t.Fatalf("ParseUnit(2621.99, 8) = %s, want %s", got, want)
	}
	if got := ParseUnit("1", 8); got.Cmp(e8) != 0 { // integer scaling
		t.Fatalf("ParseUnit(1, 8) = %s, want %s", got, e8)
	}

	// Malformed or out-of-range input must be rejected, not silently coerced.
	for _, bad := range []struct {
		val     string
		decimal uint
	}{
		{"1.999", 2}, // over-precision
		{"-0.5", 8},  // negative with a "-0" whole part
		{"-1.5", 8},  // negative
		{"1.+5", 8},  // malformed fraction (leading +)
		{"1", 1000},  // decimals out of range
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("ParseUnit(%q, %d) expected panic, got none", bad.val, bad.decimal)
				}
			}()
			ParseUnit(bad.val, bad.decimal)
		}()
	}
}
