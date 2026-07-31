package aa

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// The default is the single most consequential line in this file. MA v2
// derives DIFFERENT addresses for the same (owner, salt), so a default of
// modular_account_v2 would move every existing user's wallet the moment a
// gateway rolled out — orphaning funds and every task whose runner references
// the old address. If someone "tidies" this to the newer value, this fails.
func TestDefaultProviderIsSimpleAccount(t *testing.T) {
	if got := normaliseProvider(""); got != ProviderSimpleAccount {
		t.Fatalf("empty provider = %q, want %q — defaulting to MA v2 would move every existing wallet",
			got, ProviderSimpleAccount)
	}
}

func TestFactoryAddressForProvider(t *testing.T) {
	simple := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")

	t.Run("simple account uses the configured factory", func(t *testing.T) {
		got, err := FactoryAddressForProvider(ProviderSimpleAccount, simple)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != simple {
			t.Errorf("factory = %s, want %s", got.Hex(), simple.Hex())
		}
	})

	t.Run("empty provider behaves as simple account", func(t *testing.T) {
		got, err := FactoryAddressForProvider("", simple)
		if err != nil || got != simple {
			t.Errorf("got (%s, %v), want (%s, nil)", got.Hex(), err, simple.Hex())
		}
	})

	t.Run("MA v2 uses the canonical factory and ignores the configured one", func(t *testing.T) {
		got, err := FactoryAddressForProvider(ProviderModularAccountV2, simple)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != MAv2FactoryAddress() {
			t.Errorf("factory = %s, want %s", got.Hex(), MAv2FactoryAddress().Hex())
		}
		if got == simple {
			t.Error("MA v2 returned the SimpleAccount factory")
		}
	})

	t.Run("simple account with no configured factory is an error", func(t *testing.T) {
		// Falling back to a zero address would record wallets under a factory
		// that created nothing, and derive addresses that hold nothing.
		if _, err := FactoryAddressForProvider(ProviderSimpleAccount, common.Address{}); err == nil {
			t.Error("expected an error when no factory is configured")
		}
	})

	t.Run("unknown provider is an error, not a fallback", func(t *testing.T) {
		if _, err := FactoryAddressForProvider("modular_account", simple); err == nil {
			t.Error("expected an error for an unrecognised provider")
		}
	})
}

// The providers must resolve to different factories — that difference is what
// keeps their wsalt: index entries apart, so an MA v2 wallet cannot overwrite a
// user's canonical v0.6 wallet at the same salt.
func TestProvidersResolveToDifferentFactories(t *testing.T) {
	simple := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	a, err := FactoryAddressForProvider(ProviderSimpleAccount, simple)
	if err != nil {
		t.Fatalf("simple: %v", err)
	}
	b, err := FactoryAddressForProvider(ProviderModularAccountV2, simple)
	if err != nil {
		t.Fatalf("mav2: %v", err)
	}
	if a == b {
		t.Fatal("both providers resolve to the same factory; index entries would collide")
	}
}

func TestDeriveSenderAddressRejectsUnknownProvider(t *testing.T) {
	owner := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	if _, err := DeriveSenderAddress(nil, owner, big.NewInt(0), "not_a_provider"); err == nil {
		t.Error("expected an error for an unrecognised provider")
	}
}

// This package must accept exactly what config accepts. Config normalises
// (trim + lowercase) before storing, but callers reasonably pass the raw yaml
// value through — and a config the gateway loaded happily must not fail here
// as an "unknown provider", which reads as a code bug rather than whitespace.
func TestProviderNormalisationMatchesConfig(t *testing.T) {
	for _, in := range []AccountProvider{
		"modular_account_v2", "MODULAR_ACCOUNT_V2", " modular_account_v2 ", "Modular_Account_V2",
	} {
		if got := normaliseProvider(in); got != ProviderModularAccountV2 {
			t.Errorf("normaliseProvider(%q) = %q, want %q", in, got, ProviderModularAccountV2)
		}
	}
	for _, in := range []AccountProvider{"", "   ", "simple_account", "SIMPLE_ACCOUNT", " Simple_Account "} {
		if got := normaliseProvider(in); got != ProviderSimpleAccount {
			t.Errorf("normaliseProvider(%q) = %q, want %q", in, got, ProviderSimpleAccount)
		}
	}
}

// A whitespace/case variant must reach the right factory, not error.
func TestFactoryAddressAcceptsUnnormalisedProvider(t *testing.T) {
	simple := common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	got, err := FactoryAddressForProvider(" Modular_Account_V2 ", simple)
	if err != nil {
		t.Fatalf("unexpected error for a value config would accept: %v", err)
	}
	if got != MAv2FactoryAddress() {
		t.Errorf("factory = %s, want %s", got.Hex(), MAv2FactoryAddress().Hex())
	}
}
