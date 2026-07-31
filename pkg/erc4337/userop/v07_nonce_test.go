package userop

import (
	"bytes"
	"math/big"
	"testing"
)

// The value Alchemy's bundler actually accepted. A counterfactual semi-modular
// account with this nonce returned a gas estimate; the same operation with
// nonce 0 reverted ValidationFunctionMissing(0xb61d27f6). If this constant
// changes, that is the regression.
const acceptedNonceHex = "10000000000000000" // entityID 0, global bit, sequence 0

func TestEncodeNonceMAv2_MatchesTheValueTheBundlerAccepted(t *testing.T) {
	got, err := EncodeNonceMAv2(FallbackSignerEntityID, ValidationOptionGlobal, 0)
	if err != nil {
		t.Fatalf("EncodeNonceMAv2: %v", err)
	}
	if got.Text(16) != acceptedNonceHex {
		t.Errorf("nonce = 0x%s, want 0x%s", got.Text(16), acceptedNonceHex)
	}
}

func TestEncodeNonceMAv2Layout(t *testing.T) {
	tests := []struct {
		name     string
		entityID uint32
		options  uint8
		sequence uint64
		wantHex  string
	}{
		{"fallback signer, global, seq 0", 0, ValidationOptionGlobal, 0, "10000000000000000"},
		{"sequence occupies the low 64 bits", 0, ValidationOptionGlobal, 5, "10000000000000005"},
		{"entity id sits above the options byte", 1, ValidationOptionGlobal, 0, "1010000000000000000"},
		{"no options", 0, 0, 0, "0"},
		{"options are a bitfield", 0, ValidationOptionGlobal | ValidationOptionDeferredAction, 0, "30000000000000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EncodeNonceMAv2(tt.entityID, tt.options, tt.sequence)
			if err != nil {
				t.Fatalf("EncodeNonceMAv2: %v", err)
			}
			if got.Text(16) != tt.wantHex {
				t.Errorf("nonce = 0x%s, want 0x%s", got.Text(16), tt.wantHex)
			}
		})
	}
}

func TestNonceRoundTrip(t *testing.T) {
	cases := []struct {
		entityID uint32
		options  uint8
		sequence uint64
	}{
		{0, ValidationOptionGlobal, 0},
		{0, ValidationOptionGlobal, 42},
		{7, ValidationOptionGlobal, 1},
		{4294967295, ValidationOptionGlobal | ValidationOptionDeferredAction, 18446744073709551615},
	}
	for _, c := range cases {
		n, err := EncodeNonceMAv2(c.entityID, c.options, c.sequence)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		e, o, s, err := DecodeNonceMAv2(n)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if e != c.entityID || o != c.options || s != c.sequence {
			t.Errorf("round trip: got (%d,%d,%d) want (%d,%d,%d)", e, o, s, c.entityID, c.options, c.sequence)
		}
	}
}

// The key is what EntryPoint.getNonce(sender, key) takes. Getting the sequence
// against a different key than the one the nonce carries yields AA25.
func TestNonceKeyIsTheHigh192Bits(t *testing.T) {
	full, err := EncodeNonceMAv2(0, ValidationOptionGlobal, 99)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	key, err := NonceKeyMAv2(0, ValidationOptionGlobal)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	// full == key<<64 | sequence
	rebuilt := new(big.Int).Lsh(key, 64)
	rebuilt.Or(rebuilt, big.NewInt(99))
	if rebuilt.Cmp(full) != 0 {
		t.Errorf("key<<64|seq = 0x%s, want 0x%s", rebuilt.Text(16), full.Text(16))
	}
}

func TestEncodeNonceRejectsDirectCall(t *testing.T) {
	// Direct-call validation switches the locator union to a 20-byte module
	// address; silently encoding an entity id there would select the wrong
	// validation rather than fail.
	if _, err := EncodeNonceMAv2(0, ValidationOptionDirectCall, 0); err == nil {
		t.Error("expected an error for direct-call options")
	}
}

func TestDecodeNonceGuards(t *testing.T) {
	if _, _, _, err := DecodeNonceMAv2(nil); err == nil {
		t.Error("expected an error for nil nonce")
	}
	if _, _, _, err := DecodeNonceMAv2(big.NewInt(-1)); err == nil {
		t.Error("expected an error for negative nonce")
	}
}

func TestWrapSignatureMAv2(t *testing.T) {
	sig := bytes.Repeat([]byte{0xaa}, 65)

	t.Run("prepends the segment index and signature type", func(t *testing.T) {
		got, err := WrapSignatureMAv2(sig)
		if err != nil {
			t.Fatalf("WrapSignatureMAv2: %v", err)
		}
		if len(got) != 67 {
			t.Fatalf("len = %d, want 67", len(got))
		}
		if got[0] != 0xFF {
			t.Errorf("byte 0 = 0x%02x, want 0xFF (validation-data segment index)", got[0])
		}
		if got[1] != 0x00 {
			t.Errorf("byte 1 = 0x%02x, want 0x00 (EOA signature type)", got[1])
		}
		if !bytes.Equal(got[2:], sig) {
			t.Error("the ECDSA signature was altered")
		}
	})

	t.Run("rejects a wrong-length signature", func(t *testing.T) {
		// A bare 64-byte sig, or an already-wrapped 67-byte one, both mean the
		// caller lost track of the framing. Double-wrapping is the likelier
		// mistake and produces a signature that fails validation opaquely.
		for _, n := range []int{0, 64, 66, 67} {
			if _, err := WrapSignatureMAv2(bytes.Repeat([]byte{0x01}, n)); err == nil {
				t.Errorf("expected an error for a %d-byte signature", n)
			}
		}
	})
}
