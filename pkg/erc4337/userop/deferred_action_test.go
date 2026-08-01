package userop

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

func TestPackValidationLocator(t *testing.T) {
	loc, err := PackValidationLocator(1, ValidationOptionGlobal|ValidationOptionDeferredAction)
	if err != nil {
		t.Fatal(err)
	}
	// uint168 big-endian: entityId<<8 | options in the low 5 bytes.
	want := "000000000000000000000000000000000000000103"
	if hex.EncodeToString(loc[:]) != want {
		t.Fatalf("locator = %x, want %s", loc, want)
	}

	if _, err := PackValidationLocator(1, ValidationOptionDirectCall); err == nil {
		t.Fatal("expected the direct-call option to be rejected")
	}
}

func TestFallbackSignerLocator(t *testing.T) {
	loc := FallbackSignerLocator()
	// Entity 0 with only the global bit. The account masks the global bit off
	// when deriving the lookup key, landing on FALLBACK_VALIDATION_LOOKUP_KEY
	// (zero) — but without the bit the applicability check would consult the
	// fallback's (empty) selector set and revert.
	want := "000000000000000000000000000000000000000001"
	if hex.EncodeToString(loc[:]) != want {
		t.Fatalf("fallback locator = %x, want %s", loc, want)
	}
}

func TestEncodeDeferredActionDataLayout(t *testing.T) {
	loc := FallbackSignerLocator()
	call := common.FromHex("0x1bbf564cdeadbeef")
	data, err := EncodeDeferredActionData(loc, 1234567, call)
	if err != nil {
		t.Fatal(err)
	}
	// [:21] locator, [21:27] uint48 deadline, [27:] call — the fixed offsets
	// ModularAccountBase._handleDeferredAction slices at.
	if !bytes.Equal(data[:21], loc[:]) {
		t.Fatalf("locator segment = %x", data[:21])
	}
	deadline := binary.BigEndian.Uint64(append(make([]byte, 2), data[21:27]...))
	if deadline != 1234567 {
		t.Fatalf("deadline segment = %d, want 1234567", deadline)
	}
	if !bytes.Equal(data[27:], call) {
		t.Fatalf("call segment = %x", data[27:])
	}

	if _, err := EncodeDeferredActionData(loc, uint64(1)<<48, call); err == nil {
		t.Fatal("expected a uint48 deadline overflow to be rejected")
	}
	if _, err := EncodeDeferredActionData(loc, 0, []byte{0x1b}); err == nil {
		t.Fatal("expected a sub-selector call to be rejected")
	}
}

// TestDeferredActionDigestMatchesTypedData proves the manual digest assembly
// equals what a wallet computes from the equivalent eth_signTypedData_v4
// payload. This is the property §5.1 of the findings doc depends on: the
// grant screen hands the wallet structured typed data, and the bytes the
// wallet returns must verify against the digest the account computes.
func TestDeferredActionDigestMatchesTypedData(t *testing.T) {
	chainID := big.NewInt(11155111)
	account := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	nonce, err := EncodeNonceMAv2(1, ValidationOptionGlobal|ValidationOptionDeferredAction, 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := uint64(1790000000)
	call := common.FromHex("0x1bbf564c00112233445566778899aabbccddeeff")

	manual, err := DeferredActionDigest(chainID, account, nonce, deadline, call)
	if err != nil {
		t.Fatal(err)
	}

	// The PRODUCTION helper builds the payload — this pins wallet parity for
	// what the /policies prepare response actually returns, not a test copy.
	typed := DeferredActionTypedData(chainID, account, nonce, deadline, call)
	walletDigest, _, err := apitypes.TypedDataAndHash(typed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manual.Bytes(), walletDigest) {
		t.Fatalf("manual digest %s != typed-data digest %s", manual.Hex(), hexutil.Encode(walletDigest))
	}
}

func TestWrapSignatureMAv2Deferred(t *testing.T) {
	loc := FallbackSignerLocator()
	call := common.FromHex("0x1bbf564cdeadbeef")
	encoded, err := EncodeDeferredActionData(loc, 0, call)
	if err != nil {
		t.Fatal(err)
	}
	ownerSig := bytes.Repeat([]byte{0x11}, 65)
	ownerSig[64] = 27
	inner := bytes.Repeat([]byte{0x22}, 65)
	framedInner, err := WrapSignatureMAv2(inner)
	if err != nil {
		t.Fatal(err)
	}

	full, err := WrapSignatureMAv2Deferred(encoded, ownerSig, framedInner)
	if err != nil {
		t.Fatal(err)
	}

	// Re-slice exactly as ModularAccountBase._validateUserOp does.
	encLen := int(binary.BigEndian.Uint32(full[:4]))
	if !bytes.Equal(full[4:4+encLen], encoded) {
		t.Fatal("encodedData segment does not round-trip")
	}
	sigLen := int(binary.BigEndian.Uint32(full[4+encLen : 8+encLen]))
	deferredSig := full[8+encLen : 8+encLen+sigLen]
	if deferredSig[0] != 0x00 || !bytes.Equal(deferredSig[1:], ownerSig) {
		t.Fatal("deferred signature segment is not SignatureType.EOA ++ ownerSig")
	}
	if !bytes.Equal(full[8+encLen+sigLen:], framedInner) {
		t.Fatal("trailing user-op signature segment does not round-trip")
	}

	badV := bytes.Repeat([]byte{0x11}, 65) // v = 0x11: crypto.Sign output not normalised
	if _, err := WrapSignatureMAv2Deferred(encoded, badV, framedInner); err == nil {
		t.Fatal("expected an un-normalised v to be rejected")
	}
	if _, err := WrapSignatureMAv2Deferred(encoded, ownerSig, inner); err == nil {
		t.Fatal("expected an unframed user-op signature to be rejected")
	}
}
