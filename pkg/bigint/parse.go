// Package bigint provides shared big.Int parsing helpers.
package bigint

import (
	"fmt"
	"math/big"
	"strings"
)

// Parse decodes s as a base-10 integer, or as base-16 if s has a "0x" or "0X"
// prefix. Whitespace around s is trimmed. Returns an error on empty or
// unparseable input.
//
// Unlike (*big.Int).SetString with base 0, Parse does not treat leading-zero
// strings as octal: "010" parses as 10, not 8. Use this for any user-supplied
// numeric string where octal interpretation would be surprising or wrong
// (e.g. ERC20 amounts, RPC payloads, contract method params).
func Parse(s string) (*big.Int, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, fmt.Errorf("expected numeric value, got ''")
	}
	base := 10
	digits := trimmed
	if strings.HasPrefix(digits, "0x") || strings.HasPrefix(digits, "0X") {
		base = 16
		digits = digits[2:]
		if digits == "" {
			return nil, fmt.Errorf("expected numeric value, got '%s'", trimmed)
		}
	}
	n, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return nil, fmt.Errorf("expected numeric value, got '%s'", trimmed)
	}
	return n, nil
}
