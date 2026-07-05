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
	// Over-precision must be rejected.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("ParseUnit(1.999, 2) expected panic on over-precision")
			}
		}()
		ParseUnit("1.999", 2)
	}()
}
