package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestParseSessionSignerKeyIsRequired(t *testing.T) {
	for _, raw := range []string{"", "   ", "${SESSION_SIGNER_PRIVATE_KEY}", "0x1234"} {
		if _, err := parseSessionSignerKey(raw); err == nil {
			t.Fatalf("parseSessionSignerKey(%q) must fail: the key is required and has no fallback", raw)
		} else if !strings.Contains(err.Error(), "session_signer_private_key") {
			t.Fatalf("error must name the field, got %q", err)
		}
	}
}

func TestParseSessionSignerKeyAcceptsHexWithOrWithoutPrefix(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	hex := fmt.Sprintf("%064x", key.D)
	want := crypto.PubkeyToAddress(key.PublicKey)
	for _, raw := range []string{hex, "0x" + hex, " " + hex + "\n"} {
		got, err := parseSessionSignerKey(raw)
		if err != nil {
			t.Fatalf("parseSessionSignerKey(%q): %v", raw, err)
		}
		if crypto.PubkeyToAddress(got.PublicKey) != want {
			t.Fatalf("parseSessionSignerKey(%q) decoded the wrong key", raw)
		}
	}
}

// The session signer is chosen independently of every controller. Sharing one
// is reported per block, including a per-chain controller the top-level block
// does not name, so a gateway that reuses a network's controller hears which.
func TestSessionSignerSharedWithNamesEachControllerBlock(t *testing.T) {
	sessionKey, _ := crypto.GenerateKey()
	otherKey, _ := crypto.GenerateKey()
	sessionSigner := crypto.PubkeyToAddress(sessionKey.PublicKey)

	cfg := &Config{
		SmartWallet: &SmartWalletConfig{ControllerPrivateKey: otherKey},
		Chains: []*ChainConfig{
			{Name: "ethereum", SmartWallet: &SmartWalletConfig{ControllerPrivateKey: otherKey}},
			{Name: "base", SmartWallet: &SmartWalletConfig{ControllerPrivateKey: otherKey}},
		},
	}
	if shared := sessionSignerSharedWith(sessionSigner, cfg); len(shared) != 0 {
		t.Fatalf("distinct keys must not be reported as shared, got %v", shared)
	}

	cfg.SmartWallet.ControllerPrivateKey = sessionKey
	cfg.Chains[1].SmartWallet.ControllerPrivateKey = sessionKey
	got := sessionSignerSharedWith(sessionSigner, cfg)
	if strings.Join(got, ",") != "smart_wallet,chains[base]" {
		t.Fatalf("shared = %v, want [smart_wallet chains[base]]", got)
	}
}
