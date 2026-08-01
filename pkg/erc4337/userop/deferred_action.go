package userop

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// Deferred actions — the mechanism that lets the owner authorize the
// controller install as an off-chain EIP-712 signature, executed later during
// validation of the first controller-signed operation. The owner never signs
// a UserOperation; the grant is collected at the Studio grant screen and the
// install rides the first real op (avs-infra
// MA_v2_Authority_Model_And_Spike_Results.md §5.1).
//
// Everything here mirrors ModularAccountBase.sol v2.0.1 (the deployed
// implementation), not aa-sdk. Three facts worth stating because each was
// established by reading that source and cost nothing to get wrong silently:
//
//  1. The typed data commits to the FULL 256-bit nonce of the operation that
//     will carry the action — locator bits and sequence included. The digest
//     can therefore be produced at grant time only because a fresh account's
//     first sequence under a fresh key is predictably zero, and because the
//     gas fields are NOT part of it.
//  2. The digest is signed RAW. During validateUserOp the fallback signer's
//     1271 path skips the usual replay-safe rewrap ("the hash is already
//     replay-safe" — SemiModularAccountBase._exec1271Validation), so the
//     bytes a wallet returns from eth_signTypedData_v4 are exactly what the
//     account verifies. No EIP-191 prefix anywhere.
//  3. The account's EIP-712 domain is minimal:
//     EIP712Domain(uint256 chainId,address verifyingContract) — no name, no
//     version. The verifyingContract is the counterfactual account address.
var (
	// keccak256("DeferredAction(uint256 nonce,uint48 deadline,bytes call)")
	deferredActionTypeHash = crypto.Keccak256Hash(
		[]byte("DeferredAction(uint256 nonce,uint48 deadline,bytes call)"))
	// keccak256("EIP712Domain(uint256 chainId,address verifyingContract)")
	deferredDomainTypeHash = crypto.Keccak256Hash(
		[]byte("EIP712Domain(uint256 chainId,address verifyingContract)"))
)

// maxDeadline is the uint48 ceiling; the contract truncates anything wider,
// which would make the signed digest and the encoded data disagree.
const maxDeadline = uint64(1)<<48 - 1

// PackValidationLocator builds the bytes21 ValidationLocator the account
// reads out of a deferred action's encoded data: uint168 big-endian,
// entityId<<8 | options. For the owner's fallback validation this is entity 0
// with only the global bit — which the lookup masks back to the fallback key.
func PackValidationLocator(entityID uint32, options uint8) ([21]byte, error) {
	var out [21]byte
	if options&ValidationOptionDirectCall != 0 {
		return out, fmt.Errorf("direct-call locators carry a 20-byte address, not an entity id")
	}
	out[16] = byte(entityID >> 24)
	out[17] = byte(entityID >> 16)
	out[18] = byte(entityID >> 8)
	out[19] = byte(entityID)
	out[20] = options
	return out, nil
}

// FallbackSignerLocator is the locator naming the account's own fallback
// signer (the owner) as the validation for the deferred action's signature.
// The global bit must be set: installValidation is checked against the
// deferred validation's applicability, and the fallback signer is a global
// validation.
func FallbackSignerLocator() [21]byte {
	loc, _ := PackValidationLocator(FallbackSignerEntityID, ValidationOptionGlobal)
	return loc
}

// EncodeDeferredActionData assembles the encodedData segment of a deferred
// signature: locator (21) ++ deadline (uint48 big-endian, 6) ++ the self-call
// the account will execute during validation. A zero deadline means no
// deadline.
func EncodeDeferredActionData(locator [21]byte, deadline uint64, call []byte) ([]byte, error) {
	if deadline > maxDeadline {
		return nil, fmt.Errorf("deadline %d overflows uint48", deadline)
	}
	if len(call) < 4 {
		return nil, fmt.Errorf("deferred call is %d bytes, shorter than a selector", len(call))
	}
	out := make([]byte, 0, 21+6+len(call))
	out = append(out, locator[:]...)
	var d [8]byte
	binary.BigEndian.PutUint64(d[:], deadline)
	out = append(out, d[2:8]...)
	return append(out, call...), nil
}

