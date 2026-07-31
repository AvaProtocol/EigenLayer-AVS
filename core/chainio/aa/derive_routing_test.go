package aa

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
)

// A SimpleAccount factory is any address that is not the MA v2 constant.
var simpleAccountFactory = common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")

// The whole cutover rests on this: the factory address carries the provider,
// which is what lets ChainStateReader (and its gRPC surface) stay unchanged
// while derivation switches implementation.
func TestProviderForFactory(t *testing.T) {
	if got := ProviderForFactory(MAv2FactoryAddress()); got != ProviderModularAccountV2 {
		t.Errorf("MA v2 factory resolved to %q, want %q", got, ProviderModularAccountV2)
	}
	if got := ProviderForFactory(simpleAccountFactory); got != ProviderSimpleAccount {
		t.Errorf("SimpleAccount factory resolved to %q, want %q", got, ProviderSimpleAccount)
	}
	// The zero address is not the MA v2 factory, so it must not be treated as
	// one. Deriving MA v2 against a zero factory would silently produce an
	// address for an account nothing can deploy.
	if got := ProviderForFactory(common.Address{}); got != ProviderSimpleAccount {
		t.Errorf("zero factory resolved to %q, want %q", got, ProviderSimpleAccount)
	}
}

func TestEffectiveFactory(t *testing.T) {
	t.Run("MA v2 chain ignores the configured SimpleAccount factory", func(t *testing.T) {
		swCfg := &config.SmartWalletConfig{
			FactoryAddress:  simpleAccountFactory,
			AccountProvider: config.AccountProviderModularAccountV2,
		}
		got, err := EffectiveFactory(swCfg)
		if err != nil {
			t.Fatalf("EffectiveFactory: %v", err)
		}
		if got != MAv2FactoryAddress() {
			t.Errorf("factory = %s, want %s", got.Hex(), MAv2FactoryAddress().Hex())
		}
	})

	t.Run("unset provider is MA v2, matching the post-cutover default", func(t *testing.T) {
		swCfg := &config.SmartWalletConfig{FactoryAddress: simpleAccountFactory}
		got, err := EffectiveFactory(swCfg)
		if err != nil {
			t.Fatalf("EffectiveFactory: %v", err)
		}
		if got != MAv2FactoryAddress() {
			t.Errorf("factory = %s, want the MA v2 factory %s", got.Hex(), MAv2FactoryAddress().Hex())
		}
	})

	t.Run("pinned simple_account keeps the configured factory", func(t *testing.T) {
		swCfg := &config.SmartWalletConfig{
			FactoryAddress:  simpleAccountFactory,
			AccountProvider: config.AccountProviderSimpleAccount,
		}
		got, err := EffectiveFactory(swCfg)
		if err != nil {
			t.Fatalf("EffectiveFactory: %v", err)
		}
		if got != simpleAccountFactory {
			t.Errorf("factory = %s, want %s", got.Hex(), simpleAccountFactory.Hex())
		}
	})

	t.Run("nil config is an error, not a zero address", func(t *testing.T) {
		if _, err := EffectiveFactory(nil); err == nil {
			t.Error("expected an error for a nil config")
		}
	})

	t.Run("simple_account with no configured factory is an error", func(t *testing.T) {
		swCfg := &config.SmartWalletConfig{AccountProvider: config.AccountProviderSimpleAccount}
		if _, err := EffectiveFactory(swCfg); err == nil {
			t.Error("expected an error when simple_account has no factory configured")
		}
	})
}

// The round trip that keeps the two halves of the cutover honest: whatever
// EffectiveFactory hands out must resolve back to the provider the config
// asked for. If it doesn't, addresses get derived under one implementation and
// recorded under another.
func TestEffectiveFactoryRoundTripsToItsProvider(t *testing.T) {
	for _, provider := range []string{
		config.AccountProviderSimpleAccount,
		config.AccountProviderModularAccountV2,
		"", // the default
	} {
		swCfg := &config.SmartWalletConfig{
			FactoryAddress:  simpleAccountFactory,
			AccountProvider: provider,
		}
		factory, err := EffectiveFactory(swCfg)
		if err != nil {
			t.Fatalf("provider %q: %v", provider, err)
		}
		got := string(ProviderForFactory(factory))
		if want := swCfg.AccountProviderName(); got != want {
			t.Errorf("provider %q: factory %s resolves back to %q, want %q",
				provider, factory.Hex(), got, want)
		}
	}
}

