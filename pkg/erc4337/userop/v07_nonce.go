package userop

import (
	"fmt"
	"math/big"
)

// Modular Account v2 selects which validation function runs from the
// UserOperation's NONCE, not from the signature. This is the single most
// surprising difference from the v0.6 SimpleAccount path, where the nonce was
// only a replay counter.
//
// A nonce that does not encode a validation locator makes the account revert
// with ValidationFunctionMissing(bytes4) naming the selector it could not
// validate — e.g. 0xcf7b49f6 + 0xb61d27f6 for `execute`. That reads like a
// missing module or a bad signature and sends you looking in the wrong place,
// so the encoding lives here with a name.
//
// Layout of the 256-bit nonce:
//
//	[ 192-bit key ][ 64-bit sequential nonce ]
//
// and within the low end of the key, immediately above the sequence:
//
//	bits  64.. 71  options byte
//	bits  72..103  validation entity id (uint32)
//	bits 104..255  caller-defined parallel nonce space
//
// Options bits: 0 = global validation, 1 = has deferred action,
// 2 = direct-call validation (union tag).
//
// Verified end to end against Alchemy's Sepolia bundler: entity id 0 with the
// global bit set (nonce key 0x1) returns a gas estimate for a counterfactual
// semi-modular account, while the same operation with nonce 0 reverts
// ValidationFunctionMissing.
const (
	// ValidationOptionGlobal marks the validation as global — applicable to
	// any selector rather than scoped to specific ones. The fallback signer on
	// a semi-modular account is a global validation.
	ValidationOptionGlobal = 1 << 0
	// ValidationOptionDeferredAction signals the signature carries a deferred
	// action payload. Unused today.
	ValidationOptionDeferredAction = 1 << 1
	// ValidationOptionDirectCall switches the locator union to a 20-byte
	// module address instead of an entity id. Unused today.
	ValidationOptionDirectCall = 1 << 2

	// FallbackSignerEntityID is the entity id of the owner validation that a
	// semi-modular account is created with. Session keys installed later get
	// their own ids.
	FallbackSignerEntityID uint32 = 0

	nonceSequenceBits = 64
	nonceOptionsBits  = 8
)

// EncodeNonceMAv2 builds a nonce that selects `entityID` as the validating
// entity and carries `sequence` as the replay counter.
//
// `sequence` must be the value EntryPoint.getNonce(sender, key) returns for
// the SAME key this function produces — the counter is per-key, so deriving it
// against a different key silently yields AA25 invalid account nonce.
func EncodeNonceMAv2(entityID uint32, options uint8, sequence uint64) (*big.Int, error) {
	if options&ValidationOptionDirectCall != 0 {
		return nil, fmt.Errorf("direct-call validation uses a 20-byte module address locator, not an entity id")
	}
	key := new(big.Int).SetUint64(uint64(entityID))
	key.Lsh(key, nonceOptionsBits)
	key.Or(key, big.NewInt(int64(options)))
	key.Lsh(key, nonceSequenceBits)
	return key.Or(key, new(big.Int).SetUint64(sequence)), nil
}

// NonceKeyMAv2 returns just the 192-bit key half, which is what
// EntryPoint.getNonce(sender, key) expects in order to return the matching
// sequence.
func NonceKeyMAv2(entityID uint32, options uint8) (*big.Int, error) {
	full, err := EncodeNonceMAv2(entityID, options, 0)
	if err != nil {
		return nil, err
	}
	return full.Rsh(full, nonceSequenceBits), nil
}

// DecodeNonceMAv2 is the inverse of EncodeNonceMAv2, for logging and tests.
func DecodeNonceMAv2(nonce *big.Int) (entityID uint32, options uint8, sequence uint64, err error) {
	if nonce == nil {
		return 0, 0, 0, fmt.Errorf("nonce is nil")
	}
	if nonce.Sign() < 0 {
		return 0, 0, 0, fmt.Errorf("nonce is negative")
	}
	if nonce.BitLen() > 256 {
		return 0, 0, 0, fmt.Errorf("nonce overflows uint256 (%d bits)", nonce.BitLen())
	}
	mask64 := new(big.Int).SetUint64(^uint64(0))
	sequence = new(big.Int).And(nonce, mask64).Uint64()
	rest := new(big.Int).Rsh(nonce, nonceSequenceBits)
	options = uint8(new(big.Int).And(rest, big.NewInt(0xff)).Uint64())
	entityID = uint32(new(big.Int).Rsh(rest, nonceOptionsBits).Uint64())
	return entityID, options, sequence, nil
}

// MAv2 signature framing. The account expects the raw ECDSA signature to be
// preceded by two marker bytes:
//
//	0xFF  reserved segment index — "what follows is validation data", as
//	      opposed to a per-hook data segment
//	0x00  signature type — a plain EOA signature
//
// Total 67 bytes for a 65-byte ECDSA signature. Sending the bare 65 bytes
// reverts in validation with no indication that framing was the problem.
const (
	// SigSegmentValidationData is the reserved segment index meaning "what
	// follows is validation data" rather than per-hook data.
	SigSegmentValidationData byte = 0xFF
	// SigTypeEOA marks a plain ECDSA signature.
	SigTypeEOA byte = 0x00
)

// WrapSignatureMAv2 frames a 65-byte ECDSA signature for a semi-modular
// account's fallback signer.
func WrapSignatureMAv2(ecdsaSig []byte) ([]byte, error) {
	if len(ecdsaSig) != 65 {
		return nil, fmt.Errorf("expected a 65-byte ECDSA signature, got %d bytes", len(ecdsaSig))
	}
	out := make([]byte, 0, 2+len(ecdsaSig))
	out = append(out, SigSegmentValidationData, SigTypeEOA)
	return append(out, ecdsaSig...), nil
}