// DeferredActionDigest computes the EIP-712 digest the OWNER signs:
//
//	keccak256(0x1901 ++ domainSeparator ++ structHash)
//	domainSeparator = keccak256(abi.encode(domainTypeHash, chainId, account))
//	structHash      = keccak256(abi.encode(typeHash, nonce, deadline, keccak256(call)))
//
// nonce is the full nonce of the operation that will carry the action —
// build it with EncodeNonceMAv2 using the SAME entity id and options
// (including ValidationOptionDeferredAction) the operation will use, or the
// signature verifies against a digest the account never computes.
func DeferredActionDigest(chainID *big.Int, account common.Address, nonce *big.Int, deadline uint64, call []byte) (common.Hash, error) {
	if chainID == nil || chainID.Sign() <= 0 {
		return common.Hash{}, fmt.Errorf("chain id is nil or non-positive")
	}
	if nonce == nil || nonce.Sign() < 0 || nonce.BitLen() > 256 {
		return common.Hash{}, fmt.Errorf("nonce is nil, negative, or overflows uint256")
	}
	if deadline > maxDeadline {
		return common.Hash{}, fmt.Errorf("deadline %d overflows uint48", deadline)
	}

	var word [32]byte
	domain := make([]byte, 0, 96)
	domain = append(domain, deferredDomainTypeHash.Bytes()...)
	chainID.FillBytes(word[:])
	domain = append(domain, word[:]...)
	domain = append(domain, common.LeftPadBytes(account.Bytes(), 32)...)
	domainSeparator := crypto.Keccak256(domain)

	structData := make([]byte, 0, 128)
	structData = append(structData, deferredActionTypeHash.Bytes()...)
	nonce.FillBytes(word[:])
	structData = append(structData, word[:]...)
	new(big.Int).SetUint64(deadline).FillBytes(word[:])
	structData = append(structData, word[:]...)
	structData = append(structData, crypto.Keccak256(call)...)
	structHash := crypto.Keccak256(structData)

	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domainSeparator, structHash), nil
}

// DeferredActionTypedData builds the eth_signTypedData_v4 payload whose hash
// is DeferredActionDigest — the exact JSON a wallet needs to produce the
// owner's grant signature. Digest parity between this construction and the
// manual assembly is pinned by TestDeferredActionDigestMatchesTypedData; a
// drift between them would produce signatures the account rejects.
func DeferredActionTypedData(chainID *big.Int, account common.Address, nonce *big.Int, deadline uint64, call []byte) apitypes.TypedData {
	return apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"DeferredAction": []apitypes.Type{
				{Name: "nonce", Type: "uint256"},
				{Name: "deadline", Type: "uint48"},
				{Name: "call", Type: "bytes"},
			},
		},
		PrimaryType: "DeferredAction",
		Domain: apitypes.TypedDataDomain{
			ChainId:           (*math.HexOrDecimal256)(chainID),
			VerifyingContract: account.Hex(),
		},
		Message: apitypes.TypedDataMessage{
			"nonce":    (*math.HexOrDecimal256)(nonce),
			"deadline": (*math.HexOrDecimal256)(new(big.Int).SetUint64(deadline)),
			"call":     hexutil.Bytes(call),
		},
	}
}

// WrapSignatureMAv2Deferred assembles the full UserOperation signature for an
// operation whose nonce carries ValidationOptionDeferredAction. Layout, per
// ModularAccountBase._validateUserOp:
//
//	[0:4]   uint32 len(encodedData)
//	[4:..]  encodedData             (EncodeDeferredActionData output)
//	[..]    uint32 len(deferredSig)
//	[..]    deferredSig = 0x00 (SignatureType.EOA) ++ owner's 65-byte ECDSA
//	        over DeferredActionDigest — raw, no segment marker
//	[..]    framedUserOpSig         (WrapSignatureMAv2 output: 0xFF 0x00 ++
//	        the controller's signature over the EIP-191 hash of userOpHash)
func WrapSignatureMAv2Deferred(encodedData []byte, ownerSig []byte, framedUserOpSig []byte) ([]byte, error) {
	if len(encodedData) < 21+6+4 {
		return nil, fmt.Errorf("encoded deferred data is %d bytes, shorter than locator+deadline+selector", len(encodedData))
	}
	if len(ownerSig) != 65 {
		return nil, fmt.Errorf("expected a 65-byte owner signature, got %d bytes", len(ownerSig))
	}
	if v := ownerSig[64]; v != 27 && v != 28 {
		return nil, fmt.Errorf("owner signature v is %d, want 27 or 28 — go-ethereum's crypto.Sign returns 0/1 and needs +27", v)
	}
	if len(framedUserOpSig) < 2 || framedUserOpSig[0] != SigSegmentValidationData {
		return nil, fmt.Errorf("user-op signature is not framed (missing 0xFF segment marker); wrap it first")
	}

	deferredSig := make([]byte, 0, 1+len(ownerSig))
	deferredSig = append(deferredSig, byte(0x00)) // SignatureType.EOA
	deferredSig = append(deferredSig, ownerSig...)

	out := make([]byte, 0, 4+len(encodedData)+4+len(deferredSig)+len(framedUserOpSig))
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(encodedData)))
	out = append(out, l[:]...)
	out = append(out, encodedData...)
	binary.BigEndian.PutUint32(l[:], uint32(len(deferredSig)))
	out = append(out, l[:]...)
	out = append(out, deferredSig...)
	return append(out, framedUserOpSig...), nil
}