// Init code must route in lockstep with address derivation. If they disagree,
// the UserOperation's initCode deploys to an address other than its sender and
// the EntryPoint rejects it as AA14 — a failure that points nowhere near here.
func TestDeriveInitCodeAutoRoutesOnFactory(t *testing.T) {
	owner := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	salt := big.NewInt(0)

	maFactory, maData, err := DeriveInitCodeAuto(owner, MAv2FactoryAddress(), salt)
	if err != nil {
		t.Fatalf("MA v2 init code: %v", err)
	}
	if maFactory != MAv2FactoryAddress() {
		t.Errorf("MA v2 factory = %s, want %s", maFactory.Hex(), MAv2FactoryAddress().Hex())
	}
	if len(maData) == 0 {
		t.Error("MA v2 factory data is empty")
	}

	simpleFactoryOut, simpleData, err := DeriveInitCodeAuto(owner, simpleAccountFactory, salt)
	if err != nil {
		t.Fatalf("SimpleAccount init code: %v", err)
	}
	if simpleFactoryOut != simpleAccountFactory {
		t.Errorf("SimpleAccount factory = %s, want %s", simpleFactoryOut.Hex(), simpleAccountFactory.Hex())
	}
	if len(simpleData) == 0 {
		t.Error("SimpleAccount factory data is empty")
	}

	// Different factory methods mean different selectors. Identical calldata
	// would mean one branch is building the other's deployment.
	if string(maData) == string(simpleData) {
		t.Error("both providers produced identical factory data; the routing is not switching")
	}
}

func TestDeriveInitCodeAutoStripsTheFactoryPrefix(t *testing.T) {
	owner := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	_, data, err := DeriveInitCodeAuto(owner, simpleAccountFactory, big.NewInt(0))
	if err != nil {
		t.Fatalf("DeriveInitCodeAuto: %v", err)
	}
	// factoryData is the constructor call alone; leaving the 20-byte factory
	// prefix on it would make the v0.7 wire form double-encode the factory.
	if len(data) >= common.AddressLength &&
		common.BytesToAddress(data[:common.AddressLength]) == simpleAccountFactory {
		t.Error("factory data still begins with the factory address")
	}
	// A createAccount(address,uint256) call is a 4-byte selector plus two
	// 32-byte words.
	if len(data) != 4+32+32 {
		t.Errorf("factory data is %d bytes, want %d", len(data), 4+32+32)
	}
}

// DeriveSenderAddressForFactory and DeriveSenderAddressAuto both need a live
// client to reach the factory, so the reachable assertion here is that an
// unknown provider is refused before any dial happens.
func TestDeriveSenderAddressForFactoryRejectsUnknownProvider(t *testing.T) {
	owner := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	_, err := DeriveSenderAddressForFactory(nil, owner, simpleAccountFactory, big.NewInt(0), "not_a_provider")
	if err == nil {
		t.Error("expected an error for an unrecognised provider")
	}
}

// The package-global factory is what GetSenderAddress derives against, and
// GetSenderAddress infers the account implementation from it. So an unset
// global must resolve to the DEFAULT provider's factory, not to the v0.6
// SimpleAccountFactory.
//
// This regressed once already: the fallback stayed on
// config.DefaultFactoryProxyAddressHex after the default provider flipped, so
// every caller that reached the global before SetFactoryAddressForConfig ran
// derived a v0.6 address on an MA v2 chain — silently, since both branches
// return a well-formed address.
func TestUnsetGlobalFactoryFollowsTheDefaultProvider(t *testing.T) {
	SetFactoryAddress(common.Address{}) // simulate "never configured"

	got := getFactoryAddress()
	want, err := FactoryAddressForProvider("", common.HexToAddress(config.DefaultFactoryProxyAddressHex))
	if err != nil {
		t.Fatalf("FactoryAddressForProvider: %v", err)
	}
	if got != want {
		t.Errorf("unset global factory = %s, want %s (the default provider's factory)",
			got.Hex(), want.Hex())
	}
	if got == common.HexToAddress(config.DefaultFactoryProxyAddressHex) {
		t.Error("unset global fell back to the v0.6 SimpleAccountFactory")
	}
	if ProviderForFactory(got) != ProviderModularAccountV2 {
		t.Errorf("unset global resolves to provider %q, want %q",
			ProviderForFactory(got), ProviderModularAccountV2)
	}
}
