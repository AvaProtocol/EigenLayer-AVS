package bigint

import (
	"math/big"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string // big.Int decimal string, ignored when wantErr is set
		wantErr string
	}{
		{name: "zero", in: "0", want: "0"},
		{name: "decimal", in: "4000000", want: "4000000"},
		{name: "leading zero is decimal not octal", in: "010", want: "10"},
		{name: "negative decimal", in: "-5", want: "-5"},
		{name: "lowercase hex", in: "0x3D0900", want: "4000000"},
		{name: "uppercase hex prefix", in: "0X3d0900", want: "4000000"},
		{name: "hex with leading zero digits", in: "0x00ff", want: "255"},
		{name: "trims whitespace", in: "  42  ", want: "42"},
		{name: "huge value preserved", in: "100000000000000000000", want: "100000000000000000000"},

		{name: "empty", in: "", wantErr: "expected numeric value, got ''"},
		{name: "whitespace only", in: "   ", wantErr: "expected numeric value, got ''"},
		{name: "0x with no digits", in: "0x", wantErr: "expected numeric value, got '0x'"},
		{name: "non-numeric", in: "MAX", wantErr: "expected numeric value, got 'MAX'"},
		{name: "invalid hex digits", in: "0xZZZ", wantErr: "expected numeric value, got '0xZZZ'"},
		{name: "decimal with letters", in: "12abc", wantErr: "expected numeric value, got '12abc'"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse(%q) = %v, want error %q", tc.in, got, tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("Parse(%q) error = %q, want %q", tc.in, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.in, err)
			}
			want, _ := new(big.Int).SetString(tc.want, 10)
			if got.Cmp(want) != 0 {
				t.Fatalf("Parse(%q) = %s, want %s", tc.in, got.String(), tc.want)
			}
		})
	}
}
