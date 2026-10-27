// Package fixtures contains public contract bytecode for deterministic tests.
package fixtures

import (
	_ "embed"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

// Sepolia block 12007129, 0x69007702764179f14F51cdce752f4f775d74E139.
// Runtime: 23741 bytes, keccak256 ecfc0328ce4d953a4d452e11f54b578fa2d7f2f8126246d1120ca24d11845081.
// Alchemy modular-account v2.0.0 (bd75c9eca7ff5a66aaebc3cf2dab43ddc4622482),
// src/account/SemiModularAccount7702.sol, alchemy.sma-7702.1.0.0 (GPL-3.0-or-later).
// Includes the deployment's immutable EntryPoint and execution delegate.
//
//go:embed sma7702-runtime.hex
var sma7702Runtime string

func SMA7702Runtime() []byte {
	return hexutil.MustDecode(strings.TrimSpace(sma7702Runtime))
}
